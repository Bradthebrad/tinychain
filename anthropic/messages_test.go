package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Bradthebrad/tinychain/lc"
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
	if len(reasoning) != 0 {
		t.Fatalf("reasoning = %#v", reasoning)
	}
	if msg.Content.Parts[0].Type != "thinking" || msg.Content.Parts[0].Text != "considered the edge cases" {
		t.Fatalf("thinking part = %#v", msg.Content.Parts[0])
	}
}

func strPtr(value string) *string {
	return &value
}

func TestToolCallsAreSentOnce(t *testing.T) {
	msg := lc.BaseMessage{
		Type:      lc.RoleAI,
		Content:   lc.PartsContent(lc.ContentPart{Type: "text", Text: "reading"}, lc.ContentPart{Type: "tool_use", ID: "t1", Name: "read", Input: map[string]any{"path": "long original"}}),
		ToolCalls: []lc.ToolCall{{ID: "t1", Name: "read", Args: map[string]any{"path": "short"}}},
	}
	blocks := contentFromLC(msg).Blocks
	count := 0
	for _, block := range blocks {
		if block.Type == "tool_use" {
			count++
			if block.Input["path"] != "short" {
				t.Fatalf("sent the stale copy: %v", block.Input)
			}
		}
	}
	if count != 1 {
		t.Fatalf("%d tool_use blocks for one call", count)
	}
}
