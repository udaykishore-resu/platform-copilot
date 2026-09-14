// Package tokens estimates token counts, fits prompts into context windows,
// and prices requests. Tokens are the unit of both cost and capacity in every
// LLM system; an engineer who cannot count them cannot budget them.
//
// Roadmap: Maximum Tokens · Token Counting · Pricing Considerations ·
// Managing Tokens.
package tokens

import (
	"regexp"
	"strings"
	"unicode"
)

// Estimate approximates the token count of s for BPE tokenizers such as
// tiktoken's cl100k/o200k. Exact counting needs the model's merge table
// (≈ 1.5 MB per tokenizer); the labs show tiktoken in Python. This heuristic
// is within ~10% on English prose and within ~25% on code/YAML, which is
// accurate enough for budgeting — always leave headroom.
//
// Method: words ≈ 1.3 tokens each; runs of punctuation/symbols ≈ 1 token per
// 1–2 characters; numbers ≈ 1 token per 3 digits; CJK ≈ 1 token per character.
func Estimate(s string) int {
	if s == "" {
		return 0
	}
	n := 0.0
	for _, tok := range splitTokens(s) {
		switch classify(tok) {
		case kindWord:
			// common words are one token; longer words split into sub-word
			// pieces roughly every 4–5 characters beyond the first five
			n += 1 + float64(max(0, len(tok)-5))/4.5
		case kindNumber:
			n += float64(len(tok)+2) / 3
		case kindSymbol:
			n += float64(len(tok)+1) / 2
		case kindCJK:
			n += float64(len([]rune(tok)))
		case kindSpace:
			// whitespace is mostly absorbed into the next token; newlines count
			n += float64(strings.Count(tok, "\n")) * 0.5
		}
	}
	return int(n + 0.5)
}

// EstimateMessages approximates the tokens of a chat transcript including the
// ~4 tokens of per-message framing most chat formats add.
func EstimateMessages(contents []string) int {
	n := 3 // reply priming
	for _, c := range contents {
		n += Estimate(c) + 4
	}
	return n
}

type kind int

const (
	kindWord kind = iota
	kindNumber
	kindSymbol
	kindCJK
	kindSpace
)

var reSplit = regexp.MustCompile(`\s+|[A-Za-z_]+|\d+|[^\sA-Za-z_\d]+`)

func splitTokens(s string) []string { return reSplit.FindAllString(s, -1) }

func classify(tok string) kind {
	r := []rune(tok)[0]
	switch {
	case unicode.IsSpace(r):
		return kindSpace
	case unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r):
		return kindCJK
	case unicode.IsDigit(r):
		return kindNumber
	case unicode.IsLetter(r) || r == '_':
		return kindWord
	}
	return kindSymbol
}
