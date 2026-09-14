package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/udaykishore-resu/platform-copilot/internal/rag"
)

// SearchDocs wraps the RAG retriever as a tool, so the agent can consult
// runbooks before touching the cluster. Returning numbered passages keeps the
// agent's final answer citable.
func SearchDocs(r *rag.Retriever) Tool {
	return Tool{
		Name:        "search_docs",
		Description: "Semantic + keyword search over the team's runbooks, postmortems, Kubernetes manifests and Terraform. Use it first.",
		Schema:      Schema([]string{"query"}, map[string]string{"query": "natural-language question or keywords", "k": "int:number of passages (default 4)"}),
		Run: func(ctx context.Context, a map[string]any) (string, error) {
			q, _ := a["query"].(string)
			if q == "" {
				return "", fmt.Errorf("query is required")
			}
			k := 4
			if kv, ok := a["k"].(float64); ok && kv > 0 {
				k = int(kv)
			}
			saved := r.TopK
			r.TopK = k
			hits, err := r.Retrieve(ctx, q)
			r.TopK = saved
			if err != nil {
				return "", err
			}
			if len(hits) == 0 {
				return "No matching passages.", nil
			}
			var sb strings.Builder
			for i, h := range hits {
				sb.WriteString(fmt.Sprintf("[%d] source: %s (score %.3f)\n%s\n\n", i+1, h.Metadata["source"], h.Score, h.Text))
			}
			return sb.String(), nil
		},
	}
}

// PromQL queries a Prometheus-compatible endpoint (instant query). Read-only
// by nature. With Fake set it returns canned series for the demo.
type PromQL struct {
	URL  string // e.g. http://localhost:9090
	HTTP *http.Client
	Fake bool
}

// Tool returns the registry entry.
func (p *PromQL) Tool() Tool {
	return Tool{
		Name:        "promql",
		Description: "Run an instant PromQL query against Prometheus (e.g. error rate, p99 latency, container memory). Returns up to 20 series.",
		Schema:      Schema([]string{"query"}, map[string]string{"query": "PromQL expression"}),
		Run: func(ctx context.Context, a map[string]any) (string, error) {
			q, _ := a["query"].(string)
			if q == "" {
				return "", fmt.Errorf("query is required")
			}
			if p.Fake {
				return fakePromQL(q), nil
			}
			if p.URL == "" {
				return "", fmt.Errorf("PROMETHEUS_URL not set (or set COPILOT_FAKE_PROMETHEUS=1 for demo data)")
			}
			c := p.HTTP
			if c == nil {
				c = &http.Client{Timeout: 15 * time.Second}
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.URL, "/")+"/api/v1/query?query="+url.QueryEscape(q), nil)
			resp, err := c.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			var out struct {
				Status string `json:"status"`
				Data   struct {
					Result []struct {
						Metric map[string]string `json:"metric"`
						Value  []any             `json:"value"`
					} `json:"result"`
				} `json:"data"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(data, &out); err != nil {
				return "", err
			}
			if out.Status != "success" {
				return "", fmt.Errorf("prometheus: %s", out.Error)
			}
			var sb strings.Builder
			for i, r := range out.Data.Result {
				if i == 20 {
					sb.WriteString("…\n")
					break
				}
				lbl, _ := json.Marshal(r.Metric)
				val := ""
				if len(r.Value) == 2 {
					val = fmt.Sprint(r.Value[1])
				}
				sb.WriteString(fmt.Sprintf("%s => %s\n", lbl, val))
			}
			if sb.Len() == 0 {
				return "(empty result)", nil
			}
			return sb.String(), nil
		},
	}
}

func fakePromQL(q string) string {
	ql := strings.ToLower(q)
	switch {
	case strings.Contains(ql, "memory"):
		return `{"pod":"payments-api-7c9f8d6b5-2xk9p"} => 1.06e+09
{"pod":"payments-api-7c9f8d6b5-m4tq2"} => 1.05e+09
{"pod":"payments-api-7c9f8d6b5-vv8hn"} => 6.1e+08`
	case strings.Contains(ql, "5.."), strings.Contains(ql, "error"):
		return `{"job":"payments-api"} => 4.7`
	case strings.Contains(ql, "latency"), strings.Contains(ql, "duration"):
		return `{"job":"payments-api","quantile":"0.99"} => 2.84`
	case strings.Contains(ql, "restart"):
		return `{"pod":"payments-api-7c9f8d6b5-2xk9p"} => 7
{"pod":"payments-api-7c9f8d6b5-m4tq2"} => 7`
	}
	return `{"job":"payments-api"} => 1`
}

// Calc is a tiny arithmetic tool: models are unreliable at arithmetic, and
// capacity questions ("how many pods fit on 6 nodes × 58 IPs") need exact
// answers. Supports + - * / % and parentheses.
func Calc() Tool {
	return Tool{
		Name:        "calc",
		Description: "Evaluate an arithmetic expression exactly (+ - * / % parentheses). Use for capacity and cost math.",
		Schema:      Schema([]string{"expression"}, map[string]string{"expression": "e.g. (6*58)-(3*4)"}),
		Run: func(_ context.Context, a map[string]any) (string, error) {
			e, _ := a["expression"].(string)
			v, err := evalExpr(e)
			if err != nil {
				return "", err
			}
			return strconv.FormatFloat(v, 'f', -1, 64), nil
		},
	}
}

// evalExpr is a recursive-descent evaluator (no eval, no shell).
func evalExpr(s string) (float64, error) {
	p := &parser{s: strings.ReplaceAll(s, " ", "")}
	v, err := p.expr()
	if err != nil {
		return 0, err
	}
	if p.i != len(p.s) {
		return 0, fmt.Errorf("unexpected %q at %d", p.s[p.i:], p.i)
	}
	return v, nil
}

type parser struct {
	s string
	i int
}

func (p *parser) peek() byte {
	if p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

func (p *parser) expr() (float64, error) {
	v, err := p.term()
	if err != nil {
		return 0, err
	}
	for p.peek() == '+' || p.peek() == '-' {
		op := p.s[p.i]
		p.i++
		r, err := p.term()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			v += r
		} else {
			v -= r
		}
	}
	return v, nil
}

func (p *parser) term() (float64, error) {
	v, err := p.factor()
	if err != nil {
		return 0, err
	}
	for p.peek() == '*' || p.peek() == '/' || p.peek() == '%' {
		op := p.s[p.i]
		p.i++
		r, err := p.factor()
		if err != nil {
			return 0, err
		}
		switch op {
		case '*':
			v *= r
		case '/':
			if r == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			v /= r
		case '%':
			if r == 0 {
				return 0, fmt.Errorf("modulo by zero")
			}
			v = float64(int64(v) % int64(r))
		}
	}
	return v, nil
}

func (p *parser) factor() (float64, error) {
	if p.peek() == '(' {
		p.i++
		v, err := p.expr()
		if err != nil {
			return 0, err
		}
		if p.peek() != ')' {
			return 0, fmt.Errorf("missing )")
		}
		p.i++
		return v, nil
	}
	if p.peek() == '-' {
		p.i++
		v, err := p.factor()
		return -v, err
	}
	start := p.i
	for p.i < len(p.s) && (p.s[p.i] >= '0' && p.s[p.i] <= '9' || p.s[p.i] == '.') {
		p.i++
	}
	if start == p.i {
		return 0, fmt.Errorf("expected number at %d", start)
	}
	return strconv.ParseFloat(p.s[start:p.i], 64)
}
