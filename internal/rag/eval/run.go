package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/udaykishore-resu/platform-copilot/internal/rag"
)

// LoadCases reads a JSONL golden file (one Case per line; # lines ignored).
func LoadCases(path string) ([]Case, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var cases []Case
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var c Case
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if c.ID == "" {
			c.ID = fmt.Sprintf("case-%d", n)
		}
		cases = append(cases, c)
	}
	return cases, sc.Err()
}

// Run executes every case through the pipeline.
func Run(ctx context.Context, p *rag.Pipeline, cases []Case, log func(string, ...any)) ([]Outcome, Summary, error) {
	var outs []Outcome
	for _, c := range cases {
		t0 := time.Now()
		ans, err := p.Ask(ctx, c.Question, nil)
		o := Outcome{Case: c, Latency: float64(time.Since(t0).Microseconds()) / 1000}
		if err != nil {
			o.Answer = "ERROR: " + err.Error()
		} else {
			o.Answer = ans.Text
			o.RetrievedRank = RankOf(ans.Sources, c.ExpectSource)
			o.AnswerOK = AnswerMatches(c, ans.Text)
		}
		if log != nil {
			mark := "PASS"
			if !o.AnswerOK {
				mark = "FAIL"
			}
			log("%-4s %-28s retrieved_rank=%d  %s", mark, c.ID, o.RetrievedRank, truncate(o.Answer, 90))
		}
		outs = append(outs, o)
	}
	return outs, Summarize(outs), nil
}

// Gate fails when metrics drop below thresholds (CI).
func Gate(s Summary, minHit, minAnswer float64) error {
	var problems []string
	if s.HitAtK < minHit {
		problems = append(problems, fmt.Sprintf("hit@k %.2f < %.2f", s.HitAtK, minHit))
	}
	if s.AnswerAcc < minAnswer {
		problems = append(problems, fmt.Sprintf("answer accuracy %.2f < %.2f", s.AnswerAcc, minAnswer))
	}
	if len(problems) > 0 {
		return fmt.Errorf("eval gate failed: %s", strings.Join(problems, "; "))
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
