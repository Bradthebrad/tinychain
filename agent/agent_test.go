package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Bradthebrad/tinychain/callbacks"
	"github.com/Bradthebrad/tinychain/lc"
	"github.com/Bradthebrad/tinychain/openai"
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

type reasoningModel struct{}

func (m reasoningModel) Call(ctx context.Context, messages []lc.BaseMessage, tools []Tool) (lc.BaseMessage, error) {
	return lc.BaseMessage{
		Type:    lc.RoleAI,
		Content: lc.TextContent("answer"),
		AdditionalKwargs: map[string]any{
			"reasoning": "visible reasoning",
		},
	}, nil
}

func TestAgentRunsToolLoop(t *testing.T) {
	model := &fakeModel{}
	var events []callbacks.EventName
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
		Callbacks: callbacks.SinkFunc(func(event callbacks.Event) {
			events = append(events, event.Event)
		}),
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
	if !containsEvent(events, callbacks.EventToolStart) || !containsEvent(events, callbacks.EventToolEnd) {
		t.Fatalf("tool callbacks missing: %#v", events)
	}
}

func TestAgentEmitsVisibleReasoningCallback(t *testing.T) {
	var reasoning []string
	a := New(Config{
		Model: reasoningModel{},
		Callbacks: callbacks.SinkFunc(func(event callbacks.Event) {
			if event.Event == callbacks.EventLLMReasoning {
				reasoning = append(reasoning, event.Data.Token)
			}
		}),
	})
	if _, err := a.Invoke(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	if len(reasoning) != 1 || reasoning[0] != "visible reasoning" {
		t.Fatalf("reasoning callbacks = %#v", reasoning)
	}
}

func containsEvent(events []callbacks.EventName, want callbacks.EventName) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
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

func TestReasoningEffortMapping(t *testing.T) {
	if got := normalizeReasoningEffort("xhigh"); got != "high" {
		t.Fatalf("xhigh maps to %q", got)
	}
	reasoning := openAIReasoning("medium")
	obj, ok := reasoning.(map[string]any)
	if !ok || obj["effort"] != "medium" || obj["summary"] != "auto" {
		t.Fatalf("openai reasoning = %#v", reasoning)
	}
	thinking, outputConfig, maxTokens := anthropicThinking("claude-opus-4-8", "high", 1024)
	obj, ok = thinking.(map[string]any)
	if !ok || obj["type"] != "adaptive" || obj["display"] != "summarized" || outputConfig == nil || outputConfig.Effort != "high" || maxTokens != 1024 {
		t.Fatalf("anthropic adaptive thinking = %#v output=%#v max=%d", thinking, outputConfig, maxTokens)
	}
	thinking, outputConfig, maxTokens = anthropicThinking("claude-3-7-sonnet-20250219", "high", 1024)
	obj, ok = thinking.(map[string]any)
	if !ok || obj["type"] != "enabled" || obj["budget_tokens"] != 8192 || obj["display"] != "summarized" || outputConfig == nil || outputConfig.Effort != "high" || maxTokens <= 8192 {
		t.Fatalf("anthropic manual thinking = %#v output=%#v max=%d", thinking, outputConfig, maxTokens)
	}
}

func TestOpenAIResponseReasoningSummaryIsPreserved(t *testing.T) {
	msg := messageFromOpenAIResponse(&openai.ResponsesResponse{
		ID:     "resp_1",
		Model:  "gpt-test",
		Status: "completed",
		Output: []openai.ResponsesOutputItem{
			{
				ID:      "rs_1",
				Type:    "reasoning",
				Summary: []openai.ResponsesSummary{{Type: "summary_text", Text: "checked the constraints"}},
			},
			{
				Type:    "message",
				Content: []openai.ResponsesContent{{Type: "output_text", Text: "done"}},
			},
		},
	})
	if got := contentText(msg.Content); got != "done" {
		t.Fatalf("content = %q", got)
	}
	reasoning := lc.VisibleReasoning(msg)
	if len(reasoning) != 1 || reasoning[0] != "checked the constraints" {
		t.Fatalf("reasoning = %#v", reasoning)
	}
}
