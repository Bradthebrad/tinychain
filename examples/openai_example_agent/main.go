package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/lc"
	"github.com/Bradthebrad/tinychain/openai"
)

const defaultModel = "gpt-4o-mini"

func main() {
	apiKey := flag.String("api-key", "", "OpenAI API key. Falls back to OPENAI_API_KEY.")
	model := flag.String("model", defaultModel, "OpenAI model.")
	baseURL := flag.String("base-url", openai.DefaultBaseURL, "OpenAI-compatible API base URL.")
	useResponses := flag.Bool("responses", false, "Use the OpenAI Responses API instead of Chat Completions.")
	system := flag.String("system", "You are a concise helpful assistant.", "System prompt.")
	flag.Parse()

	key := strings.TrimSpace(*apiKey)
	if key == "" {
		key = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	}
	if key == "" {
		fmt.Fprintln(os.Stderr, "missing API key: pass -api-key or set OPENAI_API_KEY")
		os.Exit(2)
	}

	a := agent.New(agent.Config{
		Model: agent.OpenAIModel{
			Client:       openai.Client{APIKey: key, BaseURL: strings.TrimRight(*baseURL, "/")},
			Model:        *model,
			UseResponses: *useResponses,
		},
		SystemPrompt: *system,
	})

	fmt.Printf("openai_example_agent using %s\n", *model)
	fmt.Println("type /quit to exit, /help for commands")

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var history []lc.BaseMessage

	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		switch input {
		case "/quit", "/exit":
			return
		case "/help":
			fmt.Println("commands: /help, /quit, /exit, /reset")
			fmt.Println("flags: -api-key, -model, -base-url, -responses, -system")
			continue
		case "/reset":
			history = nil
			fmt.Println("conversation reset")
			continue
		}

		history = append(history, lc.Human(input))
		result, err := a.InvokeMessages(context.Background(), history)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			history = history[:len(history)-1]
			continue
		}
		fmt.Println(text(result.Output))
		history = withoutSystem(result.Messages)
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "input error:", err)
	}
}

func withoutSystem(messages []lc.BaseMessage) []lc.BaseMessage {
	out := make([]lc.BaseMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Type != lc.RoleSystem {
			out = append(out, msg)
		}
	}
	return out
}

func text(msg lc.BaseMessage) string {
	if msg.Content.Text != nil {
		return *msg.Content.Text
	}
	var b strings.Builder
	for _, part := range msg.Content.Parts {
		if part.Text != "" {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}
