package agent

import "context"

// ToolInfo is optional static metadata a tool can expose for UIs, activity
// feeds and permission policies. Hints mirror MCP tool annotations and are
// advisory only.
type ToolInfo struct {
	Title       string `json:"title,omitempty"`
	Server      string `json:"server,omitempty"`
	ServerTitle string `json:"server_title,omitempty"`
	Source      string `json:"source,omitempty"`
	ReadOnly    *bool  `json:"read_only,omitempty"`
	Destructive *bool  `json:"destructive,omitempty"`
	Idempotent  *bool  `json:"idempotent,omitempty"`
	OpenWorld   *bool  `json:"open_world,omitempty"`
}

// InfoTool is implemented by tools that expose ToolInfo.
type InfoTool interface {
	Tool
	ToolInfo() ToolInfo
}

// MetaTool is implemented by tools that can return a structured summary of a
// call (item counts, content types, links) alongside the text for the model.
type MetaTool interface {
	Tool
	CallWithMeta(ctx context.Context, args map[string]any) (string, map[string]any, error)
}

// InfoOf returns a tool's metadata when it provides any.
func InfoOf(tool Tool) (ToolInfo, bool) {
	if described, ok := tool.(InfoTool); ok {
		return described.ToolInfo(), true
	}
	return ToolInfo{}, false
}

// Map converts the info to callback metadata, omitting empty fields.
func (i ToolInfo) Map() map[string]any {
	out := map[string]any{}
	set := func(key, value string) {
		if value != "" {
			out[key] = value
		}
	}
	set("title", i.Title)
	set("server", i.Server)
	set("server_title", i.ServerTitle)
	set("source", i.Source)
	setBool := func(key string, value *bool) {
		if value != nil {
			out[key] = *value
		}
	}
	setBool("read_only", i.ReadOnly)
	setBool("destructive", i.Destructive)
	setBool("idempotent", i.Idempotent)
	setBool("open_world", i.OpenWorld)
	if len(out) == 0 {
		return nil
	}
	return out
}

type toolCallIDKey struct{}

// WithToolCallID records the provider tool call id on a context.
func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallIDKey{}, id)
}

// ToolCallID returns the provider tool call id for the running tool, if any.
func ToolCallID(ctx context.Context) string {
	id, _ := ctx.Value(toolCallIDKey{}).(string)
	return id
}
