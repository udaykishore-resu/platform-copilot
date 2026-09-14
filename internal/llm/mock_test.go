package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMockExtractiveCitesAndAbstains(t *testing.T) {
	m := NewMock("")
	sys := "rules...\n\n# Context\n\n[1] source: runbook.md\nRunbook › Remediation\nRaise the memory limit to 1.5Gi so the pods stop dying.\nRoll back the ConfigMap to v41.\n\n[2] source: policy.md\nSecondary on-call is paged after 10 minutes.\n"
	resp, err := m.Complete(context.Background(), Request{Messages: []Message{System(sys), User("What is the remediation for the memory limit?")}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Content, "1.5Gi") || !strings.Contains(resp.Content, "[1]") {
		t.Fatalf("expected cited remediation, got %q", resp.Content)
	}
	resp, _ = m.Complete(context.Background(), Request{Messages: []Message{System(sys), User("What is the mobile app release cadence?")}})
	if !strings.Contains(resp.Content, "could not find") {
		t.Fatalf("expected abstention, got %q", resp.Content)
	}
}

func TestMockStructuredOutput(t *testing.T) {
	m := NewMock("")
	schema := json.RawMessage(`{"type":"object","properties":{"severity":{"type":"string","enum":["ok","warning"]},"count":{"type":"integer"},"panels":{"type":"array"}}}`)
	resp, _ := m.Complete(context.Background(), Request{Messages: []Message{User("x")}, JSONSchema: schema})
	var out map[string]any
	if err := json.Unmarshal([]byte(resp.Content), &out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, resp.Content)
	}
	if out["severity"] != "ok" {
		t.Fatalf("enum default not used: %v", out)
	}
}

func TestRetryOnlyRetriesRetryable(t *testing.T) {
	calls := 0
	p := WithRetry(providerFunc(func() (*Response, error) {
		calls++
		if calls < 3 {
			return nil, &HTTPError{Status: 429}
		}
		return &Response{Content: "ok"}, nil
	}), 5, 0)
	if r, err := p.Complete(context.Background(), Request{}); err != nil || r.Content != "ok" || calls != 3 {
		t.Fatalf("retry: %v %v calls=%d", r, err, calls)
	}
	calls = 0
	p = WithRetry(providerFunc(func() (*Response, error) { calls++; return nil, &HTTPError{Status: 401} }), 5, 0)
	if _, err := p.Complete(context.Background(), Request{}); err == nil || calls != 1 {
		t.Fatalf("401 must not be retried (calls=%d)", calls)
	}
}

type providerFunc func() (*Response, error)

func (f providerFunc) Name() string                                         { return "fn" }
func (f providerFunc) DefaultModel() string                                 { return "fn" }
func (f providerFunc) ContextWindow(string) int                             { return 1000 }
func (f providerFunc) Complete(context.Context, Request) (*Response, error) { return f() }
