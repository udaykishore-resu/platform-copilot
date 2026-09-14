package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Anthropic implements Provider over the Messages API. Differences from OpenAI
// that the adapter hides: the system prompt is a top-level field, tool results
// travel inside user messages as content blocks, and max_tokens is required.
type Anthropic struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
}

// NewAnthropic constructs the provider. Default model: claude-sonnet-4-5 class
// (override with COPILOT_MODEL). Context window is 200k for current models.
func NewAnthropic(o Options) *Anthropic {
	return &Anthropic{
		apiKey:  firstNonEmpty(o.APIKey, envOr("ANTHROPIC_API_KEY", "")),
		baseURL: strings.TrimRight(firstNonEmpty(o.BaseURL, envOr("ANTHROPIC_BASE_URL", "https://api.anthropic.com/v1")), "/"),
		model:   firstNonEmpty(o.Model, envOr("COPILOT_MODEL", "claude-sonnet-4-5")),
		http:    o.HTTP,
	}
}

func (p *Anthropic) Name() string             { return "anthropic" }
func (p *Anthropic) DefaultModel() string     { return p.model }
func (p *Anthropic) ContextWindow(string) int { return 200_000 }

type anBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Source    *anSource       `json:"source,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type anSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type anMessage struct {
	Role    string    `json:"role"`
	Content []anBlock `json:"content"`
}

type anTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anRequest struct {
	Model       string      `json:"model"`
	System      string      `json:"system,omitempty"`
	Messages    []anMessage `json:"messages"`
	MaxTokens   int         `json:"max_tokens"`
	Temperature *float64    `json:"temperature,omitempty"`
	Tools       []anTool    `json:"tools,omitempty"`
	Metadata    *struct {
		UserID string `json:"user_id,omitempty"`
	} `json:"metadata,omitempty"`
	StopSequences []string `json:"stop_sequences,omitempty"`
}

type anResponse struct {
	Model      string    `json:"model"`
	Content    []anBlock `json:"content"`
	StopReason string    `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Complete implements Provider.
func (p *Anthropic) Complete(ctx context.Context, req Request) (*Response, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("anthropic: ANTHROPIC_API_KEY is not set")
	}
	body := anRequest{Model: firstNonEmpty(req.Model, p.model), MaxTokens: req.MaxTokens, StopSequences: req.Stop}
	if body.MaxTokens == 0 {
		body.MaxTokens = 1024
	}
	if req.Temperature > 0 {
		t := req.Temperature
		body.Temperature = &t
	}
	if req.User != "" {
		body.Metadata = &struct {
			UserID string `json:"user_id,omitempty"`
		}{UserID: req.User}
	}
	var systems []string
	for _, m := range req.Messages {
		switch m.Role {
		case RoleSystem:
			systems = append(systems, m.Content)
		case RoleTool:
			body.Messages = append(body.Messages, anMessage{Role: "user", Content: []anBlock{{
				Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content,
			}}})
		case RoleAssistant:
			blocks := []anBlock{}
			if m.Content != "" {
				blocks = append(blocks, anBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: tc.Arguments})
			}
			body.Messages = append(body.Messages, anMessage{Role: "assistant", Content: blocks})
		default:
			blocks := []anBlock{}
			for _, img := range m.Images {
				src := &anSource{Type: "base64", MediaType: img.MIME, Data: base64.StdEncoding.EncodeToString(img.Data)}
				if img.URL != "" {
					src = &anSource{Type: "url", URL: img.URL}
				}
				blocks = append(blocks, anBlock{Type: "image", Source: src})
			}
			blocks = append(blocks, anBlock{Type: "text", Text: m.Content})
			body.Messages = append(body.Messages, anMessage{Role: "user", Content: blocks})
		}
	}
	body.System = strings.Join(systems, "\n\n")
	if len(req.JSONSchema) > 0 {
		// Anthropic has no response_format; the idiomatic way to force JSON
		// is to instruct in the system prompt and validate on the way out.
		body.System += "\n\nRespond ONLY with JSON that validates against this JSON Schema:\n" + string(req.JSONSchema)
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, anTool{Name: t.Name, Description: t.Description, InputSchema: t.Parameters})
	}
	var out anResponse
	err := postJSON(ctx, p.http, p.baseURL+"/messages", map[string]string{
		"x-api-key":         p.apiKey,
		"anthropic-version": "2023-06-01",
	}, body, &out)
	if err != nil {
		return nil, err
	}
	resp := &Response{Model: out.Model, Provider: p.Name(), FinishReason: out.StopReason}
	resp.Usage = Usage{PromptTokens: out.Usage.InputTokens, CompletionTokens: out.Usage.OutputTokens, TotalTokens: out.Usage.InputTokens + out.Usage.OutputTokens}
	var sb strings.Builder
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			sb.WriteString(b.Text)
		case "tool_use":
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Arguments: b.Input})
		}
	}
	resp.Content = sb.String()
	return resp, nil
}
