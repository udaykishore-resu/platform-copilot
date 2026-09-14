package tokens

import (
	"fmt"
	"sort"
	"strings"
)

// Price is USD per 1M tokens. Figures are indicative list prices captured
// when this file was written; vendors change them often — always confirm on
// the pricing page before quoting a budget. The relative ordering (frontier
// model ≫ mid-tier ≫ small ≫ embeddings ≫ local) is what matters for design.
type Price struct {
	Input, Output float64
	Note          string
}

// Table maps model-id prefixes to prices. Longest prefix wins.
var Table = map[string]Price{
	// OpenAI
	"gpt-4o":                 {2.50, 10.00, "OpenAI flagship multimodal"},
	"gpt-4o-mini":            {0.15, 0.60, "OpenAI small; default for this repo"},
	"gpt-4.1":                {2.00, 8.00, "1M context"},
	"gpt-4.1-mini":           {0.40, 1.60, ""},
	"gpt-4.1-nano":           {0.10, 0.40, ""},
	"o3":                     {2.00, 8.00, "reasoning"},
	"o4-mini":                {1.10, 4.40, "reasoning, small"},
	"text-embedding-3-small": {0.02, 0, "1536 dims"},
	"text-embedding-3-large": {0.13, 0, "3072 dims"},
	// Anthropic
	"claude-opus-4":    {15.00, 75.00, "Anthropic frontier"},
	"claude-sonnet-4":  {3.00, 15.00, "Anthropic mid-tier"},
	"claude-haiku-4":   {1.00, 5.00, "Anthropic small"},
	"claude-3-5-haiku": {0.80, 4.00, ""},
	// Google
	"gemini-2.5-pro":   {1.25, 10.00, "≤200k prompt tier"},
	"gemini-2.5-flash": {0.30, 2.50, ""},
	"gemini-2.0-flash": {0.10, 0.40, ""},
	// Others
	"mistral-large":  {2.00, 6.00, ""},
	"command-r-plus": {2.50, 10.00, "Cohere"},
	"command-r":      {0.15, 0.60, "Cohere"},
	// Local
	"llama":            {0, 0, "Ollama/local: $0 marginal, you pay for hardware"},
	"mistral":          {0, 0, "local"},
	"qwen":             {0, 0, "local"},
	"nomic-embed-text": {0, 0, "local embeddings"},
	"mock":             {0, 0, "offline mock"},
}

// Lookup finds the price for a model id by longest matching prefix.
func Lookup(model string) (Price, bool) {
	best, found := "", false
	for prefix := range Table {
		if strings.HasPrefix(model, prefix) && len(prefix) > len(best) {
			best, found = prefix, true
		}
	}
	if !found {
		return Price{}, false
	}
	return Table[best], true
}

// Cost returns the USD cost of a request given token counts.
func Cost(model string, promptTokens, completionTokens int) (usd float64, known bool) {
	p, ok := Lookup(model)
	if !ok {
		return 0, false
	}
	return float64(promptTokens)/1e6*p.Input + float64(completionTokens)/1e6*p.Output, true
}

// FormatCost renders a cost for humans ("$0.000123", "free (local)").
func FormatCost(model string, promptTokens, completionTokens int) string {
	usd, ok := Cost(model, promptTokens, completionTokens)
	switch {
	case !ok:
		return "unknown model price"
	case usd == 0:
		return "$0 (local/mock)"
	case usd < 0.01:
		return fmt.Sprintf("$%.6f", usd)
	default:
		return fmt.Sprintf("$%.4f", usd)
	}
}

// Models lists the table sorted by input price, for `copilot models`.
func Models() []string {
	keys := make([]string, 0, len(Table))
	for k := range Table {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if Table[keys[i]].Input == Table[keys[j]].Input {
			return keys[i] < keys[j]
		}
		return Table[keys[i]].Input < Table[keys[j]].Input
	})
	return keys
}
