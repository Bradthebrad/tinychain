package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

const (
	ProtocolVersion = "2025-06-18"
	JSONRPCVersion  = "2.0"
)

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
	Handler     ToolHandler    `json:"-"`
}

type ToolHandler func(context.Context, map[string]any) (ToolResult, error)

type ToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func Text(text string) ToolResult {
	return ToolResult{Content: []Content{{Type: "text", Text: text}}}
}

type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      Implementation     `json:"serverInfo"`
}

type ServerCapabilities struct {
	Tools map[string]any `json:"tools,omitempty"`
}

type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type ListToolsResult struct {
	Tools []Tool `json:"tools"`
}

type CallToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

func newResponse(id any, result any) Response {
	return Response{JSONRPC: JSONRPCVersion, ID: id, Result: result}
}

func newError(id any, code int, message string) Response {
	return Response{JSONRPC: JSONRPCVersion, ID: id, Error: &RPCError{Code: code, Message: message}}
}

func decodeParams[T any](raw json.RawMessage) (T, error) {
	var out T
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("invalid params: %w", err)
	}
	return out, nil
}
