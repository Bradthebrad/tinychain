package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Bradthebrad/tinychain/lc"
)

type instructionModelFunc func(context.Context, []lc.BaseMessage, []Tool) (lc.BaseMessage, error)

func (f instructionModelFunc) Call(c context.Context, m []lc.BaseMessage, tools []Tool) (lc.BaseMessage, error) {
	return f(c, m, tools)
}

func TestInstructionCompleteToolBoundary(t *testing.T) {
	inbox := &InstructionInbox{}
	deliveries := 0
	inbox.OnDelivery = func(in Instruction, applied bool) {
		if applied {
			deliveries++
		}
	}
	calls := 0
	model := instructionModelFunc(func(ctx context.Context, messages []lc.BaseMessage, tools []Tool) (lc.BaseMessage, error) {
		calls++
		if calls == 1 {
			return lc.BaseMessage{Type: lc.RoleAI, ToolCalls: []lc.ToolCall{{ID: "one", Name: "echo"}, {ID: "two", Name: "echo"}}}, nil
		}
		if len(messages) != 5 || messages[2].ToolCallID != "one" || messages[3].ToolCallID != "two" || contentText(messages[4].Content) != "steer" {
			t.Fatalf("incomplete boundary: %#v", messages)
		}
		return lc.AI("done"), nil
	})
	executed := 0
	tool := ToolFunc{Name: "echo", Func: func(context.Context, map[string]any) (string, error) {
		executed++
		if executed == 1 {
			if err := inbox.Submit(Instruction{ID: "i", Text: "steer"}); err != nil {
				t.Fatal(err)
			}
		}
		return "result", nil
	}}
	_, err := New(Config{Model: model, Tools: []Tool{tool}}).InvokeMessagesWithInbox(context.Background(), []lc.BaseMessage{lc.Human("start")}, inbox)
	if err != nil || deliveries != 1 || executed != 2 {
		t.Fatalf("err=%v deliveries=%d tools=%d", err, deliveries, executed)
	}
	if !errors.Is(inbox.Submit(Instruction{Text: "late"}), ErrInstructionClosed) {
		t.Fatal("terminal accepted")
	}
}

func TestInstructionDuringFinalResponseContinues(t *testing.T) {
	inbox := &InstructionInbox{}
	calls := 0
	model := instructionModelFunc(func(_ context.Context, m []lc.BaseMessage, _ []Tool) (lc.BaseMessage, error) {
		calls++
		if calls == 1 {
			if err := inbox.Submit(Instruction{Text: "new direction"}); err != nil {
				t.Fatal(err)
			}
			return lc.AI("old answer"), nil
		}
		if contentText(m[len(m)-1].Content) != "new direction" {
			t.Fatal("instruction missing")
		}
		return lc.AI("new answer"), nil
	})
	result, err := New(Config{Model: model}).InvokeMessagesWithInbox(context.Background(), []lc.BaseMessage{lc.Human("start")}, inbox)
	if err != nil || calls != 2 || contentText(result.Output.Content) != "new answer" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestInstructionTerminalRaceExactlyOnce(t *testing.T) {
	for n := 0; n < 200; n++ {
		var mu sync.Mutex
		applied, rejected := 0, 0
		inbox := &InstructionInbox{OnDelivery: func(_ Instruction, ok bool) {
			mu.Lock()
			defer mu.Unlock()
			if ok {
				applied++
			} else {
				rejected++
			}
		}}
		var wg sync.WaitGroup
		wg.Add(2)
		var err error
		go func() { defer wg.Done(); err = inbox.Submit(Instruction{Text: "race"}) }()
		go func() { defer wg.Done(); inbox.take(true); inbox.Close() }()
		wg.Wait()
		if err == nil && applied+rejected != 1 {
			t.Fatalf("accepted not resolved: %d %d", applied, rejected)
		}
		if err != nil && applied+rejected != 0 {
			t.Fatal("rejected delivered")
		}
	}
}

func TestInstructionIterationLimitRejectsUndelivered(t *testing.T) {
	rejected := 0
	inbox := &InstructionInbox{OnDelivery: func(_ Instruction, ok bool) {
		if ok {
			t.Error("applied with no remaining iteration")
		} else {
			rejected++
		}
	}}
	model := instructionModelFunc(func(context.Context, []lc.BaseMessage, []Tool) (lc.BaseMessage, error) {
		_ = inbox.Submit(Instruction{Text: "late"})
		return lc.AI("done"), nil
	})
	_, err := New(Config{Model: model, MaxIterations: 1}).InvokeMessagesWithInbox(context.Background(), nil, inbox)
	if err != nil || rejected != 1 {
		t.Fatalf("err=%v rejected=%d", err, rejected)
	}
}
