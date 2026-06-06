# tinychain

`tinychain` is a small set of Go modules for building lightweight agent clients.

The modules are intentionally minimal and mostly stdlib-based:

- `client`: LangChain-shaped message/data models, OpenAI and Anthropic request/response models, and tiny provider clients.
- `agent`: a small LangChain/deep-agents-inspired model/tool loop with skills, memory prompt injection, todo planning, and subagent task delegation.
- `mcp`: a tiny MCP-style JSON-RPC tool server/client with stdio-first transport and HTTP/SSE compatibility helpers.

## Modules

```text
tinychain/client
tinychain/agent
tinychain/mcp
```

Each module has its own `go.mod` so consumers can import only the layer they need.

## Test

```powershell
cd client; go test ./...
cd ../agent; go test ./...
cd ../mcp; go test ./...
```

