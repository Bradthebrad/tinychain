package agent

import (
	"context"
	"errors"
	"fmt"
	"github.com/Bradthebrad/tinychain/streaming"
	"strings"
	"time"

	"github.com/Bradthebrad/tinychain/callbacks"
	"github.com/Bradthebrad/tinychain/lc"
)

const DefaultMaxIterations = 20

type Config struct {
	Name          string
	Model         Model
	SystemPrompt  string
	Tools         []Tool
	Skills        []Skill
	Memory        []Memory
	Subagents     []Subagent
	MaxIterations int
	Callbacks     callbacks.Sink
	Context       ContextPolicy
	// Gate, when set, is consulted before every model call and every tool
	// call. It can hold a run (pause), let it go on, or ask it to wrap up.
	Gate StepGate
}

// StepGate lets a host control a run between steps. Wait returns nil to go
// on, ErrWrapUp to finish now with a short summary instead of starting new
// work, or any other error to end the run with that error. Wait may block,
// which pauses the run; it should return when ctx is done.
type StepGate interface {
	Wait(ctx context.Context) error
}

// ErrWrapUp asks a run to stop gracefully: no new tool calls, one last
// model call that says what was done and what is left.
var ErrWrapUp = errors.New("agent: stop requested")

// wrapUpPrompt is what the model is told when it is asked to stop.
const wrapUpPrompt = "The person asked you to stop now. Don't call any tools and don't start anything new. In a few sentences, say what you did, what you found, and what is left undone."

type Agent struct {
	name          string
	model         Model
	systemPrompt  string
	tools         map[string]Tool
	toolOrder     []Tool
	skills        []Skill
	memory        []Memory
	maxIterations int
	callbacks     callbacks.Sink
	context       ContextPolicy
	gate          StepGate
	// meter measures context use for one run; scopedRun copies the agent,
	// so each run has its own.
	meter *contextMeter
}

type Result struct {
	Messages []lc.BaseMessage `json:"messages"`
	Output   lc.BaseMessage   `json:"output"`
	Steps    int              `json:"steps"`
}

func New(config Config) *Agent {
	a := &Agent{
		name:          config.Name,
		model:         config.Model,
		systemPrompt:  config.SystemPrompt,
		tools:         map[string]Tool{},
		skills:        config.Skills,
		memory:        config.Memory,
		maxIterations: config.MaxIterations,
		callbacks:     config.Callbacks,
		context:       normalizeContextPolicy(config.Context),
		gate:          config.Gate,
	}
	if a.maxIterations == 0 {
		a.maxIterations = DefaultMaxIterations
	}
	for _, tool := range append(DefaultTools(), config.Tools...) {
		a.AddTool(tool)
	}
	if len(config.Subagents) > 0 {
		a.AddTool(taskTool(config.Subagents, config.Gate))
	}
	return a
}

func (a *Agent) AddTool(tool Tool) {
	def := tool.Definition()
	a.tools[def.Name] = tool
	for i, existing := range a.toolOrder {
		if existing.Definition().Name == def.Name {
			a.toolOrder[i] = tool
			return
		}
	}
	a.toolOrder = append(a.toolOrder, tool)
}

func (a *Agent) Invoke(ctx context.Context, input string) (*Result, error) {
	return a.InvokeMessages(ctx, []lc.BaseMessage{lc.Human(input)})
}

func (a *Agent) InvokeMessages(ctx context.Context, input []lc.BaseMessage) (*Result, error) {
	return a.invokeMessages(ctx, input, nil)
}

func (a *Agent) invokeMessages(ctx context.Context, input []lc.BaseMessage, inbox *InstructionInbox) (*Result, error) {
	defer inbox.Close()
	if a.model == nil {
		return nil, fmt.Errorf("agent: model is required")
	}
	messages := append([]lc.BaseMessage{}, a.systemMessages()...)
	messages = append(messages, input...)
	a, ctx, runID := a.scopedRun(ctx)
	a.meter = newContextMeter(a.context, a.toolOrder, a.model)
	for step := 0; step < a.maxIterations; step++ {
		if err := ctx.Err(); err != nil {
			if a.callbacks != nil {
				a.callbacks.Handle(callbacks.Error(callbacks.EventLLMError, runID, err))
			}
			return nil, err
		}
		if err := a.waitGate(ctx); err != nil {
			return a.stopAtGate(ctx, messages, runID, step, err)
		}
		for _, in := range inbox.take(false) {
			messages = append(messages, in.message())
		}
		var msg lc.BaseMessage
		var err error
		messages, msg, err = a.callModel(ctx, messages, runID, a.toolOrder)
		if err != nil {
			if a.callbacks != nil {
				a.callbacks.Handle(callbacks.Error(callbacks.EventLLMError, runID, err))
			}
			return nil, err
		}
		messages = append(messages, msg)
		if len(msg.ToolCalls) == 0 {
			// Seal acceptance atomically with deciding this response is final.
			var instructions []Instruction
			if step+1 < a.maxIterations {
				instructions = inbox.take(true)
			} else {
				inbox.Close()
			}
			if len(instructions) > 0 {
				for _, in := range instructions {
					messages = append(messages, in.message())
				}
				continue
			}
			return a.finish(runID, messages, msg, step+1), nil
		}
		if text := contentText(msg.Content); a.callbacks != nil && text != "" {
			a.callbacks.Handle(callbacks.Event{Event: callbacks.EventLLMCommentary, RunID: runID, Data: callbacks.EventData{Token: text}})
		}
		for i, call := range msg.ToolCalls {
			if err := a.waitGate(ctx); err != nil {
				// Every requested call needs a result, even the ones not run.
				for _, skipped := range msg.ToolCalls[i:] {
					messages = append(messages, lc.BaseMessage{Type: lc.RoleTool, ToolCallID: skipped.ID, Content: lc.TextContent("Not run: the run was stopped before this call."), Status: "error"})
				}
				return a.stopAtGate(ctx, messages, runID, step+1, err)
			}
			toolMsg := a.executeTool(ctx, call, messages)
			messages = append(messages, toolMsg)
		}
	}
	err := fmt.Errorf("agent: stopped after %d iterations with pending tool calls", a.maxIterations)
	if a.callbacks != nil {
		a.callbacks.Handle(callbacks.Error(callbacks.EventLLMError, runID, err))
	}
	return nil, err
}

// callModel fits the context, calls the model with streaming callbacks,
// and retries once after compacting when the provider says the request is
// too long. It returns the messages as sent and the reply.
func (a *Agent) callModel(ctx context.Context, messages []lc.BaseMessage, runID string, tools []Tool) ([]lc.BaseMessage, lc.BaseMessage, error) {
	compactedForRetry := false
	for {
		messages = a.fitContext(ctx, messages, runID, false)
		sentRaw := a.meter.raw(messages)
		if a.callbacks != nil {
			event := callbacks.ChatModelStart("agent", runID, [][]lc.BaseMessage{messages})
			if a.context.Enabled || a.context.MaxTokens > 0 {
				event.Data.Extra = a.meter.snapshot(a.meter.scaled(sentRaw))
			}
			a.callbacks.Handle(event)
		}
		summaryStreamed := false
		modelCtx := ctx
		if a.callbacks != nil {
			modelCtx = streaming.WithSink(ctx, func(delta streaming.Delta) {
				event := callbacks.LLMNewToken(runID, delta.Text)
				if delta.Summary {
					summaryStreamed = true
					event = callbacks.LLMReasoning(runID, delta.Text)
				}
				a.callbacks.Handle(event)
			})
		}
		msg, err := a.model.Call(modelCtx, messages, tools)
		if err == nil {
			a.meter.observe(sentRaw, msg)
			if !summaryStreamed {
				a.emitReasoning(runID, msg)
			}
			return messages, msg, nil
		}
		if !compactedForRetry && a.shouldCompactAfterError(err) {
			a.meter.learnLimit(err)
			next := a.fitContext(ctx, messages, runID, true)
			if a.meter.estimate(next) < a.meter.estimate(messages) {
				messages = next
				compactedForRetry = true
				continue
			}
		}
		return messages, lc.BaseMessage{}, err
	}
}

func (a *Agent) finish(runID string, messages []lc.BaseMessage, msg lc.BaseMessage, steps int) *Result {
	if a.callbacks != nil {
		a.callbacks.Handle(callbacks.LLMEnd(runID, lc.LLMResult{
			Generations: [][]lc.ChatGeneration{{{Message: msg}}},
		}))
	}
	return &Result{Messages: messages, Output: msg, Steps: steps}
}

func (a *Agent) waitGate(ctx context.Context) error {
	if a.gate == nil {
		return nil
	}
	return a.gate.Wait(ctx)
}

// stopAtGate ends a run the gate stopped. ErrWrapUp gets one last model
// call, without running any tools, that reports what was done.
func (a *Agent) stopAtGate(ctx context.Context, messages []lc.BaseMessage, runID string, steps int, err error) (*Result, error) {
	if !errors.Is(err, ErrWrapUp) {
		if a.callbacks != nil {
			a.callbacks.Handle(callbacks.Error(callbacks.EventLLMError, runID, err))
		}
		return nil, err
	}
	messages = append(messages, lc.Human(wrapUpPrompt))
	// Tools stay defined: some providers reject a history with tool calls
	// when none are. Any the model asks for anyway are dropped.
	messages, msg, callErr := a.callModel(ctx, messages, runID, a.toolOrder)
	if callErr != nil {
		if ctx.Err() != nil {
			if a.callbacks != nil {
				a.callbacks.Handle(callbacks.Error(callbacks.EventLLMError, runID, ctx.Err()))
			}
			return nil, ctx.Err()
		}
		msg = lc.AI("Stopped at your request.")
	}
	msg.ToolCalls = nil
	if len(msg.Content.Parts) > 0 {
		kept := msg.Content.Parts[:0:0]
		for _, part := range msg.Content.Parts {
			if part.Type != "tool_use" {
				kept = append(kept, part)
			}
		}
		msg.Content.Parts = kept
	}
	if strings.TrimSpace(contentText(msg.Content)) == "" {
		msg = lc.AI("Stopped at your request.")
	}
	messages = append(messages, msg)
	return a.finish(runID, messages, msg, steps), nil
}

func (a *Agent) emitReasoning(runID string, msg lc.BaseMessage) {
	if a.callbacks == nil {
		return
	}
	for _, text := range lc.VisibleReasoning(msg) {
		a.callbacks.Handle(callbacks.LLMReasoning(runID, text))
	}
}

func (a *Agent) executeTool(ctx context.Context, call lc.ToolCall, messages []lc.BaseMessage) lc.BaseMessage {
	tool, ok := a.tools[call.Name]
	if !ok {
		if a.callbacks != nil {
			a.callbacks.Handle(callbacks.Event{
				Event: callbacks.EventToolError,
				Name:  call.Name,
				RunID: call.ID,
				Time:  time.Now().UTC(),
				Data:  callbacks.EventData{Input: call.Args, Error: fmt.Sprintf("tool %q not found", call.Name)},
			})
		}
		return lc.Tool(call.ID, fmt.Sprintf("tool %q not found", call.Name))
	}
	var metadata map[string]any
	if info, ok := InfoOf(tool); ok {
		metadata = info.Map()
	}
	started := time.Now().UTC()
	if a.callbacks != nil {
		a.callbacks.Handle(callbacks.Event{
			Event:    callbacks.EventToolStart,
			Name:     call.Name,
			RunID:    call.ID,
			Time:     started,
			Metadata: metadata,
			Data:     callbacks.EventData{Input: call.Args},
		})
	}
	toolCtx := WithToolCallID(ctx, call.ID)
	var output string
	var resultMeta map[string]any
	var err error
	if rich, ok := tool.(MetaTool); ok {
		output, resultMeta, err = rich.CallWithMeta(toolCtx, call.Args)
	} else {
		output, err = tool.Call(toolCtx, call.Args)
	}
	extra := map[string]any{"duration_ms": time.Since(started).Milliseconds()}
	if len(resultMeta) > 0 {
		extra["result_meta"] = resultMeta
	}
	if err != nil {
		text, guard := a.guardToolOutput(messages, err.Error())
		if guard != nil {
			extra["context_guard"] = guard
		}
		if a.callbacks != nil {
			a.callbacks.Handle(callbacks.Event{
				Event:    callbacks.EventToolError,
				Name:     call.Name,
				RunID:    call.ID,
				Time:     time.Now().UTC(),
				Metadata: metadata,
				Data:     callbacks.EventData{Input: call.Args, Error: err.Error(), Extra: extra},
			})
		}
		return lc.BaseMessage{
			Type:       lc.RoleTool,
			ToolCallID: call.ID,
			Content:    lc.TextContent(text),
			Status:     "error",
		}
	}
	output, guard := a.guardToolOutput(messages, output)
	if guard != nil {
		extra["context_guard"] = guard
	}
	if a.callbacks != nil {
		a.callbacks.Handle(callbacks.Event{
			Event:    callbacks.EventToolEnd,
			Name:     call.Name,
			RunID:    call.ID,
			Time:     time.Now().UTC(),
			Metadata: metadata,
			Data:     callbacks.EventData{Input: call.Args, Output: output, Extra: extra},
		})
	}
	return lc.BaseMessage{
		Type:       lc.RoleTool,
		ToolCallID: call.ID,
		Content:    lc.TextContent(output),
		Status:     "success",
	}
}

func (a *Agent) systemMessages() []lc.BaseMessage {
	prompt := ComposeSystemPrompt(a.systemPrompt, a.skills, a.memory)
	if prompt == "" {
		return nil
	}
	return []lc.BaseMessage{lc.System(prompt)}
}
