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


## Tool Metadata And Rich Results

Tools can carry the 2025-06-18 `title`, `annotations` (`readOnlyHint`,
`destructiveHint`, `idempotentHint`, `openWorldHint`) and `outputSchema`
fields. Results can include `structuredContent` and non-text blocks (image,
audio, resource links, embedded resources).

```go
server.AddTool(mcp.Tool{
	Name:        "web_search",
	Title:       "Search the web",
	Annotations: &mcp.ToolAnnotations{ReadOnlyHint: mcp.Bool(true), OpenWorldHint: mcp.Bool(true)},
	Handler:     search,
})
```

`mcp.AgentToolsForServer(ctx, client, "web", "Web tools")` returns
`*mcp.RemoteTool` values. They implement `agent.InfoTool` (title, server,
hints) and `agent.MetaTool` (a small result summary such as item counts and
content types), which the agent forwards on tool callbacks as `Metadata` and
`Data.Extra["result_meta"]`, along with `Data.Extra["duration_ms"]`. A result
with `isError: true` is returned to the agent as an error.

They also implement `agent.ContentTool`, so the model sees the images a tool
returns, such as a computer-use screenshot. `mcp.ResultContent` does the
conversion: PNG, JPEG, GIF and WebP image blocks of up to 5 MB, at most 4 per
result, become images, and other blocks read as in `mcp.ResultText`, which
`Call` and `CallWithMeta` still use. In the other direction, `ToolFromAgent`
serves the images of an `agent.ContentTool` as image blocks.

## Client Transport Behavior

- **stdio** (`NewStdioClientWithOptions`): requests are multiplexed and matched
  to responses by id; notifications from the server are ignored; server
  requests (`ping`, `roots/list`) are answered. `StdioOptions.Dir` sets the
  server's working directory. The last 32 KiB of stderr is kept
  (`client.Stderr()`). `Close` kills the whole process tree (a Job Object on
  Windows, a process group elsewhere). `client.Done()`/`client.Err()` report a
  server that exits on its own.
- **streamable HTTP** (`NewHTTPClient`): sends `MCP-Protocol-Version`, keeps
  the `Mcp-Session-Id` the server assigns, accepts both JSON and
  `text/event-stream` responses, and ends the session with `DELETE` on
  `Close`.
- `Initialize` sends `notifications/initialized` and, when roots are set with
  `client.SetRoots`, advertises the `roots` capability. Call
  `client.NotifyRootsChanged` after the allowed directories change.

### Stdio cancellation and sensitive mutations

`RunStdio` keeps a bounded, serialized handler queue while reading cancellation
notifications independently. `notifications/cancelled` cancels the matching
queued or executing request context. Queued calls recheck cancellation before
handler dispatch; handlers must also check it immediately before side effects.
Disconnect cancels queued mutations and drains the executing handler. A handler
must not return while it still owns local background mutation work.

`StdioTransport` checks cancellation before writing and again after acquiring
its write lock, and sends request-ID cancellation for dispatched calls. Ordinary
calls return promptly; sensitive mutations should use
`client.CallTool(mcp.WithCancellationDrain(ctx), name, args)`. This local stdio
option keeps the call outstanding until the original response arrives or the
transport terminates, even after cancellation. Retain authorization leases,
serialization locks and in-flight accounting until that return. There is no
implicit drain timeout: close the transport explicitly if a peer does not
cooperate. Do not use this as an HTTP cancellation guarantee.

A completion response means the handler returned, not that cancellation rolled
back a native write. In-flight UIA/CDP writes can have uncertain outcomes even
when cancellation returns an error. Never automatically retry them. Servers
that stop waiting on an external mutation before its native acknowledgement
must preserve an uncertain/poisoned session policy; a cancellation notification
alone is never a mutation-completion acknowledgement.
