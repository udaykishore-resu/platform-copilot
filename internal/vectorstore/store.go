// Package vectorstore stores embedded chunks and finds the nearest ones.
//
// The in-memory store is exact (brute-force cosine) and persists to JSON —
// right for thousands of chunks, which is where most internal knowledge bases
// live. Qdrant and Chroma adapters cover the million-chunk regime with HNSW
// indexes and server-side metadata filtering. BM25 + reciprocal-rank fusion
// gives hybrid search on any backend.
//
// Roadmap: Vector Databases · Implementing Vector Search · Indexing Embeddings
// · Performing Similarity Search.
package vectorstore

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Document is one embedded chunk.
type Document struct {
	ID       string            `json:"id"`
	Text     string            `json:"text"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Vector   []float32         `json:"vector"`
}

// Hit is a search result; Score is cosine similarity (higher is better).
type Hit struct {
	Document
	Score float32 `json:"score"`
}

// Store is the minimal interface RAG needs. Filter is exact-match on
// metadata keys (e.g. {"source_type": "runbook"}).
type Store interface {
	Name() string
	Upsert(ctx context.Context, docs []Document) error
	Search(ctx context.Context, vector []float32, k int, filter map[string]string) ([]Hit, error)
	Count(ctx context.Context) (int, error)
	// All returns every document (used by BM25 to build a keyword index).
	// Remote stores may return an error if the collection is too large.
	All(ctx context.Context) ([]Document, error)
	Delete(ctx context.Context, ids []string) error
}

// Options configures construction.
type Options struct {
	Kind       string // memory | qdrant | chroma
	Path       string // memory: JSON persistence path
	URL        string // qdrant/chroma base URL
	Collection string
	Dims       int
	APIKey     string
	HTTP       *http.Client
}

// New builds a Store.
func New(o Options) (Store, error) {
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if o.Collection == "" {
		o.Collection = "copilot"
	}
	switch strings.ToLower(strings.TrimSpace(o.Kind)) {
	case "", "memory":
		return NewMemory(firstNonEmpty(o.Path, envOr("COPILOT_INDEX_PATH", ".copilot/index.json")))
	case "qdrant":
		return NewQdrant(o)
	case "chroma":
		return NewChroma(o)
	default:
		return nil, fmt.Errorf("vectorstore: unknown kind %q (want memory|qdrant|chroma)", o.Kind)
	}
}

func matches(meta, filter map[string]string) bool {
	for k, v := range filter {
		if meta[k] != v {
			return false
		}
	}
	return true
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
