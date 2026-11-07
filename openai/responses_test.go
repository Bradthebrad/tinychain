package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Bradthebrad/tinychain/lc"
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

func TestFunctionCallOutputCarriesImages(t *testing.T) {
	input := MessageInput([]lc.BaseMessage{
		{Type: lc.RoleAI, ToolCalls: []lc.ToolCall{{ID: "c1", Name: "screenshot"}, {ID: "c2", Name: "noop"}}},
		{Type: lc.RoleTool, ToolCallID: "c1", Content: lc.PartsContent(
			lc.ContentPart{Type: "text", Text: "shot"},
			lc.ContentPart{Type: "image", Source: &lc.ContentSource{Type: "base64", MediaType: "image/png", Data: "abc"}},
		)},
		lc.Tool("c2", ""),
	})
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"type":"function_call","call_id":"c1","name":"screenshot","arguments":"null"},` +
		`{"type":"function_call","call_id":"c2","name":"noop","arguments":"null"},` +
		`{"type":"function_call_output","call_id":"c1","output":[{"type":"input_text","text":"shot"},{"type":"input_image","image_url":"data:image/png;base64,abc"}]},` +
		`{"type":"function_call_output","call_id":"c2","output":""}]`
	if string(data) != want {
		t.Fatalf("input\n got %s\nwant %s", data, want)
	}
	var back ResponsesInput
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if got := back.Items[2]; got.Output != "shot" || len(got.OutputContent) != 2 || got.OutputContent[1].ImageURL != "data:image/png;base64,abc" {
		t.Fatalf("decoded = %#v", got)
	}
	if got := back.Items[3]; got.Output != "" || got.OutputContent != nil || got.CallID != "c2" {
		t.Fatalf("decoded = %#v", got)
	}
	again, _ := json.Marshal(back)
	if string(again) != want {
		t.Fatalf("round trip = %s", again)
	}
}
