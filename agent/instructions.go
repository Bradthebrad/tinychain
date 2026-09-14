package agent

import (
	"context"
	"errors"
	"github.com/Bradthebrad/tinychain/lc"
	"strings"
	"sync"
)

var ErrInstructionClosed = errors.New("agent: invocation no longer accepts instructions")

type Instruction struct {
	ID      string
	Text    string
	Message *lc.BaseMessage
}

// InstructionInbox belongs to exactly one invocation, never to a reusable Agent.
// Acceptance and terminal sealing use the same lock. OnDelivery is called outside
// that lock, once for every accepted instruction: applied=true only at a complete
// message/tool-result boundary, false if invocation exits before delivery.
type InstructionInbox struct {
	mu         sync.Mutex
	pending    []Instruction
	closed     bool
	OnDelivery func(Instruction, bool)
}

func (i *InstructionInbox) Submit(in Instruction) error {
	if i == nil {
		return ErrInstructionClosed
	}
	if strings.TrimSpace(in.Text) == "" {
		return errors.New("agent: instruction is empty")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return ErrInstructionClosed
	}
	i.pending = append(i.pending, in)
	return nil
}

// take seals atomically if terminal and empty, otherwise consumes the inbox.
func (i *InstructionInbox) take(terminal bool) []Instruction {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	pending := i.pending
	i.pending = nil
	if terminal && len(pending) == 0 {
		i.closed = true
	}
	i.mu.Unlock()
	for _, in := range pending {
		if i.OnDelivery != nil {
			i.OnDelivery(in, true)
		}
	}
	return pending
}

func (i *InstructionInbox) Close() {
	if i == nil {
		return
	}
	i.mu.Lock()
	i.closed = true
	pending := i.pending
	i.pending = nil
	i.mu.Unlock()
	for _, in := range pending {
		if i.OnDelivery != nil {
			i.OnDelivery(in, false)
		}
	}
}

// InvokeMessagesWithInbox allows guidance without canceling the provider or
// interrupting a batch of tool results. Workers must use their own inbox (or nil).
func (a *Agent) InvokeMessagesWithInbox(ctx context.Context, input []lc.BaseMessage, inbox *InstructionInbox) (*Result, error) {
	return a.invokeMessages(ctx, input, inbox)
}

func (in Instruction) message() lc.BaseMessage {
	if in.Message != nil {
		return *in.Message
	}
	return lc.Human(in.Text)
}
