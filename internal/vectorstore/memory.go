package vectorstore

import (
	"container/heap"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/udaykishore-resu/platform-copilot/internal/embeddings"
)

// Memory is an exact-search store: every query is compared against every
// vector (O(N·D)). With N = 10k chunks and D = 1536 that is ~15M multiply-adds,
// well under a millisecond of CPU — an ANN index (HNSW/IVF) only pays off
// when N reaches the hundreds of thousands, and it costs you recall.
// The store persists to a JSON file so `copilot ask` can run after `ingest`
// in a separate process.
type Memory struct {
	mu   sync.RWMutex
	path string
	docs map[string]Document
}

// NewMemory loads the index at path if it exists.
func NewMemory(path string) (*Memory, error) {
	m := &Memory{path: path, docs: map[string]Document{}}
	if path == "" {
		return m, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	var docs []Document
	if err := json.Unmarshal(data, &docs); err != nil {
		return nil, fmt.Errorf("vectorstore: corrupt index %s: %w", path, err)
	}
	for _, d := range docs {
		m.docs[d.ID] = d
	}
	return m, nil
}

func (m *Memory) Name() string { return "memory" }

// Upsert inserts or replaces by ID and persists.
func (m *Memory) Upsert(_ context.Context, docs []Document) error {
	m.mu.Lock()
	for _, d := range docs {
		m.docs[d.ID] = d
	}
	m.mu.Unlock()
	return m.save()
}

// Delete removes by ID and persists.
func (m *Memory) Delete(_ context.Context, ids []string) error {
	m.mu.Lock()
	for _, id := range ids {
		delete(m.docs, id)
	}
	m.mu.Unlock()
	return m.save()
}

// Count implements Store.
func (m *Memory) Count(context.Context) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.docs), nil
}

// All implements Store.
func (m *Memory) All(context.Context) ([]Document, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Document, 0, len(m.docs))
	for _, d := range m.docs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Search is brute-force cosine with a bounded min-heap so memory stays O(k).
func (m *Memory) Search(_ context.Context, vector []float32, k int, filter map[string]string) ([]Hit, error) {
	if k <= 0 {
		k = 5
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	h := &hitHeap{}
	for _, d := range m.docs {
		if !matches(d.Metadata, filter) {
			continue
		}
		if len(d.Vector) != len(vector) {
			return nil, fmt.Errorf("vectorstore: dimension mismatch (index %d, query %d) — re-run ingest after changing the embedding model", len(d.Vector), len(vector))
		}
		s := embeddings.Cosine(vector, d.Vector)
		if h.Len() < k {
			heap.Push(h, Hit{Document: d, Score: s})
		} else if s > (*h)[0].Score {
			(*h)[0] = Hit{Document: d, Score: s}
			heap.Fix(h, 0)
		}
	}
	out := make([]Hit, h.Len())
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = heap.Pop(h).(Hit)
	}
	return out, nil
}

func (m *Memory) save() error {
	if m.path == "" {
		return nil
	}
	docs, _ := m.All(context.Background())
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(docs)
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

type hitHeap []Hit

func (h hitHeap) Len() int           { return len(h) }
func (h hitHeap) Less(i, j int) bool { return h[i].Score < h[j].Score }
func (h hitHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *hitHeap) Push(x any)        { *h = append(*h, x.(Hit)) }
func (h *hitHeap) Pop() any          { o := *h; x := o[len(o)-1]; *h = o[:len(o)-1]; return x }
