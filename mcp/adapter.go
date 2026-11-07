package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/lc"
)

func AgentTools(ctx context.Context, client *Client) ([]agent.Tool, error) {
	return AgentToolsForServer(ctx, client, "", "")
}

// AgentToolsForServer lists a server's tools and adapts them, tagging each with
// the server id and display title so callbacks and UIs can attribute calls.
func AgentToolsForServer(ctx context.Context, client *Client, serverID, serverTitle string) ([]agent.Tool, error) {
	tools, err := client.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	return AdaptTools(client, tools, serverID, serverTitle), nil
}

// AdaptTools wraps already-listed tools without another tools/list round trip.
func AdaptTools(client *Client, tools []Tool, serverID, serverTitle string) []agent.Tool {
	out := make([]agent.Tool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, &RemoteTool{Client: client, Spec: tool, ServerID: serverID, ServerTitle: serverTitle})
	}
	return out
}

func AgentTool(client *Client, tool Tool) agent.Tool {
	return &RemoteTool{Client: client, Spec: tool}
}

// RemoteTool is an agent.Tool backed by an MCP server. It implements
// agent.ContentTool, so the agent sends the images in its results to the
// model; Call and CallWithMeta describe them in the text instead.
type RemoteTool struct {
	Client      *Client
	Spec        Tool
	ServerID    string
	ServerTitle string
}

func (t *RemoteTool) Definition() lc.ToolDefinition {
	return lc.ToolDefinition{
		Name:        t.Spec.Name,
		Description: t.Spec.Description,
		ArgsSchema:  agent.NormalizeToolSchema(t.Spec.InputSchema),
	}
}

func (t *RemoteTool) Call(ctx context.Context, args map[string]any) (string, error) {
	out, _, err := t.CallWithMeta(ctx, args)
	return out, err
}

// CallWithMeta returns the text for the model plus a small structured summary
// of the result for activity displays.
func (t *RemoteTool) CallWithMeta(ctx context.Context, args map[string]any) (string, map[string]any, error) {
	if t.Client == nil {
		return "", nil, errors.New("mcp: tool has no client")
	}
	result, err := t.Client.CallTool(ctx, t.Spec.Name, args)
	if err != nil {
		return "", nil, err
	}
	text := ResultText(result)
	meta := ResultMeta(result)
	if result.IsError {
		if strings.TrimSpace(text) == "" {
			text = "tool reported an error"
		}
		return "", meta, errors.New(text)
	}
	return text, meta, nil
}

// CallContent returns the result's text and images for the model, plus the
// summary CallWithMeta returns. See ResultContent for how blocks convert.
func (t *RemoteTool) CallContent(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
	if t.Client == nil {
		return agent.ToolOutput{}, errors.New("mcp: tool has no client")
	}
	result, err := t.Client.CallTool(ctx, t.Spec.Name, args)
	if err != nil {
		return agent.ToolOutput{}, err
	}
	if result.IsError {
		text := ResultText(result)
		if strings.TrimSpace(text) == "" {
			text = "tool reported an error"
		}
		return agent.ToolOutput{Meta: ResultMeta(result)}, errors.New(text)
	}
	out := ResultContent(result)
	out.Meta = ResultMeta(result)
	return out, nil
}

func (t *RemoteTool) ToolInfo() agent.ToolInfo {
	info := agent.ToolInfo{
		Title:       t.Spec.DisplayTitle(),
		Server:      t.ServerID,
		ServerTitle: t.ServerTitle,
		Source:      "mcp",
	}
	if a := t.Spec.Annotations; a != nil {
		info.ReadOnly = a.ReadOnlyHint
		info.Destructive = a.DestructiveHint
		info.Idempotent = a.IdempotentHint
		info.OpenWorld = a.OpenWorldHint
	}
	return info
}

func ToolFromAgent(tool agent.Tool) Tool {
	def := tool.Definition()
	out := Tool{
		Name:        def.Name,
		Description: def.Description,
		InputSchema: def.ArgsSchema,
		Handler: func(ctx context.Context, args map[string]any) (ToolResult, error) {
			out, err := agent.CallToolContent(ctx, tool, args)
			if err != nil {
				return ToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, nil
			}
			result := Text(out.Text)
			if out.Text == "" && len(out.Images) > 0 {
				result.Content = nil
			}
			for _, img := range out.Images {
				result.Content = append(result.Content, Content{Type: "image", Data: img.Data, MimeType: img.MediaType})
			}
			return result, nil
		},
	}
	if info, ok := agent.InfoOf(tool); ok {
		out.Title = info.Title
		if info.ReadOnly != nil || info.Destructive != nil || info.OpenWorld != nil || info.Idempotent != nil {
			out.Annotations = &ToolAnnotations{ReadOnlyHint: info.ReadOnly, DestructiveHint: info.Destructive, OpenWorldHint: info.OpenWorld, IdempotentHint: info.Idempotent}
		}
	}
	return out
}

func ToolFromLangChain(def lc.ToolDefinition, handler func(context.Context, map[string]any) (string, error)) Tool {
	return Tool{
		Name:        def.Name,
		Description: def.Description,
		InputSchema: def.ArgsSchema,
		Handler: func(ctx context.Context, args map[string]any) (ToolResult, error) {
			out, err := handler(ctx, args)
			if err != nil {
				return ToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, nil
			}
			return Text(out), nil
		},
	}
}

// ResultText renders a tool result for a model. Non-text blocks are described
// rather than dropped so the model knows they exist.
func ResultText(result ToolResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		if text := blockText(content); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 && result.StructuredContent != nil {
		if data, err := json.Marshal(result.StructuredContent); err == nil {
			parts = append(parts, string(data))
		}
	}
	return strings.Join(parts, "\n")
}

// ResultContent renders a tool result for a model that can see images. Image
// blocks become images when agent.CheckToolImages accepts them: PNG, JPEG,
// GIF or WebP, up to agent.MaxToolImageBytes decoded, at most
// agent.MaxToolImagesPerResult per result. Other image blocks get a
// placeholder saying why they weren't sent, after the text. Every other
// block reads as it does in ResultText.
func ResultContent(result ToolResult) agent.ToolOutput {
	parts := make([]string, 0, len(result.Content))
	var images []agent.ToolImage
	for _, content := range result.Content {
		if content.Type == "image" {
			images = append(images, agent.ToolImage{MediaType: content.MimeType, Data: content.Data})
			continue
		}
		if text := blockText(content); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 && len(images) == 0 && result.StructuredContent != nil {
		if data, err := json.Marshal(result.StructuredContent); err == nil {
			parts = append(parts, string(data))
		}
	}
	kept, notes := agent.CheckToolImages(images)
	return agent.ToolOutput{Text: strings.Join(append(parts, notes...), "\n"), Images: kept}
}

// blockText is how one content block reads in a model's text.
func blockText(content Content) string {
	switch content.Type {
	case "text":
		return content.Text
	case "image", "audio":
		return fmt.Sprintf("[%s %s, %s]", content.Type, firstNonEmptyString(content.MimeType, "data"), approxBytes(len(content.Data)*3/4))
	case "resource_link":
		return fmt.Sprintf("[resource %s %s]", firstNonEmptyString(content.Name, "link"), content.URI)
	case "resource":
		if content.Resource != nil {
			if content.Resource.Text != "" {
				return content.Resource.Text
			}
			return fmt.Sprintf("[resource %s %s]", content.Resource.URI, content.Resource.MimeType)
		}
	}
	return ""
}

// ResultMeta summarizes a result's shape without copying its payload.
func ResultMeta(result ToolResult) map[string]any {
	meta := map[string]any{}
	types := map[string]int{}
	textBytes := 0
	var links []string
	for _, content := range result.Content {
		types[content.Type]++
		textBytes += len(content.Text)
		if content.Type == "resource_link" && content.URI != "" && len(links) < 10 {
			links = append(links, content.URI)
		}
	}
	if len(types) > 0 {
		meta["content_types"] = types
	}
	if textBytes > 0 {
		meta["bytes"] = textBytes
	}
	if len(links) > 0 {
		meta["links"] = links
	}
	if result.StructuredContent != nil {
		meta["structured"] = true
		if n, ok := countItems(result.StructuredContent); ok {
			meta["items"] = n
		}
	}
	if result.IsError {
		meta["is_error"] = true
	}
	return meta
}

// countItems finds a list size in structured content: a top-level array, or an
// object with exactly one array field.
func countItems(value any) (int, bool) {
	switch typed := value.(type) {
	case []any:
		return len(typed), true
	case map[string]any:
		count, found := 0, 0
		for _, field := range typed {
			if list, ok := field.([]any); ok {
				count = len(list)
				found++
			}
		}
		if found == 1 {
			return count, true
		}
	}
	return 0, false
}

func approxBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
