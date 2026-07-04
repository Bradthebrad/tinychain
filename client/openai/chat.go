package openai

import (
	"encoding/json"

	"tinychain/lc"
)

type ChatCompletionRequest struct {
	Model               string         `json:"model"`
	Messages            []ChatMessage  `json:"messages"`
	FrequencyPenalty    *float64       `json:"frequency_penalty,omitempty"`
	LogitBias           map[string]int `json:"logit_bias,omitempty"`
	Logprobs            *bool          `json:"logprobs,omitempty"`
	TopLogprobs         *int           `json:"top_logprobs,omitempty"`
	MaxTokens           *int           `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int           `json:"max_completion_tokens,omitempty"`
	ReasoningEffort     string         `json:"reasoning_effort,omitempty"`
	Reasoning           any            `json:"reasoning,omitempty"`
	N                   *int           `json:"n,omitempty"`
	ParallelToolCalls   *bool          `json:"parallel_tool_calls,omitempty"`
	PresencePenalty     *float64       `json:"presence_penalty,omitempty"`
	ResponseFormat      any            `json:"response_format,omitempty"`
	Seed                *int           `json:"seed,omitempty"`
	Stop                any            `json:"stop,omitempty"`
	Stream              *bool          `json:"stream,omitempty"`
	StreamOptions       any            `json:"stream_options,omitempty"`
	Temperature         *float64       `json:"temperature,omitempty"`
	ToolChoice          any            `json:"tool_choice,omitempty"`
	Tools               []Tool         `json:"tools,omitempty"`
	TopP                *float64       `json:"top_p,omitempty"`
	User                string         `json:"user,omitempty"`
	Metadata            map[string]any `json:"metadata,omitempty"`
}

type ChatMessage struct {
	Role             string     `json:"role"`
	Content          lc.Content `json:"content,omitempty"`
	Name             string     `json:"name,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	Refusal          string     `json:"refusal,omitempty"`
	Reasoning        string     `json:"reasoning,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ReasoningDetails any        `json:"reasoning_details,omitempty"`
}

type Tool struct {
	Type     string         `json:"type"`
	Function FunctionSchema `json:"function"`
}

type FunctionSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      *bool          `json:"strict,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatCompletionResponse struct {
	ID                string       `json:"id"`
	Object            string       `json:"object"`
	Created           int64        `json:"created"`
	Model             string       `json:"model"`
	Choices           []ChatChoice `json:"choices"`
	Usage             *Usage       `json:"usage,omitempty"`
	SystemFingerprint string       `json:"system_fingerprint,omitempty"`
	ServiceTier       string       `json:"service_tier,omitempty"`
}

type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	Logprobs     any         `json:"logprobs,omitempty"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

type Usage struct {
	PromptTokens            int            `json:"prompt_tokens,omitempty"`
	CompletionTokens        int            `json:"completion_tokens,omitempty"`
	TotalTokens             int            `json:"total_tokens,omitempty"`
	PromptTokensDetails     map[string]int `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails map[string]int `json:"completion_tokens_details,omitempty"`
	InputTokens             int            `json:"input_tokens,omitempty"`
	OutputTokens            int            `json:"output_tokens,omitempty"`
	InputTokensDetails      map[string]int `json:"input_tokens_details,omitempty"`
	OutputTokensDetails     map[string]int `json:"output_tokens_details,omitempty"`
}

func ChatMessages(messages []lc.BaseMessage) []ChatMessage {
	out := make([]ChatMessage, 0, len(messages))
	for _, msg := range messages {
		chat := ChatMessage{
			Role:       openAIRole(msg.Type),
			Content:    openAIContent(msg.Content),
			Name:       msg.Name,
			ToolCallID: msg.ToolCallID,
			ToolCalls:  openAIToolCalls(msg.ToolCalls),
		}
		if msg.AdditionalKwargs != nil {
			chat.Reasoning = stringKwarg(msg.AdditionalKwargs, "reasoning")
			chat.ReasoningContent = stringKwarg(msg.AdditionalKwargs, "reasoning_content")
			chat.ReasoningDetails = msg.AdditionalKwargs["reasoning_details"]
		}
		out = append(out, chat)
	}
	return out
}

func openAIContent(content lc.Content) lc.Content {
	if content.Text != nil {
		return content
	}
	parts := make([]lc.ContentPart, 0, len(content.Parts))
	for _, part := range content.Parts {
		if part.Type == "image" && part.Source != nil {
			url := part.Source.URL
			if url == "" && part.Source.Data != "" {
				url = "data:" + part.Source.MediaType + ";base64," + part.Source.Data
			}
			part.Type = "image_url"
			part.Source = nil
			part.Extra = map[string]any{"image_url": map[string]any{"url": url}}
		}
		parts = append(parts, part)
	}
	return lc.PartsContent(parts...)
}

func ToLangChainMessage(message ChatMessage) lc.BaseMessage {
	kwargs := map[string]any{
		"refusal": message.Refusal,
	}
	if message.Reasoning != "" {
		kwargs["reasoning"] = message.Reasoning
	}
	if message.ReasoningContent != "" {
		kwargs["reasoning_content"] = message.ReasoningContent
	}
	if message.ReasoningDetails != nil {
		kwargs["reasoning_details"] = message.ReasoningDetails
	}
	return lc.BaseMessage{
		Type:             lcRole(message.Role),
		Content:          message.Content,
		Name:             message.Name,
		ToolCallID:       message.ToolCallID,
		ToolCalls:        lcToolCalls(message.ToolCalls),
		AdditionalKwargs: kwargs,
	}
}

func ToolFromLangChain(tool lc.ToolDefinition) Tool {
	return Tool{
		Type: "function",
		Function: FunctionSchema{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.ArgsSchema,
		},
	}
}

func Arguments(args map[string]any) string {
	data, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func openAIRole(role lc.MessageRole) string {
	switch role {
	case lc.RoleHuman:
		return "user"
	case lc.RoleAI:
		return "assistant"
	case lc.RoleTool:
		return "tool"
	case lc.RoleDeveloper:
		return "developer"
	default:
		return string(role)
	}
}

func lcRole(role string) lc.MessageRole {
	switch role {
	case "user":
		return lc.RoleHuman
	case "assistant":
		return lc.RoleAI
	case "tool":
		return lc.RoleTool
	case "developer":
		return lc.RoleDeveloper
	default:
		return lc.MessageRole(role)
	}
}

func openAIToolCalls(calls []lc.ToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, ToolCall{
			ID:   call.ID,
			Type: "function",
			Function: FunctionCall{
				Name:      call.Name,
				Arguments: Arguments(call.Args),
			},
		})
	}
	return out
}

func lcToolCalls(calls []ToolCall) []lc.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]lc.ToolCall, 0, len(calls))
	for _, call := range calls {
		var args map[string]any
		_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
		out = append(out, lc.ToolCall{
			Name: call.Function.Name,
			Args: args,
			ID:   call.ID,
			Type: "tool_call",
		})
	}
	return out
}

func stringKwarg(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}
