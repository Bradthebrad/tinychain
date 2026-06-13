package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"tinychain/lc"
)

func TestMessageInputUsesOutputTextForAssistantHistory(t *testing.T) {
	input := MessageInput([]lc.BaseMessage{
		lc.Human("hello"),
		lc.AI("hi there"),
	})

	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var items []ResponsesInputItem
	if err := json.Unmarshal(data, &items); err != nil {
		t.Fatal(err)
	}
	if got := items[0].Content[0].Type; got != "input_text" {
		t.Fatalf("user content type = %q", got)
	}
	if got := items[1].Content[0].Type; got != "output_text" {
		t.Fatalf("assistant content type = %q", got)
	}
}

func TestMessageInputEncodesToolCallHistory(t *testing.T) {
	input := MessageInput([]lc.BaseMessage{
		{
			Type:    lc.RoleAI,
			Content: lc.TextContent(""),
			ToolCalls: []lc.ToolCall{{
				ID:   "call_1",
				Name: "skills_list",
				Args: map[string]any{},
			}},
		},
		lc.Tool("call_1", "skill-a\nskill-b"),
	})

	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var items []ResponsesInputItem
	if err := json.Unmarshal(data, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].Type != "function_call" || items[0].CallID != "call_1" || items[0].Name != "skills_list" {
		t.Fatalf("function call item = %#v", items[0])
	}
	if items[1].Type != "function_call_output" || items[1].CallID != "call_1" || items[1].Output == "" {
		t.Fatalf("function output item = %#v", items[1])
	}
}

func TestMessageInputTranslatesImageParts(t *testing.T) {
	input := MessageInput([]lc.BaseMessage{{
		Type: lc.RoleHuman,
		Content: lc.PartsContent(
			lc.ContentPart{Type: "text", Text: "look"},
			lc.ContentPart{Type: "image", Source: &lc.ContentSource{MediaType: "image/png", Data: "abc"}},
		),
	}})

	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var items []ResponsesInputItem
	if err := json.Unmarshal(data, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(items[0].Content) != 2 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].Content[1].Type != "input_image" || !strings.Contains(items[0].Content[1].ImageURL, "data:image/png;base64,abc") {
		t.Fatalf("image content = %#v", items[0].Content[1])
	}
}
