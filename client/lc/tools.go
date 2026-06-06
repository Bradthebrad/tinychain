package lc

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	ArgsSchema  map[string]any `json:"args_schema,omitempty"`
}
