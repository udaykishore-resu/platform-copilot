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

// Cohere calls the v2 /embed endpoint. Cohere's models are notable for
// asymmetric input types: documents are embedded with input_type
// "search_document" and queries with "search_query", which measurably improves
// retrieval. The Embedder interface has no query/document distinction, so
// this adapter exposes it through EmbedQuery.
type Cohere struct {
	apiKey, baseURL, model string
	dims                   int
	http                   *http.Client
}

// NewCohere constructs the embedder (default embed-english-v3.0, 1024 dims).
func NewCohere(o Options) *Cohere {
	return &Cohere{
		apiKey:  firstNonEmpty(o.APIKey, envOr("COHERE_API_KEY", "")),
		baseURL: strings.TrimRight(firstNonEmpty(o.BaseURL, envOr("COHERE_BASE_URL", "https://api.cohere.com/v2")), "/"),
		model:   firstNonEmpty(o.Model, envOr("COPILOT_EMBED_MODEL", "embed-english-v3.0")),
		dims:    1024, http: o.HTTP,
	}
}

func (e *Cohere) Dimensions() int { return e.dims }
func (e *Cohere) Model() string   { return e.model }
func (e *Cohere) Name() string    { return "cohere" }

// Embed embeds documents (input_type search_document).
func (e *Cohere) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return e.embed(ctx, texts, "search_document")
}

// EmbedQuery embeds a search query (input_type search_query).
func (e *Cohere) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	v, err := e.embed(ctx, []string{q}, "search_query")
	if err != nil {
		return nil, err
	}
	return v[0], nil
}

func (e *Cohere) embed(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	if e.apiKey == "" {
		return nil, fmt.Errorf("cohere embeddings: COHERE_API_KEY is not set")
	}
	b, _ := json.Marshal(map[string]any{"model": e.model, "texts": texts, "input_type": inputType, "embedding_types": []string{"float"}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embed", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("cohere embeddings: HTTP %d: %s", resp.StatusCode, string(data))
	}
	var parsed struct {
		Embeddings struct {
			Float [][]float32 `json:"float"`
		} `json:"embeddings"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	for i := range parsed.Embeddings.Float {
		Normalize(parsed.Embeddings.Float[i])
	}
	return parsed.Embeddings.Float, nil
}
