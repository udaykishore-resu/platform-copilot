package tokens

import "strings"

// Budget decides how much of a prompt fits. Every request must satisfy
//
//	system + context + history + question + reserve(answer) ≤ ContextWindow
//
// and when it does not, something has to be dropped in a deliberate order:
// oldest history first, then lowest-ranked retrieved context, never the
// system prompt or the user's question.
type Budget struct {
	ContextWindow int // model's total window
	ReserveOutput int // tokens kept free for the completion (MaxTokens)
}

// Available returns how many prompt tokens remain after the fixed parts.
func (b Budget) Available(fixed ...string) int {
	used := 0
	for _, f := range fixed {
		used += Estimate(f) + 4
	}
	return b.ContextWindow - b.ReserveOutput - used - 16 // framing slack
}

// FitContext keeps retrieved chunks in rank order until the budget is spent.
// Returns the kept chunks and how many were dropped.
func (b Budget) FitContext(chunks []string, available int) (kept []string, dropped int) {
	used := 0
	for i, c := range chunks {
		t := Estimate(c) + 2
		if used+t > available {
			return kept, len(chunks) - i
		}
		used += t
		kept = append(kept, c)
	}
	return kept, 0
}

// TrimHistory drops the oldest conversation turns until they fit. It always
// drops turns in pairs (user+assistant) so the transcript stays coherent.
func (b Budget) TrimHistory(turns []string, available int) []string {
	for len(turns) > 0 && EstimateMessages(turns) > available {
		if len(turns) >= 2 {
			turns = turns[2:]
		} else {
			turns = turns[1:]
		}
	}
	return turns
}

// Truncate cuts s to at most maxTokens (approximately), on a word boundary,
// and appends a marker so the model knows content was cut.
func Truncate(s string, maxTokens int) string {
	if Estimate(s) <= maxTokens {
		return s
	}
	words := strings.Fields(s)
	lo, hi := 0, len(words)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if Estimate(strings.Join(words[:mid], " ")) <= maxTokens-3 {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return strings.Join(words[:lo], " ") + " […truncated]"
}
