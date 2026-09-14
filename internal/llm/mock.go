package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Mock is a deterministic, offline Provider. It exists so that every lab,
// test, and `make run` works from a clean clone with no API key — and so that
// CI never depends on a paid API. It is not "intelligent": it extracts the
// context sentences that best overlap the question and cites them, and it
// follows a fixed tool-use script when tools are offered. That is enough to
// exercise every code path (RAG, citations, ReAct, native tool calling,
// JSON-schema output, token accounting) end to end.
type Mock struct{ model string }

// NewMock constructs the mock provider.
func NewMock(model string) *Mock {
	if model == "" {
		model = "mock-1"
	}
	return &Mock{model: model}
}

func (m *Mock) Name() string             { return "mock" }
func (m *Mock) DefaultModel() string     { return m.model }
func (m *Mock) ContextWindow(string) int { return 32_000 }

var (
	reSentence = regexp.MustCompile(`(?:[.!?])\s+`)
	reWord     = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9_\-/.]*`)
	reCite     = regexp.MustCompile(`^\[(\d+)\]`)
)

// Complete implements Provider.
func (m *Mock) Complete(_ context.Context, req Request) (*Response, error) {
	question, system, toolResults := "", "", []Message{}
	for _, msg := range req.Messages {
		switch msg.Role {
		case RoleSystem:
			system += msg.Content + "\n"
		case RoleUser:
			question = msg.Content
		case RoleTool:
			toolResults = append(toolResults, msg)
		}
	}
	prompt := countTokens(req)
	resp := &Response{Model: m.model, Provider: "mock", FinishReason: "stop"}

	// 1. Native tool calling script.
	if len(req.Tools) > 0 || len(toolResults) > 0 {
		if len(req.Tools) > 0 {
			if tc, done := m.scriptTools(req.Tools, question, toolResults); !done {
				resp.ToolCalls = []ToolCall{tc}
				resp.FinishReason = "tool_calls"
				resp.Usage = usage(prompt, 24)
				return resp, nil
			}
		}
		var obs []string
		for _, t := range toolResults {
			obs = append(obs, t.Name+"\n"+t.Content)
		}
		resp.Content = m.agentAnswer(firstUserQuestion(req.Messages), obs)
		resp.Usage = usage(prompt, estimate(resp.Content))
		return resp, nil
	}

	// 2. ReAct text protocol (no native tools, but the system prompt asks for
	// Thought/Action/Observation).
	if strings.Contains(system, "Action Input:") && !strings.Contains(question, "Final Answer") {
		resp.Content = m.scriptReAct(system, question)
		resp.Usage = usage(prompt, estimate(resp.Content))
		return resp, nil
	}

	// 3. Structured output.
	if len(req.JSONSchema) > 0 {
		resp.Content = m.fillSchema(req.JSONSchema, question, system)
		resp.Usage = usage(prompt, estimate(resp.Content))
		return resp, nil
	}

	// 4. Plain / RAG answer: pick the context sentences most similar to the
	// question and cite their source numbers.
	resp.Content = m.extractive(system, question, toolResults)
	resp.Usage = usage(prompt, estimate(resp.Content))
	return resp, nil
}

func (m *Mock) extractive(system, question string, toolResults []Message) string {
	ctx := system
	for _, t := range toolResults {
		ctx += "\n" + t.Content
	}
	// Only the context section counts; the rules above it are not evidence.
	if i := strings.Index(ctx, "# Context"); i >= 0 {
		ctx = ctx[i:]
	}
	blocks := splitBlocks(ctx)
	if len(blocks) == 0 {
		return fmt.Sprintf("(mock) You asked: %q. Set COPILOT_PROVIDER=openai|anthropic|gemini|ollama for a real model.", strings.TrimSpace(question))
	}
	qwords := wordSet(question)
	if len(qwords) == 0 {
		return "(mock) Please ask a more specific question."
	}
	// Weight question words by rarity across the retrieved blocks (IDF): a
	// word that appears in no block ("mobile") is strong evidence the corpus
	// cannot answer, and a word in every block ("payments") proves little.
	blockWords := make([]map[string]bool, len(blocks))
	df := map[string]int{}
	for i, b := range blocks {
		blockWords[i] = wordSet(b.text)
		for w := range qwords {
			if blockWords[i][w] {
				df[w]++
			}
		}
	}
	weight := map[string]float64{}
	total := 0.0
	for w := range qwords {
		weight[w] = math.Log(1 + float64(len(blocks)+1)/float64(df[w]+1))
		total += weight[w]
	}
	type bscore struct {
		b     block
		ratio float64
	}
	var ranked []bscore
	best := 0.0
	for i, b := range blocks {
		covered := 0.0
		for w := range qwords {
			if blockWords[i][w] {
				covered += weight[w]
			}
		}
		r := covered / total
		ranked = append(ranked, bscore{b, r})
		if r > best {
			best = r
		}
	}
	if best < 0.45 {
		return "I could not find this in the indexed documents. Try `copilot search` with different keywords, or add the source document to data/knowledge and re-run `make ingest`."
	}
	// Within the best blocks, pick sentences with question-word overlap and
	// concrete values (numbers, code spans) — where runbook answers live.
	type scored struct {
		text  string
		cite  string
		score float64
		order int
	}
	var cands []scored
	order := 0
	for _, rb := range ranked {
		if rb.ratio < best*0.75 {
			continue
		}
		lines := strings.Split(rb.b.text, "\n")
		prevHit := false
		for li, line := range lines {
			line = strings.TrimSpace(line)
			if li == 0 && strings.Contains(line, "›") { // heading path prefix
				continue
			}
			for _, s := range splitSentences(line) {
				order++
				if len(s) < 20 {
					continue
				}
				sc := 0.0
				for w := range wordSet(s) {
					if qwords[w] {
						sc += 1 + weight[w]
					}
				}
				if strings.ContainsAny(s, "0123456789") {
					sc++
				}
				if strings.Contains(s, "`") || strings.Contains(s, "**") {
					sc += 0.5
				}
				if prevHit {
					sc++ // answers follow the sentence that restates the question
				}
				prevHit = sc >= 2
				if sc >= 2 {
					cands = append(cands, scored{s, "[" + rb.b.n + "]", sc, order})
				}
			}
		}
	}
	if len(cands) == 0 {
		return "I could not find this in the indexed documents. The closest passages are listed under Sources."
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	seen := map[string]bool{}
	uniq := cands[:0]
	for _, c := range cands {
		if !seen[c.text] {
			seen[c.text] = true
			uniq = append(uniq, c)
		}
	}
	cands = uniq
	if len(cands) > 3 {
		cands = cands[:3]
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].order < cands[j].order })
	var sb strings.Builder
	for _, c := range cands {
		sb.WriteString(strings.TrimRight(c.text, ".") + " " + c.cite + ".\n")
	}
	return strings.TrimSpace(sb.String())
}

// agentAnswer summarises tool observations: runbook evidence first, then the
// live-cluster / metrics facts, then the recommended (not executed) action.
func (m *Mock) agentAnswer(question string, observations []string) string {
	var runbook, live []string
	for _, o := range observations {
		switch {
		case strings.Contains(o, "source:"):
			if ans := m.extractiveLoose(o, question); ans != "" {
				runbook = append(runbook, ans)
			}
		case strings.Contains(o, "$ kubectl"), strings.Contains(o, "=>"):
			for _, line := range strings.Split(o, "\n") {
				l := strings.TrimSpace(line)
				if strings.Contains(l, "CrashLoop") || strings.Contains(l, "NotReady") || strings.Contains(l, "OOMKilled") ||
					strings.Contains(l, "Error") || strings.Contains(l, "=>") || strings.HasPrefix(l, "$ kubectl") {
					live = append(live, l)
				}
			}
		default:
			if l := firstLine(strings.TrimSpace(o)); l != "" && !strings.HasPrefix(l, "<untrusted") {
				live = append(live, l)
			}
		}
	}
	var sb strings.Builder
	if len(runbook) > 0 {
		sb.WriteString("From the runbooks:\n" + strings.Join(runbook, "\n") + "\n\n")
	}
	if len(live) > 0 {
		if len(live) > 6 {
			live = live[:6]
		}
		sb.WriteString("Live observations:\n  " + strings.Join(live, "\n  ") + "\n\n")
	}
	if sb.Len() == 0 {
		return "I gathered no usable evidence. Check the index (`copilot search`) and tool connectivity."
	}
	sb.WriteString("Recommended next step: follow the runbook remediation above and verify with `kubectl get pods -n payments -w`. I did not change anything.")
	return sb.String()
}

// extractiveLoose is extractive without the abstention threshold, for
// summarising a tool result the model already chose to fetch.
func (m *Mock) extractiveLoose(ctx, question string) string {
	ans := m.extractive("# Context\n"+ctx, question, nil)
	if strings.HasPrefix(ans, "I could not find") {
		// fall back to the first concrete sentences of the first block
		blocks := splitBlocks(ctx)
		if len(blocks) == 0 {
			return ""
		}
		var picked []string
		for _, line := range strings.Split(blocks[0].text, "\n")[1:] {
			l := strings.TrimSpace(line)
			if len(l) > 30 && !strings.HasPrefix(l, "```") {
				picked = append(picked, l+" ["+blocks[0].n+"]")
			}
			if len(picked) == 2 {
				break
			}
		}
		return strings.Join(picked, "\n")
	}
	return ans
}

func firstUserQuestion(msgs []Message) string {
	for _, m := range msgs {
		if m.Role == RoleUser {
			return m.Content
		}
	}
	return ""
}

// splitSentences splits on sentence punctuation followed by whitespace, so
// "1.5Gi" and "v1.9.0" survive intact.
func splitSentences(line string) []string {
	var out []string
	last := 0
	for _, m := range reSentence.FindAllStringIndex(line, -1) {
		out = append(out, strings.TrimSpace(line[last:m[0]+1]))
		last = m[1]
	}
	if rest := strings.TrimSpace(line[last:]); rest != "" {
		out = append(out, rest)
	}
	return out
}

type block struct {
	n    string
	text string
}

// splitBlocks parses "[n] source: ...\n<text>" blocks produced by rag.BuildPrompt
// (and by the search_docs tool).
func splitBlocks(ctx string) []block {
	var out []block
	var cur *block
	for _, line := range strings.Split(ctx, "\n") {
		if m := reCite.FindStringSubmatch(strings.TrimSpace(line)); m != nil && strings.Contains(line, "source:") {
			out = append(out, block{n: m[1]})
			cur = &out[len(out)-1]
			continue
		}
		if cur != nil {
			cur.text += line + "\n"
		}
	}
	return out
}

// scriptTools returns the next tool call in a fixed script: search the docs
// first, then look at the cluster if the question is about workloads, then
// stop so the caller produces a final answer from observations.
func (m *Mock) scriptTools(tools []Tool, question string, results []Message) (ToolCall, bool) {
	has := func(name string) bool {
		for _, t := range tools {
			if t.Name == name {
				return true
			}
		}
		return false
	}
	q := strings.ToLower(question)
	switch len(results) {
	case 0:
		if has("search_docs") {
			return call("call_1", "search_docs", map[string]any{"query": question, "k": 4}), false
		}
		if has("calc") {
			return call("call_1", "calc", map[string]any{"expression": firstExpr(question)}), false
		}
	case 1:
		if has("kubectl_get") && (strings.Contains(q, "pod") || strings.Contains(q, "crash") || strings.Contains(q, "deploy") || strings.Contains(q, "node")) {
			res := "pods"
			if strings.Contains(q, "node") {
				res = "nodes"
			}
			return call("call_2", "kubectl_get", map[string]any{"resource": res, "namespace": "payments"}), false
		}
		if has("promql") && (strings.Contains(q, "latency") || strings.Contains(q, "error rate") || strings.Contains(q, "cpu")) {
			return call("call_2", "promql", map[string]any{"query": `sum(rate(http_requests_total{job="payments-api",code=~"5.."}[5m]))`}), false
		}
	}
	return ToolCall{}, true
}

func (m *Mock) scriptReAct(system, transcript string) string {
	obs := strings.Count(transcript, "Observation:")
	q := strings.ToLower(transcript)
	switch {
	case obs == 0 && strings.Contains(system, "search_docs"):
		return "Thought: I should check the runbooks before touching the cluster.\nAction: search_docs\nAction Input: " + firstLine(transcript)
	case obs == 1 && strings.Contains(system, "kubectl_get") && (strings.Contains(q, "pod") || strings.Contains(q, "crash")):
		return "Thought: The runbook names a remediation; let me confirm the live pod state.\nAction: kubectl_get\nAction Input: pods -n payments"
	default:
		var obs []string
		for _, part := range strings.Split(transcript, "Observation:")[1:] {
			obs = append(obs, part)
		}
		return "Thought: I have enough to answer.\nFinal Answer: " + m.agentAnswer(firstLine(transcript), obs)
	}
}

func (m *Mock) fillSchema(schema json.RawMessage, question, system string) string {
	var s struct {
		Properties map[string]struct {
			Type string `json:"type"`
			Enum []any  `json:"enum"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(schema, &s)
	out := map[string]any{}
	keys := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := s.Properties[k]
		switch {
		case len(p.Enum) > 0:
			out[k] = p.Enum[0]
		case p.Type == "number" || p.Type == "integer":
			out[k] = 0
		case p.Type == "boolean":
			out[k] = false
		case p.Type == "array":
			out[k] = []string{}
		default:
			out[k] = m.extractive(system, question, nil)
		}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b)
}

func call(id, name string, args map[string]any) ToolCall {
	b, _ := json.Marshal(args)
	return ToolCall{ID: id, Name: name, Arguments: b}
}

func wordSet(s string) map[string]bool {
	set := map[string]bool{}
	for _, w := range reWord.FindAllString(strings.ToLower(s), -1) {
		if len(w) > 2 && !stop[w] {
			set[w] = true
		}
	}
	return set
}

var stop = map[string]bool{"the": true, "and": true, "for": true, "what": true, "why": true, "how": true, "is": true, "are": true, "with": true, "this": true, "that": true, "from": true, "you": true, "use": true, "when": true, "should": true, "does": true, "can": true, "our": true, "which": true, "into": true}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func firstExpr(s string) string {
	re := regexp.MustCompile(`[\d\s+\-*/().%]+`)
	best := ""
	for _, m := range re.FindAllString(s, -1) {
		if len(strings.TrimSpace(m)) > len(best) {
			best = strings.TrimSpace(m)
		}
	}
	return best
}

// estimate is a local copy of the ~4 chars/token heuristic to avoid an import
// cycle with internal/tokens.
func estimate(s string) int { return (len(s) + 3) / 4 }

func countTokens(req Request) int {
	n := 0
	for _, m := range req.Messages {
		n += estimate(m.Content) + 4
	}
	for _, t := range req.Tools {
		n += estimate(t.Description) + estimate(string(t.Parameters))
	}
	return n
}

func usage(prompt, completion int) Usage {
	return Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion, Estimated: true}
}
