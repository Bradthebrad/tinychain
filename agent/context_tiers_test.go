package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Bradthebrad/tinychain/callbacks"
	"github.com/Bradthebrad/tinychain/lc"
)

// readerModel asks for `reads` tool calls, one per step, then answers. Calls
// without tools are summary requests.
type readerModel struct {
	mu        sync.Mutex
	reads     int
	tool      string
	step      int
	summaries int
	seen      [][]lc.BaseMessage
	usage     func(messages []lc.BaseMessage) *lc.UsageMetadata
	fail      func(step int, messages []lc.BaseMessage) error
}

func (m *readerModel) Call(_ context.Context, messages []lc.BaseMessage, tools []Tool) (lc.BaseMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(tools) == 0 {
		m.summaries++
		return lc.AI("SUMMARY: read several files"), nil
	}
	m.seen = append(m.seen, append([]lc.BaseMessage(nil), messages...))
	if m.fail != nil {
		if err := m.fail(m.step, messages); err != nil {
			return lc.BaseMessage{}, err
		}
	}
	m.step++
	var out lc.BaseMessage
	if m.step <= m.reads {
		name := m.tool
		if name == "" {
			name = "read"
		}
		out = lc.BaseMessage{Type: lc.RoleAI, ToolCalls: []lc.ToolCall{{ID: fmt.Sprintf("call_%d", m.step), Name: name, Args: map[string]any{"path": fmt.Sprintf("file%d.txt", m.step)}}}}
	} else {
		out = lc.AI("done")
	}
	if m.usage != nil {
		out.UsageMetadata = m.usage(messages)
	}
	return out, nil
}

func readTool(name string, size int) Tool {
	return ToolFunc{
		Name:        name,
		Description: "Read a file.",
		Schema:      ToolSchema(map[string]any{"path": map[string]any{"type": "string"}}),
		Func: func(_ context.Context, args map[string]any) (string, error) {
			return fmt.Sprintf("contents of %v: ", args["path"]) + strings.Repeat("abcd ", size/5), nil
		},
	}
}

type eventLog struct {
	mu     sync.Mutex
	events []callbacks.Event
}

func (l *eventLog) Handle(e callbacks.Event) {
	l.mu.Lock()
	l.events = append(l.events, e)
	l.mu.Unlock()
}

func (l *eventLog) named(name string) []callbacks.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []callbacks.Event
	for _, e := range l.events {
		if e.Name == name {
			out = append(out, e)
		}
	}
	return out
}

func TestShortenTierDigestsSeenToolResultsBeforeSummarizing(t *testing.T) {
	model := &readerModel{reads: 6}
	log := &eventLog{}
	agent := New(Config{
		Model:     model,
		Tools:     []Tool{readTool("read", 4000)},
		Callbacks: log,
		Context:   ContextPolicy{Enabled: true, MaxTokens: 8000, ReserveTokens: 200},
	})
	if _, err := agent.Invoke(context.Background(), "read the files"); err != nil {
		t.Fatal(err)
	}
	if model.summaries != 0 {
		t.Fatalf("summarized %d times; shortening should have been enough", model.summaries)
	}
	if len(log.named("context_shorten")) == 0 {
		t.Fatal("no shorten event")
	}
	last := model.seen[len(model.seen)-1]
	var digests, full int
	for i, msg := range last {
		if msg.Type != lc.RoleTool {
			continue
		}
		text := contentText(msg.Content)
		if strings.HasPrefix(text, shortenedMarker) {
			digests++
			if !strings.Contains(text, `read {"path":"file`) || !strings.Contains(text, "contents of file") || !strings.Contains(text, "more characters not shown") {
				t.Fatalf("digest lacks tool, args or head: %q", text)
			}
			continue
		}
		full++
		if i == len(last)-1 {
			continue
		}
	}
	if digests == 0 {
		t.Fatal("no tool result was shortened")
	}
	if tail := contentText(last[len(last)-1].Content); strings.HasPrefix(tail, shortenedMarker) {
		t.Fatal("shortened a result the model had not seen yet")
	}
}

func TestPreservedToolResultsAreNeverShortened(t *testing.T) {
	model := &readerModel{reads: 6, tool: "skill_read"}
	agent := New(Config{
		Model:   model,
		Tools:   []Tool{readTool("skill_read", 4000)},
		Context: ContextPolicy{Enabled: true, MaxTokens: 8000, ReserveTokens: 200, PreserveTools: []string{"skill_read"}},
	})
	if _, err := agent.Invoke(context.Background(), "load skills"); err != nil {
		t.Fatal(err)
	}
	for _, msg := range model.seen[len(model.seen)-1] {
		if msg.Type == lc.RoleTool && strings.HasPrefix(contentText(msg.Content), shortenedMarker) {
			t.Fatal("a preserved skill result was shortened")
		}
	}
}

func TestSummarizeTierRunsOnlyPastHardThreshold(t *testing.T) {
	model := &readerModel{reads: 8, tool: "skill_read"}
	log := &eventLog{}
	agent := New(Config{
		Model:     model,
		Tools:     []Tool{readTool("skill_read", 4000)},
		Callbacks: log,
		// Preserved results can't be shortened, so only summarizing helps.
		Context: ContextPolicy{Enabled: true, MaxTokens: 8000, ReserveTokens: 200, PreserveTools: []string{"skill_read"}},
	})
	if _, err := agent.Invoke(context.Background(), "load every skill"); err != nil {
		t.Fatal(err)
	}
	if model.summaries == 0 {
		t.Fatal("expected a summary once past the hard threshold")
	}
	starts := log.named("context_compaction")
	if len(starts) == 0 {
		t.Fatal("no compaction events")
	}
	before, _ := starts[0].Data.Extra["context_tokens"].(int)
	if before < 8000*85/100 {
		t.Fatalf("summarized at %d tokens, under the hard threshold", before)
	}
	var summary, kept strings.Builder
	for _, msg := range model.seen[len(model.seen)-1] {
		text := contentText(msg.Content)
		if msg.Type == lc.RoleSystem && strings.HasPrefix(text, summaryMarker) {
			summary.WriteString(text)
		}
		if msg.Type == lc.RoleSystem && strings.HasPrefix(text, keptMarker) {
			kept.WriteString(text)
		}
	}
	if !strings.Contains(summary.String(), "SUMMARY: read several files") {
		t.Fatalf("no model summary: %q", summary.String())
	}
	if strings.Count(summary.String(), summaryMarker) != 1 {
		t.Fatal("summaries stacked instead of folding")
	}
	if !strings.Contains(kept.String(), "Skill instructions loaded earlier") || !strings.Contains(kept.String(), "contents of file1.txt") {
		t.Fatalf("skills replaced by the summary were not kept word for word: %q", kept.String())
	}
	if !strings.Contains(kept.String(), "load every skill") {
		t.Fatalf("the request being worked on was dropped: %q", kept.String())
	}
}

func TestToolDefinitionsCountInTheEstimate(t *testing.T) {
	log := &eventLog{}
	big := ToolFunc{Name: "big", Description: strings.Repeat("describes a lot ", 400), Schema: ToolSchema(map[string]any{}), Func: func(context.Context, map[string]any) (string, error) { return "", nil }}
	agent := New(Config{Model: &readerModel{}, Tools: []Tool{big}, Callbacks: log, Context: ContextPolicy{Enabled: true, MaxTokens: 100000}})
	if _, err := agent.Invoke(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	var start callbacks.Event
	for _, e := range log.events {
		if e.Event == callbacks.EventChatModelStart {
			start = e
		}
	}
	tools, _ := start.Data.Extra["tool_tokens"].(int)
	total, _ := start.Data.Extra["context_tokens"].(int)
	if tools < 1500 || total < tools {
		t.Fatalf("tool definitions not counted: tools=%d total=%d", tools, total)
	}
}

func TestEstimatesFollowReportedInputTokens(t *testing.T) {
	var meter *contextMeter
	model := &readerModel{reads: 2}
	model.usage = func(messages []lc.BaseMessage) *lc.UsageMetadata {
		raw := meter.raw(messages)
		return &lc.UsageMetadata{InputTokens: raw, InputTokenDetails: map[string]int{"cache_read_input_tokens": raw}}
	}
	agent := New(Config{Model: model, Tools: []Tool{readTool("read", 100)}, Context: ContextPolicy{Enabled: true, MaxTokens: 100000}})
	meter = newContextMeter(agent.context, agent.toolOrder)
	meter.observe(1000, lc.BaseMessage{UsageMetadata: &lc.UsageMetadata{InputTokens: 1500, InputTokenDetails: map[string]int{"cache_read_input_tokens": 500}}})
	if meter.scale != 2 {
		t.Fatalf("scale %v, want 2 (cache reads count as input)", meter.scale)
	}
	meter.observe(1000, lc.BaseMessage{UsageMetadata: &lc.UsageMetadata{InputTokens: 1000}})
	if meter.scale != 1.5 {
		t.Fatalf("scale %v, want 1.5 after smoothing", meter.scale)
	}
	meter.observe(1000, lc.BaseMessage{UsageMetadata: &lc.UsageMetadata{InputTokens: 100000}})
	if meter.scale > 4 {
		t.Fatalf("scale %v not clamped", meter.scale)
	}
	if _, err := agent.Invoke(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
}

func TestContextErrorTeachesTheRealWindowAndRetries(t *testing.T) {
	log := &eventLog{}
	model := &readerModel{reads: 3}
	failed := false
	model.fail = func(step int, messages []lc.BaseMessage) error {
		if step == 3 && !failed {
			failed = true
			return errors.New("400: prompt is too long: 9,500 tokens > 6,000 maximum")
		}
		return nil
	}
	agent := New(Config{Model: model, Tools: []Tool{readTool("read", 6000)}, Callbacks: log, Context: ContextPolicy{Enabled: true, MaxTokens: 50000, ReserveTokens: 200}})
	if _, err := agent.Invoke(context.Background(), "read"); err != nil {
		t.Fatal(err)
	}
	var learned bool
	for _, e := range log.events {
		if e.Event == callbacks.EventChatModelStart && e.Data.Extra["learned_window"] == 6000 {
			learned = true
		}
	}
	if !learned {
		t.Fatal("the window named in the error was not adopted")
	}
}

func TestContextLimitFromError(t *testing.T) {
	cases := map[string]int{
		"This model's maximum context length is 128000 tokens. However, you requested 130500 tokens": 128000,
		"prompt is too long: 210,442 tokens > 200,000 maximum":                                       200000,
		"Your input exceeds the context window of this model.":                                       0,
		"input exceeds the context window of 272000 tokens":                                          272000,
	}
	for text, want := range cases {
		if got := ContextLimitFromError(errors.New(text)); got != want {
			t.Errorf("%q: got %d, want %d", text, got, want)
		}
	}
}

func TestGuardCountsSpaceShorteningCouldFree(t *testing.T) {
	agent := New(Config{Model: &readerModel{}, Context: ContextPolicy{Enabled: true, MaxTokens: 6000, ReserveTokens: 100}})
	agent.meter = newContextMeter(agent.context, nil)
	messages := []lc.BaseMessage{lc.Human("go")}
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("c%d", i)
		messages = append(messages,
			lc.BaseMessage{Type: lc.RoleAI, ToolCalls: []lc.ToolCall{{ID: id, Name: "read", Args: map[string]any{}}}},
			lc.Tool(id, strings.Repeat("x", 4000)))
	}
	messages = append(messages, lc.BaseMessage{Type: lc.RoleAI, ToolCalls: []lc.ToolCall{{ID: "next", Name: "read", Args: map[string]any{}}}})
	// About 4,100 tokens used, but 3,600 of them can be shortened away, so
	// a 2,000-token result fits.
	out, guard := agent.guardToolOutput(messages, strings.Repeat("y", 8000))
	if guard != nil || len(out) != 8000 {
		t.Fatalf("result cut although shortening frees room: %v", guard)
	}
	out, guard = agent.guardToolOutput(messages, strings.Repeat("y", 40000))
	if guard == nil || !strings.Contains(out, "Context guard: the full result is about 10,001 tokens (40,000 characters)") {
		t.Fatalf("oversized result not cut: %v %q", guard, out[len(out)-300:])
	}
}

func TestCompactSummarizesEverythingOnRequest(t *testing.T) {
	model := &readerModel{}
	agent := New(Config{Model: model, Context: ContextPolicy{Enabled: true, MaxTokens: 100000, Note: "Full history: use history_search."}})
	messages := []lc.BaseMessage{
		lc.System("system prompt"),
		lc.Human("first request"),
		lc.AI("first answer"),
		lc.Human("second request"),
		lc.AI("second answer"),
	}
	next, report, err := agent.Compact(context.Background(), messages, CompactOptions{Focus: "file paths"})
	if err != nil {
		t.Fatal(err)
	}
	if model.summaries != 1 || report.Summarized != 4 || len(next) != 2 {
		t.Fatalf("summaries=%d report=%+v next=%d", model.summaries, report, len(next))
	}
	if contentText(next[0].Content) != "system prompt" {
		t.Fatal("system prompt not kept first")
	}
	text := contentText(next[1].Content)
	for _, want := range []string{summaryMarker, "SUMMARY", "history_search"} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary missing %q: %q", want, text)
		}
	}
	// A second compaction folds the first summary in instead of stacking.
	again, _, err := agent.Compact(context.Background(), append(next, lc.Human("third"), lc.AI("ok")), CompactOptions{})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, msg := range again {
		if strings.HasPrefix(contentText(msg.Content), summaryMarker) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d summaries after a second compaction", count)
	}
}

func TestSummarizerTranscriptKeepsTheStartAndTheEnd(t *testing.T) {
	var messages []lc.BaseMessage
	messages = append(messages, lc.Human("GOAL: migrate the database"))
	for i := 0; i < 50; i++ {
		messages = append(messages, lc.AI(fmt.Sprintf("step %d %s", i, strings.Repeat("z", 1000))))
	}
	messages = append(messages, lc.AI("LATEST: tests pass"))
	text := formatMessagesForCompaction(messages, ContextPolicy{CompactionMaxChars: 10000, ToolResultSafetyChars: 12000})
	if !strings.Contains(text, "GOAL") || !strings.Contains(text, "LATEST") || !strings.Contains(text, "messages omitted here") {
		t.Fatalf("transcript lost its start or end: %d chars", len(text))
	}
}

func TestImagesAreNotCountedAsText(t *testing.T) {
	image := lc.BaseMessage{Type: lc.RoleHuman, Content: lc.PartsContent(lc.ContentPart{Type: "image", Source: &lc.ContentSource{Type: "base64", MediaType: "image/png", Data: strings.Repeat("A", 1_000_000)}})}
	if got := EstimateTokens([]lc.BaseMessage{image}); got > 2000 {
		t.Fatalf("a 1MB image estimated at %d tokens", got)
	}
}

func TestShortenedArgumentsStayValid(t *testing.T) {
	msg := lc.BaseMessage{Type: lc.RoleAI, ToolCalls: []lc.ToolCall{{ID: "w", Name: "write", Args: map[string]any{"path": "a.txt", "content": strings.Repeat("q", 5000), "lines": []any{strings.Repeat("r", 900)}}}}}
	next, ok := shortenToolArgs(msg, 300)
	if !ok {
		t.Fatal("long arguments not shortened")
	}
	args := next.ToolCalls[0].Args
	if args["path"] != "a.txt" || !strings.HasSuffix(args["content"].(string), "[4,700 more characters]") {
		t.Fatalf("args %v", args)
	}
	if len(msg.ToolCalls[0].Args["content"].(string)) != 5000 {
		t.Fatal("original message was modified")
	}
}
