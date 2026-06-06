# tinychain/agent

`tinychain/agent` is a tiny LangChain/deep-agents-inspired harness built on the
local `tinychain` client module.

It keeps only the small core:

- model/tool loop
- OpenAI and Anthropic model adapters
- LangChain-shaped tool definitions
- `SKILL.md` metadata loading
- optional memory prompt injection
- `write_todos` planning tool
- optional `task` tool for stateless subagents

It intentionally omits LangGraph, middleware graphs, persistence, streaming,
human-in-the-loop checkpoints, filesystem tools, and schema validation.

## Example

```go
package main

import (
	"context"
	"os"
	"strings"

	"tinychain/agent"
	"tinychain/lc"
	"tinychain/openai"
)

func main() {
	model := agent.OpenAIModel{
		Client: openai.Client{APIKey: os.Getenv("OPENAI_API_KEY")},
		Model:  "gpt-4.1-mini",
	}

	a := agent.New(agent.Config{
		Model:        model,
		SystemPrompt: "You are concise and useful.",
		Tools: []agent.Tool{agent.ToolFunc{
			Name:        "uppercase",
			Description: "Uppercase text.",
			Schema: agent.ToolSchema(map[string]any{
				"text": agent.StringProperty("Text to uppercase."),
			}, "text"),
			Func: func(ctx context.Context, args map[string]any) (string, error) {
				return strings.ToUpper(args["text"].(string)), nil
			},
		}},
	})

	_, _ = a.InvokeMessages(context.Background(), []lc.BaseMessage{lc.Human("Use the tool.")})
}
```
