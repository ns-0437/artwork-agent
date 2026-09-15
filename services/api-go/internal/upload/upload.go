package upload

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ns-0437/artwork-agent/services/api-go/internal/storage"
	"github.com/ns-0437/artwork-agent/services/api-go/internal/store"
)

const (
	ticketTTL      = 10 * time.Minute
	maxUploadBytes = 10 * 1024 * 1024 // 10MB cap per the brief's security essentials
)

// Manager issues and redeems short-lived, single-order upload tickets. It
// stands in for a signed cloud-storage URL in local dev: the token is
// HMAC-signed so the upload handler can validate it without a DB round trip,
// and it is scoped to one order and expires quickly.
type Manager struct {
	secret  []byte
	storage storage.Storage
	store   *store.Store
	baseURL string
}

func NewManager(secret []byte, s storage.Storage, st *store.Store, baseURL string) *Manager {
	return &Manager{secret: secret, storage: s, store: st, baseURL: strings.TrimRight(baseURL, "/")}
}

func (m *Manager) CreateTicket(orderID, contentType string) (uploadURL string, expiresAt time.Time, err error) {
	expiresAt = time.Now().Add(ticketTTL)
	nonce := uuid.NewString()
	token := m.sign(orderID, contentType, expiresAt, nonce)
	uploadURL = fmt.Sprintf("%s/uploads/%s", m.baseURL, token)
	return uploadURL, expiresAt, nil
}

func (m *Manager) sign(orderID, contentType string, expiresAt time.Time, nonce string) string {
	payload := fmt.Sprintf("%s|%s|%d|%s", orderID, contentType, expiresAt.Unix(), nonce)
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	encodedPayload := hex.EncodeToString([]byte(payload))
	return encodedPayload + "." + sig
}

func (m *Manager) verify(token string) (orderID string, err error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", errors.New("malformed upload token")
	}
	payloadBytes, err := hex.DecodeString(parts[0])
	if err != nil {
		return "", errors.New("malformed upload token")
	}
	mac := hmac.New(sha256.New, m.secret)
	mac.Write(payloadBytes)
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expectedSig), []byte(parts[1])) {
		return "", errors.New("invalid upload token signature")
	}

	fields := strings.SplitN(string(payloadBytes), "|", 4)
	if len(fields) != 4 {
		return "", errors.New("malformed upload token payload")
	}
	expiresUnix, convErr := strconv.ParseInt(fields[2], 10, 64)
	if convErr != nil {
		return "", errors.New("malformed upload token expiry")
	}
	if time.Now().Unix() > expiresUnix {
		return "", errors.New("upload token expired")
	}
	return fields[0], nil
}

// Handler serves POST /uploads/{token}: validates the token, stores the file
// content-addressed, and records an `assets` row of kind "original".
func (m *Manager) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		token := strings.TrimPrefix(r.URL.Path, "/uploads/")
		orderID, err := m.verify(token)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}

		contentType := r.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "file too large or unreadable (10MB limit)", http.StatusBadRequest)
			return
		}

		key, sha, err := m.storage.Put(r.Context(), data)
		if err != nil {
			http.Error(w, "failed to store upload", http.StatusInternalServerError)
			return
		}

		asset, err := m.store.CreateAsset(r.Context(), store.CreateAssetInput{
			OrderID:     orderID,
			Kind:        "original",
			StorageKey:  key,
			SHA256:      sha,
			ContentType: contentType,
		})
		if err != nil {
			http.Error(w, "failed to record asset", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"assetId":"%s"}`, asset.ID)
	}
}
