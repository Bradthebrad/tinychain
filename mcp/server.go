package mcp

import (
	"context"
	"fmt"
	"sort"
)

type Server struct {
	Name    string
	Version string
	tools   map[string]Tool
}

func NewServer(name string) *Server {
	if name == "" {
		name = "tinychain-mcp"
	}
	return &Server{Name: name, tools: map[string]Tool{}}
}

func (s *Server) AddTool(tool Tool) {
	if s.tools == nil {
		s.tools = map[string]Tool{}
	}
	s.tools[tool.Name] = tool
}

func (s *Server) Handle(ctx context.Context, req Request) Response {
	if req.JSONRPC != "" && req.JSONRPC != JSONRPCVersion {
		return newError(req.ID, -32600, "invalid jsonrpc version")
	}
	switch req.Method {
	case "initialize":
		return newResponse(req.ID, InitializeResult{
			ProtocolVersion: ProtocolVersion,
			Capabilities:    ServerCapabilities{Tools: map[string]any{}},
			ServerInfo:      Implementation{Name: s.Name, Version: s.Version},
		})
	case "notifications/initialized":
		return Response{}
	case "ping":
		return newResponse(req.ID, map[string]any{})
	case "tools/list":
		return newResponse(req.ID, ListToolsResult{Tools: s.listTools()})
	case "tools/call":
		params, err := decodeParams[CallToolParams](req.Params)
		if err != nil {
			return newError(req.ID, -32602, err.Error())
		}
		result, err := s.callTool(ctx, params)
		if err != nil {
			return newError(req.ID, -32000, err.Error())
		}
		return newResponse(req.ID, result)
	default:
		return newError(req.ID, -32601, "method not found")
	}
}

func (s *Server) listTools() []Tool {
	tools := make([]Tool, 0, len(s.tools))
	for _, tool := range s.tools {
		tool.Handler = nil
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

func (s *Server) callTool(ctx context.Context, params CallToolParams) (ToolResult, error) {
	tool, ok := s.tools[params.Name]
	if !ok {
		return ToolResult{}, fmt.Errorf("tool %q not found", params.Name)
	}
	if tool.Handler == nil {
		return ToolResult{}, fmt.Errorf("tool %q has no handler", params.Name)
	}
	return tool.Handler(ctx, params.Arguments)
}
