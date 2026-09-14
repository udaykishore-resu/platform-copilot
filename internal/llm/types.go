// Package llm defines one Provider interface over every chat model the
// copilot can talk to (OpenAI, Anthropic, Gemini, Ollama, and a deterministic
// mock) so that the rest of the system never depends on a vendor SDK.
//
// Roadmap: Pre-trained Models · OpenAI Platform · Open-Source AI.
package llm

import (
	"context"
	"encoding/json"
)

// Role is the speaker of a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ImagePart is an image attached to a user message (Multimodal AI).
type ImagePart struct {
	// MIME is e.g. "image/png".
	MIME string
	// Data is the raw bytes; providers base64-encode as they require.
	Data []byte
	// URL may be used instead of Data when the provider supports remote images.
	URL string
}

// Message is one turn in a conversation.
type Message struct {
	Role    Role
	Content string
	Images  []ImagePart
	// ToolCalls is set on assistant messages that requested tools.
	ToolCalls []ToolCall
	// ToolCallID is set on tool messages that answer a tool call.
	ToolCallID string
	// Name is the tool name for RoleTool messages.
	Name string
}

// Tool is a function the model may call (OpenAI Functions / Tools,
// Anthropic tool use, Gemini function calling). Parameters is a JSON Schema.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolCall is a request from the model to run a tool.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Usage is token accounting as reported by the provider (or estimated).
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	// Estimated is true when the provider did not return usage and the
	// numbers come from tokens.Estimate.
	Estimated bool
}

// Request is a provider-neutral chat completion request.
type Request struct {
	Model       string
	Messages    []Message
	Tools       []Tool
	MaxTokens   int
	Temperature float64
	// JSONSchema, when set, asks the provider for structured output that
	// validates against this schema (Constraining outputs).
	JSONSchema json.RawMessage
	// User is an opaque end-user identifier forwarded to the provider for
	// abuse monitoring (Adding end-user IDs in prompts).
	User string
	// Stop sequences, if supported.
	Stop []string
}

// Response is a provider-neutral chat completion response.
type Response struct {
	Content      string
	ToolCalls    []ToolCall
	Usage        Usage
	FinishReason string
	Model        string
	Provider     string
}

// Provider is the single abstraction every feature in the copilot depends on.
type Provider interface {
	// Name returns the provider identifier ("openai", "anthropic", ...).
	Name() string
	// DefaultModel returns the model used when Request.Model is empty.
	DefaultModel() string
	// Complete runs one chat completion.
	Complete(ctx context.Context, req Request) (*Response, error)
	// ContextWindow returns the context window (tokens) for a model, or a
	// conservative default when unknown.
	ContextWindow(model string) int
}

// System is a helper that builds a system message.
func System(s string) Message { return Message{Role: RoleSystem, Content: s} }

// User is a helper that builds a user message.
func User(s string) Message { return Message{Role: RoleUser, Content: s} }

// Assistant is a helper that builds an assistant message.
func Assistant(s string) Message { return Message{Role: RoleAssistant, Content: s} }

// ToolResult is a helper that builds a tool result message.
func ToolResult(callID, name, content string) Message {
	return Message{Role: RoleTool, ToolCallID: callID, Name: name, Content: content}
}
