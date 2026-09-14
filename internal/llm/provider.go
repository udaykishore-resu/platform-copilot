package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Options configures provider construction. Everything has a sane default so
// that `copilot` runs from a clean clone with no keys (mock provider).
type Options struct {
	Provider string // mock | openai | anthropic | gemini | ollama
	Model    string
	APIKey   string
	BaseURL  string
	HTTP     *http.Client
	Timeout  time.Duration
}

// New builds a Provider from Options. Unknown providers are an error so a typo
// in COPILOT_PROVIDER never silently falls back to the mock.
func New(o Options) (Provider, error) {
	if o.HTTP == nil {
		t := o.Timeout
		if t == 0 {
			t = 120 * time.Second
		}
		o.HTTP = &http.Client{Timeout: t}
	}
	switch strings.ToLower(strings.TrimSpace(o.Provider)) {
	case "", "mock":
		return NewMock(o.Model), nil
	case "openai":
		return NewOpenAI(o), nil
	case "anthropic", "claude":
		return NewAnthropic(o), nil
	case "gemini", "google":
		return NewGemini(o), nil
	case "ollama":
		return NewOllama(o), nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q (want mock|openai|anthropic|gemini|ollama)", o.Provider)
	}
}

// envOr returns the env var or a default.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// postJSON is the one HTTP helper every provider shares. We deliberately use
// net/http instead of vendor SDKs (see ADR-0009): the wire formats are small,
// stable, and owning them keeps go.mod dependency-free.
func postJSON(ctx context.Context, c *http.Client, url string, headers map[string]string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("llm: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("llm: %s: %w", url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return &HTTPError{Status: resp.StatusCode, Body: truncate(string(data), 2000), URL: url}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("llm: decode response from %s: %w", url, err)
	}
	return nil
}

// HTTPError is returned for non-2xx provider responses. Status 429 and 5xx are
// retryable; 400/401/403 are not.
type HTTPError struct {
	Status int
	Body   string
	URL    string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("llm: %s returned HTTP %d: %s", e.URL, e.Status, e.Body)
}

// Retryable reports whether the error is worth retrying with backoff.
func (e *HTTPError) Retryable() bool { return e.Status == 429 || e.Status >= 500 }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// WithRetry wraps a Provider with exponential backoff on retryable errors.
// Rate limits (429) are the most common production failure for LLM apps.
func WithRetry(p Provider, attempts int, base time.Duration) Provider {
	if attempts < 1 {
		attempts = 1
	}
	return &retrying{Provider: p, attempts: attempts, base: base}
}

type retrying struct {
	Provider
	attempts int
	base     time.Duration
}

func (r *retrying) Complete(ctx context.Context, req Request) (*Response, error) {
	var last error
	delay := r.base
	for i := 0; i < r.attempts; i++ {
		resp, err := r.Provider.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}
		last = err
		he, ok := err.(*HTTPError)
		if !ok || !he.Retryable() {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
	return nil, last
}
