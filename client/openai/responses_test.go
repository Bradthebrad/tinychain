package openai

import (
	"encoding/json"
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
