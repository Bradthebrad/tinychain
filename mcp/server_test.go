package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestServerListAndCallTools(t *testing.T) {
	server := NewServer("test")
	server.AddTool(Tool{
		Name:        "echo",
		Description: "Echo text.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"text": map[string]any{"type": "string"}},
			"required":   []string{"text"},
		},
		Handler: func(ctx context.Context, args map[string]any) (ToolResult, error) {
			return Text(args["text"].(string)), nil
		},
	})

	listResp := server.Handle(context.Background(), Request{JSONRPC: JSONRPCVersion, ID: 1, Method: "tools/list"})
	data, err := json.Marshal(listResp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var list ListToolsResult
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "echo" {
		t.Fatalf("tools = %#v", list.Tools)
	}

	params, _ := json.Marshal(CallToolParams{Name: "echo", Arguments: map[string]any{"text": "hi"}})
	callResp := server.Handle(context.Background(), Request{JSONRPC: JSONRPCVersion, ID: 2, Method: "tools/call", Params: params})
	data, _ = json.Marshal(callResp.Result)
	var result ToolResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if got := ResultText(result); got != "hi" {
		t.Fatalf("result = %q", got)
	}
}

func TestRunStdio(t *testing.T) {
	server := NewServer("stdio-test")
	input := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n")
	var output bytes.Buffer
	if err := server.RunStdio(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte(`"protocolVersion"`)) {
		t.Fatalf("output = %s", output.String())
	}
}

func TestHTTPClientListTools(t *testing.T) {
	server := NewServer("http-test")
	server.AddTool(Tool{Name: "noop", Description: "Noop.", Handler: func(ctx context.Context, args map[string]any) (ToolResult, error) {
		return Text("ok"), nil
	}})
	httpServer := httptest.NewServer(server.Handler("/mcp"))
	defer httpServer.Close()

	client := NewHTTPClient(httpServer.URL+"/mcp", nil)
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "noop" {
		t.Fatalf("tools = %#v", tools)
	}
}
