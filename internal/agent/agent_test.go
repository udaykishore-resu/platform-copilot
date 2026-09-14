package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/udaykishore-resu/platform-copilot/internal/embeddings"
	"github.com/udaykishore-resu/platform-copilot/internal/llm"
	"github.com/udaykishore-resu/platform-copilot/internal/rag"
	"github.com/udaykishore-resu/platform-copilot/internal/safety"
	"github.com/udaykishore-resu/platform-copilot/internal/vectorstore"
)

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	emb := embeddings.NewMock(128)
	store, _ := vectorstore.NewMemory("")
	ctx := context.Background()
	texts := []string{
		"Runbook › Branch A OOMKilled\nRaise the memory limit to 1.5Gi and roll back the ConfigMap to v41 (LEDGER_BATCH_SIZE=500).",
		"Runbook › Node NotReady\nAn m6i.2xlarge supports 58 usable pod IPs.",
	}
	vecs, _ := emb.Embed(ctx, texts)
	var docs []vectorstore.Document
	for i, tx := range texts {
		docs = append(docs, vectorstore.Document{ID: string(rune('a' + i)), Text: tx, Vector: vecs[i], Metadata: map[string]string{"source": "runbook.md"}})
	}
	_ = store.Upsert(ctx, docs)
	retr := &rag.Retriever{Embedder: emb, Store: store, Hybrid: true, TopK: 2}
	k := &Kubectl{Fake: DemoCluster()}
	reg := NewRegistry().Register(SearchDocs(retr)).Register(Calc())
	for _, tool := range k.Tools() {
		reg.Register(tool)
	}
	reg.Register((&PromQL{Fake: true}).Tool())
	return reg
}

func TestAgentBothModesEndToEnd(t *testing.T) {
	for _, mode := range []Mode{ModeNative, ModeReAct} {
		a := &Agent{LLM: llm.NewMock(""), Tools: testRegistry(t), Mode: mode, MaxSteps: 5, Guard: safety.DefaultGuard()}
		res, err := a.Run(context.Background(), "Why is the payments-api pod crashlooping?")
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if res.Stopped != "final" || len(res.Steps) < 2 {
			t.Fatalf("%s: expected a multi-step final run, got stopped=%s steps=%d", mode, res.Stopped, len(res.Steps))
		}
		usedSearch, usedKubectl := false, false
		for _, s := range res.Steps {
			usedSearch = usedSearch || s.Tool == "search_docs"
			usedKubectl = usedKubectl || s.Tool == "kubectl_get"
		}
		if !usedSearch || !usedKubectl {
			t.Fatalf("%s: expected search_docs then kubectl_get, got %+v", mode, res.Steps)
		}
		if !strings.Contains(res.Answer, "1.5Gi") || !strings.Contains(res.Answer, "CrashLoopBackOff") {
			t.Fatalf("%s: answer lacks runbook + live evidence:\n%s", mode, res.Answer)
		}
		if res.Usage.TotalTokens == 0 {
			t.Fatalf("%s: usage not accumulated", mode)
		}
	}
}

func TestKubectlAllowlist(t *testing.T) {
	k := &Kubectl{Fake: DemoCluster(), Namespaces: []string{"payments"}}
	ctx := context.Background()
	if _, err := k.run(ctx, "delete", map[string]any{"resource": "pods"}); err == nil {
		t.Fatal("delete must be refused")
	}
	if _, err := k.run(ctx, "get", map[string]any{"resource": "secrets"}); err == nil {
		t.Fatal("secrets are not in the read allowlist")
	}
	if _, err := k.run(ctx, "get", map[string]any{"resource": "pods", "namespace": "kube-system"}); err == nil {
		t.Fatal("namespace outside allowlist must be refused")
	}
	if _, err := k.run(ctx, "get", map[string]any{"resource": "pods", "name": "x; rm -rf /"}); err == nil {
		t.Fatal("shell metacharacters must be rejected")
	}
	out, err := k.run(ctx, "get", map[string]any{"resource": "pods", "namespace": "payments"})
	if err != nil || !strings.Contains(out, "CrashLoopBackOff") {
		t.Fatalf("expected demo output, got %v %q", err, out)
	}
}

func TestCalcAndReActArgParsing(t *testing.T) {
	for expr, want := range map[string]float64{"(6*58)-(3*4)": 336, "2+3*4": 14, "-(2+3)": -5, "10%3": 1} {
		if v, err := evalExpr(expr); err != nil || v != want {
			t.Errorf("%s = %v (%v), want %v", expr, v, err, want)
		}
	}
	if _, err := evalExpr("1/0"); err == nil {
		t.Error("division by zero must error")
	}
	reg := testRegistry(t)
	args := toJSONArgs("pods -n payments", reg, "kubectl_get")
	if !strings.Contains(string(args), `"namespace":"payments"`) || !strings.Contains(string(args), `"resource":"pods"`) {
		t.Fatalf("kubectl arg parsing: %s", args)
	}
	if got := toJSONArgs("memory limit", reg, "search_docs"); !strings.Contains(string(got), `"query":"memory limit"`) {
		t.Fatalf("bare string mapping: %s", got)
	}
}
