// Package config reads the copilot's configuration from the environment.
// Defaults are chosen so that a clean clone runs with zero keys.
package config

import (
	"os"
	"strconv"
	"strings"
)

// Config is the resolved configuration.
type Config struct {
	Provider      string
	Model         string
	EmbedProvider string
	EmbedModel    string
	VectorStore   string
	IndexPath     string
	Hybrid        bool
	TopK          int
	MinScore      float64
	KnowledgeDir  string
	FakeKubectl   bool
	FakeProm      bool
	PrometheusURL string
	KubeContext   string
	Namespaces    []string
	ListenAddr    string
	User          string
}

// Load resolves configuration from environment variables.
func Load() Config {
	c := Config{
		Provider:      get("COPILOT_PROVIDER", "mock"),
		Model:         os.Getenv("COPILOT_MODEL"),
		EmbedProvider: get("COPILOT_EMBED_PROVIDER", "mock"),
		EmbedModel:    os.Getenv("COPILOT_EMBED_MODEL"),
		VectorStore:   get("COPILOT_VECTORSTORE", "memory"),
		IndexPath:     get("COPILOT_INDEX_PATH", ".copilot/index.json"),
		Hybrid:        getBool("COPILOT_HYBRID", true),
		TopK:          getInt("COPILOT_TOP_K", 5),
		MinScore:      getFloat("COPILOT_MIN_SCORE", 0),
		KnowledgeDir:  get("COPILOT_KNOWLEDGE_DIR", "data/knowledge"),
		PrometheusURL: os.Getenv("PROMETHEUS_URL"),
		KubeContext:   os.Getenv("COPILOT_KUBE_CONTEXT"),
		ListenAddr:    get("COPILOT_LISTEN", ":8080"),
		User:          get("COPILOT_USER", "cli:"+userName()),
	}
	// With the mock provider there is no real model to drive real tools, so
	// the demo cluster is on unless explicitly disabled.
	c.FakeKubectl = getBool("COPILOT_FAKE_KUBECTL", c.Provider == "mock")
	c.FakeProm = getBool("COPILOT_FAKE_PROMETHEUS", c.Provider == "mock" || c.PrometheusURL == "")
	if ns := os.Getenv("COPILOT_NAMESPACES"); ns != "" {
		for _, n := range strings.Split(ns, ",") {
			if n = strings.TrimSpace(n); n != "" {
				c.Namespaces = append(c.Namespaces, n)
			}
		}
	}
	return c
}

func get(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func getBool(k string, d bool) bool {
	v := strings.ToLower(os.Getenv(k))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return d
}

func getInt(k string, d int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return d
}

func getFloat(k string, d float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
		return v
	}
	return d
}

func userName() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "anonymous"
}
