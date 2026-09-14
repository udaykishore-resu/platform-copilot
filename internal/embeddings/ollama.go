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

// Ollama calls /api/embed for local open-source embedding models. Good defaults:
// nomic-embed-text (768 dims, 8k context, strong on English docs), bge-m3
// (1024 dims, multilingual, hybrid-friendly), all-minilm (384 dims, fastest).
type Ollama struct {
	host, model string
	dims        int
	http        *http.Client
}

// NewOllama constructs the embedder.
func NewOllama(o Options) *Ollama {
	model := firstNonEmpty(o.Model, envOr("COPILOT_EMBED_MODEL", "nomic-embed-text"))
	dims := map[string]int{"nomic-embed-text": 768, "bge-m3": 1024, "all-minilm": 384, "mxbai-embed-large": 1024}[model]
	if dims == 0 {
		dims = 768
	}
	return &Ollama{
		host:  strings.TrimRight(firstNonEmpty(o.BaseURL, envOr("OLLAMA_HOST", "http://localhost:11434")), "/"),
		model: model, dims: dims, http: o.HTTP,
	}
}

func (e *Ollama) Dimensions() int { return e.dims }
func (e *Ollama) Model() string   { return e.model }
func (e *Ollama) Name() string    { return "ollama" }

// Embed implements Embedder.
func (e *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	b, _ := json.Marshal(map[string]any{"model": e.model, "input": texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.host+"/api/embed", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embeddings (run `ollama pull %s`): %w", e.model, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("ollama embeddings: HTTP %d: %s", resp.StatusCode, string(data))
	}
	var parsed struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Embeddings) > 0 {
		e.dims = len(parsed.Embeddings[0])
	}
	for i := range parsed.Embeddings {
		Normalize(parsed.Embeddings[i])
	}
	return parsed.Embeddings, nil
}
