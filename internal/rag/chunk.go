// Package rag implements retrieval-augmented generation: chunk → embed →
// index → retrieve → assemble → generate, with citations.
//
// Roadmap: RAG & Implementation (Chunking · Embedding · Vector Database ·
// Retrieval Process · Generation · Using SDKs Directly).
package rag

import (
	"regexp"
	"strings"

	"github.com/udaykishore-resu/platform-copilot/internal/tokens"
)

// Chunk is a piece of a source document small enough to embed and cite.
type Chunk struct {
	Text    string
	Source  string // file path or URL
	Section string // nearest markdown heading, if any
	Index   int    // position within the source
}

// Chunker splits a document. Chunk size is the single most important RAG
// knob: too small and the chunk lacks the context to answer; too large and
// retrieval gets fuzzy and the prompt fills with noise. 300–800 tokens with
// 10–20% overlap is the usual sweet spot for technical docs.
type Chunker struct {
	MaxTokens int // target size per chunk
	Overlap   int // tokens repeated between adjacent chunks
}

// DefaultChunker is tuned for runbooks and manifests.
func DefaultChunker() Chunker { return Chunker{MaxTokens: 400, Overlap: 60} }

// Fixed splits purely by token budget on word boundaries with overlap. It is
// the baseline every other strategy is measured against.
func (c Chunker) Fixed(text, source string) []Chunk {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	// approximate words-per-chunk from the token target (≈1.3 tokens/word)
	per := max(int(float64(c.MaxTokens)/1.3), 20)
	ov := min(int(float64(c.Overlap)/1.3), per/2)
	var out []Chunk
	for start, i := 0, 0; start < len(words); i++ {
		end := min(start+per, len(words))
		out = append(out, Chunk{Text: strings.Join(words[start:end], " "), Source: source, Index: i})
		if end == len(words) {
			break
		}
		start = end - ov
	}
	return out
}

var reHeading = regexp.MustCompile(`(?m)^(#{1,6})\s+(.+)$`)

// Markdown splits on headings first so that a chunk never straddles two
// sections, then applies Recursive splitting inside sections that are too
// long. Each chunk is prefixed with its heading path ("Runbook › Remediation")
// — the cheapest form of "contextual chunking" and a large retrieval win,
// because the query "OOMKilled remediation" now lexically matches the chunk.
func (c Chunker) Markdown(text, source string) []Chunk {
	idx := reHeading.FindAllStringSubmatchIndex(text, -1)
	if len(idx) == 0 {
		return c.Recursive(text, source)
	}
	type sec struct{ path, body string }
	var secs []sec
	stack := []string{}
	prev := 0
	prevPath := ""
	flush := func(end int) {
		body := strings.TrimSpace(text[prev:end])
		if body != "" {
			secs = append(secs, sec{prevPath, body})
		}
	}
	for _, m := range idx {
		flush(m[0])
		level := m[3] - m[2]
		title := strings.TrimSpace(text[m[4]:m[5]])
		if level-1 < len(stack) {
			stack = stack[:level-1]
		}
		for len(stack) < level-1 {
			stack = append(stack, "")
		}
		stack = append(stack, title)
		prevPath = strings.Join(compact(stack), " › ")
		prev = m[1]
	}
	flush(len(text))

	var out []Chunk
	for _, s := range secs {
		for _, ch := range c.Recursive(s.body, source) {
			if s.path != "" {
				ch.Text = s.path + "\n" + ch.Text
				ch.Section = s.path
			}
			ch.Index = len(out)
			out = append(out, ch)
		}
	}
	return out
}

// Recursive tries the largest natural boundary first (blank lines), then
// lines, then sentences, then words, so chunks end at meaningful places.
// This is the algorithm behind LangChain's RecursiveCharacterTextSplitter.
func (c Chunker) Recursive(text, source string) []Chunk {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	pieces := c.split(text, []string{"\n\n", "\n", ". ", " "})
	// merge pieces greedily up to MaxTokens, carrying Overlap tokens forward
	var out []Chunk
	var cur []string
	curTok := 0
	flush := func() {
		if len(cur) == 0 {
			return
		}
		out = append(out, Chunk{Text: strings.TrimSpace(strings.Join(cur, "")), Source: source, Index: len(out)})
		// overlap: keep trailing pieces worth ~Overlap tokens
		keep, tok := 0, 0
		for i := len(cur) - 1; i >= 0 && tok < c.Overlap; i-- {
			tok += tokens.Estimate(cur[i])
			keep++
		}
		if keep >= len(cur) {
			keep = 0
		}
		cur = append([]string{}, cur[len(cur)-keep:]...)
		curTok = tok
		if keep == 0 {
			curTok = 0
		}
	}
	for _, p := range pieces {
		t := tokens.Estimate(p)
		if curTok+t > c.MaxTokens && len(cur) > 0 {
			flush()
		}
		cur = append(cur, p)
		curTok += t
	}
	if len(cur) > 0 {
		out = append(out, Chunk{Text: strings.TrimSpace(strings.Join(cur, "")), Source: source, Index: len(out)})
	}
	return out
}

// split recursively breaks text on the first separator that yields pieces
// under MaxTokens, keeping separators attached so joins are lossless.
func (c Chunker) split(text string, seps []string) []string {
	if tokens.Estimate(text) <= c.MaxTokens || len(seps) == 0 {
		return []string{text}
	}
	sep := seps[0]
	parts := strings.SplitAfter(text, sep)
	var out []string
	for _, p := range parts {
		if p == "" {
			continue
		}
		if tokens.Estimate(p) > c.MaxTokens {
			out = append(out, c.split(p, seps[1:])...)
		} else {
			out = append(out, p)
		}
	}
	return out
}

func compact(ss []string) []string {
	out := ss[:0:0]
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
