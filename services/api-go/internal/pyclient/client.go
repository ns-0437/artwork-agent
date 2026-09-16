// Package pyclient calls services/image-python over HTTP. Go forwards raw
// image bytes it already fetched from storage plus the order's declared
// dimensions/intent/trim-confirmation state, so the Python side never needs
// to know which storage backend is active or reach back into Postgres
// itself.
package pyclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: 30 * time.Second}}
}

type CheckResult struct {
	CheckName   string          `json:"check_name"`
	Result      string          `json:"result"`
	Evidence    json.RawMessage `json:"evidence"`
	RuleVersion string          `json:"rule_version"`
}

type InspectResult struct {
	WidthPx      int           `json:"width_px"`
	HeightPx     int           `json:"height_px"`
	Mode         string        `json:"mode"`
	Format       string        `json:"format"`
	TrimWidthPx  *int          `json:"trim_width_px"`
	TrimHeightPx *int          `json:"trim_height_px"`
	Checks       []CheckResult `json:"checks"`
}

type InspectInput struct {
	DeclaredWidth  float64
	DeclaredHeight float64
	DeclaredUnit   string
	Intent         string

	// ArtworkIsTrimOnly is the order's confirmation state (nil = unconfirmed).
	ArtworkIsTrimOnly *bool

	// TrimWidthPx/TrimHeightPx are set only for a post-repair recheck, where
	// api-go already knows exactly where it placed the original content -
	// never guessed from the file by services/image-python.
	TrimWidthPx  *int
	TrimHeightPx *int
}

func (c *Client) Inspect(imageBytes []byte, filename, contentType string, in InspectInput) (*InspectResult, error) {
	fields := map[string]string{
		"declared_width":  strconv.FormatFloat(in.DeclaredWidth, 'f', -1, 64),
		"declared_height": strconv.FormatFloat(in.DeclaredHeight, 'f', -1, 64),
		"declared_unit":   in.DeclaredUnit,
		"intent":          in.Intent,
	}
	if in.ArtworkIsTrimOnly != nil {
		fields["artwork_is_trim_only"] = strconv.FormatBool(*in.ArtworkIsTrimOnly)
	}
	if in.TrimWidthPx != nil {
		fields["trim_width_px"] = strconv.Itoa(*in.TrimWidthPx)
	}
	if in.TrimHeightPx != nil {
		fields["trim_height_px"] = strconv.Itoa(*in.TrimHeightPx)
	}

	body, err := c.postMultipart("/inspect", imageBytes, filename, fields)
	if err != nil {
		return nil, err
	}

	var out InspectResult
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type RepairInput struct {
	DeclaredWidth  float64
	DeclaredHeight float64
	DeclaredUnit   string
}

type RepairResult struct {
	Repaired      bool    `json:"repaired"`
	Reason        string  `json:"reason"`
	ImageBase64   string  `json:"image_base64"`
	PreviewBase64 string  `json:"preview_base64"`
	WidthPx       int     `json:"width_px"`
	HeightPx      int     `json:"height_px"`
	TrimXPx       int     `json:"trim_x_px"`
	TrimYPx       int     `json:"trim_y_px"`
	TrimWidthPx   int     `json:"trim_width_px"`
	TrimHeightPx  int     `json:"trim_height_px"`
	MarginPx      int     `json:"margin_px"`
	EffectivePPI  float64 `json:"effective_ppi"`
	EdgeColor     []int   `json:"edge_color"`
}

// Repair only ever applies to the trim-only case - the caller (worker) must
// have already confirmed that precondition (order.ArtworkIsTrimOnly==true)
// before calling this; the endpoint itself treats the whole image as the
// trim, no ambiguity to resolve.
func (c *Client) Repair(imageBytes []byte, filename string, in RepairInput) (*RepairResult, error) {
	fields := map[string]string{
		"declared_width":  strconv.FormatFloat(in.DeclaredWidth, 'f', -1, 64),
		"declared_height": strconv.FormatFloat(in.DeclaredHeight, 'f', -1, 64),
		"declared_unit":   in.DeclaredUnit,
	}

	body, err := c.postMultipart("/repair", imageBytes, filename, fields)
	if err != nil {
		return nil, err
	}

	var out RepairResult
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type ProofInput struct {
	OrderID        string
	ArtworkVersion int
	CaseVersion    int
}

type ProofResult struct {
	ImageBase64 string `json:"image_base64"`
	WidthPx     int    `json:"width_px"`
	HeightPx    int    `json:"height_px"`
}

// PrepareProof renders the customer-facing proof from whichever asset the
// caller passes as imageBytes (api-go's current asset, already RESOLVED) -
// this call makes no decision about when a proof is warranted, only renders
// one on request.
func (c *Client) PrepareProof(imageBytes []byte, filename string, in ProofInput) (*ProofResult, error) {
	fields := map[string]string{
		"order_id":        in.OrderID,
		"artwork_version": strconv.Itoa(in.ArtworkVersion),
		"case_version":    strconv.Itoa(in.CaseVersion),
	}

	body, err := c.postMultipart("/proof", imageBytes, filename, fields)
	if err != nil {
		return nil, err
	}

	var out ProofResult
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) postMultipart(path string, imageBytes []byte, filename string, fields map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(imageBytes); err != nil {
		return nil, err
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image service returned %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}
