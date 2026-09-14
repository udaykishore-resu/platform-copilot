package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAI calls the /embeddings endpoint. text-embedding-3-small (1536 dims,
// cheapest) is the default; text-embedding-3-large (3072) trades ~6x cost for
// a few points of retrieval quality. Both support the `dimensions` parameter
// (Matryoshka truncation) — shrinking to 256–512 dims cuts vector-store cost
// with a small accuracy loss, and we expose that as Options.Model "@<dims>".
type OpenAI struct {
	apiKey, baseURL, model string
	dims                   int
	http                   *http.Client
}

// NewOpenAI constructs the embedder. Model may carry a dimension suffix, e.g.
// "text-embedding-3-small@512".
func NewOpenAI(o Options) *OpenAI {
	model := firstNonEmpty(o.Model, envOr("COPILOT_EMBED_MODEL", "text-embedding-3-small"))
	dims := 1536
	if i := strings.Index(model, "@"); i > 0 {
		fmt.Sscanf(model[i+1:], "%d", &dims)
		model = model[:i]
	} else if strings.Contains(model, "large") {
		dims = 3072
	}
	return &OpenAI{
		apiKey:  firstNonEmpty(o.APIKey, envOr("OPENAI_API_KEY", "")),
		baseURL: strings.TrimRight(firstNonEmpty(o.BaseURL, envOr("OPENAI_BASE_URL", "https://api.openai.com/v1")), "/"),
		model:   model, dims: dims, http: o.HTTP,
	}
}

func (e *OpenAI) Dimensions() int { return e.dims }
func (e *OpenAI) Model() string   { return e.model }
func (e *OpenAI) Name() string    { return "openai" }

// Embed implements Embedder. Inputs are sent in batches of 256 — the API
// accepts up to 2048 inputs per call, but smaller batches keep retries cheap.
func (e *OpenAI) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if e.apiKey == "" && strings.Contains(e.baseURL, "api.openai.com") {
		return nil, fmt.Errorf("openai embeddings: OPENAI_API_KEY is not set")
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += 256 {
		end := min(start+256, len(texts))
		body := map[string]any{"model": e.model, "input": texts[start:end]}
		if strings.HasPrefix(e.model, "text-embedding-3") {
			body["dimensions"] = e.dims
		}
		b, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if e.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+e.apiKey)
		}
		resp, err := e.http.Do(req)
		if err != nil {
			return nil, err
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("openai embeddings: HTTP %d: %s", resp.StatusCode, string(data))
		}
		var parsed struct {
			Data []struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, err
		}
		batch := make([][]float32, end-start)
		for _, d := range parsed.Data {
			batch[d.Index] = Normalize(d.Embedding)
		}
		out = append(out, batch...)
	}
	return out, nil
}
