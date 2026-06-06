package agent

import (
	"context"
	"strings"
	"testing"

	"tinychain/lc"
)

type fakeModel struct {
	calls int
}

func (m *fakeModel) Call(ctx context.Context, messages []lc.BaseMessage, tools []Tool) (lc.BaseMessage, error) {
	m.calls++
	if m.calls == 1 {
		return lc.BaseMessage{
			Type:    lc.RoleAI,
			Content: lc.TextContent(""),
			ToolCalls: []lc.ToolCall{{
				ID:   "call_1",
				Name: "echo",
				Args: map[string]any{"text": "hello"},
			}},
		}, nil
	}
	return lc.AI("final answer"), nil
}

func TestAgentRunsToolLoop(t *testing.T) {
	model := &fakeModel{}
	a := New(Config{
		Model: model,
		Tools: []Tool{ToolFunc{
			Name:        "echo",
			Description: "Echo text.",
			Schema: ToolSchema(map[string]any{
				"text": StringProperty("Text to echo."),
			}, "text"),
			Func: func(ctx context.Context, args map[string]any) (string, error) {
				return stringArg(args, "text"), nil
			},
		}},
	})

	result, err := a.Invoke(context.Background(), "start")
	if err != nil {
		t.Fatal(err)
	}
	if result.Steps != 2 {
		t.Fatalf("steps = %d", result.Steps)
	}
	if text := contentText(result.Output.Content); text != "final answer" {
		t.Fatalf("output = %q", text)
	}
}

func TestComposeSystemPromptIncludesSkillsAndMemory(t *testing.T) {
	prompt := ComposeSystemPrompt("base", []Skill{{
		Name:        "research",
		Description: "Research things.",
		Path:        "/skills/research/SKILL.md",
	}}, []Memory{{Path: "/AGENTS.md", Content: "Use short answers."}})

	for _, want := range []string{"base", "research", "/skills/research/SKILL.md", "Use short answers."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
