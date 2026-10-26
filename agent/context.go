package agent

// Context management.
//
// Before every model call the agent measures how full the model's context is
// and, when it needs to, works in two tiers:
//
//  1. Shorten (no model call). From SoftRatio of the window (60% by default),
//     tool results the model has already seen become a digest: the tool, its
//     arguments and the start of its output. Long string arguments of earlier
//     tool calls are shortened the same way. Results of PreserveTools (skills)
//     are never shortened. It works oldest first and stops once usage is back
//     under the target, so it runs rarely rather than on every call.
//  2. Summarize (one model call). From HardRatio (85% by default), if
//     shortening wasn't enough, older messages are summarized by the model and
//     replaced by the summary. Recent messages, about RecentRatio of the
//     window, stay word for word, and so does the request being worked on.
//
// Every tool result is also measured as it arrives, whenever the window is
// known, even with compaction turned off. One that wouldn't fit in the room
// left is cut to a preview with a notice giving its size, so the model can
// read it in smaller pieces instead of overflowing the window.
//
// Estimates count messages and tool definitions at about four characters per
// token, then follow what the provider reports: each reply's input token count
// rescales later estimates, and a context-length error that names the real
// limit lowers the window for the rest of the run.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Bradthebrad/tinychain/callbacks"
	"github.com/Bradthebrad/tinychain/lc"
)

type ContextPolicy struct {
	Enabled bool
	// MaxTokens is the model's context window. Zero means unknown: only
	// ThresholdTokens and MaxMessages can then trigger compaction.
	MaxTokens int
	// SoftRatio is where shortening starts (default 0.60).
	SoftRatio float64
	// HardRatio is where summarizing starts (default 0.85).
	HardRatio float64
	// ThresholdTokens, when set, is the summarizing threshold in tokens and
	// takes precedence over HardRatio.
	ThresholdTokens int
	// ThresholdRatio is the former name of HardRatio, still honored.
	ThresholdRatio float64
	// RecentRatio is how much of the window summarizing keeps word for word
	// (default 0.25, never more than half of SoftRatio).
	RecentRatio float64
	// KeepLastMessages is how many messages summarizing keeps when the
	// window is unknown (default 12).
	KeepLastMessages int
	// MaxMessages, when set, also starts shortening at this many messages.
	MaxMessages int
	// ToolResultSafetyChars is the most of an oversized tool result the guard
	// shows (default 12000).
	ToolResultSafetyChars int
	// ToolDigestChars is how much of a tool's output a digest keeps
	// (default 600).
	ToolDigestChars int
	// ToolArgChars is how much of each long string argument a digest keeps
	// (default 300).
	ToolArgChars int
	// PreserveTools names tools whose results are never shortened, such as
	// the tool that reads a skill. Summarizing carries them over word for word.
	PreserveTools []string
	// ReserveTokens is room kept free for the model's reply.
	ReserveTokens int
	// CompactionMaxChars caps the transcript sent to the summarizer.
	CompactionMaxChars int
	// Focus says what summaries should keep, such as "decisions and paths".
	Focus string
	// Note is added to every summary, for example to say where the full
	// history can be read.
	Note string
}

func normalizeContextPolicy(policy ContextPolicy) ContextPolicy {
	if policy.HardRatio <= 0 || policy.HardRatio >= 1 {
		if policy.ThresholdRatio > 0 && policy.ThresholdRatio < 1 {
			policy.HardRatio = policy.ThresholdRatio
		} else {
			policy.HardRatio = 0.85
		}
	}
	policy.ThresholdRatio = policy.HardRatio
	if policy.SoftRatio <= 0 {
		policy.SoftRatio = 0.60
	}
	if policy.SoftRatio >= policy.HardRatio {
		policy.SoftRatio = policy.HardRatio * 0.7
	}
	if policy.RecentRatio <= 0 || policy.RecentRatio > policy.SoftRatio/2 {
		policy.RecentRatio = math.Min(0.25, policy.SoftRatio/2)
	}
	if policy.KeepLastMessages <= 0 {
		policy.KeepLastMessages = 12
	}
	if policy.ToolResultSafetyChars <= 0 {
		policy.ToolResultSafetyChars = 12000
	}
	if policy.ToolDigestChars <= 0 {
		policy.ToolDigestChars = 600
	}
	if policy.ToolArgChars <= 0 {
		policy.ToolArgChars = 300
	}
	if policy.ReserveTokens <= 0 && policy.MaxTokens > 0 {
		policy.ReserveTokens = minInt(maxInt(2048, policy.MaxTokens/20), policy.MaxTokens/4)
	}
	if policy.CompactionMaxChars <= 0 {
		policy.CompactionMaxChars = 80000
		if policy.MaxTokens > 0 {
			policy.CompactionMaxChars = clampInt(policy.MaxTokens*4*2/5, 24000, 240000)
		}
	}
	return policy
}

// contextMeter tracks one run's context use. It belongs to a single run, so
// it needs no locking.
type contextMeter struct {
	policy     ContextPolicy
	limit      int
	hard       int
	soft       int
	toolTokens int
	scale      float64
	observed   bool
	learned    int
}

func newContextMeter(policy ContextPolicy, tools []Tool) *contextMeter {
	m := &contextMeter{policy: policy, limit: policy.MaxTokens, scale: 1}
	for _, tool := range tools {
		m.toolTokens += EstimateAnyTokens(tool.Definition()) + 8
	}
	m.recompute()
	return m
}

func (m *contextMeter) recompute() {
	p := m.policy
	switch {
	case p.ThresholdTokens > 0 && p.MaxTokens > 0:
		m.hard = int(float64(p.ThresholdTokens) * float64(m.limit) / float64(p.MaxTokens))
	case p.ThresholdTokens > 0:
		m.hard = p.ThresholdTokens
	default:
		m.hard = int(float64(m.limit) * p.HardRatio)
	}
	m.soft = int(float64(m.hard) * p.SoftRatio / p.HardRatio)
}

// target is where shortening stops: well under the soft threshold, so it
// doesn't run again on the very next call.
func (m *contextMeter) target() int { return m.soft * 3 / 4 }

func (m *contextMeter) raw(messages []lc.BaseMessage) int {
	return EstimateTokens(messages) + m.toolTokens
}

func (m *contextMeter) scaled(tokens int) int {
	return int(math.Ceil(float64(tokens) * m.scale))
}

func (m *contextMeter) estimate(messages []lc.BaseMessage) int {
	return m.scaled(m.raw(messages))
}

// observe rescales later estimates by what the provider counted for the
// request that produced reply.
func (m *contextMeter) observe(sentRaw int, reply lc.BaseMessage) {
	reported := PromptTokens(reply.UsageMetadata)
	if reported <= 0 || sentRaw <= 0 {
		return
	}
	ratio := math.Max(0.5, math.Min(4, float64(reported)/float64(sentRaw)))
	if m.observed {
		ratio = (m.scale + ratio) / 2
	}
	m.scale, m.observed = ratio, true
}

var contextLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)maximum context length is ([\d,]+)`),
	regexp.MustCompile(`(?i)[\d,]+ tokens > ([\d,]+) maximum`),
	regexp.MustCompile(`(?i)context (?:window|length|limit) of ([\d,]+)`),
	regexp.MustCompile(`(?i)limit of ([\d,]+) tokens`),
}

// learnLimit lowers the window when a provider error names a smaller one.
func (m *contextMeter) learnLimit(err error) bool {
	if err == nil {
		return false
	}
	limit := ContextLimitFromError(err)
	if limit <= 0 || (m.limit > 0 && limit >= m.limit) {
		return false
	}
	m.limit, m.learned = limit, limit
	m.recompute()
	return true
}

// ContextLimitFromError returns the context window named in a provider's
// context-length error, or 0.
func ContextLimitFromError(err error) int {
	if err == nil {
		return 0
	}
	for _, pattern := range contextLimitPatterns {
		if match := pattern.FindStringSubmatch(err.Error()); match != nil {
			if n, convErr := strconv.Atoi(strings.ReplaceAll(match[1], ",", "")); convErr == nil && n >= 1024 {
				return n
			}
		}
	}
	return 0
}

// PromptTokens is the input a provider counted for a request, including
// tokens it read from or wrote to its prompt cache.
func PromptTokens(usage *lc.UsageMetadata) int {
	if usage == nil {
		return 0
	}
	return usage.InputTokens + usage.InputTokenDetails["cache_read_input_tokens"] + usage.InputTokenDetails["cache_creation_input_tokens"]
}

func (m *contextMeter) snapshot(tokens int) map[string]any {
	out := map[string]any{"context_tokens": tokens, "tool_tokens": m.scaled(m.toolTokens)}
	if m.limit > 0 {
		out["context_window"] = m.limit
		out["context_ratio"] = math.Round(float64(tokens)/float64(m.limit)*1000) / 1000
	}
	if m.soft > 0 {
		out["shorten_at"] = m.soft
	}
	if m.hard > 0 {
		out["summarize_at"] = m.hard
	}
	if m.learned > 0 {
		out["learned_window"] = m.learned
	}
	return out
}

// fitContext runs the two tiers before a model call. force shortens
// everything already seen and summarizes regardless of usage, as after a
// context-length error.
func (a *Agent) fitContext(ctx context.Context, messages []lc.BaseMessage, runID string, force bool) []lc.BaseMessage {
	if !a.context.Enabled || a.meter == nil {
		return messages
	}
	m := a.meter
	before := m.estimate(messages)
	overSoft := m.soft > 0 && before >= m.soft
	overCount := a.context.MaxMessages > 0 && len(messages) >= a.context.MaxMessages
	if force || overSoft || overCount {
		target := m.target()
		if force || m.soft <= 0 {
			target = 0
		}
		next, shortened := shortenSeenToolResults(messages, m, target, a.context)
		if shortened > 0 {
			messages = next
			a.emitContext(runID, "context_shorten", before, m.estimate(messages), map[string]any{"shortened": shortened, "force": force})
		}
	}
	current := m.estimate(messages)
	if force || (m.hard > 0 && current >= m.hard) {
		budget := -1
		if m.limit > 0 {
			budget = int(float64(m.limit) * a.context.RecentRatio)
		}
		next, report, err := a.summarizeOlder(ctx, messages, runID, current, budget, "", force, true)
		if err != nil {
			if a.callbacks != nil {
				a.callbacks.Handle(callbacks.Error(callbacks.EventChainError, runID, err))
			}
			return messages
		}
		if report.Summarized > 0 {
			messages = next
		}
	}
	return messages
}

func (a *Agent) emitContext(runID, name string, before, after int, extra map[string]any) {
	if a.callbacks == nil {
		return
	}
	data := a.meter.snapshot(after)
	data["before_tokens"] = before
	data["after_tokens"] = after
	for key, value := range extra {
		data[key] = value
	}
	a.callbacks.Handle(callbacks.Event{
		Event: callbacks.EventChainEnd,
		Name:  name,
		RunID: runID,
		Time:  time.Now().UTC(),
		Data:  callbacks.EventData{Extra: data},
	})
}

// shortenedMarker starts every tool-result digest, so a digest is never
// shortened again.
const shortenedMarker = "[Shortened earlier tool result]"

// summaryMarker starts every summary, so a later summary folds it in.
const summaryMarker = "Summary of earlier work in this conversation. Treat it as an accurate record of the messages and tool results it replaces."

// shortenSeenToolResults replaces tool results the model has already seen
// with digests, oldest first, until the estimate is at or under target. It
// returns a new slice and how many messages it changed.
func shortenSeenToolResults(messages []lc.BaseMessage, m *contextMeter, target int, policy ContextPolicy) ([]lc.BaseMessage, int) {
	seen := lastIndexOfRole(messages, lc.RoleAI)
	if seen <= 0 {
		return messages, 0
	}
	calls := toolCallsByID(messages)
	preserve := preservedSet(policy.PreserveTools)
	estimate := m.estimate(messages)
	var out []lc.BaseMessage
	changed := 0
	for i := firstNonSystemMessage(messages); i < seen && estimate > target; i++ {
		msg := messages[i]
		var next lc.BaseMessage
		ok := false
		switch msg.Type {
		case lc.RoleTool:
			call := calls[msg.ToolCallID]
			if preserve[call.Name] || strings.HasPrefix(contentText(msg.Content), shortenedMarker) {
				continue
			}
			next = msg
			next.Content = lc.TextContent(toolDigest(call, msg, policy))
			ok = true
		case lc.RoleAI:
			next, ok = shortenToolArgs(msg, policy.ToolArgChars)
		}
		if !ok {
			continue
		}
		delta := m.scaled(EstimateTokens([]lc.BaseMessage{next})) - m.scaled(EstimateTokens([]lc.BaseMessage{msg}))
		if delta >= 0 {
			continue
		}
		if out == nil {
			out = append([]lc.BaseMessage(nil), messages...)
		}
		out[i] = next
		estimate += delta
		changed++
	}
	if out == nil {
		return messages, 0
	}
	return out, changed
}

// reclaimable estimates how many tokens shortening could free right now.
func (m *contextMeter) reclaimable(messages []lc.BaseMessage) int {
	next, _ := shortenSeenToolResults(messages, m, 0, m.policy)
	return maxInt(0, m.estimate(messages)-m.estimate(next))
}

func toolDigest(call lc.ToolCall, msg lc.BaseMessage, policy ContextPolicy) string {
	text := contentText(msg.Content)
	total := utf8.RuneCountInString(text)
	name := firstNonEmpty(call.Name, "tool")
	verb := "returned"
	if msg.Status == "error" {
		verb = "failed with"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s %s %s characters", shortenedMarker, name, compactArgs(call.Args, policy.ToolArgChars), verb, formatCount(total))
	if others := nonTextParts(msg.Content); others > 0 {
		fmt.Fprintf(&b, " and %d non-text part(s), not shown", others)
	}
	if total <= policy.ToolDigestChars {
		b.WriteString(":\n")
		b.WriteString(text)
		return b.String()
	}
	head := string([]rune(text)[:policy.ToolDigestChars])
	fmt.Fprintf(&b, "; the first %s follow.\n%s\n[%s more characters not shown. Run the tool again, with a narrower range if you can, to see them.]",
		formatCount(policy.ToolDigestChars), head, formatCount(total-policy.ToolDigestChars))
	return b.String()
}

// shortenToolArgs cuts long string arguments in an assistant message's tool
// calls. The arguments stay valid JSON.
func shortenToolArgs(msg lc.BaseMessage, limit int) (lc.BaseMessage, bool) {
	if len(msg.ToolCalls) == 0 {
		return msg, false
	}
	changed := false
	calls := make([]lc.ToolCall, len(msg.ToolCalls))
	for i, call := range msg.ToolCalls {
		calls[i] = call
		if args, ok := shortenValue(call.Args, limit).(map[string]any); ok && !equalJSON(args, call.Args) {
			calls[i].Args = args
			changed = true
		}
	}
	if !changed {
		return msg, false
	}
	msg.ToolCalls = calls
	return msg, true
}

func shortenValue(value any, limit int) any {
	switch v := value.(type) {
	case string:
		if n := utf8.RuneCountInString(v); n > limit {
			return string([]rune(v)[:limit]) + fmt.Sprintf(" [%s more characters]", formatCount(n-limit))
		}
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = shortenValue(item, limit)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = shortenValue(item, limit)
		}
		return out
	default:
		return value
	}
}

func equalJSON(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func compactArgs(args map[string]any, limit int) string {
	if len(args) == 0 {
		return "{}"
	}
	data, err := json.Marshal(shortenValue(args, minInt(limit, 160)))
	if err != nil {
		return "{…}"
	}
	return truncatePlain(string(data), maxInt(limit, 80))
}

// summarizeOlder replaces older messages with a model-written summary,
// keeping about recentBudget tokens of recent messages word for word. A
// negative budget keeps KeepLastMessages messages; zero keeps none.
func (a *Agent) summarizeOlder(ctx context.Context, messages []lc.BaseMessage, runID string, estimated, recentBudget int, focus string, force, keepRequest bool) ([]lc.BaseMessage, CompactionReport, error) {
	report := CompactionReport{BeforeTokens: estimated, AfterTokens: estimated}
	policy := a.context
	first := firstNonSystemMessage(messages)
	var prefix, earlier []lc.BaseMessage
	for _, msg := range messages[:first] {
		if msg.Type == lc.RoleSystem && strings.HasPrefix(contentText(msg.Content), summaryMarker) {
			earlier = append(earlier, msg)
			continue
		}
		prefix = append(prefix, msg)
	}
	start := a.recentStart(messages, first, recentBudget)
	if start <= first {
		return messages, report, nil
	}
	old := append(earlier, messages[first:start]...)
	recent := append([]lc.BaseMessage{}, messages[start:]...)

	if a.callbacks != nil {
		data := a.meterSnapshot(estimated)
		data["force"] = force
		data["old_messages"] = len(old)
		data["recent_messages"] = len(recent)
		a.callbacks.Handle(callbacks.Event{
			Event: callbacks.EventChainStart,
			Name:  "context_compaction",
			RunID: runID,
			Time:  time.Now().UTC(),
			Data:  callbacks.EventData{Extra: data},
		})
	}

	summary, usage := a.modelSummary(ctx, old, firstNonEmpty(focus, policy.Focus))
	if strings.TrimSpace(summary) == "" {
		summary = deterministicSummary(old, policy)
	}
	kept := keptText(old, recent, policy, keepRequest)
	build := func(summary string) []lc.BaseMessage {
		next := make([]lc.BaseMessage, 0, len(prefix)+2+len(recent))
		next = append(next, prefix...)
		if kept != "" {
			next = append(next, lc.System(kept))
		}
		next = append(next, lc.System(summaryText(summary, policy)))
		return append(next, recent...)
	}
	next := build(summary)
	if a.estimateMessages(next) >= a.estimateMessages(messages) {
		short := deterministicSummary(old, ContextPolicy{ToolResultSafetyChars: 1200, CompactionMaxChars: 24000})
		if alt := build(short); a.estimateMessages(alt) < a.estimateMessages(next) {
			summary, next = short, alt
		}
	}
	report.AfterTokens = a.estimateMessages(next)
	report.Summarized = len(old)
	report.Summary = summary
	if a.callbacks != nil {
		data := a.meterSnapshot(report.AfterTokens)
		data["before_tokens"] = estimated
		data["after_tokens"] = report.AfterTokens
		data["summarized"] = len(old)
		if usage != nil {
			data["summary_usage"] = usage
		}
		a.callbacks.Handle(callbacks.Event{
			Event: callbacks.EventChainEnd,
			Name:  "context_compaction",
			RunID: runID,
			Time:  time.Now().UTC(),
			Data:  callbacks.EventData{Output: summary, Extra: data},
		})
	}
	return next, report, nil
}

func (a *Agent) estimateMessages(messages []lc.BaseMessage) int {
	if a.meter != nil {
		return a.meter.estimate(messages)
	}
	return EstimateTokens(messages)
}

func (a *Agent) meterSnapshot(tokens int) map[string]any {
	if a.meter != nil {
		return a.meter.snapshot(tokens)
	}
	return map[string]any{"context_tokens": tokens}
}

// summaryText is the message that replaces older messages.
func summaryText(summary string, policy ContextPolicy) string {
	var b strings.Builder
	b.WriteString(summaryMarker)
	b.WriteString("\n\n")
	b.WriteString(strings.TrimSpace(summary))
	if note := strings.TrimSpace(policy.Note); note != "" {
		b.WriteString("\n\n")
		b.WriteString(note)
	}
	return b.String()
}

// keptMarker starts the message that carries text a summary must not
// paraphrase. It is an ordinary leading system message, so later summaries
// keep it as it is.
const keptMarker = "Kept word for word from earlier in this conversation."

// keptText carries skill text and the request being worked on out of the
// messages being summarized, because a summary of instructions is not the
// instructions.
func keptText(old, recent []lc.BaseMessage, policy ContextPolicy, keepRequest bool) string {
	var sections []string
	if keepRequest && lastIndexOfRole(recent, lc.RoleHuman) < 0 {
		if i := lastIndexOfRole(old, lc.RoleHuman); i >= 0 {
			sections = append(sections, "The request being worked on:\n"+truncateRunes(contentText(old[i].Content), 8000))
		}
	}
	if skills := preservedResults(old, policy.PreserveTools); skills != "" {
		sections = append(sections, "Skill instructions loaded earlier:\n\n"+skills)
	}
	if len(sections) == 0 {
		return ""
	}
	return keptMarker + "\n\n" + strings.Join(sections, "\n\n")
}

func preservedResults(messages []lc.BaseMessage, tools []string) string {
	preserve := preservedSet(tools)
	if len(preserve) == 0 {
		return ""
	}
	calls := toolCallsByID(messages)
	var parts []string
	for _, msg := range messages {
		if msg.Type != lc.RoleTool || !preserve[calls[msg.ToolCallID].Name] {
			continue
		}
		parts = append(parts, strings.TrimSpace(contentText(msg.Content)))
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// recentStart finds where the kept recent messages begin. It never splits a
// tool call from its results.
func (a *Agent) recentStart(messages []lc.BaseMessage, first, budget int) int {
	var start int
	switch {
	case budget < 0:
		start = len(messages) - a.context.KeepLastMessages
	case budget == 0:
		start = len(messages)
	default:
		start = len(messages)
		used := 0
		for start > first {
			tokens := a.estimateMessages(messages[start-1 : start])
			if used+tokens > budget && start < len(messages) {
				break
			}
			used += tokens
			start--
		}
	}
	if start < first {
		start = first
	}
	return keepToolPairs(messages, start, first)
}

func keepToolPairs(messages []lc.BaseMessage, start, first int) int {
	for {
		changed := false
		for _, msg := range messages[start:] {
			if msg.Type != lc.RoleTool || msg.ToolCallID == "" {
				continue
			}
			for i := start - 1; i >= first; i-- {
				if messageHasToolCall(messages[i], msg.ToolCallID) {
					start = i
					changed = true
					break
				}
			}
		}
		if !changed {
			return start
		}
	}
}

// compactionBoundary keeps the last keep messages and their tool calls.
func compactionBoundary(messages []lc.BaseMessage, keep int) int {
	if keep <= 0 {
		keep = 12
	}
	first := firstNonSystemMessage(messages)
	start := maxInt(len(messages)-keep, first)
	return keepToolPairs(messages, start, first)
}

// modelSummary asks the model for a summary. It also returns what the call
// used, so callers can account for it.
func (a *Agent) modelSummary(ctx context.Context, messages []lc.BaseMessage, focus string) (string, *lc.UsageMetadata) {
	transcript := formatMessagesForCompaction(messages, a.context)
	if strings.TrimSpace(transcript) == "" {
		return "", nil
	}
	system := strings.Join([]string{
		"You compact agent conversation state.",
		"Summarize older user goals, assistant decisions, tool calls, tool results, files touched, errors, and completed work.",
		"Preserve durable facts, exact names, paths, numbers and identifiers, and reasoning summaries. Do not invent results.",
		"Say what is finished and what is still open.",
		"Compress aggressively. Keep enough detail for the agent to continue without rereading the full older transcript.",
	}, " ")
	if focus = strings.TrimSpace(focus); focus != "" {
		system += " Keep in particular: " + focus
	}
	prompt := "Compact these older messages and tool results into a concise state summary.\n\n" + transcript
	msg, err := a.model.Call(ctx, []lc.BaseMessage{lc.System(system), lc.Human(prompt)}, nil)
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(contentText(msg.Content)), msg.UsageMetadata
}

// CompactionReport describes one compaction.
type CompactionReport struct {
	BeforeTokens int    `json:"before_tokens"`
	AfterTokens  int    `json:"after_tokens"`
	Shortened    int    `json:"shortened"`
	Summarized   int    `json:"summarized"`
	Summary      string `json:"summary,omitempty"`
	Window       int    `json:"window,omitempty"`
}

// CompactOptions control a compaction requested directly.
type CompactOptions struct {
	// Focus says what the summary should keep, in addition to the policy's.
	Focus string
	// KeepRecentTokens keeps about this many tokens of recent messages word
	// for word. Zero summarizes everything.
	KeepRecentTokens int
}

// Compact runs the same passes the agent runs before a model call, but now
// and regardless of usage: every tool result already seen is shortened, then
// older messages are summarized. Leading system messages are kept, and so is
// skill text. Use it for a compaction someone asks for between requests; the
// summary is the last message of the result.
func (a *Agent) Compact(ctx context.Context, messages []lc.BaseMessage, options CompactOptions) ([]lc.BaseMessage, CompactionReport, error) {
	if a.model == nil {
		return messages, CompactionReport{}, fmt.Errorf("agent: model is required")
	}
	local, ctx, runID := a.scopedRun(ctx)
	local.meter = newContextMeter(local.context, local.toolOrder)
	before := local.meter.estimate(messages)
	next, shortened := shortenSeenToolResults(messages, local.meter, 0, local.context)
	if shortened > 0 {
		local.emitContext(runID, "context_shorten", before, local.meter.estimate(next), map[string]any{"shortened": shortened, "force": true})
	}
	summarized, report, err := local.summarizeOlder(ctx, next, runID, local.meter.estimate(next), maxInt(0, options.KeepRecentTokens), options.Focus, true, false)
	report.BeforeTokens = before
	report.Shortened = shortened
	report.Window = local.meter.limit
	if err != nil {
		return next, report, err
	}
	if report.Summarized == 0 {
		report.AfterTokens = local.meter.estimate(next)
		return next, report, nil
	}
	return summarized, report, nil
}

func (a *Agent) shouldCompactAfterError(err error) bool {
	if err == nil || !a.context.Enabled {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, phrase := range []string{"context window", "exceeds the context", "too many tokens", "maximum context", "context length", "context_length_exceeded", "prompt is too long", "input is too long", "reduce the length"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

// guardToolOutput checks a tool result against the room left. A result that
// wouldn't fit is cut to a preview with a notice giving its size, so the
// model can choose a smaller read. It returns the text to send and, when it
// cut anything, what it measured.
func (a *Agent) guardToolOutput(messages []lc.BaseMessage, output string) (string, map[string]any) {
	m := a.meter
	if m == nil || m.limit <= 0 || output == "" {
		return output, nil
	}
	size := m.scaled(EstimateTextTokens(output))
	if size < 256 {
		return output, nil
	}
	used := m.estimate(messages) - m.reclaimable(messages)
	free := m.limit - a.context.ReserveTokens - used
	allowed := minInt(free, m.limit/2)
	if size <= allowed {
		return output, nil
	}
	keep := a.context.ToolResultSafetyChars
	if room := int(float64(maxInt(allowed, 0)) * 4 / m.scale / 2); room < keep {
		keep = maxInt(room, 400)
	}
	total := utf8.RuneCountInString(output)
	keep = minInt(keep, total)
	body := string([]rune(output)[:keep])
	notice := fmt.Sprintf("\n\n[Context guard: the full result is about %s tokens (%s characters), more than the room left in this conversation (about %s of %s tokens free). Showing the first %s characters. Read it in smaller pieces instead: a line range, a search or filter, or one page of results at a time.]",
		formatCount(size), formatCount(total), formatCount(maxInt(free, 0)), formatCount(m.limit), formatCount(keep))
	return body + notice, map[string]any{
		"full_tokens":    size,
		"full_chars":     total,
		"shown_chars":    keep,
		"free_tokens":    maxInt(free, 0),
		"context_window": m.limit,
	}
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

// imagePartTokens is a typical cost of one image. Counting its base64 data
// as text would overstate it a hundredfold.
const imagePartTokens = 1600

func EstimateContentTokens(content lc.Content) int {
	if content.Text != nil {
		return EstimateTextTokens(*content.Text)
	}
	total := 0
	for _, part := range content.Parts {
		total += EstimateTextTokens(part.Type) + EstimateTextTokens(part.Text) + EstimateTextTokens(part.Name) + EstimateAnyTokens(part.Input) + 6
		if part.Source != nil {
			total += EstimateTextTokens(part.Source.MediaType) + EstimateTextTokens(part.Source.URL)
			switch {
			case isImagePart(part):
				total += imagePartTokens
			case part.Source.Data != "":
				// Documents: about one token per 12 bytes of base64.
				total += utf8.RuneCountInString(part.Source.Data)/12 + 1
			}
		}
		if part.Content != nil {
			total += EstimateAnyTokens(part.Content)
		}
	}
	return total
}

func isImagePart(part lc.ContentPart) bool {
	return strings.Contains(part.Type, "image") || (part.Source != nil && strings.HasPrefix(part.Source.MediaType, "image/"))
}

func nonTextParts(content lc.Content) int {
	count := 0
	for _, part := range content.Parts {
		if part.Text == "" {
			count++
		}
	}
	return count
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

func firstNonSystemMessage(messages []lc.BaseMessage) int {
	for i, msg := range messages {
		if msg.Type != lc.RoleSystem && msg.Type != lc.RoleDeveloper {
			return i
		}
	}
	return len(messages)
}

func lastIndexOfRole(messages []lc.BaseMessage, role lc.MessageRole) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Type == role {
			return i
		}
	}
	return -1
}

func toolCallsByID(messages []lc.BaseMessage) map[string]lc.ToolCall {
	calls := map[string]lc.ToolCall{}
	for _, msg := range messages {
		for _, call := range msg.ToolCalls {
			if call.ID != "" {
				calls[call.ID] = call
			}
		}
	}
	return calls
}

func preservedSet(names []string) map[string]bool {
	out := map[string]bool{}
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = true
		}
	}
	return out
}

func messageHasToolCall(msg lc.BaseMessage, id string) bool {
	for _, call := range msg.ToolCalls {
		if call.ID == id {
			return true
		}
	}
	return false
}

// formatMessagesForCompaction renders messages for the summarizer. When the
// transcript is too long it keeps the opening messages, which usually hold
// the goal, and the latest ones, and says how many it left out between.
func formatMessagesForCompaction(messages []lc.BaseMessage, policy ContextPolicy) string {
	limit := policy.CompactionMaxChars
	if limit <= 0 {
		limit = 80000
	}
	perTool := policy.ToolResultSafetyChars
	if perTool <= 0 {
		perTool = 12000
	}
	entries := make([]string, len(messages))
	total := 0
	for i, msg := range messages {
		var b strings.Builder
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
		entries[i] = b.String()
		total += len(entries[i])
	}
	if total <= limit {
		return strings.TrimSpace(strings.Join(entries, ""))
	}
	var head, tail []string
	used := 0
	for i := 0; i < len(entries) && i < 2 && used+len(entries[i]) <= limit/3; i++ {
		head = append(head, entries[i])
		used += len(entries[i])
	}
	for i := len(entries) - 1; i >= len(head); i-- {
		if used+len(entries[i]) > limit {
			break
		}
		tail = append([]string{entries[i]}, tail...)
		used += len(entries[i])
	}
	omitted := len(entries) - len(head) - len(tail)
	out := strings.Join(head, "")
	if omitted > 0 {
		out += fmt.Sprintf("\n[%d messages omitted here to fit the summarizer.]\n", omitted)
	}
	out += strings.Join(tail, "")
	return strings.TrimSpace(truncateRunes(out, limit))
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

func truncatePlain(text string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit]) + "…"
}

// formatCount writes n with thousands separators.
func formatCount(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + formatCount(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func clampInt(value, low, high int) int {
	return maxInt(low, minInt(high, value))
}
