package agent

import (
	"context"
	"github.com/Bradthebrad/tinychain/callbacks"
	"github.com/Bradthebrad/tinychain/lc"
	"github.com/Bradthebrad/tinychain/streaming"
	"testing"
)

type deltaModel struct{}

func (deltaModel) Call(ctx context.Context, _ []lc.BaseMessage, _ []Tool) (lc.BaseMessage, error) {
	streaming.Emit(ctx, " hello", false)
	streaming.Emit(ctx, "summary", true)
	return lc.AI(" hello"), nil
}
func TestStreamingRouting(t *testing.T) {
	var events []callbacks.Event
	a := New(Config{Name: "worker", Model: deltaModel{}, Callbacks: callbacks.SinkFunc(func(e callbacks.Event) { events = append(events, e) })})
	for i := 0; i < 2; i++ {
		if _, err := a.Invoke(context.Background(), "go"); err != nil {
			t.Fatal(err)
		}
	}
	var runs []string
	for _, e := range events {
		if e.AgentID != "worker" || e.RunID == "" {
			t.Fatalf("missing routing: %#v", e)
		}
		if e.Event == callbacks.EventLLMNewToken {
			if e.Data.Token != " hello" {
				t.Fatal("whitespace lost")
			}
			runs = append(runs, e.RunID)
		}
	}
	if len(runs) != 2 || runs[0] == runs[1] {
		t.Fatalf("runs = %v", runs)
	}
}

type selfRunningModel struct{}

func (selfRunningModel) Call(ctx context.Context, _ []lc.BaseMessage, _ []Tool) (lc.BaseMessage, error) {
	streaming.Emit(ctx, "Let me look.", false)
	streaming.EmitCommentary(ctx, "Let me look.")
	streaming.EmitStep(ctx, streaming.Step{ID: "toolu_1", Name: "Read", Input: map[string]any{"file_path": "a.go"}, Metadata: map[string]any{"via": "cli"}})
	streaming.EmitStep(ctx, streaming.Step{ID: "toolu_1", Name: "Read", Output: "package a", Done: true})
	streaming.EmitStep(ctx, streaming.Step{ID: "toolu_2", Name: "Bash", Error: "denied", Done: true})
	return lc.AI("Done."), nil
}

func TestModelStepsAndCommentary(t *testing.T) {
	var events []callbacks.Event
	a := New(Config{Name: "main", Model: selfRunningModel{}, Callbacks: callbacks.SinkFunc(func(e callbacks.Event) { events = append(events, e) })})
	result, err := a.Invoke(context.Background(), "go")
	if err != nil || contentText(result.Output.Content) != "Done." {
		t.Fatalf("result %v %v", result, err)
	}
	var runID string
	var seen []callbacks.EventName
	for _, e := range events {
		switch e.Event {
		case callbacks.EventChatModelStart:
			runID = e.RunID
		case callbacks.EventLLMCommentary, callbacks.EventToolStart, callbacks.EventToolEnd, callbacks.EventToolError:
			seen = append(seen, e.Event)
		}
		if e.Event == callbacks.EventLLMCommentary && (e.Data.Token != "Let me look." || e.RunID != runID) {
			t.Fatalf("commentary %#v", e)
		}
		if e.Event == callbacks.EventToolStart && (e.Name != "Read" || e.RunID != "toolu_1" || e.ParentRunID != runID || e.Metadata["via"] != "cli") {
			t.Fatalf("step start %#v", e)
		}
		if e.Event == callbacks.EventToolEnd && e.Data.Output != "package a" {
			t.Fatalf("step end %#v", e)
		}
		if e.Event == callbacks.EventToolError && e.Data.Error != "denied" {
			t.Fatalf("step error %#v", e)
		}
	}
	want := []callbacks.EventName{callbacks.EventLLMCommentary, callbacks.EventToolStart, callbacks.EventToolEnd, callbacks.EventToolError}
	if len(seen) != len(want) {
		t.Fatalf("events %v", seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("events %v", seen)
		}
	}
}
