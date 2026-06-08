package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"tinychain/lc"
)

func TestChatMessagesConvertLangChainRolesAndToolCalls(t *testing.T) {
	messages := ChatMessages([]lc.BaseMessage{{
		Type:    lc.RoleAI,
		Content: lc.TextContent(""),
		ToolCalls: []lc.ToolCall{{
			ID:   "call_1",
			Name: "lookup",
			Args: map[string]any{"q": "go"},
		}},
	}})

	if messages[0].Role != "assistant" {
		t.Fatalf("role = %q", messages[0].Role)
	}
	if messages[0].ToolCalls[0].Function.Arguments != `{"q":"go"}` {
		t.Fatalf("arguments = %s", messages[0].ToolCalls[0].Function.Arguments)
	}

	data, err := json.Marshal(ChatCompletionRequest{Model: "gpt-test", Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("empty JSON")
	}
}

func TestClientRetriesTransientStatus(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			http.Error(w, "upstream timeout", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"id":"chatcmpl_test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL}
	resp, err := client.ChatCompletion(context.Background(), ChatCompletionRequest{
		Model: "test",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: lc.TextContent("hi"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d", attempts)
	}
	if got := *resp.Choices[0].Message.Content.Text; got != "ok" {
		t.Fatalf("content = %q", got)
	}
}
