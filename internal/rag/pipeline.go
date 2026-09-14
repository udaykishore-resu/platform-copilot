package rag

import (
	"context"
	"fmt"
	"time"

	"github.com/udaykishore-resu/platform-copilot/internal/llm"
	"github.com/udaykishore-resu/platform-copilot/internal/safety"
	"github.com/udaykishore-resu/platform-copilot/internal/tokens"
	"github.com/udaykishore-resu/platform-copilot/internal/vectorstore"
)

// Pipeline is the end-to-end "ask" path: guard → retrieve → assemble →
// generate → guard. It is the code every framework (LangChain, LlamaIndex)
// generates for you; here it is ~60 lines you can read.
type Pipeline struct {
	LLM       llm.Provider
	Retriever *Retriever
	Guard     *safety.Guard
	MaxTokens int
	// User is the end-user ID forwarded to the provider.
	User string
}

// Answer is the result of one question.
type Answer struct {
	Text     string
	Sources  []vectorstore.Hit
	Usage    llm.Usage
	Model    string
	Cost     string
	Latency  time.Duration
	Prompt   []llm.Message // the exact messages sent (for --print-prompt)
	Warnings []string
}

// Ask runs one RAG turn. history is prior user/assistant turns (may be nil).
func (p *Pipeline) Ask(ctx context.Context, question string, history []llm.Message) (*Answer, error) {
	start := time.Now()
	ans := &Answer{}

	if p.Guard != nil {
		res := p.Guard.CheckInput(question)
		if res.Blocked {
			return nil, fmt.Errorf("request blocked by safety guard: %s", res.Reason)
		}
		ans.Warnings = append(ans.Warnings, res.Warnings...)
		question = res.Text
	}

	hits, err := p.Retriever.Retrieve(ctx, question)
	if err != nil {
		return nil, err
	}

	model := p.LLM.DefaultModel()
	maxOut := p.MaxTokens
	if maxOut == 0 {
		maxOut = 600
	}
	budget := tokens.Budget{ContextWindow: p.LLM.ContextWindow(model), ReserveOutput: maxOut}
	msgs, included := BuildPrompt(question, hits, history, budget)
	ans.Prompt = msgs
	ans.Sources = included

	resp, err := p.LLM.Complete(ctx, llm.Request{Model: model, Messages: msgs, MaxTokens: maxOut, Temperature: 0.1, User: p.User})
	if err != nil {
		return nil, err
	}
	ans.Text = resp.Content
	if p.Guard != nil {
		out := p.Guard.CheckOutput(resp.Content)
		if out.Blocked {
			ans.Text = "[response withheld: " + out.Reason + "]"
		} else {
			ans.Text = out.Text
		}
		ans.Warnings = append(ans.Warnings, out.Warnings...)
	}
	ans.Usage = resp.Usage
	ans.Model = firstNonEmpty(resp.Model, model)
	ans.Cost = tokens.FormatCost(ans.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	ans.Latency = time.Since(start)
	return ans, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
