package agent

import (
	"context"
	"strings"
	"testing"

	"tinychain/lc"
)

type compactingModel struct {
	summaryCalls int
}

func (m *compactingModel) Call(ctx context.Context, messages []lc.BaseMessage, tools []Tool) (lc.BaseMessage, error) {
	if len(tools) == 0 {
		m.summaryCalls++
		return lc.AI("summary compacted older state"), nil
	}
	return lc.AI("done"), nil
}

func TestAgentCompactsBeforeModelCall(t *testing.T) {
	model := &compactingModel{}
	var input []lc.BaseMessage
	for i := 0; i < 18; i++ {
		input = append(input, lc.Human(strings.Repeat("older context ", 120)))
	}
	input = append(input, lc.Human("recent request"))

	agent := New(Config{
		Model: model,
		Context: ContextPolicy{
			Enabled:          true,
			MaxTokens:        1000,
			ThresholdTokens:  200,
			KeepLastMessages: 4,
		},
	})
	result, err := agent.InvokeMessages(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if model.summaryCalls == 0 {
		t.Fatal("expected model-based compaction to run")
	}
	foundSummary := false
	for _, msg := range result.Messages {
		if msg.Type == lc.RoleSystem && strings.Contains(contentText(msg.Content), "summary compacted older state") {
			foundSummary = true
		}
	}
	if !foundSummary {
		t.Fatalf("compacted summary not present in result messages: %#v", result.Messages)
	}
}

type largeToolModel struct {
	calls      int
	toolResult string
}

func (m *largeToolModel) Call(ctx context.Context, messages []lc.BaseMessage, tools []Tool) (lc.BaseMessage, error) {
	m.calls++
	if m.calls == 1 {
		return lc.BaseMessage{
			Type:    lc.RoleAI,
			Content: lc.TextContent(""),
			ToolCalls: []lc.ToolCall{{
				ID:   "call_large",
				Name: "large",
				Args: map[string]any{},
			}},
		}, nil
	}
	for _, msg := range messages {
		if msg.Type == lc.RoleTool {
			m.toolResult = contentText(msg.Content)
		}
	}
	return lc.AI("done"), nil
}

func TestAgentGuardsOversizedToolResult(t *testing.T) {
	model := &largeToolModel{}
	agent := New(Config{
		Model: model,
		Tools: []Tool{ToolFunc{
			Name:        "large",
			Description: "Return a large payload.",
			Schema:      ToolSchema(map[string]any{}),
			Func: func(ctx context.Context, args map[string]any) (string, error) {
				return strings.Repeat("x", 5000), nil
			},
		}},
		Context: ContextPolicy{
			Enabled:               true,
			MaxTokens:             250,
			ThresholdTokens:       100000,
			ToolResultSafetyChars: 300,
			ReserveTokens:         50,
		},
	})
	if _, err := agent.Invoke(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.toolResult, "NullBot context guard") {
		t.Fatalf("tool result was not guarded: %q", model.toolResult)
	}
	if len(model.toolResult) > 900 {
		t.Fatalf("guarded tool result too large: %d", len(model.toolResult))
	}
}
