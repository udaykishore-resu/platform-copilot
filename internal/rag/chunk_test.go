package rag

import (
	"strings"
	"testing"

	"github.com/udaykishore-resu/platform-copilot/internal/tokens"
)

const doc = `# Runbook: payments-api

## Symptoms
Pods restart repeatedly. Alert fires after 3 restarts in 10 minutes.

## Remediation
1. Raise the memory limit to 1.5Gi.
2. Roll back the ConfigMap to v41.

### Verification
Watch the pods for five minutes.
`

func TestMarkdownChunksCarryHeadingPath(t *testing.T) {
	c := Chunker{MaxTokens: 60, Overlap: 10}
	chunks := c.Markdown(doc, "runbook.md")
	if len(chunks) < 3 {
		t.Fatalf("expected ≥3 chunks, got %d", len(chunks))
	}
	found := false
	for _, ch := range chunks {
		if strings.HasPrefix(ch.Text, "Runbook: payments-api › Remediation\n") {
			found = true
			if !strings.Contains(ch.Text, "1.5Gi") {
				t.Errorf("remediation chunk lost its body: %q", ch.Text)
			}
		}
		if tokens.Estimate(ch.Text) > c.MaxTokens*2 {
			t.Errorf("chunk far over budget: %d tokens", tokens.Estimate(ch.Text))
		}
	}
	if !found {
		t.Errorf("no chunk carries the Remediation heading path: %+v", chunks)
	}
}

func TestRecursiveRespectsBudgetAndOverlap(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 80; i++ {
		sb.WriteString("Sentence number ")
		sb.WriteString(strings.Repeat("x", i%7+1))
		sb.WriteString(" about pods and nodes. ")
		if i%10 == 9 {
			sb.WriteString("\n\n")
		}
	}
	c := Chunker{MaxTokens: 80, Overlap: 16}
	chunks := c.Recursive(sb.String(), "x")
	if len(chunks) < 4 {
		t.Fatalf("expected several chunks, got %d", len(chunks))
	}
	for i, ch := range chunks {
		if n := tokens.Estimate(ch.Text); n > 120 {
			t.Errorf("chunk %d is %d tokens (> budget+slack)", i, n)
		}
	}
	// overlap: the start of chunk i+1 should appear in chunk i
	for i := 0; i+1 < len(chunks); i++ {
		head := firstWords(chunks[i+1].Text, 3)
		if !strings.Contains(chunks[i].Text, head) {
			t.Errorf("chunk %d does not overlap with %d (head %q)", i, i+1, head)
		}
	}
}

func TestFixedCoversEverything(t *testing.T) {
	c := Chunker{MaxTokens: 30, Overlap: 5}
	text := strings.Repeat("alpha beta gamma delta ", 40)
	chunks := c.Fixed(text, "t")
	joined := ""
	for _, ch := range chunks {
		joined += ch.Text + " "
	}
	if !strings.Contains(joined, "alpha beta gamma delta") || len(chunks) < 3 {
		t.Fatalf("fixed chunking lost content or under-split: %d chunks", len(chunks))
	}
}

func firstWords(s string, n int) string {
	w := strings.Fields(s)
	if len(w) > n {
		w = w[:n]
	}
	return strings.Join(w, " ")
}
