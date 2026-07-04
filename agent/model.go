package agent

import (
	"context"
	"errors"
	"strings"

	"tinychain/anthropic"
	"tinychain/lc"
	"tinychain/openai"
)

type Model interface {
	Call(ctx context.Context, messages []lc.BaseMessage, tools []Tool) (lc.BaseMessage, error)
}

type OpenAIModel struct {
	Client          openai.Client
	Model           string
	UseResponses    bool
	Temperature     *float64
	MaxTokens       *int
	ReasoningEffort string
	Provider        string
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
			Reasoning:       openAIReasoning(m.ReasoningEffort),
		}
		resp, err := m.Client.Responses(ctx, req)
		if err != nil && openAIReasoningSummaryRequested(req.Reasoning) && unsupportedReasoningSummaryError(err) {
			req.Reasoning = openAIReasoningWithoutSummary(m.ReasoningEffort)
			resp, err = m.Client.Responses(ctx, req)
		}
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
	if effort := normalizeReasoningEffort(m.ReasoningEffort); effort != "" {
		req.ReasoningEffort = effort
		if m.Provider == "openrouter" {
			req.Reasoning = map[string]any{"effort": effort}
		}
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
			InputTokens:        firstNonZero(resp.Usage.PromptTokens, resp.Usage.InputTokens),
			OutputTokens:       firstNonZero(resp.Usage.CompletionTokens, resp.Usage.OutputTokens),
			TotalTokens:        resp.Usage.TotalTokens,
			InputTokenDetails:  mergeTokenDetails(resp.Usage.PromptTokensDetails, resp.Usage.InputTokensDetails),
			OutputTokenDetails: mergeTokenDetails(resp.Usage.CompletionTokensDetails, resp.Usage.OutputTokensDetails),
		}
	}
	return msg, nil
}

type AnthropicModel struct {
	Client          anthropic.Client
	Model           string
	MaxTokens       int
	Temperature     *float64
	ReasoningEffort string
}

func (m AnthropicModel) Call(ctx context.Context, messages []lc.BaseMessage, tools []Tool) (lc.BaseMessage, error) {
	if m.Model == "" {
		return lc.BaseMessage{}, errors.New("agent: Anthropic model is required")
	}
	maxTokens := m.MaxTokens
	if maxTokens == 0 {
		maxTokens = 1024
	}
	thinking, outputConfig, adjustedMaxTokens := anthropicThinking(m.Model, m.ReasoningEffort, maxTokens)
	maxTokens = adjustedMaxTokens
	req := anthropic.MessageRequest{
		Model:        m.Model,
		MaxTokens:    maxTokens,
		System:       anthropic.SystemFromMessages(messages),
		Messages:     anthropic.Messages(messages),
		Temperature:  m.Temperature,
		Thinking:     thinking,
		OutputConfig: outputConfig,
		Tools:        anthropicTools(tools),
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
	var reasoningSummaries []string
	var reasoningDetails []map[string]any
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Text != "" {
					parts = append(parts, lc.ContentPart{Type: content.Type, Text: content.Text})
				}
			}
		case "reasoning":
			for _, summary := range item.Summary {
				if summary.Text == "" {
					continue
				}
				reasoningSummaries = append(reasoningSummaries, summary.Text)
				reasoningDetails = append(reasoningDetails, map[string]any{
					"type": "reasoning.summary",
					"text": summary.Text,
					"id":   item.ID,
				})
			}
			if item.EncryptedContent != "" {
				reasoningDetails = append(reasoningDetails, map[string]any{
					"type": "reasoning.encrypted",
					"data": item.EncryptedContent,
					"id":   item.ID,
				})
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
	if len(reasoningSummaries) > 0 || len(reasoningDetails) > 0 {
		msg.AdditionalKwargs = map[string]any{}
		if len(reasoningSummaries) > 0 {
			msg.AdditionalKwargs["reasoning_summaries"] = reasoningSummaries
		}
		if len(reasoningDetails) > 0 {
			msg.AdditionalKwargs["reasoning_details"] = reasoningDetails
		}
	}
	if resp.Usage != nil {
		msg.UsageMetadata = &lc.UsageMetadata{
			InputTokens:        resp.Usage.InputTokens,
			OutputTokens:       resp.Usage.OutputTokens,
			TotalTokens:        resp.Usage.TotalTokens,
			InputTokenDetails:  resp.Usage.InputTokensDetails,
			OutputTokenDetails: resp.Usage.OutputTokensDetails,
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

func openAIReasoning(effort string) any {
	effort = normalizeReasoningEffort(effort)
	if effort == "" {
		return nil
	}
	return map[string]any{"effort": effort, "summary": "auto"}
}

func openAIReasoningWithoutSummary(effort string) any {
	effort = normalizeReasoningEffort(effort)
	if effort == "" {
		return nil
	}
	return map[string]any{"effort": effort}
}

func openAIReasoningSummaryRequested(reasoning any) bool {
	values, ok := reasoning.(map[string]any)
	if !ok {
		return false
	}
	_, ok = values["summary"]
	return ok
}

func unsupportedReasoningSummaryError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "summary") &&
		(strings.Contains(text, "unsupported") || strings.Contains(text, "unknown") || strings.Contains(text, "invalid"))
}

func normalizeReasoningEffort(effort string) string {
	switch effort {
	case "minimal", "low", "medium", "high":
		return effort
	case "xhigh":
		return "high"
	default:
		return ""
	}
}

func anthropicThinking(model, effort string, maxTokens int) (any, *anthropic.OutputConfig, int) {
	effort = normalizeAnthropicEffort(effort)
	if effort == "" {
		return nil, nil, maxTokens
	}
	outputConfig := &anthropic.OutputConfig{Effort: effort}
	if anthropicAdaptiveThinkingModel(model) {
		return map[string]any{"type": "adaptive", "display": "summarized"}, outputConfig, maxTokens
	}
	var budget int
	switch effort {
	case "low":
		budget = 1024
	case "medium":
		budget = 4096
	case "high":
		budget = 8192
	case "xhigh":
		budget = 16384
	case "max":
		budget = 32768
	default:
		return nil, outputConfig, maxTokens
	}
	if maxTokens <= budget {
		maxTokens = budget + 1024
	}
	return map[string]any{"type": "enabled", "budget_tokens": budget, "display": "summarized"}, outputConfig, maxTokens
}

func normalizeAnthropicEffort(effort string) string {
	switch effort {
	case "low", "medium", "high", "xhigh", "max":
		return effort
	default:
		return ""
	}
}

func anthropicAdaptiveThinkingModel(model string) bool {
	model = strings.ToLower(model)
	return strings.Contains(model, "fable-5") ||
		strings.Contains(model, "mythos-5") ||
		strings.Contains(model, "mythos") ||
		strings.Contains(model, "opus-4-8") ||
		strings.Contains(model, "opus-4-7") ||
		strings.Contains(model, "opus-4-6") ||
		strings.Contains(model, "sonnet-4-6")
}

func mergeTokenDetails(a, b map[string]int) map[string]int {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := map[string]int{}
	for k, v := range a {
		out[k] += v
	}
	for k, v := range b {
		out[k] += v
	}
	return out
}
