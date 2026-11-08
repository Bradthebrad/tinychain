package openai

import (
	"encoding/json"

	"github.com/Bradthebrad/tinychain/lc"
)

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
				Type:          "function_call_output",
				CallID:        msg.ToolCallID,
				Output:        contentText(msg.Content),
				OutputContent: functionOutputContent(msg.Content),
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
			Content: responsesContent(role, msg.Content),
		})
	}
	return ResponsesInput{Items: items}
}

func responsesContent(role string, content lc.Content) []ResponsesContent {
	if content.Text != nil {
		return []ResponsesContent{{Type: responsesTextType(role), Text: *content.Text}}
	}
	out := make([]ResponsesContent, 0, len(content.Parts))
	for _, part := range content.Parts {
		if part.Text != "" {
			out = append(out, ResponsesContent{Type: responsesTextType(role), Text: part.Text})
			continue
		}
		if part.Type == "image" && part.Source != nil {
			url := part.Source.URL
			if url == "" && part.Source.Data != "" {
				url = "data:" + part.Source.MediaType + ";base64," + part.Source.Data
			}
			out = append(out, ResponsesContent{Type: "input_image", ImageURL: url})
		}
	}
	return out
}

// functionOutputContent is a function_call_output's output for a tool result
// with images: its text and images in order. It is nil for a result without
// images, whose output stays a string.
func functionOutputContent(content lc.Content) []ResponsesContent {
	if content.Text != nil {
		return nil
	}
	hasImage := false
	for _, part := range content.Parts {
		if part.Type == "image" && part.Source != nil {
			hasImage = true
		}
	}
	if !hasImage {
		return nil
	}
	return responsesContent("user", content)
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
	// Output is a function_call_output's text.
	Output string `json:"output,omitempty"`
	// OutputContent, when set, is sent as the output instead of Output: an
	// array of input_text and input_image parts, for a result with images.
	OutputContent []ResponsesContent `json:"-"`
}

// MarshalJSON sends OutputContent, when set, as the output array, and always
// sends a function_call_output's output, even an empty one.
func (i ResponsesInputItem) MarshalJSON() ([]byte, error) {
	type alias ResponsesInputItem
	switch {
	case len(i.OutputContent) > 0:
		return jsonMarshal(struct {
			alias
			Output []ResponsesContent `json:"output"`
		}{alias(i), i.OutputContent})
	case i.Type == "function_call_output":
		return jsonMarshal(struct {
			alias
			Output string `json:"output"`
		}{alias(i), i.Output})
	default:
		return jsonMarshal(alias(i))
	}
}

// UnmarshalJSON reads an output given as a string into Output and one given
// as an array into OutputContent, with its text also in Output.
func (i *ResponsesInputItem) UnmarshalJSON(data []byte) error {
	type alias ResponsesInputItem
	var raw struct {
		alias
		Output json.RawMessage `json:"output,omitempty"`
	}
	if err := jsonUnmarshal(data, &raw); err != nil {
		return err
	}
	*i = ResponsesInputItem(raw.alias)
	if len(raw.Output) == 0 || string(raw.Output) == "null" {
		return nil
	}
	if err := jsonUnmarshal(raw.Output, &i.Output); err == nil {
		return nil
	}
	if err := jsonUnmarshal(raw.Output, &i.OutputContent); err != nil {
		return err
	}
	for _, part := range i.OutputContent {
		if part.Text != "" {
			i.Output = part.Text
			break
		}
	}
	return nil
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
	ID               string             `json:"id,omitempty"`
	Type             string             `json:"type"`
	Status           string             `json:"status,omitempty"`
	Role             string             `json:"role,omitempty"`
	Content          []ResponsesContent `json:"content,omitempty"`
	Summary          []ResponsesSummary `json:"summary,omitempty"`
	EncryptedContent string             `json:"encrypted_content,omitempty"`
	CallID           string             `json:"call_id,omitempty"`
	Name             string             `json:"name,omitempty"`
	Arguments        string             `json:"arguments,omitempty"`
	Output           string             `json:"output,omitempty"`
}

type ResponsesSummary struct {
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
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

// ResponsesToolFromLangChain makes a function tool for the Responses API.
// Strict is sent as false: the Responses API treats a function without it
// as strict, which makes the model fill in every property, so optional
// arguments arrive as "" or 0 instead of being left out.
func ResponsesToolFromLangChain(tool lc.ToolDefinition) ResponsesTool {
	return ResponsesTool{
		Type:        "function",
		Name:        tool.Name,
		Description: tool.Description,
		Parameters:  tool.ArgsSchema,
		Strict:      NotStrict(),
	}
}

// NotStrict is the Strict value for tools whose optional arguments may be
// left out.
func NotStrict() *bool {
	f := false
	return &f
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
