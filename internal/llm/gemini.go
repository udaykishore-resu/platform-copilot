package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Gemini implements Provider over the Gemini generateContent REST API.
// Notable differences hidden here: roles are "user"/"model", the system prompt
// is `systemInstruction`, tool calls are `functionCall` parts and tool results
// are `functionResponse` parts.
type Gemini struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
}

// NewGemini constructs the provider. Default model gemini-2.5-flash.
func NewGemini(o Options) *Gemini {
	return &Gemini{
		apiKey:  firstNonEmpty(o.APIKey, envOr("GEMINI_API_KEY", envOr("GOOGLE_API_KEY", ""))),
		baseURL: strings.TrimRight(firstNonEmpty(o.BaseURL, envOr("GEMINI_BASE_URL", "https://generativelanguage.googleapis.com/v1beta")), "/"),
		model:   firstNonEmpty(o.Model, envOr("COPILOT_MODEL", "gemini-2.5-flash")),
		http:    o.HTTP,
	}
}

func (p *Gemini) Name() string             { return "gemini" }
func (p *Gemini) DefaultModel() string     { return p.model }
func (p *Gemini) ContextWindow(string) int { return 1_000_000 }

type gmPart struct {
	Text             string          `json:"text,omitempty"`
	InlineData       *gmInline       `json:"inlineData,omitempty"`
	FunctionCall     *gmFunctionCall `json:"functionCall,omitempty"`
	FunctionResponse *gmFunctionResp `json:"functionResponse,omitempty"`
}

type gmInline struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gmFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type gmFunctionResp struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type gmContent struct {
	Role  string   `json:"role,omitempty"`
	Parts []gmPart `json:"parts"`
}

type gmRequest struct {
	SystemInstruction *gmContent  `json:"systemInstruction,omitempty"`
	Contents          []gmContent `json:"contents"`
	Tools             []struct {
		FunctionDeclarations []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters,omitempty"`
		} `json:"functionDeclarations"`
	} `json:"tools,omitempty"`
	GenerationConfig map[string]any `json:"generationConfig,omitempty"`
}

type gmResponse struct {
	Candidates []struct {
		Content      gmContent `json:"content"`
		FinishReason string    `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

// Complete implements Provider.
func (p *Gemini) Complete(ctx context.Context, req Request) (*Response, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("gemini: GEMINI_API_KEY is not set")
	}
	model := firstNonEmpty(req.Model, p.model)
	body := gmRequest{GenerationConfig: map[string]any{}}
	if req.MaxTokens > 0 {
		body.GenerationConfig["maxOutputTokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		body.GenerationConfig["temperature"] = req.Temperature
	}
	if len(req.Stop) > 0 {
		body.GenerationConfig["stopSequences"] = req.Stop
	}
	if len(req.JSONSchema) > 0 {
		body.GenerationConfig["responseMimeType"] = "application/json"
		body.GenerationConfig["responseSchema"] = req.JSONSchema
	}
	var systems []string
	for _, m := range req.Messages {
		switch m.Role {
		case RoleSystem:
			systems = append(systems, m.Content)
		case RoleAssistant:
			parts := []gmPart{}
			if m.Content != "" {
				parts = append(parts, gmPart{Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				parts = append(parts, gmPart{FunctionCall: &gmFunctionCall{Name: tc.Name, Args: tc.Arguments}})
			}
			body.Contents = append(body.Contents, gmContent{Role: "model", Parts: parts})
		case RoleTool:
			body.Contents = append(body.Contents, gmContent{Role: "user", Parts: []gmPart{{
				FunctionResponse: &gmFunctionResp{Name: m.Name, Response: map[string]any{"result": m.Content}},
			}}})
		default:
			parts := []gmPart{{Text: m.Content}}
			for _, img := range m.Images {
				parts = append(parts, gmPart{InlineData: &gmInline{MIMEType: img.MIME, Data: base64.StdEncoding.EncodeToString(img.Data)}})
			}
			body.Contents = append(body.Contents, gmContent{Role: "user", Parts: parts})
		}
	}
	if len(systems) > 0 {
		body.SystemInstruction = &gmContent{Parts: []gmPart{{Text: strings.Join(systems, "\n\n")}}}
	}
	if len(req.Tools) > 0 {
		body.Tools = make([]struct {
			FunctionDeclarations []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters,omitempty"`
			} `json:"functionDeclarations"`
		}, 1)
		for _, t := range req.Tools {
			body.Tools[0].FunctionDeclarations = append(body.Tools[0].FunctionDeclarations, struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters,omitempty"`
			}{t.Name, t.Description, t.Parameters})
		}
	}
	url := fmt.Sprintf("%s/models/%s:generateContent", p.baseURL, model)
	var out gmResponse
	if err := postJSON(ctx, p.http, url, map[string]string{"x-goog-api-key": p.apiKey}, body, &out); err != nil {
		return nil, err
	}
	if len(out.Candidates) == 0 {
		return nil, fmt.Errorf("gemini: no candidates (safety block?)")
	}
	c := out.Candidates[0]
	resp := &Response{Model: model, Provider: p.Name(), FinishReason: c.FinishReason}
	resp.Usage = Usage{PromptTokens: out.UsageMetadata.PromptTokenCount, CompletionTokens: out.UsageMetadata.CandidatesTokenCount, TotalTokens: out.UsageMetadata.TotalTokenCount}
	var sb strings.Builder
	for i, part := range c.Content.Parts {
		if part.Text != "" {
			sb.WriteString(part.Text)
		}
		if part.FunctionCall != nil {
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{ID: fmt.Sprintf("call_%d", i), Name: part.FunctionCall.Name, Arguments: part.FunctionCall.Args})
		}
	}
	resp.Content = sb.String()
	return resp, nil
}
