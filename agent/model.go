package agent

import (
	"context"
	"errors"

	"tinychain/anthropic"
	"tinychain/lc"
	"tinychain/openai"
)

type Model interface {
	Call(ctx context.Context, messages []lc.BaseMessage, tools []Tool) (lc.BaseMessage, error)
}

type OpenAIModel struct {
	Client       openai.Client
	Model        string
	UseResponses bool
	Temperature  *float64
	MaxTokens    *int
}

func (m OpenAIModel) Call(ctx context.Context, messages []lc.BaseMessage, tools []Tool) (lc.BaseMessage, error) {
	if m.Model == "" {
		return lc.BaseMessage{}, errors.New("agent: OpenAI model is required")
	}
	if m.UseResponses {
		req := openai.ResponsesRequest{
			Model:           m.Model,
			Input:           openai.MessageInput(messages),
			Temperature:     m.Temperature,
			MaxOutputTokens: m.MaxTokens,
			Tools:           responseTools(tools),
		}
		resp, err := m.Client.Responses(ctx, req)
		if err != nil {
			return lc.BaseMessage{}, err
		}
		return messageFromOpenAIResponse(resp), nil
	}
	req := openai.ChatCompletionRequest{
		Model:       m.Model,
		Messages:    openai.ChatMessages(messages),
		Temperature: m.Temperature,
		MaxTokens:   m.MaxTokens,
		Tools:       chatTools(tools),
	}
	resp, err := m.Client.ChatCompletion(ctx, req)
	if err != nil {
		return lc.BaseMessage{}, err
	}
	if len(resp.Choices) == 0 {
		return lc.BaseMessage{}, errors.New("agent: OpenAI returned no choices")
	}
	msg := openai.ToLangChainMessage(resp.Choices[0].Message)
	if resp.Usage != nil {
		msg.UsageMetadata = &lc.UsageMetadata{
			InputTokens:  firstNonZero(resp.Usage.PromptTokens, resp.Usage.InputTokens),
			OutputTokens: firstNonZero(resp.Usage.CompletionTokens, resp.Usage.OutputTokens),
			TotalTokens:  resp.Usage.TotalTokens,
		}
	}
	return msg, nil
}

type AnthropicModel struct {
	Client      anthropic.Client
	Model       string
	MaxTokens   int
	Temperature *float64
}

func (m AnthropicModel) Call(ctx context.Context, messages []lc.BaseMessage, tools []Tool) (lc.BaseMessage, error) {
	if m.Model == "" {
		return lc.BaseMessage{}, errors.New("agent: Anthropic model is required")
	}
	maxTokens := m.MaxTokens
	if maxTokens == 0 {
		maxTokens = 1024
	}
	req := anthropic.MessageRequest{
		Model:       m.Model,
		MaxTokens:   maxTokens,
		System:      anthropic.SystemFromMessages(messages),
		Messages:    anthropic.Messages(messages),
		Temperature: m.Temperature,
		Tools:       anthropicTools(tools),
	}
	resp, err := m.Client.Messages(ctx, req)
	if err != nil {
		return lc.BaseMessage{}, err
	}
	return anthropic.ToLangChainMessage(*resp), nil
}

func chatTools(tools []Tool) []openai.Tool {
	out := make([]openai.Tool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, openai.ToolFromLangChain(tool.Definition()))
	}
	return out
}

func responseTools(tools []Tool) []openai.ResponsesTool {
	out := make([]openai.ResponsesTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, openai.ResponsesToolFromLangChain(tool.Definition()))
	}
	return out
}

func anthropicTools(tools []Tool) []anthropic.Tool {
	out := make([]anthropic.Tool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, anthropic.ToolFromLangChain(tool.Definition()))
	}
	return out
}

func messageFromOpenAIResponse(resp *openai.ResponsesResponse) lc.BaseMessage {
	msg := lc.BaseMessage{
		Type:             lc.RoleAI,
		ID:               resp.ID,
		Content:          lc.PartsContent(),
		ResponseMetadata: map[string]any{"model": resp.Model, "status": resp.Status},
	}
	var parts []lc.ContentPart
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Text != "" {
					parts = append(parts, lc.ContentPart{Type: content.Type, Text: content.Text})
				}
			}
		case "function_call":
			msg.ToolCalls = append(msg.ToolCalls, lc.ToolCall{
				Name: item.Name,
				Args: parseArgs(item.Arguments),
				ID:   item.CallID,
				Type: "tool_call",
			})
		}
	}
	if len(parts) == 1 && parts[0].Text != "" && len(msg.ToolCalls) == 0 {
		msg.Content = lc.TextContent(parts[0].Text)
	} else {
		msg.Content = lc.PartsContent(parts...)
	}
	if resp.Usage != nil {
		msg.UsageMetadata = &lc.UsageMetadata{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		}
	}
	return msg
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}
