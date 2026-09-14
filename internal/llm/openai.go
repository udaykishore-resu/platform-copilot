package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// OpenAI implements Provider over the Chat Completions API. The same wire
// format is spoken by many OpenAI-compatible servers (vLLM, Groq, Together,
// LM Studio, Ollama's /v1), so BaseURL is configurable.
type OpenAI struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
}

// NewOpenAI constructs the provider. Defaults: OPENAI_API_KEY, OPENAI_BASE_URL,
// model gpt-4o-mini (cheap, 128k context, tool calling, vision).
func NewOpenAI(o Options) *OpenAI {
	return &OpenAI{
		apiKey:  firstNonEmpty(o.APIKey, envOr("OPENAI_API_KEY", "")),
		baseURL: strings.TrimRight(firstNonEmpty(o.BaseURL, envOr("OPENAI_BASE_URL", "https://api.openai.com/v1")), "/"),
		model:   firstNonEmpty(o.Model, envOr("COPILOT_MODEL", "gpt-4o-mini")),
		http:    o.HTTP,
	}
}

func (p *OpenAI) Name() string         { return "openai" }
func (p *OpenAI) DefaultModel() string { return p.model }

// ContextWindow returns known windows; unknown models get a conservative 8k.
func (p *OpenAI) ContextWindow(model string) int {
	switch {
	case strings.HasPrefix(model, "gpt-4o"), strings.HasPrefix(model, "gpt-4-turbo"), strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"), strings.HasPrefix(model, "o4"):
		return 128_000
	case strings.HasPrefix(model, "gpt-4.1"), strings.HasPrefix(model, "gpt-5"):
		return 1_000_000
	case strings.HasPrefix(model, "gpt-3.5"):
		return 16_385
	}
	return 8_192
}

// --- wire types (subset of the Chat Completions schema) ---

type oaMessage struct {
	Role       string       `json:"role"`
	Content    any          `json:"content,omitempty"` // string or []oaPart
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	Name       string       `json:"name,omitempty"`
}

type oaPart struct {
	Type     string      `json:"type"`
	Text     string      `json:"text,omitempty"`
	ImageURL *oaImageURL `json:"image_url,omitempty"`
}

type oaImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oaRequest struct {
	Model          string      `json:"model"`
	Messages       []oaMessage `json:"messages"`
	Tools          []oaTool    `json:"tools,omitempty"`
	MaxTokens      int         `json:"max_completion_tokens,omitempty"`
	Temperature    *float64    `json:"temperature,omitempty"`
	User           string      `json:"user,omitempty"`
	Stop           []string    `json:"stop,omitempty"`
	ResponseFormat any         `json:"response_format,omitempty"`
}

type oaResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      oaMessage `json:"message"`
		FinishReason string    `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// Complete implements Provider.
func (p *OpenAI) Complete(ctx context.Context, req Request) (*Response, error) {
	if p.apiKey == "" && strings.Contains(p.baseURL, "api.openai.com") {
		return nil, fmt.Errorf("openai: OPENAI_API_KEY is not set (use COPILOT_PROVIDER=mock to run without keys)")
	}
	model := firstNonEmpty(req.Model, p.model)
	body := oaRequest{Model: model, MaxTokens: req.MaxTokens, User: req.User, Stop: req.Stop}
	if req.Temperature > 0 {
		t := req.Temperature
		body.Temperature = &t
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, toOAMessage(m))
	}
	for _, t := range req.Tools {
		var ot oaTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.Parameters
		body.Tools = append(body.Tools, ot)
	}
	if len(req.JSONSchema) > 0 {
		body.ResponseFormat = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "copilot_output",
				"schema": req.JSONSchema,
				"strict": false,
			},
		}
	}
	var out oaResponse
	headers := map[string]string{}
	if p.apiKey != "" {
		headers["Authorization"] = "Bearer " + p.apiKey
	}
	if err := postJSON(ctx, p.http, p.baseURL+"/chat/completions", headers, body, &out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("openai: empty choices")
	}
	ch := out.Choices[0]
	resp := &Response{
		Content:      contentString(ch.Message.Content),
		FinishReason: ch.FinishReason,
		Model:        out.Model,
		Provider:     p.Name(),
		Usage: Usage{
			PromptTokens:     out.Usage.PromptTokens,
			CompletionTokens: out.Usage.CompletionTokens,
			TotalTokens:      out.Usage.TotalTokens,
		},
	}
	for _, tc := range ch.Message.ToolCalls {
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{
			ID: tc.ID, Name: tc.Function.Name, Arguments: json.RawMessage(tc.Function.Arguments),
		})
	}
	return resp, nil
}

func toOAMessage(m Message) oaMessage {
	om := oaMessage{Role: string(m.Role), ToolCallID: m.ToolCallID, Name: m.Name}
	if len(m.Images) == 0 {
		om.Content = m.Content
	} else {
		parts := []oaPart{{Type: "text", Text: m.Content}}
		for _, img := range m.Images {
			url := img.URL
			if url == "" {
				url = "data:" + img.MIME + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
			}
			parts = append(parts, oaPart{Type: "image_url", ImageURL: &oaImageURL{URL: url, Detail: "auto"}})
		}
		om.Content = parts
	}
	for _, tc := range m.ToolCalls {
		var otc oaToolCall
		otc.ID, otc.Type = tc.ID, "function"
		otc.Function.Name = tc.Name
		otc.Function.Arguments = string(tc.Arguments)
		om.ToolCalls = append(om.ToolCalls, otc)
	}
	if om.Role == "tool" && om.Content == "" {
		om.Content = ""
	}
	return om
}

func contentString(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, p := range v {
			if m, ok := p.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	}
	return ""
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
