package safety

import "regexp"

// PII and secret redaction runs on input before it reaches a third-party API
// (privacy) and on retrieved chunks before they reach the model (a runbook
// with a pasted token must not become a completion). Regexes are a floor, not
// a ceiling: production systems add a NER model (Presidio, AWS Comprehend) for
// names and addresses.

type redactor struct {
	re    *regexp.Regexp
	label string
}

var piiRedactors = []redactor{
	{regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`), "EMAIL"},
	{regexp.MustCompile(`\b(?:\+?1[-. ]?)?\(?\d{3}\)?[-. ]?\d{3}[-. ]?\d{4}\b`), "PHONE"},
	{regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`), "SSN"},
	{regexp.MustCompile(`\b(?:\d[ -]*?){13,16}\b`), "CARD"},
	{regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`), "IPV4"},
}

var secretRedactors = []redactor{
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "AWS_ACCESS_KEY"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{20,}\b`), "OPENAI_KEY"},
	{regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}\b`), "ANTHROPIC_KEY"},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`), "GITHUB_TOKEN"},
	{regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9\-]{10,}\b`), "SLACK_TOKEN"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\b`), "JWT"},
	{regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----[\s\S]*?-----END (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`), "PRIVATE_KEY"},
	{regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key)\s*[:=]\s*['"]?[^\s'"]{8,}`), "CREDENTIAL_ASSIGNMENT"},
}

// Redaction reports what was replaced.
type Redaction struct {
	Text   string
	Counts map[string]int
}

// RedactPII replaces personal data with typed placeholders.
func RedactPII(text string) Redaction {
	return apply(text, piiRedactors)
}

// RedactPIIExcept is RedactPII with some labels skipped (e.g. "IPV4").
func RedactPIIExcept(text string, skip ...string) Redaction {
	s := map[string]bool{}
	for _, l := range skip {
		s[l] = true
	}
	var rs []redactor
	for _, r := range piiRedactors {
		if !s[r.label] {
			rs = append(rs, r)
		}
	}
	return apply(text, rs)
}

// RedactSecrets replaces credentials with typed placeholders. Run this on
// everything that leaves the process.
func RedactSecrets(text string) Redaction {
	return apply(text, secretRedactors)
}

func apply(text string, rs []redactor) Redaction {
	out := Redaction{Text: text, Counts: map[string]int{}}
	for _, r := range rs {
		out.Text = r.re.ReplaceAllStringFunc(out.Text, func(m string) string {
			out.Counts[r.label]++
			return "[" + r.label + "]"
		})
	}
	return out
}
