package callbacks

import (
	"time"

	"tinychain/lc"
)

type EventName string

const (
	EventChatModelStart EventName = "on_chat_model_start"
	EventLLMStart       EventName = "on_llm_start"
	EventLLMNewToken    EventName = "on_llm_new_token"
	EventLLMEnd         EventName = "on_llm_end"
	EventLLMError       EventName = "on_llm_error"
	EventChainStart     EventName = "on_chain_start"
	EventChainEnd       EventName = "on_chain_end"
	EventChainError     EventName = "on_chain_error"
	EventToolStart      EventName = "on_tool_start"
	EventToolEnd        EventName = "on_tool_end"
	EventToolError      EventName = "on_tool_error"
	EventRetrieverStart EventName = "on_retriever_start"
	EventRetrieverEnd   EventName = "on_retriever_end"
	EventRetrieverError EventName = "on_retriever_error"
)

type Event struct {
	Event       EventName      `json:"event"`
	Name        string         `json:"name,omitempty"`
	RunID       string         `json:"run_id,omitempty"`
	ParentRunID string         `json:"parent_run_id,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Data        EventData      `json:"data,omitempty"`
	Time        time.Time      `json:"time,omitempty"`
}

type EventData struct {
	Serialized       map[string]any     `json:"serialized,omitempty"`
	Messages         [][]lc.BaseMessage `json:"messages,omitempty"`
	Prompts          []string           `json:"prompts,omitempty"`
	Input            any                `json:"input,omitempty"`
	Output           any                `json:"output,omitempty"`
	Token            string             `json:"token,omitempty"`
	Chunk            *GenerationChunk   `json:"chunk,omitempty"`
	Response         *lc.LLMResult      `json:"response,omitempty"`
	Error            string             `json:"error,omitempty"`
	InvocationParams map[string]any     `json:"invocation_params,omitempty"`
	Options          map[string]any     `json:"options,omitempty"`
	Extra            map[string]any     `json:"extra,omitempty"`
}

type GenerationChunk struct {
	Text           string          `json:"text,omitempty"`
	Message        *lc.BaseMessage `json:"message,omitempty"`
	GenerationInfo map[string]any  `json:"generation_info,omitempty"`
	Type           string          `json:"type,omitempty"`
}

type Sink interface {
	Handle(Event)
}

type SinkFunc func(Event)

func (f SinkFunc) Handle(event Event) {
	f(event)
}

func ChatModelStart(name, runID string, messages [][]lc.BaseMessage) Event {
	return Event{
		Event: EventChatModelStart,
		Name:  name,
		RunID: runID,
		Time:  time.Now().UTC(),
		Data:  EventData{Messages: messages},
	}
}

func LLMNewToken(runID, token string) Event {
	return Event{
		Event: EventLLMNewToken,
		RunID: runID,
		Time:  time.Now().UTC(),
		Data:  EventData{Token: token, Chunk: &GenerationChunk{Text: token}},
	}
}

func LLMEnd(runID string, result lc.LLMResult) Event {
	return Event{
		Event: EventLLMEnd,
		RunID: runID,
		Time:  time.Now().UTC(),
		Data:  EventData{Response: &result, Output: result},
	}
}

func Error(event EventName, runID string, err error) Event {
	message := ""
	if err != nil {
		message = err.Error()
	}
	return Event{
		Event: event,
		RunID: runID,
		Time:  time.Now().UTC(),
		Data:  EventData{Error: message},
	}
}
