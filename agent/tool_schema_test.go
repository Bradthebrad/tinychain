package agent

import (
	"encoding/json"
	"testing"
)

// A property may be named after a schema keyword: Discord's command options
// have one called "required". Its schema must come through as a schema.
func TestNormalizeToolSchemaKeepsPropertiesNamedLikeKeywords(t *testing.T) {
	option := map[string]any{"type": "object", "required": []string{"name"}, "properties": map[string]any{
		"name":       map[string]any{"type": "string"},
		"required":   map[string]any{"type": "boolean", "description": "Required options come first."},
		"properties": map[string]any{"type": "string"},
	}}
	in := map[string]any{"type": "object", "properties": map[string]any{
		"commands": map[string]any{"type": "array", "items": option},
		"mode":     map[string]any{"type": "string", "enum": []any{"a", "b"}, "default": map[string]any{"required": "x"}},
	}, "$defs": map[string]any{"required": map[string]any{"type": "integer"}}}
	out := NormalizeToolSchema(in)
	data, _ := json.Marshal(out)
	var got struct {
		Properties struct {
			Commands struct {
				Items struct {
					Required   []string `json:"required"`
					Properties map[string]map[string]any
				}
			}
			Mode struct {
				Default map[string]any
			}
		}
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	props := got.Properties.Commands.Items.Properties
	if props["required"]["type"] != "boolean" || props["properties"]["type"] != "string" {
		t.Fatalf("a property named like a keyword was changed: %s", data)
	}
	if len(got.Properties.Commands.Items.Required) != 1 {
		t.Fatalf("the real required list was lost: %s", data)
	}
	if got.Properties.Mode.Default["required"] != "x" {
		t.Fatalf("a default value was rewritten: %s", data)
	}
	if got.Defs["required"]["type"] != "integer" {
		t.Fatalf("a definition named required was changed: %s", data)
	}
	// Schemas without a required list still get one.
	if r, ok := out["required"].([]string); !ok || r == nil {
		t.Fatalf("top-level required: %#v", out["required"])
	}
}
