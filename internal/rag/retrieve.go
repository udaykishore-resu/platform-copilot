package rag

import (
	"context"
	"fmt"

	"github.com/udaykishore-resu/platform-copilot/internal/embeddings"
	"github.com/udaykishore-resu/platform-copilot/internal/vectorstore"
)

// Retriever finds the chunks most relevant to a query. With Hybrid on it runs
// dense (vector) and sparse (BM25) search and fuses them with RRF; the
// MinScore threshold on the dense side stops "nearest" from meaning
// "relevant" when the index has nothing on the topic.
type Retriever struct {
	Embedder embeddings.Embedder
	Store    vectorstore.Store
	Hybrid   bool
	TopK     int
	// MinScore drops dense hits below this cosine similarity (0 disables).
	// Typical: 0.25–0.35 for OpenAI embeddings, lower for the hashing mock.
	MinScore float32
	// Filter restricts by metadata, e.g. {"source_type": "runbook"}.
	Filter map[string]string

	bm25 *vectorstore.BM25 // lazily built keyword index
}

// Retrieve returns ranked hits. When Hybrid is on, each hit's Metadata also
// carries score_vector / score_bm25 so `copilot search --explain` can show
// why it ranked where it did.
func (r *Retriever) Retrieve(ctx context.Context, query string) ([]vectorstore.Hit, error) {
	k := r.TopK
	if k <= 0 {
		k = 5
	}
	vecs, err := r.Embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	// over-fetch for fusion so the two lists have enough overlap to matter
	fetch := k
	if r.Hybrid {
		fetch = k * 3
	}
	dense, err := r.Store.Search(ctx, vecs[0], fetch, r.Filter)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	if r.MinScore > 0 {
		kept := dense[:0]
		for _, h := range dense {
			if h.Score >= r.MinScore {
				kept = append(kept, h)
			}
		}
		dense = kept
	}
	if !r.Hybrid {
		return trim(dense, k), nil
	}
	if r.bm25 == nil {
		docs, err := r.Store.All(ctx)
		if err != nil {
			// remote store too large to scan: degrade to dense-only
			return trim(dense, k), nil
		}
		r.bm25 = vectorstore.NewBM25(docs)
	}
	sparse := r.bm25.Search(query, fetch, r.Filter)
	return trim(vectorstore.RRF(60, dense, sparse), k), nil
}

func trim(h []vectorstore.Hit, k int) []vectorstore.Hit {
	if len(h) > k {
		return h[:k]
	}
	return h
}
