// Package eval measures the RAG pipeline. "It seems to work" is not a
// metric; retrieval recall and answer faithfulness against a golden set are.
// This is the harness `make eval` runs in CI so a chunking or prompt change
// that regresses retrieval fails the build.
package eval

import (
	"strings"

	"github.com/udaykishore-resu/platform-copilot/internal/vectorstore"
)

// Case is one golden question.
type Case struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	// ExpectSource is a substring of the source path that must appear in the
	// top-k retrieved chunks (e.g. "runbook-payments-api").
	ExpectSource string `json:"expect_source"`
	// ExpectAnswer lists substrings at least one of which must appear in the
	// generated answer (case-insensitive).
	ExpectAnswer []string `json:"expect_answer"`
	// Unanswerable marks questions the corpus cannot answer; the correct
	// behaviour is to say so, not to hallucinate.
	Unanswerable bool `json:"unanswerable,omitempty"`
}

// Outcome is the result for one case.
type Outcome struct {
	Case
	RetrievedRank int     `json:"retrieved_rank"` // 1-based rank of expected source, 0 if missed
	AnswerOK      bool    `json:"answer_ok"`
	Answer        string  `json:"answer"`
	Latency       float64 `json:"latency_ms"`
}

// Summary aggregates outcomes.
type Summary struct {
	Cases      int     `json:"cases"`
	HitAtK     float64 `json:"hit_at_k"` // fraction with expected source in top-k
	MRR        float64 `json:"mrr"`      // mean reciprocal rank of expected source
	AnswerAcc  float64 `json:"answer_accuracy"`
	AbstainAcc float64 `json:"abstain_accuracy"` // unanswerable cases correctly abstained
	MeanLatMs  float64 `json:"mean_latency_ms"`
}

// RankOf returns the 1-based rank of the first hit whose source contains want.
func RankOf(hits []vectorstore.Hit, want string) int {
	if want == "" {
		return 0
	}
	for i, h := range hits {
		if strings.Contains(h.Metadata["source"], want) {
			return i + 1
		}
	}
	return 0
}

// AnswerMatches reports whether the answer satisfies the expectation.
func AnswerMatches(c Case, answer string) bool {
	a := strings.ToLower(answer)
	if c.Unanswerable {
		return strings.Contains(a, "could not find") || strings.Contains(a, "not in the indexed") || strings.Contains(a, "don't have") || strings.Contains(a, "do not have")
	}
	if len(c.ExpectAnswer) == 0 {
		return true
	}
	for _, want := range c.ExpectAnswer {
		if strings.Contains(a, strings.ToLower(want)) {
			return true
		}
	}
	return false
}

// Summarize computes the aggregate metrics.
func Summarize(outs []Outcome) Summary {
	var s Summary
	s.Cases = len(outs)
	if s.Cases == 0 {
		return s
	}
	var hits, ansOK, unans, unansOK int
	var mrr, lat float64
	for _, o := range outs {
		if o.ExpectSource != "" && o.RetrievedRank > 0 {
			hits++
			mrr += 1 / float64(o.RetrievedRank)
		}
		if o.AnswerOK {
			ansOK++
		}
		if o.Unanswerable {
			unans++
			if o.AnswerOK {
				unansOK++
			}
		}
		lat += o.Latency
	}
	retrievable := 0
	for _, o := range outs {
		if o.ExpectSource != "" {
			retrievable++
		}
	}
	if retrievable > 0 {
		s.HitAtK = float64(hits) / float64(retrievable)
		s.MRR = mrr / float64(retrievable)
	}
	s.AnswerAcc = float64(ansOK) / float64(s.Cases)
	if unans > 0 {
		s.AbstainAcc = float64(unansOK) / float64(unans)
	} else {
		s.AbstainAcc = 1
	}
	s.MeanLatMs = lat / float64(s.Cases)
	return s
}
