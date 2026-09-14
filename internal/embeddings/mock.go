package embeddings

import (
	"context"
	"hash/fnv"
	"regexp"
	"strings"
)

// Mock is an offline embedder built on the "hashing trick": every token (and
// every character trigram, for robustness to plurals and typos) is hashed into
// one of D buckets; the vector is the normalised bucket-count histogram.
//
// This is not a semantic embedding — "pod restarting" and "container
// crashlooping" will not be close — but texts that share vocabulary are close,
// which is enough for the labs to demonstrate retrieval, ranking, thresholds,
// hybrid fusion and citations deterministically and for free. Swap in OpenAI
// or Ollama (nomic-embed-text, bge-m3) to see the semantic gap close.
type Mock struct{ dims int }

// NewMock creates a hashing embedder with the given dimensionality.
func NewMock(dims int) *Mock {
	if dims <= 0 {
		dims = 256
	}
	return &Mock{dims: dims}
}

func (m *Mock) Dimensions() int { return m.dims }
func (m *Mock) Model() string   { return "hashing-bow-trigram" }
func (m *Mock) Name() string    { return "mock" }

var reTok = regexp.MustCompile(`[a-z0-9][a-z0-9_\-./]*`)

// Embed implements Embedder.
func (m *Mock) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, m.dims)
		for _, tok := range reTok.FindAllString(strings.ToLower(t), -1) {
			if len(tok) < 2 {
				continue
			}
			v[bucket(tok, m.dims)] += 2 // whole-token feature weighs more
			if len(tok) >= 5 {
				for j := 0; j+3 <= len(tok); j++ {
					v[bucket("#"+tok[j:j+3], m.dims)] += 1
				}
			}
		}
		out[i] = Normalize(v)
	}
	return out, nil
}

func bucket(s string, dims int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int(h.Sum32() % uint32(dims))
}
