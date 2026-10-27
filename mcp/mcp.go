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

// Tool describes an MCP tool. Title, Annotations and OutputSchema follow the
// 2025-06-18 specification and are optional; older servers simply omit them.
type Tool struct {
	Name         string           `json:"name"`
	Title        string           `json:"title,omitempty"`
	Description  string           `json:"description,omitempty"`
	InputSchema  map[string]any   `json:"inputSchema,omitempty"`
	OutputSchema map[string]any   `json:"outputSchema,omitempty"`
	Annotations  *ToolAnnotations `json:"annotations,omitempty"`
	Handler      ToolHandler      `json:"-"`
}

// ToolAnnotations are hints a server gives about a tool's behavior. They are
// hints, not guarantees, and clients must not treat them as a security boundary
// on their own.
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// DisplayTitle returns the most human-friendly name the server supplied.
func (t Tool) DisplayTitle() string {
	if t.Title != "" {
		return t.Title
	}
	if t.Annotations != nil && t.Annotations.Title != "" {
		return t.Annotations.Title
	}
	return ""
}

// Bool is a small helper for building annotation hints.
func Bool(value bool) *bool { return &value }

type ToolHandler func(context.Context, map[string]any) (ToolResult, error)

type ToolResult struct {
	Content           []Content `json:"content"`
	StructuredContent any       `json:"structuredContent,omitempty"`
	IsError           bool      `json:"isError,omitempty"`
}

// Content is one block of a tool result. Text blocks use Text; image and audio
// blocks use Data (base64) and MimeType; resource links use URI and Name;
// embedded resources use Resource.
type Content struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	Data     string            `json:"data,omitempty"`
	MimeType string            `json:"mimeType,omitempty"`
	URI      string            `json:"uri,omitempty"`
	Name     string            `json:"name,omitempty"`
	Resource *ResourceContents `json:"resource,omitempty"`
}

type ResourceContents struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

func Text(text string) ToolResult {
	return ToolResult{Content: []Content{{Type: "text", Text: text}}}
}

type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      Implementation     `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

type ServerCapabilities struct {
	Tools map[string]any `json:"tools,omitempty"`
}

type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

// Root is a filesystem location the client allows a server to operate in.
// URIs use the file:// scheme.
type Root struct {
	URI  string `json:"uri"`
	Name string `json:"name,omitempty"`
}

type ListRootsResult struct {
	Roots []Root `json:"roots"`
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
