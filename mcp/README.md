# github.com/Bradthebrad/tinychain/mcp

`github.com/Bradthebrad/tinychain/mcp` is a tiny, stdlib-only MCP-style tool server/client module.

It supports the minimal methods needed for agent tooling:

- `initialize`
- `ping`
- `tools/list`
- `tools/call`

STDIO is the default transport. Streamable HTTP-style POSTs are supported on one
endpoint, and a small legacy SSE-compatible pair is available for clients that
still expect `/sse` plus a message endpoint.

## Server

```go
package main

import (
	"context"
	"strings"

	"github.com/Bradthebrad/tinychain/mcp"
)

func main() {
	server := mcp.NewServer("example-tools")
	server.AddTool(mcp.Tool{
		Name:        "uppercase",
		Description: "Uppercase text.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{"type": "string"},
			},
			"required": []string{"text"},
		},
		Handler: func(ctx context.Context, args map[string]any) (mcp.ToolResult, error) {
			return mcp.Text(strings.ToUpper(args["text"].(string))), nil
		},
	})

	_ = server.Run(context.Background()) // defaults to stdio
}
```

Run over HTTP instead:

```go
_ = server.Run(
	context.Background(),
	mcp.WithTransport("streamable-http"),
	mcp.WithAddr(":8080"),
	mcp.WithPath("/mcp"),
)
```

## Client To Agent Tools

```go
client := mcp.NewHTTPClient("http://localhost:8080/mcp", nil)
_, _ = client.Initialize(context.Background())

tools, err := mcp.AgentTools(context.Background(), client)
if err != nil {
	panic(err)
}

agent := agent.New(agent.Config{
	Model: myModel,
	Tools: tools,
})
```

