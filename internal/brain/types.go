// Package brain provides LLM provider abstraction with a unified tool-use interface.
package brain

// ToolCall represents a tool invocation requested by the LLM.
type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

// BrainResponse is the result from an LLM call.
type BrainResponse struct {
	Text       string
	ToolCalls  []ToolCall
	StopReason string // "end_turn" | "tool_use" | "max_tokens"
	Usage      Usage
}

// Usage tracks token consumption.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Message represents a conversation message.
type Message struct {
	Role       string // "user" | "assistant" | "tool_result"
	Content    string
	ToolCallID string     // set when Role == "tool_result"
	ToolCalls  []ToolCall // set when Role == "assistant" and tool use
}

// ToolSchema describes a tool for the LLM.
type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}
