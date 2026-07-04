package lc

import (
	"fmt"
	"strings"
)

// VisibleReasoning returns provider-exposed reasoning text or summaries.
// It intentionally ignores encrypted/redacted reasoning blocks.
func VisibleReasoning(message BaseMessage) []string {
	var out []string
	seen := map[string]bool{}
	add := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" || seen[text] {
			return
		}
		seen[text] = true
		out = append(out, text)
	}

	for _, part := range message.Content.Parts {
		typ := strings.ToLower(strings.TrimSpace(part.Type))
		if reasoningPartType(typ) {
			add(part.Text)
			collectReasoningValue(part.Extra, add, 0)
			collectReasoningValue(part.Content, add, 0)
		}
	}
	collectReasoningValue(message.AdditionalKwargs["reasoning"], add, 0)
	collectReasoningValue(message.AdditionalKwargs["reasoning_content"], add, 0)
	collectReasoningValue(message.AdditionalKwargs["reasoning_details"], add, 0)
	collectReasoningValue(message.AdditionalKwargs["reasoning_summaries"], add, 0)
	collectReasoningValue(message.AdditionalKwargs["thinking"], add, 0)
	collectReasoningValue(message.ResponseMetadata["reasoning"], add, 0)
	return out
}

func reasoningPartType(typ string) bool {
	switch typ {
	case "thinking", "reasoning", "reasoning.text", "reasoning.summary", "summary_text":
		return true
	case "redacted_thinking", "reasoning.encrypted":
		return false
	default:
		return strings.Contains(typ, "reasoning") || strings.Contains(typ, "thinking")
	}
}

func collectReasoningValue(value any, add func(string), depth int) {
	if value == nil || depth > 8 {
		return
	}
	switch v := value.(type) {
	case string:
		add(v)
	case []string:
		for _, item := range v {
			add(item)
		}
	case []any:
		for _, item := range v {
			collectReasoningValue(item, add, depth+1)
		}
	case map[string]string:
		if skipReasoningMap(v["type"]) {
			return
		}
		for _, key := range []string{"text", "summary", "thinking", "reasoning", "reasoning_content"} {
			add(v[key])
		}
	case map[string]any:
		if skipReasoningMap(fmt.Sprint(v["type"])) {
			return
		}
		for _, key := range []string{"text", "summary", "thinking", "reasoning", "reasoning_content"} {
			collectReasoningValue(v[key], add, depth+1)
		}
		for _, key := range []string{"summary_text", "summaries", "reasoning_details", "content"} {
			collectReasoningValue(v[key], add, depth+1)
		}
	}
}

func skipReasoningMap(typ string) bool {
	typ = strings.ToLower(strings.TrimSpace(typ))
	return strings.Contains(typ, "encrypted") || strings.Contains(typ, "redacted")
}
