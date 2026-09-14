package agent

import (
	"context"
	"fmt"
	"github.com/Bradthebrad/tinychain/streaming"
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
}

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
	}
	if a.maxIterations == 0 {
		a.maxIterations = DefaultMaxIterations
	}
	for _, tool := range append(DefaultTools(), config.Tools...) {
		a.AddTool(tool)
	}
	if len(config.Subagents) > 0 {
		a.AddTool(TaskTool(config.Subagents))
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
	for step := 0; step < a.maxIterations; step++ {
		if err := ctx.Err(); err != nil {
			if a.callbacks != nil {
				a.callbacks.Handle(callbacks.Error(callbacks.EventLLMError, runID, err))
			}
			return nil, err
		}
		for _, in := range inbox.take(false) {
			messages = append(messages, in.message())
		}
		compactedForRetry := false
		var msg lc.BaseMessage
		var err error
		summaryStreamed := false
		for {
			messages = a.compactIfNeeded(ctx, messages, runID, false)
			if a.callbacks != nil {
				a.callbacks.Handle(callbacks.ChatModelStart("agent", runID, [][]lc.BaseMessage{messages}))
			}
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
			msg, err = a.model.Call(modelCtx, messages, a.toolOrder)
			if err != nil && !compactedForRetry && a.shouldCompactAfterError(err) {
				next := a.compactIfNeeded(ctx, messages, runID, true)
				if EstimateTokens(next) < EstimateTokens(messages) {
					messages = next
					compactedForRetry = true
					continue
				}
			}
			break
		}
		if err != nil {
			if a.callbacks != nil {
				a.callbacks.Handle(callbacks.Error(callbacks.EventLLMError, runID, err))
			}
			return nil, err
		}
		messages = append(messages, msg)
		if !summaryStreamed {
			a.emitReasoning(runID, msg)
		}
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
			result := &Result{Messages: messages, Output: msg, Steps: step + 1}
			if a.callbacks != nil {
				a.callbacks.Handle(callbacks.LLMEnd(runID, lc.LLMResult{
					Generations: [][]lc.ChatGeneration{{{Message: msg}}},
				}))
			}
			return result, nil
		}
		if text := contentText(msg.Content); a.callbacks != nil && text != "" {
			a.callbacks.Handle(callbacks.Event{Event: callbacks.EventLLMCommentary, RunID: runID, Data: callbacks.EventData{Token: text}})
		}
		for _, call := range msg.ToolCalls {
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
	if a.callbacks != nil {
		a.callbacks.Handle(callbacks.Event{
			Event: callbacks.EventToolStart,
			Name:  call.Name,
			RunID: call.ID,
			Time:  time.Now().UTC(),
			Data:  callbacks.EventData{Input: call.Args},
		})
	}
	output, err := tool.Call(ctx, call.Args)
	if err != nil {
		if a.callbacks != nil {
			a.callbacks.Handle(callbacks.Event{
				Event: callbacks.EventToolError,
				Name:  call.Name,
				RunID: call.ID,
				Time:  time.Now().UTC(),
				Data:  callbacks.EventData{Input: call.Args, Error: err.Error()},
			})
		}
		return lc.BaseMessage{
			Type:       lc.RoleTool,
			ToolCallID: call.ID,
			Content:    lc.TextContent(err.Error()),
			Status:     "error",
		}
	}
	output = a.guardToolOutput(messages, output)
	if a.callbacks != nil {
		a.callbacks.Handle(callbacks.Event{
			Event: callbacks.EventToolEnd,
			Name:  call.Name,
			RunID: call.ID,
			Time:  time.Now().UTC(),
			Data:  callbacks.EventData{Input: call.Args, Output: output},
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
