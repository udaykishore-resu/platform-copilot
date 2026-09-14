package vectorstore

import (
	"context"
	"path/filepath"
	"testing"
)

func vec(xs ...float32) []float32 { return xs }

func TestMemorySearchPersistAndFilter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idx.json")
	m, err := NewMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	docs := []Document{
		{ID: "a", Text: "payments oom", Vector: vec(1, 0, 0), Metadata: map[string]string{"source_type": "runbook"}},
		{ID: "b", Text: "node notready", Vector: vec(0.9, 0.1, 0), Metadata: map[string]string{"source_type": "runbook"}},
		{ID: "c", Text: "redis postmortem", Vector: vec(0, 1, 0), Metadata: map[string]string{"source_type": "postmortem"}},
	}
	if err := m.Upsert(ctx, docs); err != nil {
		t.Fatal(err)
	}
	hits, _ := m.Search(ctx, vec(1, 0, 0), 2, nil)
	if len(hits) != 2 || hits[0].ID != "a" || hits[1].ID != "b" {
		t.Fatalf("bad ranking: %+v", hits)
	}
	hits, _ = m.Search(ctx, vec(1, 0, 0), 5, map[string]string{"source_type": "postmortem"})
	if len(hits) != 1 || hits[0].ID != "c" {
		t.Fatalf("filter failed: %+v", hits)
	}
	// reload from disk
	m2, err := NewMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := m2.Count(ctx); n != 3 {
		t.Fatalf("persistence lost docs: %d", n)
	}
	if _, err := m2.Search(ctx, vec(1, 0), 1, nil); err == nil {
		t.Fatal("dimension mismatch must error")
	}
}

func TestBM25PrefersExactIdentifiers(t *testing.T) {
	docs := []Document{
		{ID: "1", Text: "The HPA is capped at 14 replicas until PLAT-1901 raises the pgbouncer ceiling."},
		{ID: "2", Text: "Pods restart when the memory limit is too low; raise it to 1.5Gi."},
		{ID: "3", Text: "Node group payments-general uses m6i.2xlarge instances."},
	}
	idx := NewBM25(docs)
	hits := idx.Search("what is PLAT-1901", 3, nil)
	if len(hits) == 0 || hits[0].ID != "1" {
		t.Fatalf("expected doc 1 first, got %+v", hits)
	}
	hits = idx.Search("m6i.2xlarge", 3, nil)
	if len(hits) != 1 || hits[0].ID != "3" {
		t.Fatalf("identifier tokenisation broke: %+v", hits)
	}
}

func TestRRFFusesLists(t *testing.T) {
	a := []Hit{{Document: Document{ID: "x"}, Score: 0.9}, {Document: Document{ID: "y"}, Score: 0.8}}
	b := []Hit{{Document: Document{ID: "y"}, Score: 7}, {Document: Document{ID: "z"}, Score: 6}}
	fused := RRF(60, a, b)
	if fused[0].ID != "y" {
		t.Fatalf("y appears in both lists and must win: %+v", fused)
	}
	if fused[0].Metadata["score_vector"] == "" || fused[0].Metadata["score_bm25"] == "" {
		t.Fatalf("per-list scores missing: %+v", fused[0].Metadata)
	}
	if len(fused) != 3 {
		t.Fatalf("expected 3 unique, got %d", len(fused))
	}
}
