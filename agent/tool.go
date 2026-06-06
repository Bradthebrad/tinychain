package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"tinychain/lc"
)

type Tool interface {
	Definition() lc.ToolDefinition
	Call(ctx context.Context, args map[string]any) (string, error)
}

type ToolFunc struct {
	Name        string
	Description string
	Schema      map[string]any
	Func        func(context.Context, map[string]any) (string, error)
}

func (t ToolFunc) Definition() lc.ToolDefinition {
	return lc.ToolDefinition{
		Name:        t.Name,
		Description: t.Description,
		ArgsSchema:  t.Schema,
	}
}

func (t ToolFunc) Call(ctx context.Context, args map[string]any) (string, error) {
	if t.Func == nil {
		return "", fmt.Errorf("agent: tool %q has no function", t.Name)
	}
	return t.Func(ctx, args)
}

func ToolSchema(properties map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}

func StringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func BoolProperty(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func NumberProperty(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
}

func parseArgs(raw string) map[string]any {
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return map[string]any{}
	}
	return args
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}
