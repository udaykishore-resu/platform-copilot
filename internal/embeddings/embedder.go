// Package embeddings turns text into vectors and measures how close vectors
// are. Everything downstream — semantic search, classification,
// recommendation, anomaly detection, RAG — is built on these two operations.
//
// Roadmap: Embeddings · Open-Source Embeddings · Use Cases for Embeddings.
package embeddings

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
)

// Embedder produces one vector per input text. Implementations must return
// vectors of a fixed Dimensions() and should L2-normalise them so cosine and
// dot product agree.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dimensions() int
	Model() string
	Name() string
}

// Options configures construction.
type Options struct {
	Provider string // mock | openai | ollama | cohere
	Model    string
	APIKey   string
	BaseURL  string
	HTTP     *http.Client
}

// New builds an Embedder from Options.
func New(o Options) (Embedder, error) {
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	switch strings.ToLower(strings.TrimSpace(o.Provider)) {
	case "", "mock":
		return NewMock(256), nil
	case "openai":
		return NewOpenAI(o), nil
	case "ollama":
		return NewOllama(o), nil
	case "cohere":
		return NewCohere(o), nil
	default:
		return nil, fmt.Errorf("embeddings: unknown provider %q (want mock|openai|ollama|cohere)", o.Provider)
	}
}

// Cosine similarity in [-1, 1]. For normalised vectors this equals Dot.
func Cosine(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// Dot product. Cheaper than Cosine when vectors are already normalised.
func Dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// Euclidean (L2) distance. Lower is closer; used by anomaly detection.
func Euclidean(a, b []float32) float32 {
	var s float64
	for i := range a {
		d := float64(a[i] - b[i])
		s += d * d
	}
	return float32(math.Sqrt(s))
}

// Normalize scales v to unit length in place and returns it.
func Normalize(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(n))
	for i := range v {
		v[i] *= inv
	}
	return v
}

// Centroid returns the mean vector of a set (used for anomaly detection and
// simple nearest-centroid classification).
func Centroid(vs [][]float32) []float32 {
	if len(vs) == 0 {
		return nil
	}
	c := make([]float32, len(vs[0]))
	for _, v := range vs {
		for i := range v {
			c[i] += v[i]
		}
	}
	for i := range c {
		c[i] /= float32(len(vs))
	}
	return c
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
