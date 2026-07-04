package anthropic

import "github.com/Bradthebrad/tinychain/lc"

type MessageRequest struct {
	Model         string        `json:"model"`
	MaxTokens     int           `json:"max_tokens"`
	Messages      []Message     `json:"messages"`
	System        SystemContent `json:"system,omitempty"`
	Metadata      *Metadata     `json:"metadata,omitempty"`
	StopSequences []string      `json:"stop_sequences,omitempty"`
	Stream        *bool         `json:"stream,omitempty"`
	Temperature   *float64      `json:"temperature,omitempty"`
	Thinking      any           `json:"thinking,omitempty"`
	OutputConfig  *OutputConfig `json:"output_config,omitempty"`
	ToolChoice    any           `json:"tool_choice,omitempty"`
	Tools         []Tool        `json:"tools,omitempty"`
	TopK          *int          `json:"top_k,omitempty"`
	TopP          *float64      `json:"top_p,omitempty"`
}

type OutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

type SystemContent struct {
	Text  *string
	Parts []ContentBlock
}

func TextSystem(text string) SystemContent {
	return SystemContent{Text: &text}
}

type Message struct {
	Role    string      `json:"role"`
	Content ContentList `json:"content"`
}

type ContentList struct {
	Text   *string
	Blocks []ContentBlock
}

type ContentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	Thinking  string         `json:"thinking,omitempty"`
	Signature string         `json:"signature,omitempty"`
	Data      string         `json:"data,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   any            `json:"content,omitempty"`
	Source    any            `json:"source,omitempty"`
	Citations any            `json:"citations,omitempty"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type Metadata struct {
	UserID string `json:"user_id,omitempty"`
}

type MessageResponse struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Model        string         `json:"model"`
	Content      []ContentBlock `json:"content"`
	StopReason   string         `json:"stop_reason,omitempty"`
	StopSequence string         `json:"stop_sequence,omitempty"`
	Usage        Usage          `json:"usage"`
}

type Usage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

func Messages(messages []lc.BaseMessage) []Message {
	out := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Type == lc.RoleSystem || msg.Type == lc.RoleDeveloper {
			continue
		}
		out = append(out, Message{
			Role:    anthropicRole(msg.Type),
			Content: contentFromLC(msg),
		})
	}
	return out
}

func SystemFromMessages(messages []lc.BaseMessage) SystemContent {
	var blocks []ContentBlock
	for _, msg := range messages {
		if msg.Type != lc.RoleSystem && msg.Type != lc.RoleDeveloper {
			continue
		}
		if msg.Content.Text != nil {
			blocks = append(blocks, ContentBlock{Type: "text", Text: *msg.Content.Text})
			continue
		}
		for _, part := range msg.Content.Parts {
			blocks = append(blocks, blockFromLC(part))
		}
	}
	if len(blocks) == 1 && blocks[0].Type == "text" {
		return TextSystem(blocks[0].Text)
	}
	return SystemContent{Parts: blocks}
}

func ToolFromLangChain(tool lc.ToolDefinition) Tool {
	return Tool{
		Name:        tool.Name,
		Description: tool.Description,
		InputSchema: tool.ArgsSchema,
	}
}

func ToLangChainMessage(resp MessageResponse) lc.BaseMessage {
	msg := lc.BaseMessage{
		Type:    lc.RoleAI,
		ID:      resp.ID,
		Content: lc.PartsContent(partsFromAnthropic(resp.Content)...),
		UsageMetadata: &lc.UsageMetadata{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			TotalTokens:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
			InputTokenDetails: map[string]int{
				"cache_creation_input_tokens": resp.Usage.CacheCreationInputTokens,
				"cache_read_input_tokens":     resp.Usage.CacheReadInputTokens,
			},
		},
		ResponseMetadata: map[string]any{
			"model":         resp.Model,
			"stop_reason":   resp.StopReason,
			"stop_sequence": resp.StopSequence,
		},
	}
	for _, block := range resp.Content {
		if block.Type == "tool_use" {
			msg.ToolCalls = append(msg.ToolCalls, lc.ToolCall{
				Name: block.Name,
				Args: block.Input,
				ID:   block.ID,
				Type: "tool_call",
			})
		}
	}
	return msg
}

func (s SystemContent) MarshalJSON() ([]byte, error) {
	if s.Text != nil {
		return jsonMarshal(*s.Text)
	}
	return jsonMarshal(s.Parts)
}

func (s *SystemContent) UnmarshalJSON(data []byte) error {
	var text string
	if err := jsonUnmarshal(data, &text); err == nil {
		*s = TextSystem(text)
		return nil
	}
	var parts []ContentBlock
	if err := jsonUnmarshal(data, &parts); err != nil {
		return err
	}
	*s = SystemContent{Parts: parts}
	return nil
}

func (c ContentList) MarshalJSON() ([]byte, error) {
	if c.Text != nil {
		return jsonMarshal(*c.Text)
	}
	return jsonMarshal(c.Blocks)
}

func (c *ContentList) UnmarshalJSON(data []byte) error {
	var text string
	if err := jsonUnmarshal(data, &text); err == nil {
		*c = ContentList{Text: &text}
		return nil
	}
	var blocks []ContentBlock
	if err := jsonUnmarshal(data, &blocks); err != nil {
		return err
	}
	*c = ContentList{Blocks: blocks}
	return nil
}

func anthropicRole(role lc.MessageRole) string {
	if role == lc.RoleAI {
		return "assistant"
	}
	return "user"
}

func contentFromLC(msg lc.BaseMessage) ContentList {
	if msg.Type == lc.RoleTool {
		return ContentList{Blocks: []ContentBlock{{
			Type:      "tool_result",
			ToolUseID: msg.ToolCallID,
			Content:   textOrParts(msg.Content),
		}}}
	}
	if msg.Content.Text != nil && len(msg.ToolCalls) == 0 {
		return ContentList{Text: msg.Content.Text}
	}
	blocks := make([]ContentBlock, 0, len(msg.Content.Parts)+len(msg.ToolCalls))
	for _, part := range msg.Content.Parts {
		blocks = append(blocks, blockFromLC(part))
	}
	for _, call := range msg.ToolCalls {
		blocks = append(blocks, ContentBlock{
			Type:  "tool_use",
			ID:    call.ID,
			Name:  call.Name,
			Input: call.Args,
		})
	}
	return ContentList{Blocks: blocks}
}

func blockFromLC(part lc.ContentPart) ContentBlock {
	block := ContentBlock{
		Type:      part.Type,
		Text:      part.Text,
		ID:        part.ID,
		Name:      part.Name,
		Input:     part.Input,
		ToolUseID: part.ToolCallID,
		Content:   part.Content,
	}
	if part.Source != nil {
		block.Source = part.Source
	}
	if part.Type == "thinking" {
		block.Thinking = part.Text
		block.Text = ""
	}
	if value, ok := part.Extra["thinking"].(string); ok {
		block.Thinking = value
	}
	if value, ok := part.Extra["signature"].(string); ok {
		block.Signature = value
	}
	if value, ok := part.Extra["data"].(string); ok {
		block.Data = value
	}
	return block
}

func partsFromAnthropic(blocks []ContentBlock) []lc.ContentPart {
	parts := make([]lc.ContentPart, 0, len(blocks))
	for _, block := range blocks {
		part := lc.ContentPart{
			Type:       block.Type,
			Text:       block.Text,
			ID:         block.ID,
			Name:       block.Name,
			Input:      block.Input,
			ToolCallID: block.ToolUseID,
			Content:    block.Content,
		}
		switch block.Type {
		case "thinking":
			part.Text = block.Thinking
			part.Extra = map[string]any{
				"thinking":  block.Thinking,
				"signature": block.Signature,
			}
		case "redacted_thinking":
			part.Extra = map[string]any{"data": block.Data}
		}
		parts = append(parts, part)
	}
	return parts
}

func textOrParts(content lc.Content) any {
	if content.Text != nil {
		return *content.Text
	}
	return content.Parts
}
