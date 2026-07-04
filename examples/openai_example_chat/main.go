package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Bradthebrad/tinychain/lc"
	"github.com/Bradthebrad/tinychain/openai"
)

const defaultModel = "gpt-4o-mini"

func main() {
	apiKey := flag.String("api-key", "", "OpenAI API key. Falls back to OPENAI_API_KEY.")
	model := flag.String("model", defaultModel, "OpenAI model.")
	baseURL := flag.String("base-url", openai.DefaultBaseURL, "OpenAI-compatible API base URL.")
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

	client := openai.Client{APIKey: key, BaseURL: strings.TrimRight(*baseURL, "/")}
	messages := []lc.BaseMessage{lc.System(*system)}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	fmt.Printf("openai_example_chat using %s\n", *model)
	fmt.Println("type /quit to exit, /help for commands")

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
			fmt.Println("flags: -api-key, -model, -base-url, -system")
			continue
		case "/reset":
			messages = []lc.BaseMessage{lc.System(*system)}
			fmt.Println("conversation reset")
			continue
		}

		messages = append(messages, lc.Human(input))
		resp, err := client.ChatCompletion(context.Background(), openai.ChatCompletionRequest{
			Model:    *model,
			Messages: openai.ChatMessages(messages),
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			messages = messages[:len(messages)-1]
			continue
		}
		if len(resp.Choices) == 0 {
			fmt.Fprintln(os.Stderr, "error: no choices returned")
			messages = messages[:len(messages)-1]
			continue
		}
		msg := openai.ToLangChainMessage(resp.Choices[0].Message)
		fmt.Println(text(msg))
		messages = append(messages, msg)
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "input error:", err)
	}
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
