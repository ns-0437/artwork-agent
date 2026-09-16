package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"cloud.google.com/go/storage"
)

// GCS is the Day 5 deploy-time Storage backend - the same content-addressed
// scheme as LocalDisk (point 17), just against a Cloud Storage bucket
// instead of a local directory, so nothing outside this package (or the
// tests exercising the Storage interface) needs to know which is active.
type GCS struct {
	client *storage.Client
	bucket string
}

// NewGCS opens a client against bucketName. Callers are expected to close
// the returned client's underlying *storage.Client via Close when the
// process shuts down (cmd/server and cmd/worker both run for the life of
// the process, so this is best-effort cleanup, not load-bearing).
func NewGCS(ctx context.Context, bucketName string) (*GCS, error) {
	if bucketName == "" {
		return nil, errors.New("storage: GCS bucket name is required")
	}
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage: failed to create GCS client: %w", err)
	}
	return &GCS{client: client, bucket: bucketName}, nil
}

func (g *GCS) Close() error {
	return g.client.Close()
}

func (g *GCS) objectKey(hexSum string) string {
	return fmt.Sprintf("%s/%s", hexSum[:2], hexSum)
}

// Put mirrors LocalDisk.Put exactly: content-addressed by SHA-256, and a
// retried write of bytes already present is a safe no-op rather than a
// second upload - checked via an existence probe (Attrs) before writing,
// the same "storage write, then crash before commit" safety property
// point 32/48 rely on, now against GCS instead of a local directory.
func (g *GCS) Put(ctx context.Context, data []byte) (string, string, error) {
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])
	key := g.objectKey(hexSum)

	obj := g.client.Bucket(g.bucket).Object(key)
	if _, err := obj.Attrs(ctx); err == nil {
		return key, hexSum, nil // already stored under this content hash
	} else if !errors.Is(err, storage.ErrObjectNotExist) {
		return "", "", fmt.Errorf("storage: failed to check existing object %s: %w", key, err)
	}

	w := obj.If(storage.Conditions{DoesNotExist: true}).NewWriter(ctx)
	if _, err := io.Copy(w, bytes.NewReader(data)); err != nil {
		_ = w.Close()
		return "", "", fmt.Errorf("storage: failed to write object %s: %w", key, err)
	}
	if err := w.Close(); err != nil {
		// A precondition failure here means another writer raced us to the
		// same content-addressed key with the same bytes - a safe outcome,
		// not an error, since the key is a pure function of the content.
		var apiErr interface{ Error() string }
		if errors.As(err, &apiErr) {
			if _, attrErr := obj.Attrs(ctx); attrErr == nil {
				return key, hexSum, nil
			}
		}
		return "", "", fmt.Errorf("storage: failed to finalize object %s: %w", key, err)
	}
	return key, hexSum, nil
}

func (g *GCS) Get(ctx context.Context, key string) ([]byte, error) {
	r, err := g.client.Bucket(g.bucket).Object(key).NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage: failed to open object %s: %w", key, err)
	}
	defer r.Close()
	return io.ReadAll(r)
}
