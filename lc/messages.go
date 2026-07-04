package lc

type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleHuman     MessageRole = "human"
	RoleAI        MessageRole = "ai"
	RoleTool      MessageRole = "tool"
	RoleFunction  MessageRole = "function"
	RoleChat      MessageRole = "chat"
	RoleDeveloper MessageRole = "developer"
)

// BaseMessage mirrors the fields LangChain serializes across concrete message
// classes such as HumanMessage, AIMessage, SystemMessage, and ToolMessage.
type BaseMessage struct {
	Type             MessageRole       `json:"type"`
	Content          Content           `json:"content"`
	ID               string            `json:"id,omitempty"`
	Name             string            `json:"name,omitempty"`
	AdditionalKwargs map[string]any    `json:"additional_kwargs,omitempty"`
	ResponseMetadata map[string]any    `json:"response_metadata,omitempty"`
	Artifact         any               `json:"artifact,omitempty"`
	Status           string            `json:"status,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCall        `json:"tool_calls,omitempty"`
	InvalidToolCalls []InvalidToolCall `json:"invalid_tool_calls,omitempty"`
	UsageMetadata    *UsageMetadata    `json:"usage_metadata,omitempty"`
}

type ToolCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
	ID   string         `json:"id,omitempty"`
	Type string         `json:"type,omitempty"`
}

type InvalidToolCall struct {
	Name  string `json:"name,omitempty"`
	Args  string `json:"args,omitempty"`
	ID    string `json:"id,omitempty"`
	Error string `json:"error,omitempty"`
	Type  string `json:"type,omitempty"`
}

type UsageMetadata struct {
	InputTokens        int            `json:"input_tokens,omitempty"`
	OutputTokens       int            `json:"output_tokens,omitempty"`
	TotalTokens        int            `json:"total_tokens,omitempty"`
	InputTokenDetails  map[string]int `json:"input_token_details,omitempty"`
	OutputTokenDetails map[string]int `json:"output_token_details,omitempty"`
}

func System(text string) BaseMessage {
	return BaseMessage{Type: RoleSystem, Content: TextContent(text)}
}

func Human(text string) BaseMessage {
	return BaseMessage{Type: RoleHuman, Content: TextContent(text)}
}

func AI(text string) BaseMessage {
	return BaseMessage{Type: RoleAI, Content: TextContent(text)}
}

func Tool(toolCallID, text string) BaseMessage {
	return BaseMessage{Type: RoleTool, ToolCallID: toolCallID, Content: TextContent(text)}
}
