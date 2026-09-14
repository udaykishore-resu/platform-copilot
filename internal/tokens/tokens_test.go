package tokens

import (
	"strings"
	"testing"
)

func TestEstimateIsInRange(t *testing.T) {
	prose := "The quick brown fox jumps over the lazy dog while the on-call engineer restarts the payments service."
	n := Estimate(prose)
	if n < 18 || n > 30 { // tiktoken cl100k: ~21
		t.Fatalf("prose estimate %d out of plausible range", n)
	}
	yaml := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: payments-api\n  namespace: payments\n"
	if n := Estimate(yaml); n < 18 || n > 45 { // tiktoken: ~28
		t.Fatalf("yaml estimate %d out of range", n)
	}
	if Estimate("") != 0 {
		t.Fatal("empty must be 0")
	}
}

func TestBudgetFitAndTrim(t *testing.T) {
	b := Budget{ContextWindow: 300, ReserveOutput: 100}
	avail := b.Available("system prompt here", "the question")
	chunks := []string{strings.Repeat("a ", 60), strings.Repeat("b ", 60), strings.Repeat("c ", 600)}
	kept, dropped := b.FitContext(chunks, avail)
	if len(kept) != 2 || dropped != 1 {
		t.Fatalf("kept=%d dropped=%d", len(kept), dropped)
	}
	turns := []string{"u1", "a1", "u2", "a2", strings.Repeat("u3 ", 100), "a3"}
	trimmed := b.TrimHistory(turns, 80)
	if len(trimmed) >= len(turns) || len(trimmed)%2 != 0 {
		t.Fatalf("history trim wrong: %d turns", len(trimmed))
	}
	if s := Truncate(strings.Repeat("word ", 500), 50); Estimate(s) > 55 || !strings.HasSuffix(s, "[…truncated]") {
		t.Fatalf("truncate failed: %d tokens", Estimate(s))
	}
}

func TestPricing(t *testing.T) {
	if _, ok := Lookup("gpt-4o-mini-2024-07-18"); !ok {
		t.Fatal("prefix lookup failed")
	}
	p, _ := Lookup("gpt-4o-mini")
	pf, _ := Lookup("gpt-4o")
	if p.Input >= pf.Input {
		t.Fatal("mini must be cheaper than full and longest prefix must win")
	}
	usd, ok := Cost("gpt-4o-mini", 1_000_000, 0)
	if !ok || usd != p.Input {
		t.Fatalf("cost math: %v %v", usd, ok)
	}
	if FormatCost("llama3.2", 1000, 1000) != "$0 (local/mock)" {
		t.Fatal("local models are free at the margin")
	}
}
