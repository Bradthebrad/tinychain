package lc

import "encoding/json"

// ContentPart represents LangChain-style mixed message content.
//
// LangChain accepts message content as either a plain string or a list of
// provider-specific content blocks. Keeping Raw map values here preserves
// unknown provider fields without pulling in a large schema layer.
type ContentPart struct {
	Type       string         `json:"type"`
	Text       string         `json:"text,omitempty"`
	ID         string         `json:"id,omitempty"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Source     *ContentSource `json:"source,omitempty"`
	Input      map[string]any `json:"input,omitempty"`
	Content    any            `json:"content,omitempty"`
	Extra      map[string]any `json:"-"`
}

func (p ContentPart) MarshalJSON() ([]byte, error) {
	type alias ContentPart
	baseBytes, err := json.Marshal(alias(p))
	if err != nil {
		return nil, err
	}
	if len(p.Extra) == 0 {
		return baseBytes, nil
	}
	var fields map[string]any
	if err := json.Unmarshal(baseBytes, &fields); err != nil {
		return nil, err
	}
	for key, value := range p.Extra {
		fields[key] = value
	}
	return json.Marshal(fields)
}

type ContentSource struct {
	Type      string `json:"type,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type Content struct {
	Text  *string
	Parts []ContentPart
}

func TextContent(text string) Content {
	return Content{Text: &text}
}

func PartsContent(parts ...ContentPart) Content {
	return Content{Parts: parts}
}

func (c Content) IsZero() bool {
	return c.Text == nil && len(c.Parts) == 0
}

func (c Content) MarshalJSON() ([]byte, error) {
	if c.Text != nil {
		return json.Marshal(*c.Text)
	}
	if c.Parts == nil {
		return []byte("null"), nil
	}
	return json.Marshal(c.Parts)
}

func (c *Content) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*c = Content{}
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*c = TextContent(text)
		return nil
	}
	var parts []ContentPart
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	*c = PartsContent(parts...)
	return nil
}
