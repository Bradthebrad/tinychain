package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"tinychain/callbacks"
	"tinychain/lc"
)

type ContextPolicy struct {
	Enabled               bool
	MaxTokens             int
	ThresholdTokens       int
	ThresholdRatio        float64
	KeepLastMessages      int
	MaxMessages           int
	ToolResultSafetyChars int
	ReserveTokens         int
	CompactionMaxChars    int
}

func normalizeContextPolicy(policy ContextPolicy) ContextPolicy {
	if policy.ThresholdRatio <= 0 || policy.ThresholdRatio >= 1 {
		policy.ThresholdRatio = 0.75
	}
	if policy.MaxTokens > 0 && policy.ThresholdTokens <= 0 {
		policy.ThresholdTokens = int(float64(policy.MaxTokens) * policy.ThresholdRatio)
	}
	if policy.KeepLastMessages <= 0 {
		policy.KeepLastMessages = 12
	}
	if policy.ToolResultSafetyChars <= 0 {
		policy.ToolResultSafetyChars = 12000
	}
	if policy.ReserveTokens <= 0 && policy.MaxTokens > 0 {
		policy.ReserveTokens = maxInt(2048, policy.MaxTokens/20)
	}
	if policy.CompactionMaxChars <= 0 {
		policy.CompactionMaxChars = 80000
	}
	return policy
}

func (a *Agent) compactIfNeeded(ctx context.Context, messages []lc.BaseMessage, runID string, force bool) []lc.BaseMessage {
	policy := a.context
	if !policy.Enabled {
		return messages
	}
	estimated := EstimateTokens(messages)
	overTokenLimit := policy.ThresholdTokens > 0 && estimated >= policy.ThresholdTokens
	overMessageLimit := policy.MaxMessages > 0 && len(messages) >= policy.MaxMessages
	if !force && !overTokenLimit && !overMessageLimit {
		return messages
	}
	next, err := a.compactMessages(ctx, messages, runID, estimated, force)
	if err != nil {
		if a.callbacks != nil {
			a.callbacks.Handle(callbacks.Error(callbacks.EventChainError, runID, err))
		}
		return messages
	}
	return next
}

func (a *Agent) compactMessages(ctx context.Context, messages []lc.BaseMessage, runID string, estimated int, force bool) ([]lc.BaseMessage, error) {
	policy := a.context
	if len(messages) <= policy.KeepLastMessages+1 {
		return messages, nil
	}
	start := compactionBoundary(messages, policy.KeepLastMessages)
	first := firstNonSystemMessage(messages)
	if start <= first {
		return messages, nil
	}
	old := append([]lc.BaseMessage{}, messages[first:start]...)
	recent := append([]lc.BaseMessage{}, messages[start:]...)
	prefix := append([]lc.BaseMessage{}, messages[:first]...)
	if len(old) == 0 {
		return messages, nil
	}

	if a.callbacks != nil {
		a.callbacks.Handle(callbacks.Event{
			Event: callbacks.EventChainStart,
			Name:  "context_compaction",
			RunID: runID,
			Time:  time.Now().UTC(),
			Data: callbacks.EventData{Extra: map[string]any{
				"estimated_tokens": estimated,
				"threshold_tokens": policy.ThresholdTokens,
				"force":            force,
				"old_messages":     len(old),
				"recent_messages":  len(recent),
			}},
		})
	}

	summary := a.modelSummary(ctx, old)
	if strings.TrimSpace(summary) == "" {
		summary = deterministicSummary(old, policy)
	}
	summaryMessage := lc.System(strings.TrimSpace("Compacted prior conversation state. Use this summary as authoritative context for earlier messages and tool calls.\n\n" + summary))
	next := make([]lc.BaseMessage, 0, len(prefix)+1+len(recent))
	next = append(next, prefix...)
	next = append(next, summaryMessage)
	next = append(next, recent...)
	if EstimateTokens(next) >= EstimateTokens(messages) {
		summary = deterministicSummary(old, ContextPolicy{ToolResultSafetyChars: 1200, CompactionMaxChars: 24000})
		next = append([]lc.BaseMessage{}, prefix...)
		next = append(next, lc.System("Compacted prior conversation state.\n\n"+summary))
		next = append(next, recent...)
	}
	if a.callbacks != nil {
		a.callbacks.Handle(callbacks.Event{
			Event: callbacks.EventChainEnd,
			Name:  "context_compaction",
			RunID: runID,
			Time:  time.Now().UTC(),
			Data: callbacks.EventData{Output: summary, Extra: map[string]any{
				"before_tokens": estimated,
				"after_tokens":  EstimateTokens(next),
			}},
		})
	}
	return next, nil
}

func (a *Agent) modelSummary(ctx context.Context, messages []lc.BaseMessage) string {
	transcript := formatMessagesForCompaction(messages, a.context)
	if strings.TrimSpace(transcript) == "" {
		return ""
	}
	system := strings.Join([]string{
		"You compact agent conversation state.",
		"Summarize older user goals, assistant decisions, tool calls, tool results, files touched, errors, and completed work.",
		"Preserve durable facts and reasoning summaries. Do not invent results.",
		"Compress aggressively. Keep enough detail for the agent to continue without rereading the full older transcript.",
	}, " ")
	prompt := "Compact these older messages and tool results into a concise state summary.\n\n" + transcript
	msg, err := a.model.Call(ctx, []lc.BaseMessage{lc.System(system), lc.Human(prompt)}, nil)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(contentText(msg.Content))
}

func (a *Agent) shouldCompactAfterError(err error) bool {
	if err == nil || !a.context.Enabled {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "context window") ||
		strings.Contains(text, "exceeds the context") ||
		strings.Contains(text, "too many tokens") ||
		strings.Contains(text, "maximum context") ||
		strings.Contains(text, "context length")
}

func (a *Agent) guardToolOutput(messages []lc.BaseMessage, output string) string {
	policy := a.context
	if !policy.Enabled || policy.MaxTokens <= 0 || output == "" {
		return output
	}
	current := EstimateTokens(messages)
	projected := current + EstimateTextTokens(output) + policy.ReserveTokens
	if projected <= policy.MaxTokens {
		return output
	}
	limit := policy.ToolResultSafetyChars
	body := truncateRunes(output, limit)
	notice := fmt.Sprintf("\n\n[NullBot context guard: This tool result was truncated before returning to the model because the full result would exceed the remaining context window. Current estimate: %d tokens; projected with full result: %d tokens; model limit: %d tokens. Ask for a smaller subset, use filters/ranges, or create/use tooling that reads the data in smaller chunks.]", current, projected, policy.MaxTokens)
	return body + notice
}

func EstimateTokens(messages []lc.BaseMessage) int {
	total := 0
	for _, msg := range messages {
		total += 8
		total += EstimateTextTokens(string(msg.Type))
		total += EstimateContentTokens(msg.Content)
		if msg.ToolCallID != "" {
			total += EstimateTextTokens(msg.ToolCallID)
		}
		for _, call := range msg.ToolCalls {
			total += EstimateTextTokens(call.Name) + EstimateTextTokens(call.ID) + EstimateAnyTokens(call.Args) + 8
		}
		if len(msg.AdditionalKwargs) > 0 {
			total += EstimateAnyTokens(msg.AdditionalKwargs)
		}
	}
	return total
}

func EstimateContentTokens(content lc.Content) int {
	if content.Text != nil {
		return EstimateTextTokens(*content.Text)
	}
	total := 0
	for _, part := range content.Parts {
		total += EstimateTextTokens(part.Type) + EstimateTextTokens(part.Text) + EstimateTextTokens(part.Name) + EstimateAnyTokens(part.Input) + 6
		if part.Source != nil {
			total += EstimateTextTokens(part.Source.MediaType) + EstimateTextTokens(part.Source.Data) + EstimateTextTokens(part.Source.URL)
		}
		if part.Content != nil {
			total += EstimateAnyTokens(part.Content)
		}
	}
	return total
}

func EstimateAnyTokens(value any) int {
	if value == nil {
		return 0
	}
	data, err := json.Marshal(value)
	if err != nil {
		return EstimateTextTokens(fmt.Sprint(value))
	}
	return EstimateTextTokens(string(data))
}

func EstimateTextTokens(text string) int {
	runes := utf8.RuneCountInString(text)
	if runes == 0 {
		return 0
	}
	return (runes+3)/4 + 1
}

func compactionBoundary(messages []lc.BaseMessage, keep int) int {
	if keep <= 0 {
		keep = 12
	}
	start := len(messages) - keep
	if start < firstNonSystemMessage(messages) {
		start = firstNonSystemMessage(messages)
	}
	for {
		changed := false
		retainedToolIDs := map[string]bool{}
		for _, msg := range messages[start:] {
			if msg.Type == lc.RoleTool && msg.ToolCallID != "" {
				retainedToolIDs[msg.ToolCallID] = true
			}
		}
		for id := range retainedToolIDs {
			for i := start - 1; i >= firstNonSystemMessage(messages); i-- {
				if messageHasToolCall(messages[i], id) {
					start = i
					changed = true
					break
				}
			}
		}
		if !changed {
			break
		}
	}
	return start
}

func firstNonSystemMessage(messages []lc.BaseMessage) int {
	for i, msg := range messages {
		if msg.Type != lc.RoleSystem && msg.Type != lc.RoleDeveloper {
			return i
		}
	}
	return len(messages)
}

func messageHasToolCall(msg lc.BaseMessage, id string) bool {
	for _, call := range msg.ToolCalls {
		if call.ID == id {
			return true
		}
	}
	return false
}

func formatMessagesForCompaction(messages []lc.BaseMessage, policy ContextPolicy) string {
	var b strings.Builder
	limit := policy.CompactionMaxChars
	if limit <= 0 {
		limit = 80000
	}
	perTool := policy.ToolResultSafetyChars
	if perTool <= 0 {
		perTool = 12000
	}
	for i, msg := range messages {
		if b.Len() >= limit {
			b.WriteString("\n[Older transcript omitted from compaction prompt due to size.]\n")
			break
		}
		fmt.Fprintf(&b, "\n--- message %d role=%s", i+1, msg.Type)
		if msg.ToolCallID != "" {
			fmt.Fprintf(&b, " tool_call_id=%s", msg.ToolCallID)
		}
		b.WriteString(" ---\n")
		text := contentText(msg.Content)
		if msg.Type == lc.RoleTool {
			text = truncateRunes(text, perTool)
		} else {
			text = truncateRunes(text, maxInt(4000, perTool/2))
		}
		b.WriteString(text)
		if len(msg.ToolCalls) > 0 {
			data, _ := json.Marshal(msg.ToolCalls)
			b.WriteString("\nTool calls: ")
			b.WriteString(truncateRunes(string(data), 4000))
		}
		for _, reasoning := range lc.VisibleReasoning(msg) {
			b.WriteString("\nReasoning summary: ")
			b.WriteString(truncateRunes(reasoning, 2000))
		}
		if b.Len() > limit {
			return truncateRunes(b.String(), limit)
		}
	}
	return strings.TrimSpace(b.String())
}

func deterministicSummary(messages []lc.BaseMessage, policy ContextPolicy) string {
	var b strings.Builder
	b.WriteString("Deterministic compaction summary of older context:\n")
	for i, msg := range messages {
		text := strings.ReplaceAll(contentText(msg.Content), "\n", " ")
		if msg.Type == lc.RoleTool {
			fmt.Fprintf(&b, "- tool result %s: %s\n", firstNonEmpty(msg.ToolCallID, fmt.Sprintf("#%d", i+1)), truncateRunes(text, 600))
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", msg.Type, truncateRunes(text, 500))
		if len(msg.ToolCalls) > 0 {
			names := make([]string, 0, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				names = append(names, call.Name)
			}
			fmt.Fprintf(&b, "  tool calls: %s\n", strings.Join(names, ", "))
		}
	}
	return truncateRunes(strings.TrimSpace(b.String()), maxInt(policy.CompactionMaxChars, 12000))
}

func truncateRunes(text string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit]) + "\n[truncated]"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
