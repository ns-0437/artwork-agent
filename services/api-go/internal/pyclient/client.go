// Package pyclient calls services/image-python over HTTP. Go forwards raw
// image bytes it already fetched from storage plus the order's declared
// dimensions/intent, so the Python side never needs to know which storage
// backend is active or reach back into Postgres itself.
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
	WidthPx  int           `json:"width_px"`
	HeightPx int           `json:"height_px"`
	Mode     string        `json:"mode"`
	Format   string        `json:"format"`
	Checks   []CheckResult `json:"checks"`
}

type InspectInput struct {
	DeclaredWidth  float64
	DeclaredHeight float64
	DeclaredUnit   string
	Intent         string
}

func (c *Client) Inspect(imageBytes []byte, filename, contentType string, in InspectInput) (*InspectResult, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(imageBytes); err != nil {
		return nil, err
	}

	fields := map[string]string{
		"declared_width":  strconv.FormatFloat(in.DeclaredWidth, 'f', -1, 64),
		"declared_height": strconv.FormatFloat(in.DeclaredHeight, 'f', -1, 64),
		"declared_unit":   in.DeclaredUnit,
		"intent":          in.Intent,
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/inspect", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("image service returned %d: %s", resp.StatusCode, string(body))
	}

	var out InspectResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}
