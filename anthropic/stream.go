package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Bradthebrad/tinychain/streaming"
	"io"
	"net/http"
	"strings"
)

func (c Client) messagesStream(ctx context.Context, req MessageRequest) (*MessageResponse, error) {
	yes := true
	req.Stream = &yes
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	version := c.Version
	if version == "" {
		version = DefaultVersion
	}
	r, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "text/event-stream")
	r.Header.Set("anthropic-version", version)
	r.Header.Set("x-api-key", c.APIKey)
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
		return nil, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, b)
	}
	out := &MessageResponse{}
	args := map[int]string{}
	done := false
	err = streaming.ReadSSE(resp.Body, func(b []byte) error {
		var e struct {
			Type         string           `json:"type"`
			Index        int              `json:"index"`
			Message      *MessageResponse `json:"message"`
			ContentBlock ContentBlock     `json:"content_block"`
			Usage        Usage            `json:"usage"`
			Delta        struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(b, &e); err != nil {
			return err
		}
		switch e.Type {
		case "error":
			return fmt.Errorf("anthropic: stream error")
		case "message_start":
			if e.Message != nil {
				out = e.Message
			}
		case "content_block_start":
			if e.Index < 0 || e.Index > 4096 {
				return fmt.Errorf("anthropic: invalid block index")
			}
			for len(out.Content) <= e.Index {
				out.Content = append(out.Content, ContentBlock{})
			}
			out.Content[e.Index] = e.ContentBlock
		case "content_block_delta":
			if e.Index < 0 || e.Index >= len(out.Content) {
				return fmt.Errorf("anthropic: invalid delta index")
			}
			v := &out.Content[e.Index]
			switch e.Delta.Type {
			case "text_delta":
				v.Text += e.Delta.Text
				streaming.Emit(ctx, e.Delta.Text, false)
			case "thinking_delta":
				v.Thinking += e.Delta.Thinking // Keep provider continuation state private; no raw thinking events.
			case "signature_delta":
				v.Signature += e.Delta.Signature
			case "input_json_delta":
				args[e.Index] += e.Delta.PartialJSON
			}
		case "content_block_stop":
			if raw := args[e.Index]; raw != "" {
				if err := json.Unmarshal([]byte(raw), &out.Content[e.Index].Input); err != nil {
					return err
				}
			}
		case "message_delta":
			out.StopReason = e.Delta.StopReason
			out.Usage.OutputTokens = e.Usage.OutputTokens
		case "message_stop":
			done = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !done {
		return nil, fmt.Errorf("anthropic: truncated stream")
	}
	return out, nil
}
