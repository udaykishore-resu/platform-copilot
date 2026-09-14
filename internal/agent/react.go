package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/udaykishore-resu/platform-copilot/internal/llm"
)

var (
	reThought = regexp.MustCompile(`(?s)Thought:\s*(.*?)\s*(?:Action:|Final Answer:|$)`)
	reAction  = regexp.MustCompile(`Action:\s*([A-Za-z0-9_\-]+)`)
	reInput   = regexp.MustCompile(`(?s)Action Input:\s*(.*?)\s*(?:Observation:|$)`)
	reFinal   = regexp.MustCompile(`(?s)Final Answer:\s*(.*)$`)
)

// runReAct drives the text protocol. The transcript is a single growing user
// message ("scratchpad") — the classic implementation — and the model is
// stopped at "Observation:" so it cannot hallucinate tool output.
func (a *Agent) runReAct(ctx context.Context, task string, res *Result) error {
	system := fmt.Sprintf(ReActSystem, a.Tools.Describe(), a.MaxSteps)
	scratch := task + "\n\n"
	for i := 0; i < a.MaxSteps; i++ {
		t0 := time.Now()
		resp, err := a.LLM.Complete(ctx, llm.Request{
			Messages:  []llm.Message{llm.System(system), llm.User(scratch)},
			MaxTokens: 600,
			Stop:      []string{"Observation:"},
			User:      a.User,
		})
		if err != nil {
			return err
		}
		text := resp.Content
		thought := submatch(reThought, text)
		if final := submatch(reFinal, text); final != "" {
			a.record(res, Step{Thought: thought, Latency: time.Since(t0), Usage: resp.Usage})
			res.Answer = strings.TrimSpace(final)
			res.Stopped = "final"
			return nil
		}
		action := submatch(reAction, text)
		input := strings.TrimSpace(submatch(reInput, text))
		if action == "" {
			// Model broke protocol; nudge it once, then treat the text as the answer.
			scratch += text + "\nObservation: Your reply did not follow the format. Use Action/Action Input or Final Answer.\n"
			a.record(res, Step{Thought: text, Observation: "format violation", Latency: time.Since(t0), Usage: resp.Usage})
			continue
		}
		obs := a.Tools.Call(ctx, action, toJSONArgs(input, a.Tools, action))
		scratch += fmt.Sprintf("Thought: %s\nAction: %s\nAction Input: %s\nObservation: %s\n\n", thought, action, input, a.observe(action, obs, res))
		a.record(res, Step{Thought: thought, Tool: action, Args: input, Observation: obs, Latency: time.Since(t0), Usage: resp.Usage})
	}
	scratch += "You have used all your actions. Reply with Final Answer now.\n"
	resp, err := a.LLM.Complete(ctx, llm.Request{Messages: []llm.Message{llm.System(system), llm.User(scratch)}, MaxTokens: 600, User: a.User})
	if err != nil {
		return err
	}
	ans := submatch(reFinal, resp.Content)
	if ans == "" {
		ans = resp.Content
	}
	a.record(res, Step{Thought: resp.Content, Usage: resp.Usage})
	res.Answer = strings.TrimSpace(ans)
	res.Stopped = "max_steps"
	return nil
}

func submatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// toJSONArgs accepts either a JSON object or a bare string. Bare strings are
// mapped onto the tool's first required property, with a special case for
// kubectl-style "pods -n payments".
func toJSONArgs(input string, reg *Registry, tool string) json.RawMessage {
	input = strings.Trim(input, "`\" \n")
	if strings.HasPrefix(input, "{") {
		return json.RawMessage(input)
	}
	t, ok := reg.Get(tool)
	if !ok {
		return json.RawMessage(`{}`)
	}
	args := map[string]any{}
	if tool == "kubectl_get" {
		fields := strings.Fields(input)
		for i := 0; i < len(fields); i++ {
			switch {
			case fields[i] == "-n" && i+1 < len(fields):
				args["namespace"] = fields[i+1]
				i++
			case fields[i] == "-A" || fields[i] == "--all-namespaces":
				args["namespace"] = ""
			case args["resource"] == nil:
				args["resource"] = fields[i]
			default:
				args["name"] = fields[i]
			}
		}
	} else {
		args[firstProperty(t.Schema)] = input
	}
	b, _ := json.Marshal(args)
	return b
}
