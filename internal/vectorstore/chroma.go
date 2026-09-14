package vectorstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Chroma adapts the Chroma HTTP API (v2). Chroma is the "batteries included"
// developer store: trivial to start (`pip install chromadb && chroma run`),
// embedded or client/server, HNSW under the hood, metadata `where` filters.
// It is the right default for prototypes; its operational story (sharding,
// replication, auth) is younger than Qdrant/Weaviate/pgvector, which is why
// ADR-0004 keeps it out of production.
type Chroma struct {
	url, collection, collectionID string
	tenant, database              string
	http                          *http.Client
}

// NewChroma constructs the adapter and resolves (or creates) the collection.
func NewChroma(o Options) (*Chroma, error) {
	c := &Chroma{
		url:        strings.TrimRight(firstNonEmpty(o.URL, envOr("CHROMA_URL", "http://localhost:8000")), "/"),
		collection: o.Collection, tenant: "default_tenant", database: "default_database", http: o.HTTP,
	}
	return c, nil
}

func (c *Chroma) Name() string { return "chroma" }

func (c *Chroma) base() string {
	return fmt.Sprintf("%s/api/v2/tenants/%s/databases/%s", c.url, c.tenant, c.database)
}

func (c *Chroma) do(ctx context.Context, method, url string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("chroma: %w (is Chroma running at %s? try `docker run -p 8000:8000 chromadb/chroma`)", err, c.url)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("chroma: %s %s: HTTP %d: %s", method, url, resp.StatusCode, string(data))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *Chroma) ensure(ctx context.Context) error {
	if c.collectionID != "" {
		return nil
	}
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, c.base()+"/collections", map[string]any{
		"name": c.collection, "get_or_create": true,
		"metadata": map[string]any{"hnsw:space": "cosine"},
	}, &out)
	if err != nil {
		return err
	}
	c.collectionID = out.ID
	return nil
}

// Upsert implements Store.
func (c *Chroma) Upsert(ctx context.Context, docs []Document) error {
	if err := c.ensure(ctx); err != nil {
		return err
	}
	ids, embs, texts, metas := []string{}, [][]float32{}, []string{}, []map[string]any{}
	for _, d := range docs {
		ids = append(ids, d.ID)
		embs = append(embs, d.Vector)
		texts = append(texts, d.Text)
		m := map[string]any{}
		for k, v := range d.Metadata {
			m[k] = v
		}
		if len(m) == 0 {
			m["_"] = "x" // Chroma rejects empty metadata dicts
		}
		metas = append(metas, m)
	}
	return c.do(ctx, http.MethodPost, c.base()+"/collections/"+c.collectionID+"/upsert",
		map[string]any{"ids": ids, "embeddings": embs, "documents": texts, "metadatas": metas}, nil)
}

// Delete implements Store.
func (c *Chroma) Delete(ctx context.Context, ids []string) error {
	if err := c.ensure(ctx); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, c.base()+"/collections/"+c.collectionID+"/delete", map[string]any{"ids": ids}, nil)
}

// Count implements Store.
func (c *Chroma) Count(ctx context.Context) (int, error) {
	if err := c.ensure(ctx); err != nil {
		return 0, err
	}
	var n int
	err := c.do(ctx, http.MethodGet, c.base()+"/collections/"+c.collectionID+"/count", nil, &n)
	return n, err
}

// Search implements Store. Chroma returns distances (cosine distance =
// 1 - similarity), which we convert back to similarity.
func (c *Chroma) Search(ctx context.Context, vector []float32, k int, filter map[string]string) ([]Hit, error) {
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	body := map[string]any{"query_embeddings": [][]float32{vector}, "n_results": k, "include": []string{"documents", "metadatas", "distances"}}
	if len(filter) > 0 {
		where := map[string]any{}
		for k, v := range filter {
			where[k] = v
		}
		body["where"] = where
	}
	var out struct {
		IDs       [][]string         `json:"ids"`
		Documents [][]string         `json:"documents"`
		Metadatas [][]map[string]any `json:"metadatas"`
		Distances [][]float32        `json:"distances"`
	}
	if err := c.do(ctx, http.MethodPost, c.base()+"/collections/"+c.collectionID+"/query", body, &out); err != nil {
		return nil, err
	}
	if len(out.IDs) == 0 {
		return nil, nil
	}
	hits := make([]Hit, 0, len(out.IDs[0]))
	for i, id := range out.IDs[0] {
		meta := map[string]string{}
		for k, v := range out.Metadatas[0][i] {
			if s, ok := v.(string); ok && k != "_" {
				meta[k] = s
			}
		}
		hits = append(hits, Hit{Document: Document{ID: id, Text: out.Documents[0][i], Metadata: meta}, Score: 1 - out.Distances[0][i]})
	}
	return hits, nil
}

// All implements Store.
func (c *Chroma) All(ctx context.Context) ([]Document, error) {
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	var out struct {
		IDs       []string         `json:"ids"`
		Documents []string         `json:"documents"`
		Metadatas []map[string]any `json:"metadatas"`
	}
	if err := c.do(ctx, http.MethodPost, c.base()+"/collections/"+c.collectionID+"/get", map[string]any{"include": []string{"documents", "metadatas"}}, &out); err != nil {
		return nil, err
	}
	docs := make([]Document, 0, len(out.IDs))
	for i, id := range out.IDs {
		meta := map[string]string{}
		for k, v := range out.Metadatas[i] {
			if s, ok := v.(string); ok && k != "_" {
				meta[k] = s
			}
		}
		docs = append(docs, Document{ID: id, Text: out.Documents[i], Metadata: meta})
	}
	return docs, nil
}
