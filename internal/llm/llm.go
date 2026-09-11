// Package llm defines the provider-agnostic contract between the agent and a
// language model. Provider adapters (OpenAI, test fakes) implement Client.
package llm

import (
	"context"
	"encoding/json"
)

// Role identifies the author of a message.
type Role string

// Message roles.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one entry of the conversation.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall // assistant messages only
	ToolCallID string     // tool messages only: the call this message answers
}

// ToolCall is a request by the model to invoke a tool.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ToolSpec describes a tool the model may call.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema of the arguments
}

// Request is a single completion request.
type Request struct {
	Messages []Message
	Tools    []ToolSpec
	// RequireToolCall forces the model to answer with at least one tool call
	// instead of free text.
	RequireToolCall bool
}

// Response is the model's reply.
type Response struct {
	Message Message
	Usage   Usage
}

// Usage reports token consumption.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Add accumulates other into u.
func (u *Usage) Add(other Usage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
}

// Total returns input plus output tokens.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// Client completes a conversation. Implementations own retries and timeouts
// for transient provider failures; any returned error ends the review.
type Client interface {
	Complete(ctx context.Context, req Request) (*Response, error)
}
