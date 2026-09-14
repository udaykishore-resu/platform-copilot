// Package safety is the layer before and after the model: prompt-injection
// detection, PII and secret redaction, content moderation, and output
// constraints. Detection is heuristic and will never be complete — the real
// defence is containment (read-only tools, allowlists, least privilege), which
// lives in internal/agent. Both are needed.
//
// Roadmap: AI Safety and Ethics.
package safety

import (
	"regexp"
	"strings"
)

// InjectionResult explains a detection.
type InjectionResult struct {
	Suspicious bool
	Score      float64 // 0–1, sum of matched pattern weights (capped)
	Matches    []string
}

type pattern struct {
	re     *regexp.Regexp
	weight float64
	label  string
}

// Patterns cover the common injection families. They are intentionally
// readable so a reviewer can audit them; an ML classifier (Llama Guard,
// Prompt Guard, Azure Prompt Shields) can be layered on top.
var patterns = []pattern{
	{regexp.MustCompile(`(?i)ignore\s+(all\s+|the\s+|any\s+)?(previous|prior|above|earlier)\s+(instructions?|prompts?|rules?|directions?)`), 0.9, "ignore-previous"},
	{regexp.MustCompile(`(?i)disregard\s+(all\s+|the\s+|your\s+)?(previous|prior|above|system)\s+(instructions?|prompts?|rules?)`), 0.9, "disregard"},
	{regexp.MustCompile(`(?i)you\s+are\s+now\s+(a|an|in)\s+`), 0.6, "role-override"},
	{regexp.MustCompile(`(?i)\b(DAN|developer|jailbreak|god)\s+mode\b`), 0.8, "jailbreak-mode"},
	{regexp.MustCompile(`(?i)(reveal|print|show|repeat|output|leak)\s+(me\s+)?(your|the)\s+(system\s+prompt|instructions|hidden\s+prompt|initial\s+prompt)`), 0.8, "prompt-exfil"},
	{regexp.MustCompile(`(?i)\bsystem\s*:\s*`), 0.4, "fake-system-role"},
	{regexp.MustCompile(`(?i)<\s*/?\s*(system|assistant|instructions?)\s*>`), 0.6, "fake-tags"},
	{regexp.MustCompile(`(?i)\bnew\s+(instructions?|rules?|directives?)\s*:`), 0.4, "new-instructions"},
	{regexp.MustCompile(`(?i)\bAI\b.*\b(must|will)\s+(now\s+)?(comply|obey|follow)\b`), 0.5, "coercion"},
	{regexp.MustCompile(`(?i)(do\s+not|don't|never)\s+(tell|inform|mention)\s+(the\s+)?(user|human|operator)`), 0.7, "conceal"},
	{regexp.MustCompile(`(?i)\b(kubectl|helm)\s+(delete|drain|cordon|scale|apply|patch|exec|edit)\b`), 0.5, "mutating-command"},
	{regexp.MustCompile(`(?i)(exfiltrate|send|post|upload)\s+.*\b(secret|token|credential|kubeconfig|env)\b.*\b(to|at)\s+https?://`), 0.9, "exfil-url"},
	{regexp.MustCompile(`(?i)\bbase64\s*(-d|--decode)\b`), 0.3, "encoded-payload"},
	{regexp.MustCompile(`(?i)\bimportant\s+(new\s+)?(instructions?|update)\s*(from|for)\s+(the\s+)?(admin|operator|developer|openai|anthropic)\b`), 0.7, "authority-claim"},
	{regexp.MustCompile(`(?i)(translate|summari[sz]e|rewrite|encode)\s+(the\s+)?(above|previous|your)\s+.*\b(instructions|prompt)`), 0.6, "translate-exfil"},
	{regexp.MustCompile(`(?i)\b(don't|do\s+not|never)\s+(mention|report|log|disclose)\s+(it|this|that|anything)\b`), 0.4, "conceal-action"},
}

// leetspeak normalisation so "1gn0re pr3vious" is still caught.
var leet = strings.NewReplacer("0", "o", "1", "i", "3", "e", "4", "a", "5", "s", "7", "t", "@", "a", "$", "s", "|", "l")

// DetectInjection scores text against the pattern set. Score ≥ 0.6 is a
// confident detection; 0.3–0.6 is worth logging. Thresholds are deliberately
// conservative for input (block) and looser for retrieved documents (warn):
// a runbook that says "ignore previous alerts" is not an attack.
func DetectInjection(text string) InjectionResult {
	var res InjectionResult
	norm := leet.Replace(strings.ToLower(text))
	seen := map[string]bool{}
	for _, p := range patterns {
		if p.re.MatchString(text) || p.re.MatchString(norm) {
			if !seen[p.label] {
				seen[p.label] = true
				res.Matches = append(res.Matches, p.label)
				res.Score += p.weight
			}
		}
	}
	if res.Score > 1 {
		res.Score = 1
	}
	res.Suspicious = res.Score >= 0.6
	return res
}

// WrapUntrusted delimits retrieved or tool-returned text so the model can be
// told "this is data, not instructions". Delimiting is not a guarantee — but
// combined with the system-prompt rule it measurably lowers injection success.
func WrapUntrusted(label, text string) string {
	// strip anything that looks like our own delimiter to prevent escaping
	text = strings.ReplaceAll(text, "</untrusted>", "&lt;/untrusted&gt;")
	return "<untrusted source=\"" + label + "\">\n" + text + "\n</untrusted>"
}
