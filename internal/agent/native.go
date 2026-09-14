package agent

import (
	"context"
	"time"

	"github.com/udaykishore-resu/platform-copilot/internal/llm"
)

// runNative is the manual tool-calling loop that every "agent framework"
// wraps: send messages + tool definitions; if the model returns tool calls,
// run them, append results, repeat; otherwise the content is the answer.
func (a *Agent) runNative(ctx context.Context, task string, res *Result) error {
	msgs := []llm.Message{llm.System(NativeSystem), llm.User(task)}
	tools := a.Tools.LLMTools()
	for i := 0; i < a.MaxSteps; i++ {
		t0 := time.Now()
		resp, err := a.LLM.Complete(ctx, llm.Request{Messages: msgs, Tools: tools, MaxTokens: 800, User: a.User})
		if err != nil {
			return err
		}
		if len(resp.ToolCalls) == 0 {
			a.record(res, Step{Thought: resp.Content, Latency: time.Since(t0), Usage: resp.Usage})
			res.Answer = resp.Content
			res.Stopped = "final"
			return nil
		}
		// append the assistant turn that requested the tools, then one tool
		// result message per call
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: resp.Content, ToolCalls: resp.ToolCalls})
		for _, tc := range resp.ToolCalls {
			obs := a.Tools.Call(ctx, tc.Name, tc.Arguments)
			wrapped := a.observe(tc.Name, obs, res)
			msgs = append(msgs, llm.ToolResult(tc.ID, tc.Name, wrapped))
			a.record(res, Step{Thought: resp.Content, Tool: tc.Name, Args: string(tc.Arguments), Observation: obs, Latency: time.Since(t0), Usage: resp.Usage})
			resp.Usage = llm.Usage{} // count usage once per model call
		}
	}
	// Out of steps: ask for a final answer without tools.
	msgs = append(msgs, llm.User("You have used all your tool calls. Give your best final answer from the observations so far."))
	resp, err := a.LLM.Complete(ctx, llm.Request{Messages: msgs, MaxTokens: 600, User: a.User})
	if err != nil {
		return err
	}
	a.record(res, Step{Thought: resp.Content, Usage: resp.Usage})
	res.Answer = resp.Content
	res.Stopped = "max_steps"
	return nil
}
