package safety

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Guard applies input and output constraints. Every request through the
// copilot passes CheckInput before the model and CheckOutput after it. The
// guard never talks to the network, so it is cheap enough to run everywhere.
type Guard struct {
	// MaxInputChars bounds prompt size (cost + injection surface). 0 = 8000.
	MaxInputChars int
	// BlockInjection blocks inputs with a confident injection score.
	BlockInjection bool
	// RedactInput replaces PII/secrets in the input before it leaves.
	RedactInput bool
	// RedactOutput replaces secrets the model may have echoed.
	RedactOutput bool
	// BannedOutput patterns cause the response to be withheld — e.g. a
	// mutating kubectl command in a read-only assistant.
	BannedOutput []*regexp.Regexp
	// AllowedTopics, when set, requires the input to match at least one
	// (Know your customers / use cases: an SRE copilot should decline
	// to write poetry).
	AllowedTopics []*regexp.Regexp
}

// DefaultGuard is tuned for the platform copilot.
func DefaultGuard() *Guard {
	return &Guard{
		MaxInputChars:  8000,
		BlockInjection: true,
		RedactInput:    true,
		RedactOutput:   true,
		BannedOutput: []*regexp.Regexp{
			regexp.MustCompile(`(?i)\bkubectl\s+delete\s+(ns|namespace)\b`),
			regexp.MustCompile(`(?i)\brm\s+-rf\s+/\b`),
			regexp.MustCompile(`(?i)\bterraform\s+destroy\b.*-auto-approve`),
		},
	}
}

// Result of a guard check. Text is the (possibly redacted) content to use.
type Result struct {
	Text     string
	Blocked  bool
	Reason   string
	Warnings []string
}

// CheckInput validates and sanitises a user message.
func (g *Guard) CheckInput(text string) Result {
	res := Result{Text: text}
	max := g.MaxInputChars
	if max == 0 {
		max = 8000
	}
	if len(text) > max {
		return Result{Blocked: true, Reason: fmt.Sprintf("input too long (%d > %d chars)", len(text), max)}
	}
	if strings.TrimSpace(text) == "" {
		return Result{Blocked: true, Reason: "empty input"}
	}
	if len(g.AllowedTopics) > 0 {
		ok := false
		for _, re := range g.AllowedTopics {
			if re.MatchString(text) {
				ok = true
				break
			}
		}
		if !ok {
			return Result{Blocked: true, Reason: "off-topic for this assistant"}
		}
	}
	inj := DetectInjection(text)
	if inj.Suspicious && g.BlockInjection {
		return Result{Blocked: true, Reason: fmt.Sprintf("possible prompt injection (%s, score %.1f)", strings.Join(inj.Matches, ","), inj.Score)}
	}
	if inj.Score > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("injection heuristics matched %s (score %.1f)", strings.Join(inj.Matches, ","), inj.Score))
	}
	if g.RedactInput {
		r := RedactSecrets(res.Text)
		// IPs are normal in SRE questions; redacting them hurts retrieval.
		p := RedactPIIExcept(r.Text, "IPV4")
		res.Text = p.Text
		for k, v := range r.Counts {
			res.Warnings = append(res.Warnings, fmt.Sprintf("redacted %d %s from input", v, k))
		}
		for k, v := range p.Counts {
			res.Warnings = append(res.Warnings, fmt.Sprintf("redacted %d %s from input", v, k))
		}
	}
	return res
}

// CheckOutput validates a model response.
func (g *Guard) CheckOutput(text string) Result {
	res := Result{Text: text}
	for _, re := range g.BannedOutput {
		if re.MatchString(text) {
			return Result{Blocked: true, Reason: "response contained a banned destructive command: " + re.String()}
		}
	}
	if g.RedactOutput {
		r := RedactSecrets(text)
		res.Text = r.Text
		for k, v := range r.Counts {
			res.Warnings = append(res.Warnings, fmt.Sprintf("redacted %d %s from output", v, k))
		}
	}
	return res
}

// ValidateJSON checks that text is JSON containing every required key of the
// schema (a pragmatic subset of JSON Schema validation: required + type of
// top-level properties). It returns the parsed object on success.
func ValidateJSON(text string, schema json.RawMessage) (map[string]any, error) {
	text = strings.TrimSpace(text)
	// tolerate ```json fences
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &obj); err != nil {
		return nil, fmt.Errorf("output is not valid JSON: %w", err)
	}
	var s struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string `json:"type"`
			Enum []any  `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil, fmt.Errorf("bad schema: %w", err)
	}
	for _, k := range s.Required {
		if _, ok := obj[k]; !ok {
			return nil, fmt.Errorf("missing required key %q", k)
		}
	}
	for k, p := range s.Properties {
		v, ok := obj[k]
		if !ok {
			continue
		}
		if !typeMatches(v, p.Type) {
			return nil, fmt.Errorf("key %q: expected %s, got %T", k, p.Type, v)
		}
		if len(p.Enum) > 0 && !inEnum(v, p.Enum) {
			return nil, fmt.Errorf("key %q: %v not in enum %v", k, v, p.Enum)
		}
	}
	return obj, nil
}

func typeMatches(v any, t string) bool {
	switch t {
	case "", "object":
		_, ok := v.(map[string]any)
		return t == "" || ok
	case "string":
		_, ok := v.(string)
		return ok
	case "number", "integer":
		_, ok := v.(float64)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	}
	return true
}

func inEnum(v any, enum []any) bool {
	for _, e := range enum {
		if fmt.Sprint(e) == fmt.Sprint(v) {
			return true
		}
	}
	return false
}
