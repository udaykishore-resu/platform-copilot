package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Ollama implements Provider over Ollama's native /api/chat endpoint so the
// copilot runs fully offline on a laptop (Open-Source AI · Ollama SDK).
// Ollama also exposes an OpenAI-compatible /v1 — you could point the OpenAI
// provider at it — but the native API reports model metadata and supports
// images and tools uniformly across models.
type Ollama struct {
	host  string
	model string
	http  *http.Client
}

// NewOllama constructs the provider. Defaults: OLLAMA_HOST=http://localhost:11434,
// model llama3.2 (small, tool-capable).
func NewOllama(o Options) *Ollama {
	return &Ollama{
		host:  strings.TrimRight(firstNonEmpty(o.BaseURL, envOr("OLLAMA_HOST", "http://localhost:11434")), "/"),
		model: firstNonEmpty(o.Model, envOr("COPILOT_MODEL", "llama3.2")),
		http:  o.HTTP,
	}
}

func (p *Ollama) Name() string         { return "ollama" }
func (p *Ollama) DefaultModel() string { return p.model }

// ContextWindow: Ollama defaults num_ctx to 2048–8192 unless the Modelfile
// raises it; we stay conservative so the token budget never overflows.
func (p *Ollama) ContextWindow(string) int { return 8_192 }

type olMessage struct {
	Role      string       `json:"role"`
	Content   string       `json:"content"`
	Images    []string     `json:"images,omitempty"`
	ToolCalls []olToolCall `json:"tool_calls,omitempty"`
}

type olToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type olRequest struct {
	Model    string         `json:"model"`
	Messages []olMessage    `json:"messages"`
	Stream   bool           `json:"stream"`
	Tools    []oaTool       `json:"tools,omitempty"` // Ollama uses the OpenAI tool schema
	Format   any            `json:"format,omitempty"`
	Options  map[string]any `json:"options,omitempty"`
}

type olResponse struct {
	Model           string    `json:"model"`
	Message         olMessage `json:"message"`
	DoneReason      string    `json:"done_reason"`
	PromptEvalCount int       `json:"prompt_eval_count"`
	EvalCount       int       `json:"eval_count"`
}

// Complete implements Provider.
func (p *Ollama) Complete(ctx context.Context, req Request) (*Response, error) {
	body := olRequest{Model: firstNonEmpty(req.Model, p.model), Stream: false, Options: map[string]any{}}
	if req.MaxTokens > 0 {
		body.Options["num_predict"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		body.Options["temperature"] = req.Temperature
	}
	if len(req.Stop) > 0 {
		body.Options["stop"] = req.Stop
	}
	if len(req.JSONSchema) > 0 {
		body.Format = req.JSONSchema
	}
	for _, m := range req.Messages {
		om := olMessage{Role: string(m.Role), Content: m.Content}
		for _, img := range m.Images {
			om.Images = append(om.Images, base64.StdEncoding.EncodeToString(img.Data))
		}
		for _, tc := range m.ToolCalls {
			var otc olToolCall
			otc.Function.Name = tc.Name
			otc.Function.Arguments = tc.Arguments
			om.ToolCalls = append(om.ToolCalls, otc)
		}
		body.Messages = append(body.Messages, om)
	}
	for _, t := range req.Tools {
		var ot oaTool
		ot.Type = "function"
		ot.Function.Name, ot.Function.Description, ot.Function.Parameters = t.Name, t.Description, t.Parameters
		body.Tools = append(body.Tools, ot)
	}
	var out olResponse
	if err := postJSON(ctx, p.http, p.host+"/api/chat", nil, body, &out); err != nil {
		return nil, fmt.Errorf("ollama (is `ollama serve` running and is the model pulled?): %w", err)
	}
	resp := &Response{
		Content:      out.Message.Content,
		FinishReason: out.DoneReason,
		Model:        out.Model,
		Provider:     p.Name(),
		Usage:        Usage{PromptTokens: out.PromptEvalCount, CompletionTokens: out.EvalCount, TotalTokens: out.PromptEvalCount + out.EvalCount},
	}
	for i, tc := range out.Message.ToolCalls {
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{ID: fmt.Sprintf("call_%d", i), Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	return resp, nil
}
