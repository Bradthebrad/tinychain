package lc

import "strings"

// VisibleReasoning returns only explicitly labelled provider summaries.
// Raw reasoning/thinking, encrypted state and redacted blocks are never display text.
func VisibleReasoning(message BaseMessage) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	var summaries func(any)
	summaries = func(v any) {
		switch x := v.(type) {
		case string:
			add(x)
		case []string:
			for _, s := range x {
				add(s)
			}
		case []any:
			for _, s := range x {
				summaries(s)
			}
		case map[string]any:
			if t, _ := x["type"].(string); t == "summary_text" || t == "reasoning.summary" {
				summaries(x["text"])
				summaries(x["summary"])
			}
		}
	}
	summaries(message.AdditionalKwargs["reasoning_summaries"])
	var details func(any)
	details = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, s := range x {
				details(s)
			}
		case []map[string]any:
			for _, s := range x {
				details(s)
			}
		case map[string]any:
			summaries(x)
		}
	}
	details(message.AdditionalKwargs["reasoning_details"])
	for _, p := range message.Content.Parts {
		if p.Type == "summary_text" || p.Type == "reasoning.summary" {
			add(p.Text)
		}
	}
	return out
}
