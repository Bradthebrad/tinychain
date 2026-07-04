package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Bradthebrad/tinychain/lc"
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
		ArgsSchema:  NormalizeToolSchema(t.Schema),
	}
}

func (t ToolFunc) Call(ctx context.Context, args map[string]any) (string, error) {
	if t.Func == nil {
		return "", fmt.Errorf("agent: tool %q has no function", t.Name)
	}
	return t.Func(ctx, args)
}

func ToolSchema(properties map[string]any, required ...string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}

func NormalizeToolSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return ToolSchema(map[string]any{})
	}
	out := normalizeToolSchemaMap(schema)
	if strings.TrimSpace(fmt.Sprint(out["type"])) == "" {
		out["type"] = "object"
	}
	if _, ok := out["properties"]; !ok || out["properties"] == nil {
		out["properties"] = map[string]any{}
	}
	ensureRequiredArray(out)
	return out
}

func normalizeToolSchemaMap(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema)+2)
	for key, value := range schema {
		out[key] = normalizeToolSchemaValue(value)
	}
	if properties, ok := out["properties"]; ok && properties == nil {
		out["properties"] = map[string]any{}
	}
	if _, ok := out["required"]; ok {
		ensureRequiredArray(out)
	}
	return out
}

func normalizeToolSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return normalizeToolSchemaMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = normalizeToolSchemaValue(item)
		}
		return out
	default:
		return value
	}
}

func ensureRequiredArray(out map[string]any) {
	required, ok := out["required"]
	if !ok || required == nil {
		out["required"] = []string{}
		return
	}
	switch typed := required.(type) {
	case []string:
		if typed == nil {
			out["required"] = []string{}
		}
	case []any:
		if typed == nil {
			out["required"] = []string{}
		}
	default:
		out["required"] = []string{}
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
