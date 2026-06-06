package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	DefaultBaseURL = "https://api.anthropic.com/v1"
	DefaultVersion = "2023-06-01"
)

type Client struct {
	APIKey     string
	BaseURL    string
	Version    string
	HTTPClient *http.Client
}

func (c Client) Messages(ctx context.Context, req MessageRequest) (*MessageResponse, error) {
	var out MessageResponse
	if err := c.post(ctx, "/messages", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c Client) post(ctx context.Context, path string, in any, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	version := c.Version
	if version == "" {
		version = DefaultVersion
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", version)
	if c.APIKey != "" {
		httpReq.Header.Set("x-api-key", c.APIKey)
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, string(data))
	}
	return json.Unmarshal(data, out)
}
