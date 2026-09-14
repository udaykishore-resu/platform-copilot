# Module 09 · Development Tools

> **Roadmap nodes covered:** AI Code Editors, Code Completion Tools — plus the roadmap's closing material: agentic coding CLIs, an evaluation harness for the capstone, CI for LLM applications, and the two "Continue learning" tracks (AI & Data Scientist, Prompt Engineering)
>
> **Capstone step:** adds `internal/config` (`Load() Config`, `COPILOT_PROVIDER=mock` as the zero-key default so every tool and CI job runs without credentials), the `copilot models` and `copilot tokens` subcommands, the evaluation harness under `internal/rag/eval/` with `make eval`, the GitHub Actions workflow in `.github/workflows/ci.yml` (lint, unit tests, adversarial suite, eval on mock, roadmap-coverage and docs build, nightly eval on a real provider), `ARCHITECTURE.md` / `CONTRIBUTING.md` as the contract the agentic tools used to build the repo were pointed at, and the two Continue Learning cards below
>
> **Time:** ~4 hours · **Prerequisites:** Module 08

## Why this module exists

The first eight modules build the product. This one is about the workshop: the editors, completion engines and agentic CLIs that wrote much of the capstone's code, and the harness that keeps the capstone honest as models, prompts and dependencies change underneath it. An engineer who has not thought about this ships an LLM application whose behaviour silently drifts with every provider model update, and develops it with tools whose output they cannot evaluate. After this module, the same engineer can choose a coding assistant on evidence rather than marketing, set it up so it produces code that fits the repo's contracts, and wire an evaluation loop so "did this prompt change make answers better or worse?" is a number in a pull request, not an opinion in a review.

Two things make LLM applications different to test. First, outputs are non-deterministic and graded on a spectrum, so a green unit test says little about answer quality — you need an evaluation set, metrics that tolerate paraphrase, and a baseline to compare against. Second, the system has an external dependency that changes without a version bump in your `go.mod`: the model. The capstone answers both with the mock provider (deterministic, free, runs on every PR) and a nightly job against real providers that compares against committed baselines. That split — fast deterministic CI plus slow probabilistic nightly — is the pattern to take away.

The module closes with the two learning tracks the roadmap points at next. Both are framed through what the capstone exposed as gaps: the evaluation and statistics that an AI & Data Scientist track would sharpen, and the systematic prompt design that the Prompt Engineering track formalises.

## Concept cards

### AI Code Editors

**What it is.** An AI code editor is an IDE with a model integrated into the editing loop beyond inline completion: chat grounded in the open repository (with retrieval over the codebase), multi-file edits proposed as diffs, an "agent" mode that runs commands, reads their output and iterates, and project-level instruction files the model reads on every request. Architecturally they combine a local index of the repo (embeddings and symbol graphs), context assembly (open files, diagnostics, terminal output, explicit `@file` references), one or more hosted or local models, and an apply/review UI. The instruction file (`.cursor/rules`, `.windsurfrules`, `AGENTS.md`, JetBrains' guidelines) is the part the engineer controls and the part that determines whether generated code matches the repo's conventions.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Cursor | Mature agent mode and multi-file Composer; strong codebase indexing; `.cursor/rules` with glob scoping; model choice (OpenAI, Anthropic, Gemini, own) | VS Code fork — extension lag and a second editor to maintain; subscription plus usage-based pricing for premium models; index uploads code hashes to their servers (privacy mode available) | A team already on VS Code that wants the strongest agentic editing today |
| Windsurf (Codeium) | Cascade agent with deep terminal integration; good at long multi-step tasks; generous tier | Also a VS Code fork; ownership changes in 2025 created roadmap uncertainty | Same as Cursor; compare on your own repo — they leapfrog each other quarterly |
| Zed | Native, very fast; open source; agent panel with model choice including local; collaborative editing built in | Smaller extension ecosystem; fewer language server integrations than VS Code; Linux/macOS first | Performance-sensitive engineers who want open source and local models |
| JetBrains AI Assistant / Junie | Deep semantic understanding from JetBrains' own indexes (refactorings are type-aware); integrated into GoLand/IntelliJ you already use | Model choice narrower; separate subscription; agent mode (Junie) newer | Teams standardised on JetBrains, especially for Go and JVM |
| VS Code + GitHub Copilot (Chat, Edits, agent mode) | No new editor; enterprise policy controls; org-wide rollout; agent mode and MCP support now in the base product | Agentic features arrived later and are still catching up; model selection limited to what GitHub offers | Organisations that need centralised governance and already license Copilot |

**Why it wins (and when it doesn't).** There is no durable winner; capabilities converge within months and the right choice is the one your team evaluates on its own repo with the same task list. What does not converge is governance: if the organisation needs to control which models see which code, VS Code plus Copilot (or JetBrains) with enterprise policies wins regardless of feature gap, and Cursor/Windsurf win only where individuals choose their tools. The capstone was written with an agentic editor for the bulk of `internal/` and reviewed by hand; the instruction file was the highest-leverage configuration — without it the editor generated SDK-based provider clients, which ADR-0009 forbids, in the first three attempts. Every editor here respects some form of repo-level rules; invest there before comparing benchmarks.

**Problem it solves → value added.** The failure mode without an AI editor is not slowness; it is *inconsistency* — each package written by a different hand in a different style. With a rules file that states the package contract from `ARCHITECTURE.md` (interfaces first, raw HTTP, mock provider must work, table-driven tests), the editor generated adapters for four providers that share one shape, and the time from "ADR written" to "adapter with tests" fell from a day to an hour. The value is measured in review comments, which dropped once the rules file existed, more than in typing speed.

**In the capstone.** `ARCHITECTURE.md` and `CONTRIBUTING.md` state the package contract and the forbidden shortcuts; point your editor's rules file (`.cursor/rules`, `AGENTS.md`) at them so the editor retrieves them. This node is conceptual for the Go code.

### Code Completion Tools

**What it is.** Code completion tools predict the next tokens at the cursor — single line, multi-line block, or "next edit" suggestions elsewhere in the file — using a fill-in-the-middle model conditioned on the prefix, suffix, open tabs and recent edits. They run as editor plugins and optimise for latency (sub-second) and acceptance rate rather than reasoning depth; the model is typically small and specialised, and the product engineering is in context selection and ranking. Most now bundle a chat sidebar, but completion is the distinctive capability and the one with the clearest metrics (acceptance rate, characters accepted, retention of accepted code).

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| GitHub Copilot | Best-integrated with GitHub (PR summaries, code review, Actions); enterprise controls, IP indemnity, content exclusion; next-edit suggestions | Completion model is not switchable; usage caps on premium models | Default for organisations on GitHub |
| Codeium (now Windsurf plugin) | Free individual tier; wide IDE support including JetBrains, Vim, Emacs; self-hosted enterprise option | Product focus shifted to the Windsurf editor | Mixed-editor teams; self-hosted requirement |
| Tabnine | Privacy-first: fully air-gapped deployment; models trained only on permissively licensed code; per-team fine-tuning | Completion quality behind the leaders on general code | Regulated environments that cannot send code out |
| Continue | Open source; bring-your-own model (Ollama, vLLM, any API); configurable context providers; works in VS Code and JetBrains | You operate the model; quality depends on the model you choose; less polish | Local models, full control, or as the way to use a fine-tuned completion model |
| Amazon Q Developer (formerly CodeWhisperer) | Deep AWS API knowledge; security scanning; IAM-integrated; free tier | Weaker outside AWS-heavy code; agent features tied to AWS tooling | AWS-centric teams |

**Why it wins (and when it doesn't).** Copilot wins on governance and integration for most organisations, and that matters more than a few points of acceptance rate. Tabnine and self-hosted Continue win wherever code must not leave the network — for a platform team whose repos contain Terraform for production accounts, that is a serious constraint and the honest answer is often "Continue with a local Qwen2.5-Coder or DeepSeek-Coder model", accepting lower quality for zero egress. Amazon Q wins narrowly on AWS SDK-heavy code. The completion tool matters less than the editor-level agent for a project like the capstone, where most work is designing interfaces and writing tests; completion shines on boilerplate — the provider adapters' request/response structs and the table-driven test cases were largely completions.

**Problem it solves → value added.** Completion removes the typing cost of boilerplate, which in Go is considerable: struct definitions mirroring JSON wire formats, error wrapping, table-driven tests. On the capstone, measured by accepted characters, roughly a third of the Go in `internal/llm/*.go` was completion output. The risk it introduces is plausible-looking wrong code — a completed `json:"tool_call_id"` tag where Anthropic's field is differently named — which is why every adapter's wire structs are kept next to the request-building code in one file, and the shared behaviour (retry, structured output, tool calls) is tested against the mock in `internal/llm/mock_test.go`.

**In the capstone.** Conceptual. The tests that catch completion errors in the shared provider code are in `internal/llm/mock_test.go`; per-adapter wire-format tests against recorded responses are the natural addition. Continue with a local model is `COPILOT_PROVIDER=ollama`'s editor-side twin.

### Agentic CLIs

**What it is.** Agentic coding CLIs run an agent loop in the terminal with the repository as the environment: they read files, run tests and build commands, inspect output, edit files, and iterate toward a stated goal, with permission prompts before commands or file writes. They differ from editor agents in being scriptable (usable in CI or from another tool), editor-independent, and typically more capable of long multi-step tasks because the terminal gives them ground truth from compilers and tests. Project-level instruction files (`CLAUDE.md`, `AGENTS.md`, `.aider.conf.yml`) play the same role as editor rules.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Claude Code | Strong on large multi-file refactors and on following a written contract; `CLAUDE.md` memory; MCP tool integration; hooks; non-interactive mode for CI | Anthropic models only; usage-based cost adds up on long sessions | Long tasks against a documented architecture — how most of this repo was built |
| OpenAI Codex CLI | Open source; sandboxed execution modes; OpenAI models; cloud-delegated runs | Younger; fewer integrations | OpenAI-standardised teams |
| Aider | Open source; any model including local; excellent git integration (every edit is a commit); repo map for context; cheap | Less autonomous — closer to pair programming than delegation; terminal-only UI | Budget-conscious or local-model use; engineers who want to stay in control of every edit |
| Gemini CLI | Open source; large context; generous free tier | Newer | Gemini-standardised teams |
| Editor agent modes (Cursor, Copilot agent) | Visual diff review | Not scriptable | Interactive work |

**Why it wins (and when it doesn't).** An agentic CLI wins for tasks with a clear spec and an objective check: "implement `internal/vectorstore/qdrant.go` against the `Store` interface, with tests that pass against the mock and skip without `QDRANT_URL`" is a task the CLI can run to a green test suite. It loses when the spec is vague — it will confidently build the wrong thing — and when the cost of a wrong action is high (never give it production credentials; the same read-only principle as ADR-0007 applies to the tools you build with). Aider's every-edit-is-a-commit model is the best fit when you want auditability over autonomy; Claude Code and Codex fit when you want to delegate and review the result. The capstone's instructions to the agent were short and mostly pointed at `ARCHITECTURE.md`, the ADRs and the module `TEMPLATE.md`; the single most useful line turned out to be "run `make test` and `make adversarial` before declaring a task done" (now `make check`).

**Problem it solves → value added.** The problem is the gap between a written architecture and working code across a dozen packages. The value on the capstone was concrete: the provider adapters, vector-store adapters and their tests were produced as a sequence of delegated tasks, each checked by the existing test suite, and the ADRs were written *first* precisely so the agent had a contract to implement. The cost was real (model usage measured in tens of dollars for the repo) and the review burden shifted from writing code to reading it.

**In the capstone.** `ARCHITECTURE.md`, `CONTRIBUTING.md`, `modules/TEMPLATE.md` as the contract; `make docs-check` (`scripts/check_roadmap_coverage.py`) is the objective check that every module covers its roadmap nodes. Conceptual for the Go code.

### Evaluation Harness

**What it is.** An evaluation harness for an LLM application is a dataset of inputs with expected outcomes, a runner that executes the application on each input, metrics that score the outputs, and a report that compares against a baseline. For RAG the metrics are retrieval recall@k and MRR (did the right chunk come back), faithfulness (is every claim supported by the retrieved context), answer correctness (against a reference, via exact match, token overlap, embedding similarity or an LLM judge), and citation accuracy. For agents add task success, steps, tool-call validity and cost. The harness is to an LLM app what a benchmark suite is to a database: the thing that makes a change defensible.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| In-repo harness in Go (capstone `internal/rag/eval`) | Runs on the mock provider in CI in seconds; same language as the product; no new service | You write the metrics; LLM-judge metrics need a real model | Always as the floor |
| promptfoo | Declarative YAML evals; many providers; red-team plugins; CI-friendly; good diff UI | Node dependency; evaluates prompts more naturally than whole applications | Prompt-level comparisons; red-team generation |
| Ragas / DeepEval / TruLens | RAG-specific metrics (faithfulness, context precision) implemented with LLM judges | Python; judge cost; metric definitions shift between versions | Python RAG stacks; richer RAG metrics |
| LangSmith / Langfuse / Braintrust / Arize Phoenix | Tracing plus datasets plus evals in one UI; production monitoring | Hosted service (Langfuse and Phoenix self-hostable); integration effort | You want production traces and evals in one place |
| OpenAI Evals / provider eval products | Close to the model; managed | Single provider | Fine-tuning workflows |

**Why it wins (and when it doesn't).** The in-repo harness wins because of the mock provider: with deterministic embeddings and canned completions, retrieval metrics are exact and the whole suite runs on every pull request with no key and no cost, which is the only way eval becomes a habit rather than a ceremony. It does not measure answer quality on a real model — for that the nightly job runs the same dataset against `COPILOT_PROVIDER=openai` and `anthropic`, computes correctness with an LLM judge, and fails if the score drops more than a threshold from the committed baseline. promptfoo or Langfuse become worth adding when several people need to inspect traces and compare prompts visually; the capstone keeps the harness in Go and exports results as JSON that any of those tools can import.

**Problem it solves → value added.** Without it, "I improved the chunker" is unverifiable and a model upgrade is a leap of faith. With it, the chunker change in Module 05 is a PR whose description says "hit@k 0.90 → 1.00 on the golden set, answer accuracy unchanged, mean latency +4%". That sentence is the value; it is also what makes the repo credible to someone deciding whether the author knows what they are doing.

**In the capstone.** `data/eval/golden.jsonl` (questions, `expect_source`, `expect_answer` substrings, `unanswerable` flag), `internal/rag/eval/run.go` (`LoadCases`, `Run`, `Gate`), `internal/rag/eval/metrics.go` (hit@k, MRR, answer accuracy, abstain accuracy), `make eval`, `COPILOT_PROVIDER=openai COPILOT_EMBED_PROVIDER=openai make eval-real`; `copilot agent --trace out.json` saves agent trajectories for a future agent suite.

### CI for LLM Apps

**What it is.** Continuous integration for an LLM application adds three things to an ordinary Go CI pipeline: deterministic tests of everything around the model using a mock provider (loop logic, parsing, budgets, safety, retrieval metrics); a gated, scheduled job against real providers with cost caps that compares to baselines rather than asserting exact outputs; and versioning of the non-code inputs — prompts, eval datasets, model identifiers — so that a behaviour change can be bisected to a commit. It also includes secret hygiene (keys only in the scheduled job's environment, never in PR builds from forks) and the adversarial suite as a required check.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Two-tier CI: mock on every PR, real models nightly with baselines — capstone | Fast PR feedback; cost bounded; drift detected within a day | Real-model regressions surface after merge, not before | Always; the default shape |
| Real models on every PR | Catches quality regressions before merge | Slow, expensive, flaky; keys exposed to PR builds | Small teams with tight budgets on a single provider and no fork PRs |
| Recorded-response replay (cassettes) | Deterministic tests of real wire formats without calls | Recordings go stale; must re-record on API changes | Adapter tests — the capstone uses this for `internal/llm` golden files |
| No model-facing tests; manual QA | Nothing to maintain | Drift is discovered by users | Never |

**Why it wins (and when it doesn't).** The two-tier shape wins because it matches the two kinds of change: code changes are deterministic and should be tested deterministically; model changes are external and should be monitored. It loses a little on latency of feedback for prompt changes — a prompt edit's real effect is known the next morning — which the capstone mitigates with a manual `workflow_dispatch` that runs the real-model eval on a branch with a spend cap. Recorded responses are the right tool for wire-format tests and the wrong tool for quality tests; the capstone uses both deliberately.

**Problem it solves → value added.** Without CI designed for LLM apps, either every PR costs money and flakes, or nothing model-facing is tested. With it, a PR gets unit tests, adversarial containment, and mock-provider eval in about two minutes with no credentials, and the team learns of a provider-side model change from a failed nightly job rather than from an incident. The cost of the nightly run (cents on ten questions) is bounded by the size of `data/eval/golden.jsonl` and `Pipeline.MaxTokens`.

**In the capstone.** `.github/workflows/ci.yml` (jobs: `go` — lint, test, build, `make eval`, `make adversarial`, `make run`; `course` — `make docs-check`, lab compile, `make docs-build`; `eval-real` — scheduled nightly against OpenAI when the secret is set), `Makefile` targets `test`, `adversarial`, `eval`, `eval-real`, `redteam`, `check`; prompts in `internal/rag/prompt.go` pinned by `internal/rag/prompts_test.go`; `internal/config` makes `mock` the default so CI needs no secrets.

### Continue Learning: AI & Data Scientist

**What it is.** The roadmap's AI & Data Scientist track covers what an AI engineer building applications tends to skip: statistics and experimental design, classical machine learning, model evaluation theory, feature engineering, and the mathematics behind embeddings and attention. It is the track that turns "the eval score went up" into "the eval score went up by more than the noise, on a sample large enough to matter".

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Follow an AI & Data Scientist track | Structured; free; complements this course directly | Long; much of it is not needed for application work | You want to own evaluation and fine-tuning decisions |
| Targeted study: statistics for A/B tests, evaluation metrics, information retrieval | Fast payoff for an AI engineer | Leaves gaps in ML fundamentals | You need to make eval conclusions rigorous now |
| Stay application-focused; rely on data scientists for rigour | Immediate productivity | Misread eval results; ship regressions | Only with a data scientist on the team |

**Why it wins (and when it doesn't).** The full track wins for engineers who will own model selection, fine-tuning or evaluation methodology; targeted study wins for most platform engineers, who mostly need to avoid two mistakes — reading noise as signal in a 30-question eval, and trusting a single aggregate number. The capstone's eval harness has both mistakes built in as teaching points: the dataset is deliberately small enough that a single question flipping moves recall by nearly a point, and the report prints a bootstrap confidence interval so the reader sees the noise floor.

**Problem it solves → value added.** It makes the eval harness trustworthy. The per-case PASS/FAIL lines in `make eval` output are the capstone's smallest concession to this track; the next would be a bootstrap confidence interval on hit@k, then stratified sampling of the eval set by source, and then a proper paired comparison when changing models.

**In the capstone.** `internal/rag/eval/metrics.go → Summarize` (point estimates only — a `Bootstrap` confidence interval is the first thing to add); this card and the next map the track's nodes onto the capstone's open questions.

### Continue Learning: Prompt Engineering

**What it is.** The Prompt Engineering roadmap formalises what this course used informally: prompt structure, few-shot and chain-of-thought techniques, output formatting, role and context management, evaluation of prompts, and defensive prompting. For an engineer who has completed the AI Engineer roadmap it is less about learning tricks and more about building a *process* — prompts as versioned artefacts with tests, changelogs and owners.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Follow a Prompt Engineering track | Systematic coverage of techniques; free | Techniques age quickly as models improve | You write or review prompts regularly |
| Provider prompt guides (OpenAI, Anthropic, Google) | Model-specific and current | Vendor scope | Always, for the model you use |
| Learn by eval: change prompts, measure with the harness | Grounded in your own system | Slow without a framework of techniques to try | Combined with either of the above |

**Why it wins (and when it doesn't).** The track wins as a vocabulary; the eval harness wins as the judge. The capstone's prompts in `internal/rag/prompt.go` and `internal/agent/prompts.go` are exported constants covered by `internal/rag/prompts_test.go` and `internal/agent/agent_test.go`; every technique from the track that was tried (chain-of-thought in the agent's `Thought:`, few-shot trajectories for small models, explicit output schemas, instruction hierarchy for safety) is there because it moved an eval number, and at least two that were tried were removed because they did not.

**Problem it solves → value added.** It converts prompt editing from craft to engineering: a prompt change is a PR with an eval delta, like any other change. The value is that prompts stop being the unreviewed part of the system.

**In the capstone.** `internal/rag/prompt.go → SystemPrompt`, `internal/agent/prompts.go → ReActSystem`, `NativeSystem` (constants with the rationale in their doc comments), `internal/rag/prompts_test.go`.

## Lab

### Part 1 — no keys: the full CI path locally

```bash
make build && make test                 # unit tests, mock provider
make adversarial                        # prompt-injection regression suite (detection rate gate + false-positive check)
make eval                               # retrieval + answer metrics on the mock provider
```

Expected `make eval` output:

```
PASS oom-remediation              retrieved_rank=1  Raise the memory limit to **1.5Gi** so the pods stop dying while you fix config: [2]. …
PASS oom-trigger                  retrieved_rank=1  # Config version is tracked here so `rollout history` shows which ConfigMap [4]. v42 …
...
FAIL redis-fix                    retrieved_rank=1  I could not find this in the indexed documents. Try `copilot search` with different …
PASS unanswerable                 retrieved_rank=0  I could not find this in the indexed documents. …

cases=10 hit@k=1.00 mrr=0.89 answer_acc=0.80 abstain_acc=1.00 mean_latency=2ms provider=mock embed=mock hybrid=true
```

`make eval` passes `--min-hit 0.9 --min-answer 0.7` and writes the outcomes to `eval-mock.json`; `Gate` fails the run below either threshold.

Inspect the tools' configuration:

```bash
./bin/copilot models                    # price table per 1M tokens plus the active provider/model/embedder/store
./bin/copilot tokens "some prompt text"  # token estimate and cost for each model
cat ARCHITECTURE.md CONTRIBUTING.md      # the contract an agentic tool is pointed at
```

Make a change and see the harness react: set `COPILOT_HYBRID=false` and re-run `make eval`; `answer_acc` drops from 0.80 to 0.70 (BM25 was finding identifiers that the mock embedder misses); raise the gate to `--min-answer 0.8` and the run fails.

### Part 2 — with real models

```bash
export OPENAI_API_KEY=sk-... ANTHROPIC_API_KEY=sk-ant-...
COPILOT_PROVIDER=openai    COPILOT_EMBED_PROVIDER=openai make eval-real   # same dataset, real model; writes eval-openai.json
COPILOT_PROVIDER=anthropic COPILOT_EMBED_PROVIDER=openai make eval-real   # writes eval-anthropic.json
make redteam                            # Go suite plus the Python moderation/injection lab; add any new attack to the injections slice
```

Diff `eval-openai.json` against a committed baseline from the previous run; the nightly `eval-real` job runs the same target and fails on the gate.

### Part 3 — use the tools on the repo

Pick one agentic tool and give it a bounded task with an objective check, for example: "Add a pgvector adapter to `internal/vectorstore` implementing `Store`, following `chroma.go`, with a test that skips without `PGVECTOR_URL`. Run `make test` before finishing." Review the diff with the ADRs open. Note what the rules file did and did not prevent.

## Production notes

- **Mock as the default is a safety and cost property**, not a convenience: no credential is needed to build, test or run the eval on a PR, so fork PRs are safe and a leaked CI log contains nothing.
- **Pin model identifiers** (`COPILOT_MODEL=gpt-4o-2024-08-06`, not `gpt-4o`) in production and in baselines; let the nightly job test the alias and tell you when it moves.
- **Baselines are committed JSON** (copy `eval-mock.json` / `eval-<provider>.json` into the repo) regenerated only by an explicit PR explaining why. Regression thresholds are per metric; recall and containment are strict, judge-based correctness has a wider band because the judge is noisy.
- **Spend caps everywhere:** `tokens.Budget` in the eval runner, a `MAX_EVAL_USD` environment variable in the nightly workflow, and provider-side monthly limits as the backstop.
- **Keys only in the scheduled workflow's environment**, never in `pull_request` jobs; `pull_request_target` is not used.
- **Coding assistants get the same treatment as agents:** no production credentials in their environment, a rules file that points at the contracts, and every delegated task ends with the test suite. Review generated code against ADRs, not just for correctness.
- **Observability in production mirrors the eval:** log the same fields the harness computes (sources, citation count, tokens, cost, guard result) so production drift can be compared with the nightly eval.
- **Keep the Continue Learning cards honest:** they list the open questions the capstone does not answer (fine-tuning a small VLM on dashboards, proper paired model comparisons, multi-tenant ACLs), which is more useful to a reader than a claim of completeness.

## Check your understanding

1. You are asked to standardise the organisation on one AI code editor. What is your evaluation plan and what will you refuse to decide on?
2. A PR changes the RAG system prompt. What must the PR show before merge, and what will you only know tomorrow?
3. The nightly eval against OpenAI drops 9 points in correctness with no code change. Walk through the diagnosis.
4. A colleague wants to run the real-model eval on every PR "so we catch regressions earlier". Respond.
5. You have two weeks of learning time after finishing this course. Which track, and what is the first thing you would change in the capstone with it?

<details>
<summary>Answers</summary>

1. Define a task list from real repo work (implement an adapter from an ADR, fix a failing test, add a CLI flag with docs), run each candidate on the same tasks with the same rules file, score on correctness, review burden and time; separately evaluate governance (model control, data handling, SSO, audit) because that is usually decisive. Refuse to decide on vendor benchmarks or a single engineer's preference, and plan to re-evaluate in six months because the products change that fast.
2. The PR must include the prompt version bump and changelog comment, passing golden tests, a green adversarial suite (prompt changes can weaken the instruction hierarchy), and the mock eval delta (which will be near zero for a prompt change, since mock completions are canned — say so). Real-model quality and cost deltas arrive from the nightly or a manual `eval-real` dispatch; link the dispatch run if you triggered one.
3. First confirm it is not the judge: re-run the judge on yesterday's stored outputs. Then check whether the model alias moved (the `model` field in the eval outcomes JSON is what the provider returned; compare with the baseline's). Then look at the disaggregated results — one service dropping points to a corpus or retrieval change (did an ingest job run?), all services dropping points to the model or provider. Pin the previous snapshot if it was an alias move, and record the finding.
4. Cost and flakiness would make PRs slow and untrusted, keys would be exposed to PR builds, and real-model variance is larger than most code-change effects, so the signal is poor. Offer the compromise the repo already has: a manual `workflow_dispatch` with a spend cap for prompt or retrieval PRs where the author wants the signal before merge, plus the nightly as the safety net.
5. For most platform engineers: targeted statistics and IR from the AI & Data Scientist track, and the first change is turning the eval's single-run comparison into a paired comparison with a significance test, so model and prompt decisions stop being made on differences inside the noise. If you write prompts daily, the Prompt Engineering track, and the first change is a prompt ownership and review process with the harness as the gate.

</details>

## References

- Cursor, *Rules for AI* documentation. https://docs.cursor.com/context/rules
- Windsurf, documentation. https://docs.windsurf.com/
- Zed, *AI* documentation. https://zed.dev/docs/ai/overview
- JetBrains, *AI Assistant* documentation. https://www.jetbrains.com/help/ai-assistant/
- GitHub, *Copilot* documentation (agent mode, content exclusion, enterprise policies). https://docs.github.com/copilot
- Tabnine, documentation (air-gapped deployment). https://docs.tabnine.com/
- Continue, documentation (model configuration, context providers). https://docs.continue.dev/
- Amazon Q Developer, documentation. https://docs.aws.amazon.com/amazonq/
- Anthropic, *Claude Code* documentation (`CLAUDE.md`, hooks, non-interactive mode). https://docs.anthropic.com/en/docs/claude-code
- OpenAI, *Codex CLI*. https://github.com/openai/codex
- Aider, documentation. https://aider.chat/docs/
- promptfoo, documentation. https://www.promptfoo.dev/docs/intro/
- Ragas, documentation (RAG metrics). https://docs.ragas.io/
- Langfuse, documentation (tracing, datasets, evals). https://langfuse.com/docs
- Es et al., *RAGAS: Automated Evaluation of Retrieval Augmented Generation* (2023). https://arxiv.org/abs/2309.15217
- Zheng et al., *Judging LLM-as-a-Judge with MT-Bench and Chatbot Arena* (2023). https://arxiv.org/abs/2306.05685
- GitHub Docs, *Security hardening for GitHub Actions* (secrets in `pull_request` vs scheduled workflows). https://docs.github.com/actions/security-guides/security-hardening-for-github-actions
