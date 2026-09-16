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
