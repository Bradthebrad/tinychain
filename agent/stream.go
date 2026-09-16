package agent

import (
	"context"
	"fmt"
	"github.com/Bradthebrad/tinychain/callbacks"
	"sync/atomic"
	"time"
)

var runSequence atomic.Uint64

type runContextKey struct{}
type runContext struct {
	ID      string
	AgentID string
}

// scopedRun uses a local copy so concurrent invocations never share routing state.
func (a *Agent) scopedRun(ctx context.Context) (*Agent, context.Context, string) {
	parent, _ := ctx.Value(runContextKey{}).(runContext)
	id := fmt.Sprintf("run-%d-%d", time.Now().UnixNano(), runSequence.Add(1))
	agentID := a.name
	if agentID == "" {
		agentID = "main"
	}
	local := *a
	if a.callbacks != nil {
		local.callbacks = callbacks.SinkFunc(func(e callbacks.Event) {
			e.AgentID = agentID
			if e.RunID == "" {
				e.RunID = id
			}
			if e.RunID != id {
				e.ParentRunID = id
			} else {
				e.ParentRunID = parent.ID
			}
			a.callbacks.Handle(e)
		})
	}
	return &local, context.WithValue(ctx, runContextKey{}, runContext{id, agentID}), id
}
