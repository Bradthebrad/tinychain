package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"tinychain/lc"
)

func TestMessagesSeparateSystemAndToolResult(t *testing.T) {
	input := []lc.BaseMessage{
		lc.System("be brief"),
		lc.Human("hi"),
		lc.Tool("toolu_1", "42"),
	}

	system := SystemFromMessages(input)
	systemJSON, err := json.Marshal(system)
	if err != nil {
		t.Fatal(err)
	}
	if string(systemJSON) != `"be brief"` {
		t.Fatalf("system JSON = %s", systemJSON)
	}

	messages := Messages(input)
	if len(messages) != 2 {
		t.Fatalf("message count = %d", len(messages))
	}
	if messages[1].Content.Blocks[0].Type != "tool_result" {
		t.Fatalf("tool block type = %q", messages[1].Content.Blocks[0].Type)
	}
}

func TestClientRetriesTransientStatus(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			http.Error(w, "overloaded", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL}
	resp, err := client.Messages(context.Background(), MessageRequest{
		Model:     "claude-test",
		MaxTokens: 32,
		Messages: []Message{{
			Role:    "user",
			Content: ContentList{Text: strPtr("hi")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d", attempts)
	}
	if got := resp.Content[0].Text; got != "ok" {
		t.Fatalf("content = %q", got)
	}
}

func TestThinkingBlocksArePreservedAsReasoning(t *testing.T) {
	msg := ToLangChainMessage(MessageResponse{
		ID:    "msg_1",
		Role:  "assistant",
		Model: "claude-test",
		Content: []ContentBlock{
			{Type: "thinking", Thinking: "considered the edge cases", Signature: "sig"},
			{Type: "text", Text: "answer"},
		},
		Usage: Usage{InputTokens: 1, OutputTokens: 2},
	})
	reasoning := lc.VisibleReasoning(msg)
	if len(reasoning) != 1 || reasoning[0] != "considered the edge cases" {
		t.Fatalf("reasoning = %#v", reasoning)
	}
	if msg.Content.Parts[0].Type != "thinking" || msg.Content.Parts[0].Text != "considered the edge cases" {
		t.Fatalf("thinking part = %#v", msg.Content.Parts[0])
	}
}

func strPtr(value string) *string {
	return &value
}
