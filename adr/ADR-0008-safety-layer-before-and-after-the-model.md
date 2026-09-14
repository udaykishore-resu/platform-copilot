# ADR-0008 · One safety package, invoked before and after every model call

**Status:** Accepted · **Date:** 2025-09 · **Owner:** Udaykishore Resu · **Modules:** 08 (AI Safety
& Ethics), 05, 06, 07

## Context

Module 08 of the roadmap lists eleven safety nodes — prompt injection, security and privacy, bias,
moderation, end-user IDs, adversarial testing, robust prompting, knowing your users, constraining
inputs and outputs, best practices. Each is usually presented as a separate concern. In the capstone
they have to become a small number of functions called from a small number of places, or they will
be applied inconsistently: the RAG path will redact PII and the agent path will forget to, or the
CLI will moderate and the HTTP server will not.

There are three model-calling paths (`rag.Pipeline.Ask`, `agent.Agent.Run`, `multimodal` vision
requests), two entry points (`cmd/copilot` and `internal/server`), and several channels through
which untrusted text reaches the model: the user's question, retrieved chunks, tool outputs, and
text inside images. The controls split naturally into those that must run on *inputs* (injection
detection, PII and secret redaction, moderation of the user's text, size and rate limits) and those
that must run on *outputs* (moderation of the answer, length and banned-pattern checks, citation
requirements, JSON-schema validation when structured output is requested).

Neither side is sufficient alone. Input screening is heuristic or classifier-based and misses novel
attacks; output validation catches the consequences (a leaked secret, an uncited claim, a malformed
structure) but cannot undo a model that has already seen something it should not have. Running both,
with the output side being deterministic and strict, is the only configuration in which the
adversarial suite's containment rate reaches 100 percent while detection stays below it (ADR-0007
covers the tool-side containment).

A further constraint is the mock provider: every control must run and be testable with no API key,
so moderation in particular needs a local fallback.

## Decision

All safety controls live in `internal/safety` and are invoked at fixed points:

- **Before the model:** `Guard.CheckInput(text) Result` on the user's input — length cap
  (`MaxInputChars`, 8000), optional `AllowedTopics`, `DetectInjection` (blocking when
  `Suspicious`), then `RedactSecrets` and `RedactPIIExcept(…, "IPV4")` with a warning per
  redaction; `Agent.observe` runs `RedactSecrets`, `DetectInjection` and `WrapUntrusted` on every
  tool output before it is placed in a prompt; request size is capped by `http.MaxBytesReader` in
  `internal/server/server.go` and tool arguments are validated in each tool (ADR-0007).
- **After the model:** `Guard.CheckOutput(text) Result` on every final answer — `BannedOutput`
  patterns (destructive `kubectl delete ns`, `rm -rf /`, `terraform destroy -auto-approve`) withhold
  the response with `[response withheld: …]`, and `RedactSecrets` scrubs echoed credentials;
  `ValidateJSON(text, schema)` checks structured outputs.
- **Wiring is in the pipelines, not in the entry points:** `rag/pipeline.go` (`Pipeline.Guard`) and
  `agent/agent.go` (`Agent.Guard`, `Agent.observe`) call the safety functions themselves, so the CLI
  and the HTTP server cannot bypass them and new entry points inherit them. `multimodal` readings
  go through the same `Pipeline`/`Agent` when they are used for retrieval.
- **`Moderator` has two backends behind one interface** (`moderation.go`): `OpenAIModerator` (the
  Moderation API) when `OPENAI_API_KEY` is set and `COPILOT_MODERATION` is not `local`, otherwise
  `LocalModerator`, a keyword classifier. `NewModerator()` picks one and `ModerationResult.Provider`
  says which ran. The fallback exists to keep the path exercised offline, not as an adequate
  policy. Moderation is exposed via `copilot moderate`; wiring it into `Pipeline.Ask` is the next
  step.
- **Untrusted text is delimited.** Tool outputs are wrapped by `WrapUntrusted` in
  `<untrusted source=…>` markers (with the closing tag escaped inside), retrieved chunks are
  numbered `[n] source:` blocks under a system prompt that says to treat them as data, and flagged
  content adds a `Warnings` entry that the CLI prints to the user.
- **Every decision is visible** with the end-user ID (ADR-0010): guard reason and warnings are on
  the `Answer`/`Result`, and the HTTP server logs each request with its user and request ID.
- **The controls are tested adversarially in CI** (`internal/safety/adversarial_test.go`: the
  `injections` and `benign` slices, `TestInjectionDetection`, `TestGuardBlocksAndRedacts`,
  `TestValidateJSON`; `internal/agent/agent_test.go → TestKubectlAllowlist` for containment), with
  `make adversarial` failing below an 85 % detection rate or on any benign false positive.

## Alternatives considered

| Option | Why not |
|---|---|
| **Safety as middleware at the entry points** (HTTP middleware and a CLI wrapper) | Clean separation, but the agent's *internal* observations and the RAG pipeline's retrieved chunks never pass through an entry point; indirect injection would be unscreened. The controls must sit where the untrusted text enters the prompt. |
| **Input-side only** | Cheaper and lower latency, but leaves leaked secrets, uncited claims and malformed outputs unchecked — exactly the failures that reach users. |
| **Output-side only** | Catches consequences but sends PII and secrets to the provider first, and lets injected instructions influence tool selection in the agent before anything is checked. |
| **Rely on provider-side safety** (model refusals, vendor moderation built into the chat endpoint) | Addresses misuse categories only, varies by provider, is not observable, and does nothing for injection, PII or output structure. Used as an additional layer, never the design. |
| **A dedicated guardrail framework** (NeMo Guardrails, Guardrails AI, LLM Guard) | Rich rule languages and classifier bundles, but Python-only, heavy, and they would hide the mechanism Module 08 must teach. The capstone's functions are a few hundred lines; a Python service could be adopted later behind the same four functions. |
| **An LLM-as-judge for every input and output** | Most flexible, doubles cost and latency, and the judge is itself injectable. Reserved for the nightly red-team job, not the request path. |
| **Per-path bespoke checks** (each pipeline does what its author thought of) | This is the default outcome without a decision and is the failure mode the decision prevents. |

## Consequences

**Positive.**
- Consistency: `copilot ask`, `copilot agent`, `copilot vision` and `/v1/*` all get the same
  controls because the pipelines own them. Adding a new entry point cannot weaken safety.
- Testability: every control runs on the mock provider; the adversarial suite runs on every PR with
  no keys.
- Observability: one structured log line per request summarises the safety decisions; `make
  safety-report` prints the active configuration.
- Clear division of labour with ADR-0007: this ADR screens and validates text; ADR-0007 makes bad
  actions impossible. Together they are the containment story.

**Negative / costs.**
- Latency and cost: two moderation calls per request when using the API backend (input and output),
  plus the redaction and heuristic passes. Moderation is free on OpenAI and about 100 ms; the local
  passes are microseconds. Acceptable for an interactive tool.
- False positives: runbooks legitimately contain phrases like "ignore the alert" or "disregard the
  warning", and `DetectInjection` flags some. Flagging is non-blocking for retrieved content (the
  chunk is wrapped and marked, not dropped) precisely because of this, at the cost of a noisier
  prompt.
- Over-redaction can damage answers; the PII set is deliberately conservative (emails, phone
  numbers, card-shaped numbers, known credential formats) and leaves hostnames and namespaces
  intact. The list is configurable and every redaction is counted and logged.
- `Guard` rejections waste a paid model call; the one-retry policy bounds it, and structured outputs
  on providers that support them keep the rejection rate low.
- The local moderation fallback is weak and could be mistaken for adequate; the output always names
  the backend, and the Production notes in Module 08 say plainly that it is not a policy.
- Maintenance: heuristic patterns, banned-output patterns and the adversarial corpus need curation
  as attacks and the corpus evolve. The nightly red-team job feeds new cases into the suite.

**Follow-ups.**
- Add an optional classifier backend for `DetectInjection` (Prompt Guard via Ollama or HF) as a
  second layer when the copilot is exposed beyond the platform team.
- Pixel-level redaction for screenshots is out of scope for `RedactPII`; Module 07 documents routing
  sensitive dashboards to an in-VPC vision model instead.
- Revisit the input/output moderation policy if latency budgets tighten: output moderation could
  move to asynchronous sampling with a delayed retraction, at a measured cost in safety.

