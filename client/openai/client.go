package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const DefaultBaseURL = "https://api.openai.com/v1"

type Client struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

func (c Client) ChatCompletion(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
	var out ChatCompletionResponse
	if err := c.post(ctx, "/chat/completions", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c Client) Responses(ctx context.Context, req ResponsesRequest) (*ResponsesResponse, error) {
	var out ResponsesResponse
	if err := c.post(ctx, "/responses", req, &out); err != nil {
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
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
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
		return fmt.Errorf("openai: status %d: %s", resp.StatusCode, string(data))
	}
	return json.Unmarshal(data, out)
}
