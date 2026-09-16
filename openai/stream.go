package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/Bradthebrad/tinychain/lc"
	"github.com/Bradthebrad/tinychain/streaming"
)

func (c Client) stream(ctx context.Context, path string, in any, handle func([]byte) error) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
		return fmt.Errorf("openai: status %d: %s", resp.StatusCode, b)
	}
	return streaming.ReadSSE(resp.Body, handle)
}

func (c Client) chatStream(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
	yes := true
	req.Stream = &yes
	req.StreamOptions = map[string]any{"include_usage": true}
	out := &ChatCompletionResponse{}
	msg := ChatMessage{Role: "assistant"}
	var text strings.Builder
	calls := map[int]*ToolCall{}
	done := false
	err := c.stream(ctx, "/chat/completions", req, func(b []byte) error {
		if string(b) == "[DONE]" {
			done = true
			return nil
		}
		var e struct {
			ID      string          `json:"id"`
			Model   string          `json:"model"`
			Usage   *Usage          `json:"usage"`
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Index  int    `json:"index"`
				Finish string `json:"finish_reason"`
				Delta  struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int          `json:"index"`
						ID       string       `json:"id"`
						Type     string       `json:"type"`
						Function FunctionCall `json:"function"`
					} `json:"tool_calls"`
					ReasoningDetails []struct {
						Type    string `json:"type"`
						Summary string `json:"summary"`
					} `json:"reasoning_details"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(b, &e); err != nil {
			return err
		}
		if len(e.Error) > 0 {
			return fmt.Errorf("openai stream: %s", e.Error)
		}
		if e.ID != "" {
			out.ID = e.ID
		}
		if e.Model != "" {
			out.Model = e.Model
		}
		if e.Usage != nil {
			out.Usage = e.Usage
		}
		for _, ch := range e.Choices {
			if ch.Index != 0 {
				continue
			}
			text.WriteString(ch.Delta.Content)
			streaming.Emit(ctx, ch.Delta.Content, false)
			for _, r := range ch.Delta.ReasoningDetails {
				if r.Type == "reasoning.summary" {
					streaming.Emit(ctx, r.Summary, true)
				}
			}
			for _, t := range ch.Delta.ToolCalls {
				v := calls[t.Index]
				if v == nil {
					v = &ToolCall{}
					calls[t.Index] = v
				}
				v.ID += t.ID
				if t.Type != "" {
					v.Type = t.Type
				}
				v.Function.Name += t.Function.Name
				v.Function.Arguments += t.Function.Arguments
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !done {
		return nil, fmt.Errorf("openai: truncated chat stream")
	}
	keys := make([]int, 0, len(calls))
	for k := range calls {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		msg.ToolCalls = append(msg.ToolCalls, *calls[k])
	}
	msg.Content = lc.TextContent(text.String())
	out.Choices = []ChatChoice{{Message: msg}}
	return out, nil
}

func (c Client) responsesStream(ctx context.Context, req ResponsesRequest) (*ResponsesResponse, error) {
	yes := true
	req.Stream = &yes
	var out *ResponsesResponse
	err := c.stream(ctx, "/responses", req, func(b []byte) error {
		if string(b) == "[DONE]" {
			return nil
		}
		var e struct {
			Type     string             `json:"type"`
			Delta    string             `json:"delta"`
			Response *ResponsesResponse `json:"response"`
		}
		if err := json.Unmarshal(b, &e); err != nil {
			return err
		}
		switch e.Type {
		case "response.output_text.delta":
			streaming.Emit(ctx, e.Delta, false)
		case "response.reasoning_summary_text.delta":
			streaming.Emit(ctx, e.Delta, true)
		case "response.completed":
			out = e.Response
		case "response.failed", "response.incomplete", "error":
			return fmt.Errorf("openai: %s", e.Type)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("openai: truncated responses stream")
	}
	return out, nil
}
