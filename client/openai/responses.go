package openai

import "tinychain/lc"

type ResponsesRequest struct {
	Model              string          `json:"model"`
	Input              ResponsesInput  `json:"input"`
	Instructions       string          `json:"instructions,omitempty"`
	MaxOutputTokens    *int            `json:"max_output_tokens,omitempty"`
	Metadata           map[string]any  `json:"metadata,omitempty"`
	ParallelToolCalls  *bool           `json:"parallel_tool_calls,omitempty"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	Reasoning          any             `json:"reasoning,omitempty"`
	ServiceTier        string          `json:"service_tier,omitempty"`
	Store              *bool           `json:"store,omitempty"`
	Stream             *bool           `json:"stream,omitempty"`
	Temperature        *float64        `json:"temperature,omitempty"`
	Text               any             `json:"text,omitempty"`
	ToolChoice         any             `json:"tool_choice,omitempty"`
	Tools              []ResponsesTool `json:"tools,omitempty"`
	TopLogprobs        *int            `json:"top_logprobs,omitempty"`
	TopP               *float64        `json:"top_p,omitempty"`
	Truncation         string          `json:"truncation,omitempty"`
	User               string          `json:"user,omitempty"`
}

type ResponsesInput struct {
	Text  *string
	Items []ResponsesInputItem
}

func TextInput(text string) ResponsesInput {
	return ResponsesInput{Text: &text}
}

func MessageInput(messages []lc.BaseMessage) ResponsesInput {
	items := make([]ResponsesInputItem, 0, len(messages))
	for _, msg := range messages {
		role := openAIRole(msg.Type)
		if role == "tool" {
			items = append(items, ResponsesInputItem{
				Type:   "function_call_output",
				CallID: msg.ToolCallID,
				Output: contentText(msg.Content),
			})
			continue
		}
		if len(msg.ToolCalls) > 0 {
			for _, call := range msg.ToolCalls {
				items = append(items, ResponsesInputItem{
					Type:      "function_call",
					CallID:    call.ID,
					Name:      call.Name,
					Arguments: Arguments(call.Args),
				})
			}
			if contentText(msg.Content) == "" {
				continue
			}
		}
		items = append(items, ResponsesInputItem{
			Type:    "message",
			Role:    role,
			Content: []ResponsesContent{{Type: responsesTextType(role), Text: contentText(msg.Content)}},
		})
	}
	return ResponsesInput{Items: items}
}

type ResponsesInputItem struct {
	Type      string             `json:"type"`
	Role      string             `json:"role,omitempty"`
	Content   []ResponsesContent `json:"content,omitempty"`
	ID        string             `json:"id,omitempty"`
	CallID    string             `json:"call_id,omitempty"`
	Name      string             `json:"name,omitempty"`
	Arguments string             `json:"arguments,omitempty"`
	Input     map[string]any     `json:"input,omitempty"`
	Output    string             `json:"output,omitempty"`
}

type ResponsesContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	FileID   string `json:"file_id,omitempty"`
}

type ResponsesTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      *bool          `json:"strict,omitempty"`
}

type ResponsesResponse struct {
	ID                 string                 `json:"id"`
	Object             string                 `json:"object"`
	CreatedAt          float64                `json:"created_at"`
	Status             string                 `json:"status"`
	Error              any                    `json:"error,omitempty"`
	IncompleteDetails  any                    `json:"incomplete_details,omitempty"`
	Instructions       string                 `json:"instructions,omitempty"`
	MaxOutputTokens    int                    `json:"max_output_tokens,omitempty"`
	Model              string                 `json:"model"`
	Output             []ResponsesOutputItem  `json:"output"`
	ParallelToolCalls  bool                   `json:"parallel_tool_calls,omitempty"`
	PreviousResponseID string                 `json:"previous_response_id,omitempty"`
	Reasoning          any                    `json:"reasoning,omitempty"`
	ServiceTier        string                 `json:"service_tier,omitempty"`
	Store              bool                   `json:"store,omitempty"`
	Temperature        float64                `json:"temperature,omitempty"`
	Text               any                    `json:"text,omitempty"`
	ToolChoice         any                    `json:"tool_choice,omitempty"`
	Tools              []ResponsesTool        `json:"tools,omitempty"`
	TopP               float64                `json:"top_p,omitempty"`
	Truncation         string                 `json:"truncation,omitempty"`
	Usage              *Usage                 `json:"usage,omitempty"`
	User               string                 `json:"user,omitempty"`
	Metadata           map[string]any         `json:"metadata,omitempty"`
	Raw                map[string]interface{} `json:"-"`
}

type ResponsesOutputItem struct {
	ID        string             `json:"id,omitempty"`
	Type      string             `json:"type"`
	Status    string             `json:"status,omitempty"`
	Role      string             `json:"role,omitempty"`
	Content   []ResponsesContent `json:"content,omitempty"`
	CallID    string             `json:"call_id,omitempty"`
	Name      string             `json:"name,omitempty"`
	Arguments string             `json:"arguments,omitempty"`
	Output    string             `json:"output,omitempty"`
}

func (i ResponsesInput) MarshalJSON() ([]byte, error) {
	if i.Text != nil {
		return jsonMarshal(*i.Text)
	}
	return jsonMarshal(i.Items)
}

func (i *ResponsesInput) UnmarshalJSON(data []byte) error {
	var text string
	if err := jsonUnmarshal(data, &text); err == nil {
		*i = TextInput(text)
		return nil
	}
	var items []ResponsesInputItem
	if err := jsonUnmarshal(data, &items); err != nil {
		return err
	}
	*i = ResponsesInput{Items: items}
	return nil
}

func ResponsesToolFromLangChain(tool lc.ToolDefinition) ResponsesTool {
	return ResponsesTool{
		Type:        "function",
		Name:        tool.Name,
		Description: tool.Description,
		Parameters:  tool.ArgsSchema,
	}
}

func contentText(content lc.Content) string {
	if content.Text != nil {
		return *content.Text
	}
	for _, part := range content.Parts {
		if part.Text != "" {
			return part.Text
		}
	}
	return ""
}

func responsesTextType(role string) string {
	if role == "assistant" {
		return "output_text"
	}
	return "input_text"
}
