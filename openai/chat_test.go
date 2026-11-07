package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bradthebrad/tinychain/lc"
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

func TestChatMessagesTranslateImageParts(t *testing.T) {
	messages := ChatMessages([]lc.BaseMessage{{
		Type: lc.RoleHuman,
		Content: lc.PartsContent(
			lc.ContentPart{Type: "text", Text: "look"},
			lc.ContentPart{Type: "image", Source: &lc.ContentSource{MediaType: "image/png", Data: "abc"}},
		),
	}})
	data, err := json.Marshal(messages[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"type":"image_url"`) || !strings.Contains(text, `data:image/png;base64,abc`) {
		t.Fatalf("content JSON = %s", text)
	}
}

func TestToLangChainMessagePreservesOpenRouterReasoning(t *testing.T) {
	msg := ToLangChainMessage(ChatMessage{
		Role:    "assistant",
		Content: lc.TextContent("answer"),
		ReasoningDetails: []any{
			map[string]any{"type": "reasoning.summary", "summary": "checked options"},
			map[string]any{"type": "reasoning.encrypted", "data": "opaque"},
		},
	})
	reasoning := lc.VisibleReasoning(msg)
	if len(reasoning) != 1 || reasoning[0] != "checked options" {
		t.Fatalf("reasoning = %#v", reasoning)
	}
	roundTrip := ChatMessages([]lc.BaseMessage{msg})
	if roundTrip[0].ReasoningDetails == nil {
		t.Fatalf("reasoning details were not preserved: %#v", roundTrip[0])
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

func TestChatMessagesSendToolImagesAfterTheTurnsToolMessages(t *testing.T) {
	image := lc.ContentPart{Type: "image", Source: &lc.ContentSource{Type: "base64", MediaType: "image/png", Data: "abc"}}
	messages := ChatMessages([]lc.BaseMessage{
		lc.Human("look"),
		{Type: lc.RoleAI, ToolCalls: []lc.ToolCall{{ID: "c1", Name: "screenshot"}, {ID: "c2", Name: "read"}}},
		{Type: lc.RoleTool, ToolCallID: "c1", Content: lc.PartsContent(lc.ContentPart{Type: "text", Text: "shot"}, image)},
		{Type: lc.RoleTool, ToolCallID: "c2", Content: lc.PartsContent(lc.ContentPart{Type: "text", Text: "a"}, lc.ContentPart{Type: "text", Text: "b"})},
		lc.Human("and now?"),
		{Type: lc.RoleAI, ToolCalls: []lc.ToolCall{{ID: "c3", Name: "screenshot"}}},
		{Type: lc.RoleTool, ToolCallID: "c3", Content: lc.PartsContent(image)},
	})
	data, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"role":"user","content":"look"},` +
		`{"role":"assistant","content":[],"tool_calls":[{"id":"c1","type":"function","function":{"name":"screenshot","arguments":"null"}},{"id":"c2","type":"function","function":{"name":"read","arguments":"null"}}]},` +
		`{"role":"tool","content":"shot","tool_call_id":"c1"},` +
		`{"role":"tool","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"tool_call_id":"c2"},` +
		`{"role":"user","content":[{"type":"text","text":"Image from tool screenshot (call c1):"},{"image_url":{"url":"data:image/png;base64,abc"},"type":"image_url"}]},` +
		`{"role":"user","content":"and now?"},` +
		`{"role":"assistant","content":[],"tool_calls":[{"id":"c3","type":"function","function":{"name":"screenshot","arguments":"null"}}]},` +
		`{"role":"tool","content":"[The image is in the next message.]","tool_call_id":"c3"},` +
		`{"role":"user","content":[{"type":"text","text":"Image from tool screenshot (call c3):"},{"image_url":{"url":"data:image/png;base64,abc"},"type":"image_url"}]}]`
	if string(data) != want {
		t.Fatalf("messages\n got %s\nwant %s", data, want)
	}
}
