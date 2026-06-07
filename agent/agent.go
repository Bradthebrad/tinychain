package agent

import (
	"context"
	"fmt"
	"time"

	"tinychain/callbacks"
	"tinychain/lc"
)

const DefaultMaxIterations = 8

type Config struct {
	Model         Model
	SystemPrompt  string
	Tools         []Tool
	Skills        []Skill
	Memory        []Memory
	Subagents     []Subagent
	MaxIterations int
	Callbacks     callbacks.Sink
}

type Agent struct {
	model         Model
	systemPrompt  string
	tools         map[string]Tool
	toolOrder     []Tool
	skills        []Skill
	memory        []Memory
	maxIterations int
	callbacks     callbacks.Sink
}

type Result struct {
	Messages []lc.BaseMessage `json:"messages"`
	Output   lc.BaseMessage   `json:"output"`
	Steps    int              `json:"steps"`
}

func New(config Config) *Agent {
	a := &Agent{
		model:         config.Model,
		systemPrompt:  config.SystemPrompt,
		tools:         map[string]Tool{},
		skills:        config.Skills,
		memory:        config.Memory,
		maxIterations: config.MaxIterations,
		callbacks:     config.Callbacks,
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
	if a.model == nil {
		return nil, fmt.Errorf("agent: model is required")
	}
	messages := append([]lc.BaseMessage{}, a.systemMessages()...)
	messages = append(messages, input...)
	runID := "agent"
	if a.callbacks != nil {
		a.callbacks.Handle(callbacks.ChatModelStart("agent", runID, [][]lc.BaseMessage{messages}))
	}
	for step := 0; step < a.maxIterations; step++ {
		msg, err := a.model.Call(ctx, messages, a.toolOrder)
		if err != nil {
			if a.callbacks != nil {
				a.callbacks.Handle(callbacks.Error(callbacks.EventLLMError, runID, err))
			}
			return nil, err
		}
		messages = append(messages, msg)
		if len(msg.ToolCalls) == 0 {
			result := &Result{Messages: messages, Output: msg, Steps: step + 1}
			if a.callbacks != nil {
				a.callbacks.Handle(callbacks.LLMEnd(runID, lc.LLMResult{
					Generations: [][]lc.ChatGeneration{{{Message: msg}}},
				}))
			}
			return result, nil
		}
		for _, call := range msg.ToolCalls {
			toolMsg := a.executeTool(ctx, call)
			messages = append(messages, toolMsg)
		}
	}
	return nil, fmt.Errorf("agent: stopped after %d iterations with pending tool calls", a.maxIterations)
}

func (a *Agent) executeTool(ctx context.Context, call lc.ToolCall) lc.BaseMessage {
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
