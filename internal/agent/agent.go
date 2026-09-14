package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/udaykishore-resu/platform-copilot/internal/llm"
	"github.com/udaykishore-resu/platform-copilot/internal/safety"
	"github.com/udaykishore-resu/platform-copilot/internal/tokens"
)

// Mode selects the protocol.
type Mode string

const (
	// ModeReAct uses the text protocol (works with every model).
	ModeReAct Mode = "react"
	// ModeNative uses provider tool calling (OpenAI tools, Anthropic tool
	// use, Gemini function calling, Ollama tools).
	ModeNative Mode = "native"
)

// Step is one iteration of the loop, kept for the trace.
type Step struct {
	N           int           `json:"n"`
	Thought     string        `json:"thought,omitempty"`
	Tool        string        `json:"tool,omitempty"`
	Args        string        `json:"args,omitempty"`
	Observation string        `json:"observation,omitempty"`
	Latency     time.Duration `json:"latency_ns"`
	Usage       llm.Usage     `json:"usage"`
}

// Result is the outcome of a run.
type Result struct {
	Answer   string    `json:"answer"`
	Steps    []Step    `json:"steps"`
	Usage    llm.Usage `json:"usage"`
	Cost     string    `json:"cost"`
	Model    string    `json:"model"`
	Mode     Mode      `json:"mode"`
	Warnings []string  `json:"warnings,omitempty"`
	Stopped  string    `json:"stopped"` // final | max_steps | error
}

// Agent runs the loop.
type Agent struct {
	LLM      llm.Provider
	Tools    *Registry
	Mode     Mode
	MaxSteps int
	Guard    *safety.Guard
	User     string
	// Trace receives each step as it happens (for streaming UIs / logs).
	Trace func(Step)
}

// Run answers a task by alternating model calls and tool calls.
func (a *Agent) Run(ctx context.Context, task string) (*Result, error) {
	if a.MaxSteps <= 0 {
		a.MaxSteps = 6
	}
	if a.Mode == "" {
		a.Mode = ModeNative
	}
	res := &Result{Mode: a.Mode, Model: a.LLM.DefaultModel()}
	if a.Guard != nil {
		in := a.Guard.CheckInput(task)
		if in.Blocked {
			return nil, fmt.Errorf("task blocked by safety guard: %s", in.Reason)
		}
		res.Warnings = append(res.Warnings, in.Warnings...)
		task = in.Text
	}
	var err error
	switch a.Mode {
	case ModeReAct:
		err = a.runReAct(ctx, task, res)
	default:
		err = a.runNative(ctx, task, res)
	}
	if err != nil {
		res.Stopped = "error"
		return res, err
	}
	if a.Guard != nil {
		out := a.Guard.CheckOutput(res.Answer)
		if out.Blocked {
			res.Answer = "[answer withheld: " + out.Reason + "]"
		} else {
			res.Answer = out.Text
		}
		res.Warnings = append(res.Warnings, out.Warnings...)
	}
	res.Cost = tokens.FormatCost(res.Model, res.Usage.PromptTokens, res.Usage.CompletionTokens)
	return res, nil
}

func (a *Agent) record(res *Result, s Step) {
	s.N = len(res.Steps) + 1
	res.Steps = append(res.Steps, s)
	res.Usage.PromptTokens += s.Usage.PromptTokens
	res.Usage.CompletionTokens += s.Usage.CompletionTokens
	res.Usage.TotalTokens += s.Usage.TotalTokens
	res.Usage.Estimated = res.Usage.Estimated || s.Usage.Estimated
	if a.Trace != nil {
		a.Trace(s)
	}
}

// observe screens a tool result before the model sees it: secrets are
// redacted and injection attempts inside tool output are flagged (a pod
// annotation or a ConfigMap can carry an attack just as a web page can).
func (a *Agent) observe(label, out string, res *Result) string {
	out = safety.RedactSecrets(out).Text
	if inj := safety.DetectInjection(out); inj.Score >= 0.6 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("tool %s returned text matching injection patterns (%v)", label, inj.Matches))
	}
	return safety.WrapUntrusted(label, out)
}
