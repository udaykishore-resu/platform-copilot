package vectorstore

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// BM25 is a classic lexical ranker. Dense vectors are great at paraphrase
// ("pod keeps dying" ≈ "CrashLoopBackOff") and bad at exact identifiers
// ("PLAT-1901", "v42", "m6i.2xlarge") — which are exactly what SRE questions
// contain. Fusing BM25 with vector search (see RRF) fixes that failure mode
// cheaply; every serious RAG system ships hybrid retrieval.
type BM25 struct {
	k1, b  float64
	docs   []Document
	tf     []map[string]int
	df     map[string]int
	lens   []int
	avgLen float64
}

var reBM25Tok = regexp.MustCompile(`[a-z0-9][a-z0-9_\-./:]*`)

// Tokenize lower-cases and keeps identifiers intact ("payments-api", "v1.9.0").
func Tokenize(s string) []string {
	return reBM25Tok.FindAllString(strings.ToLower(s), -1)
}

// NewBM25 indexes the documents (k1 = 1.2, b = 0.75 are the standard Lucene
// defaults: k1 controls term-frequency saturation, b controls length
// normalisation).
func NewBM25(docs []Document) *BM25 {
	idx := &BM25{k1: 1.2, b: 0.75, docs: docs, df: map[string]int{}}
	total := 0
	for _, d := range docs {
		tf := map[string]int{}
		toks := Tokenize(d.Text)
		for _, t := range toks {
			tf[t]++
		}
		for t := range tf {
			idx.df[t]++
		}
		idx.tf = append(idx.tf, tf)
		idx.lens = append(idx.lens, len(toks))
		total += len(toks)
	}
	if len(docs) > 0 {
		idx.avgLen = float64(total) / float64(len(docs))
	}
	return idx
}

// Search returns the top-k documents by BM25 score (score > 0 only).
func (b *BM25) Search(query string, k int, filter map[string]string) []Hit {
	n := float64(len(b.docs))
	type sc struct {
		i int
		s float64
	}
	var scores []sc
	qtoks := Tokenize(query)
	for i, d := range b.docs {
		if !matches(d.Metadata, filter) {
			continue
		}
		var s float64
		for _, q := range qtoks {
			f := float64(b.tf[i][q])
			if f == 0 {
				continue
			}
			df := float64(b.df[q])
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			norm := f * (b.k1 + 1) / (f + b.k1*(1-b.b+b.b*float64(b.lens[i])/b.avgLen))
			s += idf * norm
		}
		if s > 0 {
			scores = append(scores, sc{i, s})
		}
	}
	sort.Slice(scores, func(x, y int) bool { return scores[x].s > scores[y].s })
	if len(scores) > k {
		scores = scores[:k]
	}
	hits := make([]Hit, 0, len(scores))
	for _, s := range scores {
		hits = append(hits, Hit{Document: b.docs[s.i], Score: float32(s.s)})
	}
	return hits
}
