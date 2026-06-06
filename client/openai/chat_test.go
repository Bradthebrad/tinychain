package openai

import (
	"encoding/json"
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
