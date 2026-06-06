# tinychain

`tinychain` is a small, stdlib-only Go package for LangChain-shaped chat data.

It is intentionally not a full LangChain port. The package focuses on the data
models and endpoint payloads needed to talk to:

- OpenAI Chat Completions: `/v1/chat/completions`
- OpenAI Responses: `/v1/responses`
- Anthropic Messages: `/v1/messages`

## Packages

- `tinychain/lc`: LangChain-like messages, content blocks, tool calls, usage,
  generations, and tool definitions.
- `tinychain/openai`: OpenAI request/response types, conversion helpers, and a
  minimal HTTP client.
- `tinychain/anthropic`: Anthropic request/response types, conversion helpers,
  and a minimal HTTP client.
- `tinychain/callbacks`: LangChain-like activity event models and a tiny sink
  interface.

## Example

```go
package main

import (
	"context"
	"fmt"
	"os"

	"tinychain/lc"
	"tinychain/openai"
)

func main() {
	client := openai.Client{APIKey: os.Getenv("OPENAI_API_KEY")}
	resp, err := client.ChatCompletion(context.Background(), openai.ChatCompletionRequest{
		Model:    "gpt-4.1-mini",
		Messages: openai.ChatMessages([]lc.BaseMessage{lc.Human("Say hi in five words.")}),
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(resp.Choices[0].Message.Content)
}
```

