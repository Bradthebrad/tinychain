package agent

import (
	"context"
	"testing"

	"github.com/Bradthebrad/tinychain/callbacks"
	"github.com/Bradthebrad/tinychain/lc"
)

type describedTool struct {
	sawCallID string
}

func (t *describedTool) Definition() lc.ToolDefinition {
	return lc.ToolDefinition{Name: "echo", Description: "Echo.", ArgsSchema: ToolSchema(nil)}
}

func (t *describedTool) Call(ctx context.Context, args map[string]any) (string, error) {
	out, _, err := t.CallWithMeta(ctx, args)
	return out, err
}

func (t *describedTool) CallWithMeta(ctx context.Context, args map[string]any) (string, map[string]any, error) {
	t.sawCallID = ToolCallID(ctx)
	return "hello", map[string]any{"items": 3}, nil
}

func (t *describedTool) ToolInfo() ToolInfo {
	readOnly := true
	return ToolInfo{Title: "Echo text", Server: "echo-server", Source: "mcp", ReadOnly: &readOnly}
}

func TestToolCallbacksCarryInfoDurationAndResultMeta(t *testing.T) {
	tool := &describedTool{}
	var start, end callbacks.Event
	a := New(Config{
		Model: &fakeModel{},
		Tools: []Tool{tool},
		Callbacks: callbacks.SinkFunc(func(event callbacks.Event) {
			switch event.Event {
			case callbacks.EventToolStart:
				start = event
			case callbacks.EventToolEnd:
				end = event
			}
		}),
	})
	if _, err := a.Invoke(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if tool.sawCallID != "call_1" {
		t.Fatalf("tool saw call id %q", tool.sawCallID)
	}
	if start.Metadata["title"] != "Echo text" || start.Metadata["server"] != "echo-server" || start.Metadata["read_only"] != true {
		t.Fatalf("start metadata = %#v", start.Metadata)
	}
	if _, ok := end.Data.Extra["duration_ms"]; !ok {
		t.Fatalf("end extra = %#v", end.Data.Extra)
	}
	meta, _ := end.Data.Extra["result_meta"].(map[string]any)
	if meta["items"] != 3 {
		t.Fatalf("result meta = %#v", end.Data.Extra)
	}
}
