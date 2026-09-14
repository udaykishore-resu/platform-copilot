// Command copilot is the platform/SRE knowledge copilot CLI. Each subcommand
// corresponds to a stage of the roadmap.sh AI Engineer roadmap; run with no
// arguments for the list.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/udaykishore-resu/platform-copilot/internal/agent"
	"github.com/udaykishore-resu/platform-copilot/internal/config"
	"github.com/udaykishore-resu/platform-copilot/internal/embeddings"
	"github.com/udaykishore-resu/platform-copilot/internal/llm"
	"github.com/udaykishore-resu/platform-copilot/internal/multimodal"
	"github.com/udaykishore-resu/platform-copilot/internal/rag"
	"github.com/udaykishore-resu/platform-copilot/internal/rag/eval"
	"github.com/udaykishore-resu/platform-copilot/internal/safety"
	"github.com/udaykishore-resu/platform-copilot/internal/server"
	"github.com/udaykishore-resu/platform-copilot/internal/tokens"
	"github.com/udaykishore-resu/platform-copilot/internal/vectorstore"
)

var version = "dev"

const usage = `platform-copilot — an AI copilot for platform & SRE teams, built along the roadmap.sh AI Engineer roadmap.

Usage: copilot <command> [flags]

Knowledge (Modules 03–05)
  ingest [dir]            chunk, embed and index documents (default: data/knowledge)
  search <query>          retrieve the top-k chunks without generating (--k, --explain, --source)
  ask <question>          retrieval-augmented answer with citations (--top-k, --source, --user, --print-prompt)
  chat                    multi-turn RAG conversation (type /quit to exit)

Agents (Module 06)
  agent <task>            tool-using agent over docs + read-only cluster (--mode react|native, --trace file.json)

Multimodal (Module 07)
  vision <image|video> [q] read a dashboard screenshot or a screen recording (--json structured reading, --every 5s frame sampling, --generate "prompt" for DALL-E)
  transcribe <audio>      speech-to-text via Whisper (--prompt "vocabulary hints")
  speak <text>            text-to-speech to an mp3 (--out summary.mp3 --voice alloy)
  diagram <description>   text → Mermaid architecture diagram (the right tool for diagrams; see Module 07)

Platform (Modules 01, 08, 09)
  models                  list known models with indicative prices and context windows
  tokens <file|text>      estimate tokens and cost for each model
  embed <text>            print an embedding vector (and its dimensions)
  moderate <text>         run the moderation + injection + PII checks on a text
  eval [golden.jsonl]     run the RAG evaluation harness (--min-hit 0.8 --min-answer 0.7 to gate)
  serve                   HTTP API on COPILOT_LISTEN (default :8080)
  version

Environment: COPILOT_PROVIDER=mock|openai|anthropic|gemini|ollama (default mock — no keys needed),
COPILOT_EMBED_PROVIDER, COPILOT_VECTORSTORE=memory|qdrant|chroma, see ARCHITECTURE.md for all variables.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cfg := config.Load()
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "ingest":
		err = cmdIngest(ctx, cfg, args)
	case "search":
		err = cmdSearch(ctx, cfg, args)
	case "ask":
		err = cmdAsk(ctx, cfg, args)
	case "chat":
		err = cmdChat(ctx, cfg, args)
	case "agent":
		err = cmdAgent(ctx, cfg, args)
	case "vision":
		err = cmdVision(ctx, cfg, args)
	case "transcribe":
		err = cmdTranscribe(ctx, args)
	case "speak":
		err = cmdSpeak(ctx, args)
	case "diagram":
		err = cmdDiagram(ctx, cfg, args)
	case "models":
		err = cmdModels(cfg)
	case "tokens":
		err = cmdTokens(args)
	case "embed":
		err = cmdEmbed(ctx, cfg, args)
	case "moderate":
		err = cmdModerate(ctx, args)
	case "eval":
		err = cmdEval(ctx, cfg, args)
	case "serve":
		err = cmdServe(ctx, cfg, args)
	case "version", "--version", "-v":
		fmt.Println("platform-copilot", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// ---- wiring -------------------------------------------------------------

type deps struct {
	cfg      config.Config
	llm      llm.Provider
	embedder embeddings.Embedder
	store    vectorstore.Store
	retr     *rag.Retriever
	guard    *safety.Guard
	pipeline *rag.Pipeline
	tools    *agent.Registry
}

func wire(cfg config.Config, needLLM bool) (*deps, error) {
	d := &deps{cfg: cfg, guard: safety.DefaultGuard()}
	var err error
	if d.embedder, err = embeddings.New(embeddings.Options{Provider: cfg.EmbedProvider, Model: cfg.EmbedModel}); err != nil {
		return nil, err
	}
	if d.store, err = vectorstore.New(vectorstore.Options{Kind: cfg.VectorStore, Path: cfg.IndexPath, Dims: d.embedder.Dimensions()}); err != nil {
		return nil, err
	}
	d.retr = &rag.Retriever{Embedder: d.embedder, Store: d.store, Hybrid: cfg.Hybrid, TopK: cfg.TopK, MinScore: float32(cfg.MinScore)}
	if needLLM {
		p, err := llm.New(llm.Options{Provider: cfg.Provider, Model: cfg.Model})
		if err != nil {
			return nil, err
		}
		d.llm = llm.WithRetry(p, 3, 500*time.Millisecond)
		d.pipeline = &rag.Pipeline{LLM: d.llm, Retriever: d.retr, Guard: d.guard, User: cfg.User}
	}
	d.tools = agent.NewRegistry().Register(agent.SearchDocs(d.retr)).Register(agent.Calc())
	k := &agent.Kubectl{Context: cfg.KubeContext, Namespaces: cfg.Namespaces}
	if cfg.FakeKubectl {
		k.Fake = agent.DemoCluster()
	}
	for _, t := range k.Tools() {
		d.tools.Register(t)
	}
	d.tools.Register((&agent.PromQL{URL: cfg.PrometheusURL, Fake: cfg.FakeProm}).Tool())
	return d, nil
}

func ensureIndexed(ctx context.Context, d *deps) error {
	n, err := d.store.Count(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		fmt.Fprintf(os.Stderr, "index is empty — ingesting %s first\n", d.cfg.KnowledgeDir)
		return ingest(ctx, d, d.cfg.KnowledgeDir)
	}
	return nil
}

func ingest(ctx context.Context, d *deps, dir string) error {
	in := &rag.Ingester{Embedder: d.embedder, Store: d.store, Log: func(f string, a ...any) { fmt.Fprintf(os.Stderr, "  "+f+"\n", a...) }}
	st, err := in.Ingest(ctx, dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "ingested %d files → %d chunks (%d unchanged) with %s/%s (%d dims) into %s in %s\n",
		st.Files, st.Chunks, st.Skipped, d.embedder.Name(), st.Embedder, st.Dims, d.store.Name(), st.Duration.Round(time.Millisecond))
	return nil
}

// ---- commands -----------------------------------------------------------

func cmdIngest(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	chunk := fs.Int("chunk-tokens", 400, "target chunk size in tokens")
	overlap := fs.Int("overlap", 60, "overlap in tokens")
	_ = fs.Parse(args)
	dir := cfg.KnowledgeDir
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	d, err := wire(cfg, false)
	if err != nil {
		return err
	}
	in := &rag.Ingester{Embedder: d.embedder, Store: d.store, Chunker: rag.Chunker{MaxTokens: *chunk, Overlap: *overlap},
		Log: func(f string, a ...any) { fmt.Fprintf(os.Stderr, "  "+f+"\n", a...) }}
	st, err := in.Ingest(ctx, dir)
	if err != nil {
		return err
	}
	fmt.Printf("ingested %d files → %d chunks (%d unchanged) with %s/%s (%d dims) into %s in %s\n",
		st.Files, st.Chunks, st.Skipped, d.embedder.Name(), st.Embedder, st.Dims, d.store.Name(), st.Duration.Round(time.Millisecond))
	return nil
}

func cmdSearch(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	k := fs.Int("k", cfg.TopK, "results")
	explain := fs.Bool("explain", false, "show per-list scores (vector vs bm25)")
	source := fs.String("source", "", "filter by source_type: runbook|postmortem|kubernetes|terraform|policy|doc")
	dense := fs.Bool("dense-only", false, "disable hybrid (BM25) fusion")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: copilot search <query>")
	}
	d, err := wire(cfg, false)
	if err != nil {
		return err
	}
	if err := ensureIndexed(ctx, d); err != nil {
		return err
	}
	d.retr.TopK = *k
	if *dense {
		d.retr.Hybrid = false
	}
	if *source != "" {
		d.retr.Filter = map[string]string{"source_type": *source}
	}
	hits, err := d.retr.Retrieve(ctx, strings.Join(fs.Args(), " "))
	if err != nil {
		return err
	}
	for i, h := range hits {
		fmt.Printf("[%d] %.4f  %s", i+1, h.Score, h.Metadata["source"])
		if s := h.Metadata["section"]; s != "" {
			fmt.Printf(" › %s", s)
		}
		if *explain {
			fmt.Printf("   (vector=%s bm25=%s)", orDash(h.Metadata["score_vector"]), orDash(h.Metadata["score_bm25"]))
		}
		fmt.Println()
		fmt.Println(indent(truncateText(h.Text, 300)))
		fmt.Println()
	}
	if len(hits) == 0 {
		fmt.Println("no results")
	}
	return nil
}

func cmdAsk(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	k := fs.Int("top-k", cfg.TopK, "chunks to retrieve")
	source := fs.String("source", "", "filter by source_type")
	user := fs.String("user", cfg.User, "end-user ID forwarded to the provider")
	printPrompt := fs.Bool("print-prompt", false, "show the exact prompt sent to the model")
	speak := fs.Bool("speak", false, "also synthesise the answer to answer.mp3 (needs OPENAI_API_KEY)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: copilot ask <question>")
	}
	d, err := wire(cfg, true)
	if err != nil {
		return err
	}
	if err := ensureIndexed(ctx, d); err != nil {
		return err
	}
	d.retr.TopK = *k
	if *source != "" {
		d.retr.Filter = map[string]string{"source_type": *source}
	}
	d.pipeline.User = *user
	ans, err := d.pipeline.Ask(ctx, strings.Join(fs.Args(), " "), nil)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(ans)
	}
	if *printPrompt {
		fmt.Println("----- prompt -----")
		for _, m := range ans.Prompt {
			fmt.Printf("[%s]\n%s\n\n", m.Role, m.Content)
		}
		fmt.Println("----- /prompt -----")
	}
	fmt.Println(ans.Text)
	fmt.Println()
	fmt.Print(rag.FormatSources(ans.Sources))
	fmt.Printf("model=%s/%s tokens=%d+%d cost=%s latency=%s\n", cfg.Provider, ans.Model, ans.Usage.PromptTokens, ans.Usage.CompletionTokens, ans.Cost, ans.Latency.Round(time.Millisecond))
	for _, w := range ans.Warnings {
		fmt.Println("warning:", w)
	}
	if *speak {
		if err := multimodal.NewOpenAIAudio().Speak(ctx, ans.Text, "alloy", "answer.mp3"); err != nil {
			return err
		}
		fmt.Println("wrote answer.mp3")
	}
	return nil
}

func cmdChat(ctx context.Context, cfg config.Config, args []string) error {
	d, err := wire(cfg, true)
	if err != nil {
		return err
	}
	if err := ensureIndexed(ctx, d); err != nil {
		return err
	}
	fmt.Printf("platform-copilot chat (%s). Type /quit to exit, /sources to toggle citations.\n", cfg.Provider)
	var history []llm.Message
	showSources := true
	sc := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\nyou> ")
		if !sc.Scan() {
			return nil
		}
		q := strings.TrimSpace(sc.Text())
		switch q {
		case "":
			continue
		case "/quit", "/exit":
			return nil
		case "/sources":
			showSources = !showSources
			continue
		}
		ans, err := d.pipeline.Ask(ctx, q, history)
		if err != nil {
			fmt.Println("error:", err)
			continue
		}
		fmt.Println("\ncopilot>", ans.Text)
		if showSources {
			fmt.Print(rag.FormatSources(ans.Sources))
		}
		history = append(history, llm.User(q), llm.Assistant(ans.Text))
		if len(history) > 20 { // keep the last 10 turns; the budget trims further if needed
			history = history[len(history)-20:]
		}
	}
}

func cmdAgent(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	mode := fs.String("mode", "native", "react|native")
	steps := fs.Int("max-steps", 6, "maximum tool calls")
	trace := fs.String("trace", "", "write the full step trace to this JSON file")
	user := fs.String("user", cfg.User, "end-user ID")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: copilot agent <task>")
	}
	d, err := wire(cfg, true)
	if err != nil {
		return err
	}
	if err := ensureIndexed(ctx, d); err != nil {
		return err
	}
	a := &agent.Agent{LLM: d.llm, Tools: d.tools, Mode: agent.Mode(*mode), MaxSteps: *steps, Guard: d.guard, User: *user,
		Trace: func(s agent.Step) {
			if s.Tool != "" {
				fmt.Fprintf(os.Stderr, "step %d  %s(%s)\n%s\n\n", s.N, s.Tool, s.Args, indent(truncateText(s.Observation, 600)))
			}
		}}
	res, err := a.Run(ctx, strings.Join(fs.Args(), " "))
	if *trace != "" && res != nil {
		b, _ := json.MarshalIndent(res, "", "  ")
		_ = os.WriteFile(*trace, b, 0o644)
	}
	if err != nil {
		return err
	}
	fmt.Println(res.Answer)
	fmt.Printf("\nmode=%s steps=%d stopped=%s tokens=%d cost=%s\n", res.Mode, len(res.Steps), res.Stopped, res.Usage.TotalTokens, res.Cost)
	for _, w := range res.Warnings {
		fmt.Println("warning:", w)
	}
	return nil
}

func cmdVision(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("vision", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "structured DashboardReading output")
	generate := fs.String("generate", "", "generate an image from this prompt instead (DALL-E)")
	every := fs.Duration("every", 5*time.Second, "for video input: sample one frame every N seconds")
	_ = fs.Parse(args)
	if *generate != "" {
		url, err := multimodal.NewOpenAIAudio().GenerateImage(ctx, *generate, "", "")
		if err != nil {
			return err
		}
		fmt.Println(url)
		return nil
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: copilot vision <image.png|video.mp4> [question]")
	}
	p, err := llm.New(llm.Options{Provider: cfg.Provider, Model: cfg.Model})
	if err != nil {
		return err
	}
	if cfg.Provider == "mock" {
		fmt.Fprintln(os.Stderr, "note: the mock provider cannot see images; set COPILOT_PROVIDER=openai|anthropic|gemini|ollama (e.g. llava) for real vision")
	}
	switch strings.ToLower(filepath.Ext(fs.Arg(0))) {
	case ".mp4", ".mov", ".webm", ".mkv":
		frames, err := multimodal.SampleFrames(ctx, fs.Arg(0), *every, 10)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "sampled %d frames\n", len(frames))
		resp, err := multimodal.DescribeVideo(ctx, p, frames, "", strings.Join(fs.Args()[1:], " "), cfg.User)
		if err != nil {
			return err
		}
		fmt.Println(resp.Content)
		return nil
	}
	img, err := multimodal.LoadImage(fs.Arg(0))
	if err != nil {
		return err
	}
	if *asJSON {
		r, resp, err := multimodal.ReadDashboard(ctx, p, img, cfg.User)
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(b))
		fmt.Fprintf(os.Stderr, "tokens=%d+%d\n", resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
		return nil
	}
	resp, err := multimodal.DescribeImage(ctx, p, img, strings.Join(fs.Args()[1:], " "), cfg.User)
	if err != nil {
		return err
	}
	fmt.Println(resp.Content)
	fmt.Fprintf(os.Stderr, "tokens=%d+%d cost=%s\n", resp.Usage.PromptTokens, resp.Usage.CompletionTokens, tokens.FormatCost(resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens))
	return nil
}

func cmdTranscribe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("transcribe", flag.ExitOnError)
	prompt := fs.String("prompt", "payments-api, ledger-worker, checkout-web, EKS, Kubernetes, PromQL", "vocabulary hints")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: copilot transcribe <audio-file>")
	}
	t, err := multimodal.NewOpenAIAudio().Transcribe(ctx, fs.Arg(0), *prompt)
	if err != nil {
		return err
	}
	fmt.Println(t.Text)
	return nil
}

func cmdSpeak(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("speak", flag.ExitOnError)
	out := fs.String("out", "speech.mp3", "output file")
	voice := fs.String("voice", "alloy", "voice")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: copilot speak <text>")
	}
	if err := multimodal.NewOpenAIAudio().Speak(ctx, strings.Join(fs.Args(), " "), *voice, *out); err != nil {
		return err
	}
	fmt.Println("wrote", *out)
	return nil
}

func cmdDiagram(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: copilot diagram <description>")
	}
	p, err := llm.New(llm.Options{Provider: cfg.Provider, Model: cfg.Model})
	if err != nil {
		return err
	}
	resp, err := p.Complete(ctx, llm.Request{Messages: []llm.Message{
		llm.System("You produce Mermaid diagrams for platform architecture. Reply with ONLY a ```mermaid code block. Use flowchart LR unless a sequence is described. Label every edge."),
		llm.User(strings.Join(args, " ")),
	}, MaxTokens: 800, User: cfg.User})
	if err != nil {
		return err
	}
	if cfg.Provider == "mock" {
		fmt.Println("```mermaid\nflowchart LR\n  U[User] -->|question| C[copilot]\n  C -->|embed| E[Embedder]\n  C -->|search| V[(Vector store)]\n  C -->|prompt + context| L[LLM]\n  L -->|answer + citations| U\n```")
		return nil
	}
	fmt.Println(resp.Content)
	return nil
}

func cmdModels(cfg config.Config) error {
	fmt.Printf("%-26s %10s %10s  %s\n", "MODEL (prefix)", "$/1M in", "$/1M out", "NOTE")
	for _, m := range tokens.Models() {
		p := tokens.Table[m]
		fmt.Printf("%-26s %10.2f %10.2f  %s\n", m, p.Input, p.Output, p.Note)
	}
	fmt.Printf("\nActive: provider=%s model=%s embed=%s/%s store=%s\n", cfg.Provider, orDefault(cfg.Model), cfg.EmbedProvider, orDefault(cfg.EmbedModel), cfg.VectorStore)
	fmt.Println("Prices are indicative list prices at time of writing — confirm on the vendor pricing page.")
	return nil
}

func cmdTokens(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: copilot tokens <file|text>")
	}
	text := strings.Join(args, " ")
	if data, err := os.ReadFile(args[0]); err == nil && len(args) == 1 {
		text = string(data)
	}
	n := tokens.Estimate(text)
	fmt.Printf("chars=%d words=%d estimated_tokens=%d (±10%% prose, ±25%% code/YAML)\n\n", len(text), len(strings.Fields(text)), n)
	fmt.Printf("%-26s %12s %12s\n", "MODEL", "as input", "if output")
	for _, m := range []string{"gpt-4o-mini", "gpt-4o", "gpt-4.1", "claude-sonnet-4", "claude-opus-4", "gemini-2.5-flash", "gemini-2.5-pro", "llama"} {
		fmt.Printf("%-26s %12s %12s\n", m, tokens.FormatCost(m, n, 0), tokens.FormatCost(m, 0, n))
	}
	return nil
}

func cmdEmbed(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: copilot embed <text>")
	}
	e, err := embeddings.New(embeddings.Options{Provider: cfg.EmbedProvider, Model: cfg.EmbedModel})
	if err != nil {
		return err
	}
	vecs, err := e.Embed(ctx, []string{strings.Join(args, " ")})
	if err != nil {
		return err
	}
	v := vecs[0]
	fmt.Printf("model=%s/%s dims=%d\n", e.Name(), e.Model(), len(v))
	show := v
	if len(show) > 12 {
		show = show[:12]
	}
	fmt.Printf("%v …\n", show)
	return nil
}

func cmdModerate(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: copilot moderate <text>")
	}
	text := strings.Join(args, " ")
	inj := safety.DetectInjection(text)
	fmt.Printf("injection: suspicious=%v score=%.2f matches=%v\n", inj.Suspicious, inj.Score, inj.Matches)
	pii := safety.RedactPII(text)
	sec := safety.RedactSecrets(text)
	fmt.Printf("pii: %v\nsecrets: %v\n", pii.Counts, sec.Counts)
	mod := safety.NewModerator()
	res, err := mod.Moderate(ctx, text)
	if err != nil {
		return err
	}
	fmt.Printf("moderation(%s): flagged=%v", res.Provider, res.Flagged)
	for k, v := range res.Categories {
		if v >= 0.2 {
			fmt.Printf(" %s=%.2f", k, v)
		}
	}
	fmt.Println()
	g := safety.DefaultGuard().CheckInput(text)
	fmt.Printf("guard: blocked=%v reason=%q warnings=%v\n", g.Blocked, g.Reason, g.Warnings)
	return nil
}

func cmdEval(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	minHit := fs.Float64("min-hit", 0, "fail if hit@k below this")
	minAns := fs.Float64("min-answer", 0, "fail if answer accuracy below this")
	out := fs.String("out", "", "write outcomes JSON here")
	_ = fs.Parse(args)
	path := "data/eval/golden.jsonl"
	if fs.NArg() > 0 {
		path = fs.Arg(0)
	}
	cases, err := eval.LoadCases(path)
	if err != nil {
		return err
	}
	d, err := wire(cfg, true)
	if err != nil {
		return err
	}
	if err := ensureIndexed(ctx, d); err != nil {
		return err
	}
	outs, sum, err := eval.Run(ctx, d.pipeline, cases, func(f string, a ...any) { fmt.Printf(f+"\n", a...) })
	if err != nil {
		return err
	}
	fmt.Printf("\ncases=%d hit@k=%.2f mrr=%.2f answer_acc=%.2f abstain_acc=%.2f mean_latency=%.0fms provider=%s embed=%s hybrid=%v\n",
		sum.Cases, sum.HitAtK, sum.MRR, sum.AnswerAcc, sum.AbstainAcc, sum.MeanLatMs, cfg.Provider, cfg.EmbedProvider, cfg.Hybrid)
	if *out != "" {
		b, _ := json.MarshalIndent(map[string]any{"summary": sum, "outcomes": outs}, "", "  ")
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			return err
		}
	}
	return eval.Gate(sum, *minHit, *minAns)
}

func cmdServe(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("listen", cfg.ListenAddr, "listen address")
	_ = fs.Parse(args)
	d, err := wire(cfg, true)
	if err != nil {
		return err
	}
	if err := ensureIndexed(ctx, d); err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	a := &agent.Agent{LLM: d.llm, Tools: d.tools, Mode: agent.ModeNative, MaxSteps: 6, Guard: d.guard}
	s := server.New(server.Deps{Pipeline: d.pipeline, Agent: a, Log: log, Version: version})
	errc := make(chan error, 1)
	go func() { errc <- s.ListenAndServe(*addr) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		return err
	}
}

// ---- helpers ------------------------------------------------------------

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n    ")
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func orDefault(s string) string {
	if s == "" {
		return "(default)"
	}
	return s
}
