package lc

import "testing"

func TestVisibleReasoningExtractsProviderFields(t *testing.T) {
	msg := BaseMessage{
		Type:    RoleAI,
		Content: PartsContent(ContentPart{Type: "thinking", Text: "checked constraints"}),
		AdditionalKwargs: map[string]any{
			"reasoning_details": []any{
				map[string]any{"type": "reasoning.summary", "summary": "summarized path"},
				map[string]any{"type": "reasoning.encrypted", "data": "secret"},
				map[string]any{"type": "reasoning.text", "text": "raw provider text"},
			},
		},
	}
	got := VisibleReasoning(msg)
	if len(got) != 3 {
		t.Fatalf("reasoning = %#v", got)
	}
	for _, forbidden := range got {
		if forbidden == "secret" {
			t.Fatalf("encrypted data leaked: %#v", got)
		}
	}
}
