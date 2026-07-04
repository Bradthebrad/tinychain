package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Bradthebrad/tinychain/lc"
)

type Todo struct {
	Content  string `json:"content"`
	Status   string `json:"status,omitempty"`
	Priority string `json:"priority,omitempty"`
}

func DefaultTools() []Tool {
	return []Tool{WriteTodosTool()}
}

func WriteTodosTool() Tool {
	var todos []Todo
	return ToolFunc{
		Name:        "write_todos",
		Description: "Create or replace the agent's todo list for complex multi-step work.",
		Schema: ToolSchema(map[string]any{
			"todos": map[string]any{
				"type":        "array",
				"description": "Todo items. Each item should include content and may include status and priority.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"content":  StringProperty("The work item."),
						"status":   StringProperty("pending, in_progress, or completed."),
						"priority": StringProperty("low, medium, or high."),
					},
					"required": []string{"content"},
				},
			},
		}, "todos"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			data, err := json.Marshal(args["todos"])
			if err != nil {
				return "", err
			}
			var next []Todo
			if err := json.Unmarshal(data, &next); err != nil {
				return "", err
			}
			todos = next
			return formatTodos(todos), nil
		},
	}
}

type Subagent struct {
	Name          string
	Description   string
	SystemPrompt  string
	Model         Model
	Tools         []Tool
	Skills        []Skill
	Memory        []Memory
	MaxIterations int
}

func TaskTool(subagents []Subagent) Tool {
	byName := map[string]Subagent{}
	for _, subagent := range subagents {
		byName[subagent.Name] = subagent
	}
	return ToolFunc{
		Name:        "task",
		Description: taskDescription(subagents),
		Schema: ToolSchema(map[string]any{
			"description":   StringProperty("Detailed task for the subagent to perform autonomously, including context and expected output."),
			"subagent_type": StringProperty("The subagent name to use."),
		}, "description", "subagent_type"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			name := stringArg(args, "subagent_type")
			spec, ok := byName[name]
			if !ok {
				return "", fmt.Errorf("unknown subagent %q", name)
			}
			if spec.Model == nil {
				return "", fmt.Errorf("subagent %q has no model", name)
			}
			sub := New(Config{
				Model:         spec.Model,
				SystemPrompt:  defaultSubagentPrompt(spec.SystemPrompt),
				Tools:         spec.Tools,
				Skills:        spec.Skills,
				Memory:        spec.Memory,
				MaxIterations: spec.MaxIterations,
			})
			result, err := sub.InvokeMessages(ctx, []lc.BaseMessage{lc.Human(stringArg(args, "description"))})
			if err != nil {
				return "", err
			}
			return contentText(result.Output.Content), nil
		},
	}
}

func formatTodos(todos []Todo) string {
	if len(todos) == 0 {
		return "todo list is empty"
	}
	var b strings.Builder
	for i, todo := range todos {
		status := todo.Status
		if status == "" {
			status = "pending"
		}
		if todo.Priority != "" {
			fmt.Fprintf(&b, "%d. [%s/%s] %s\n", i+1, status, todo.Priority, todo.Content)
		} else {
			fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, status, todo.Content)
		}
	}
	return strings.TrimSpace(b.String())
}

func taskDescription(subagents []Subagent) string {
	var b strings.Builder
	b.WriteString("Launch an isolated subagent for complex, multi-step tasks. Available subagents:")
	for _, subagent := range subagents {
		fmt.Fprintf(&b, "\n- %s: %s", subagent.Name, subagent.Description)
	}
	return b.String()
}

func defaultSubagentPrompt(prompt string) string {
	base := "You are an isolated subagent. Complete the task autonomously and return one final assistant message. The parent agent sees only your final message."
	if strings.TrimSpace(prompt) == "" {
		return base
	}
	return strings.TrimSpace(prompt) + "\n\n" + base
}

func contentText(content lc.Content) string {
	if content.Text != nil {
		return *content.Text
	}
	var parts []string
	for _, part := range content.Parts {
		if part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}
