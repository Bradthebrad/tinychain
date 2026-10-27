package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bradthebrad/tinychain/lc"
)

// testGate pauses while paused is set and wraps up once stop is set.
type testGate struct {
	mu      sync.Mutex
	paused  bool
	stop    bool
	fail    error
	waits   int
	changed chan struct{}
}

func newTestGate() *testGate { return &testGate{changed: make(chan struct{})} }

func (g *testGate) set(f func(*testGate)) {
	g.mu.Lock()
	f(g)
	close(g.changed)
	g.changed = make(chan struct{})
	g.mu.Unlock()
}

func (g *testGate) Wait(ctx context.Context) error {
	for {
		g.mu.Lock()
		g.waits++
		stop, paused, fail, changed := g.stop, g.paused, g.fail, g.changed
		g.mu.Unlock()
		switch {
		case fail != nil:
			return fail
		case stop:
			return ErrWrapUp
		case !paused:
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// stepModel asks for two reads per step for `steps` steps, then answers.
type stepModel struct {
	mu       sync.Mutex
	steps    int
	calls    int
	lastSeen []lc.BaseMessage
	onCall   func(n int)
}

func (m *stepModel) Call(_ context.Context, messages []lc.BaseMessage, _ []Tool) (lc.BaseMessage, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.lastSeen = messages
	m.mu.Unlock()
	if m.onCall != nil {
		m.onCall(n)
	}
	last := messages[len(messages)-1]
	if last.Type == lc.RoleHuman && contentText(last.Content) == wrapUpPrompt {
		return lc.BaseMessage{Type: lc.RoleAI, Content: lc.TextContent("Read two files; the third is left."), ToolCalls: []lc.ToolCall{{ID: "ignored", Name: "read"}}}, nil
	}
	if n <= m.steps {
		return lc.BaseMessage{Type: lc.RoleAI, ToolCalls: []lc.ToolCall{{ID: "a" + string(rune('0'+n)), Name: "read", Args: map[string]any{}}, {ID: "b" + string(rune('0'+n)), Name: "read", Args: map[string]any{}}}}, nil
	}
	return lc.AI("all done"), nil
}

func countingTool(count *int, mu *sync.Mutex, during func()) Tool {
	return ToolFunc{Name: "read", Description: "read", Schema: ToolSchema(map[string]any{}), Func: func(context.Context, map[string]any) (string, error) {
		mu.Lock()
		*count++
		mu.Unlock()
		if during != nil {
			during()
		}
		return "ok", nil
	}}
}

func TestGatePausesBetweenStepsAndResumes(t *testing.T) {
	gate := newTestGate()
	var ran int
	var mu sync.Mutex
	model := &stepModel{steps: 2}
	model.onCall = func(n int) {
		if n == 1 {
			gate.set(func(g *testGate) { g.paused = true })
		}
	}
	agent := New(Config{Model: model, Tools: []Tool{countingTool(&ran, &mu, nil)}, Gate: gate})
	done := make(chan *Result, 1)
	go func() {
		result, err := agent.Invoke(context.Background(), "go")
		if err != nil {
			t.Error(err)
		}
		done <- result
	}()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if ran != 0 {
		t.Fatalf("%d tools ran while paused", ran)
	}
	mu.Unlock()
	gate.set(func(g *testGate) { g.paused = false })
	select {
	case result := <-done:
		if contentText(result.Output.Content) != "all done" || ran != 4 {
			t.Fatalf("after resume: %q, %d tools", contentText(result.Output.Content), ran)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not resume")
	}
}

func TestWrapUpStopsBeforeTheNextToolAndSummarizes(t *testing.T) {
	gate := newTestGate()
	var ran int
	var mu sync.Mutex
	model := &stepModel{steps: 3}
	tool := countingTool(&ran, &mu, func() { gate.set(func(g *testGate) { g.stop = true }) })
	agent := New(Config{Model: model, Tools: []Tool{tool}, Gate: gate})
	result, err := agent.Invoke(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if ran != 1 {
		t.Fatalf("%d tools ran after the stop", ran)
	}
	if got := contentText(result.Output.Content); got != "Read two files; the third is left." || len(result.Output.ToolCalls) != 0 {
		t.Fatalf("wrap-up reply %q %v", got, result.Output.ToolCalls)
	}
	// The call that was skipped still has a result, as providers require.
	var skipped bool
	for _, msg := range model.lastSeen {
		if msg.Type == lc.RoleTool && msg.ToolCallID == "b1" && strings.HasPrefix(contentText(msg.Content), "Not run") {
			skipped = true
		}
	}
	if !skipped {
		t.Fatal("the skipped call has no result")
	}
}

func TestGateErrorEndsTheRun(t *testing.T) {
	gate := newTestGate()
	gate.fail = errors.New("halted")
	agent := New(Config{Model: &stepModel{}, Gate: gate})
	if _, err := agent.Invoke(context.Background(), "go"); err == nil || err.Error() != "halted" {
		t.Fatalf("err %v", err)
	}
}

func TestCancelReleasesAPausedRun(t *testing.T) {
	gate := newTestGate()
	gate.paused = true
	agent := New(Config{Model: &stepModel{}, Gate: gate})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	if _, err := agent.Invoke(ctx, "go"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}
