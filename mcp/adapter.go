package mcp

import (
	"context"
	"strings"

	"tinychain/agent"
	"tinychain/lc"
)

func AgentTools(ctx context.Context, client *Client) ([]agent.Tool, error) {
	tools, err := client.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]agent.Tool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, AgentTool(client, tool))
	}
	return out, nil
}

func AgentTool(client *Client, tool Tool) agent.Tool {
	return agent.ToolFunc{
		Name:        tool.Name,
		Description: tool.Description,
		Schema:      tool.InputSchema,
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			result, err := client.CallTool(ctx, tool.Name, args)
			if err != nil {
				return "", err
			}
			return ResultText(result), nil
		},
	}
}

func ToolFromAgent(tool agent.Tool) Tool {
	def := tool.Definition()
	return Tool{
		Name:        def.Name,
		Description: def.Description,
		InputSchema: def.ArgsSchema,
		Handler: func(ctx context.Context, args map[string]any) (ToolResult, error) {
			out, err := tool.Call(ctx, args)
			if err != nil {
				return ToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, nil
			}
			return Text(out), nil
		},
	}
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

func ResultText(result ToolResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		if content.Type == "text" && content.Text != "" {
			parts = append(parts, content.Text)
		}
	}
	return strings.Join(parts, "\n")
}
