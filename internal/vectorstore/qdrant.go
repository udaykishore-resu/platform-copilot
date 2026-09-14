package vectorstore

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Qdrant adapts the Qdrant REST API. Qdrant is the production choice in
// ADR-0004: Rust, HNSW with payload-aware filtering (filters are applied
// during graph traversal, not after, so filtered recall does not collapse),
// built-in sparse vectors for hybrid search, quantisation, and a first-class
// Go client (we use REST here to stay dependency-free).
type Qdrant struct {
	url, collection, apiKey string
	dims                    int
	http                    *http.Client
}

// NewQdrant constructs the adapter and ensures the collection exists.
func NewQdrant(o Options) (*Qdrant, error) {
	q := &Qdrant{
		url:        strings.TrimRight(firstNonEmpty(o.URL, envOr("QDRANT_URL", "http://localhost:6333")), "/"),
		collection: o.Collection, apiKey: firstNonEmpty(o.APIKey, envOr("QDRANT_API_KEY", "")),
		dims: o.Dims, http: o.HTTP,
	}
	if q.dims > 0 {
		if err := q.ensureCollection(context.Background()); err != nil {
			return nil, err
		}
	}
	return q, nil
}

func (q *Qdrant) Name() string { return "qdrant" }

func (q *Qdrant) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, q.url+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if q.apiKey != "" {
		req.Header.Set("api-key", q.apiKey)
	}
	resp, err := q.http.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant: %w (is Qdrant running at %s? try `docker run -p 6333:6333 qdrant/qdrant`)", err, q.url)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("qdrant: %s %s: HTTP %d: %s", method, path, resp.StatusCode, string(data))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (q *Qdrant) ensureCollection(ctx context.Context) error {
	err := q.do(ctx, http.MethodGet, "/collections/"+q.collection, nil, nil)
	if err == nil {
		return nil
	}
	return q.do(ctx, http.MethodPut, "/collections/"+q.collection, map[string]any{
		"vectors": map[string]any{"size": q.dims, "distance": "Cosine"},
		// HNSW parameters: m = graph degree, ef_construct = build-time beam.
		// Higher = better recall, slower build, more RAM.
		"hnsw_config": map[string]any{"m": 16, "ef_construct": 128},
	}, nil)
}

// pointID converts our string IDs to the UUID-ish form Qdrant requires.
func pointID(id string) string {
	sum := sha1.Sum([]byte(id))
	h := hex.EncodeToString(sum[:16])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// Upsert implements Store.
func (q *Qdrant) Upsert(ctx context.Context, docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	if q.dims == 0 {
		q.dims = len(docs[0].Vector)
		if err := q.ensureCollection(ctx); err != nil {
			return err
		}
	}
	points := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		payload := map[string]any{"id": d.ID, "text": d.Text}
		for k, v := range d.Metadata {
			payload[k] = v
		}
		points = append(points, map[string]any{"id": pointID(d.ID), "vector": d.Vector, "payload": payload})
	}
	return q.do(ctx, http.MethodPut, "/collections/"+q.collection+"/points?wait=true", map[string]any{"points": points}, nil)
}

// Delete implements Store.
func (q *Qdrant) Delete(ctx context.Context, ids []string) error {
	pids := make([]string, len(ids))
	for i, id := range ids {
		pids[i] = pointID(id)
	}
	return q.do(ctx, http.MethodPost, "/collections/"+q.collection+"/points/delete?wait=true", map[string]any{"points": pids}, nil)
}

// Count implements Store.
func (q *Qdrant) Count(ctx context.Context) (int, error) {
	var out struct {
		Result struct {
			Count int `json:"count"`
		} `json:"result"`
	}
	err := q.do(ctx, http.MethodPost, "/collections/"+q.collection+"/points/count", map[string]any{"exact": true}, &out)
	return out.Result.Count, err
}

// Search implements Store with server-side payload filtering.
func (q *Qdrant) Search(ctx context.Context, vector []float32, k int, filter map[string]string) ([]Hit, error) {
	body := map[string]any{"vector": vector, "limit": k, "with_payload": true}
	if len(filter) > 0 {
		must := []map[string]any{}
		for key, val := range filter {
			must = append(must, map[string]any{"key": key, "match": map[string]any{"value": val}})
		}
		body["filter"] = map[string]any{"must": must}
	}
	var out struct {
		Result []struct {
			Score   float32        `json:"score"`
			Payload map[string]any `json:"payload"`
		} `json:"result"`
	}
	if err := q.do(ctx, http.MethodPost, "/collections/"+q.collection+"/points/search", body, &out); err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(out.Result))
	for _, r := range out.Result {
		hits = append(hits, Hit{Document: fromPayload(r.Payload), Score: r.Score})
	}
	return hits, nil
}

// All scrolls the whole collection (fine for BM25 over ≤ ~100k chunks).
func (q *Qdrant) All(ctx context.Context) ([]Document, error) {
	var docs []Document
	var offset any
	for {
		body := map[string]any{"limit": 1000, "with_payload": true, "with_vector": false}
		if offset != nil {
			body["offset"] = offset
		}
		var out struct {
			Result struct {
				Points []struct {
					Payload map[string]any `json:"payload"`
				} `json:"points"`
				NextPageOffset any `json:"next_page_offset"`
			} `json:"result"`
		}
		if err := q.do(ctx, http.MethodPost, "/collections/"+q.collection+"/points/scroll", body, &out); err != nil {
			return nil, err
		}
		for _, p := range out.Result.Points {
			docs = append(docs, fromPayload(p.Payload))
		}
		if out.Result.NextPageOffset == nil {
			return docs, nil
		}
		offset = out.Result.NextPageOffset
	}
}

func fromPayload(p map[string]any) Document {
	d := Document{Metadata: map[string]string{}}
	for k, v := range p {
		s, _ := v.(string)
		switch k {
		case "id":
			d.ID = s
		case "text":
			d.Text = s
		default:
			d.Metadata[k] = s
		}
	}
	return d
}
