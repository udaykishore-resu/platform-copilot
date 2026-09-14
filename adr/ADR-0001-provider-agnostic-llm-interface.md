# ADR-0001 · One provider-agnostic `llm.Provider` interface over every chat model

**Status:** Accepted · **Date:** 2025-09 · **Owner:** Udaykishore Resu · **Modules:** 01, 02, 06, 07

## Context

`platform-copilot` must run against OpenAI, Anthropic, Google Gemini, a local Ollama server, and a
deterministic mock — the last because every module's lab and the whole CI pipeline must work with no
API key (ADR-0002, Module 09). The roadmap's "Pre-trained Models" and "Open-Source AI" sections are
explicitly about being able to move between hosted and open models, and a platform team's real
constraints (data residency for some corpora, cost for others, an existing enterprise agreement with
one vendor) make provider choice a deployment-time decision rather than a build-time one.

The providers' chat APIs are similar but not identical. Roles and system prompts are expressed
differently (OpenAI `system`/`developer` messages; Anthropic a top-level `system` parameter; Gemini
`systemInstruction`). Tool calling exists on all four but with different wire formats (OpenAI
top-level `tool_calls`; Anthropic `tool_use` content blocks; Gemini `functionCall` parts; Ollama an
OpenAI-compatible subset). Images are `image_url` parts, `image` blocks, or `inlineData`. Usage
accounting, finish reasons, streaming chunk formats and error shapes all differ. Without an
abstraction, every feature — RAG prompt assembly, the agent loop, safety checks, vision, cost
reporting — would be written once per provider, and the mock would be a fifth copy that drifts.

Two other forces matter. First, the agent loop (Module 06) and the multimodal layer (Module 07) need
the abstraction to carry *structured* content — tool definitions, tool calls, tool results, image
parts — not only strings; a lowest-common-denominator "prompt in, text out" interface would force
ReAct-only agents and no vision. Second, the interface is the seam where end-user IDs (ADR-0010),
token budgets (`internal/tokens`) and the safety layer (ADR-0008) attach, so it has to expose
`Usage` and accept per-request metadata.

## Decision

Define a single interface in `internal/llm`:

- `Provider` with `Complete(ctx, Request) (*Response, error)`, `Name() string`, `DefaultModel()
  string` and `ContextWindow(model string) int` (`internal/llm/types.go`). Streaming and capability
  probes are deliberately absent in this version: every adapter accepts tools and images, and a
  caller that wants ReAct picks it explicitly (ADR-0006).
- `Request{Model, Messages []Message, Tools []Tool, MaxTokens, Temperature, JSONSchema
  json.RawMessage, User string, Stop []string}`.
- `Message{Role, Content string, Images []ImagePart, ToolCalls []ToolCall, ToolCallID, Name string}`
  — `Content` for the common text case, `Images` for multimodal, and tool fields so native tool
  calling round-trips.
- `Tool{Name, Description, Parameters json.RawMessage}`, `ToolCall{ID, Name, Arguments
  json.RawMessage}`, `Usage{PromptTokens, CompletionTokens, TotalTokens int}`,
  `Response{Content, ToolCalls, Usage, FinishReason, Model, Provider}`.
- `New(Options{Provider, Model, APIKey, BaseURL, HTTP}) (Provider, error)` (`provider.go`) selects
  `openai.go`, `anthropic.go`, `gemini.go`, `ollama.go` or `mock.go` from `COPILOT_PROVIDER`;
  `WithRetry(p, attempts, base)` wraps any provider with backoff on `HTTPError.Retryable()`.

Each adapter owns the translation between these types and its wire format, including normalising
tool calls and content parts in both directions. Nothing outside `internal/llm` imports a
provider-specific type. The mock provider implements the full interface, including tool calls and
structured output, with deterministic extractive behaviour over the prompt's context (`mock.go`), so
agents and RAG are unit-testable; it cannot see images and says so.

## Alternatives considered

| Option | Why not |
|---|---|
| **Use one vendor's SDK types as the canonical model** (e.g. OpenAI's request/response structs everywhere, adapt others to them) | Bakes one vendor's evolution into every package; Anthropic and Gemini features that do not fit (content-block tool use, `inlineData`) become second-class; the mock would have to imitate OpenAI's shape exactly. Also conflicts with ADR-0009. |
| **Lowest-common-denominator string interface** (`Complete(prompt string) string`) | Would have been enough for Modules 01–05 but blocks native tool calling, structured outputs, and vision. Retrofitting later means touching every caller. |
| **A gateway/proxy in front of providers** (LiteLLM, OpenRouter, Portkey, an internal gateway) | Operationally attractive in production and compatible with this decision — the `openai.go` adapter can point at any OpenAI-compatible gateway via base URL. But as the *only* abstraction it adds a network hop to the mock path, hides provider differences we want students to see, and moves the seam out of the codebase. Kept as a deployment option, not the design. |
| **Go frameworks (LangChainGo, genkit)** | Provide an abstraction but a heavier one, with their own prompt and chain models that overlap the course's own `internal/rag` and `internal/agent`. The course exists to show the mechanism. |
| **One package per provider with duplicated pipelines** | Five copies of RAG, agent and safety code. Rejected on maintenance grounds alone. |

## Consequences

**Positive.**
- Every feature is written once; `COPILOT_PROVIDER` switches it. The labs in every module have a "no
  keys" and a "real models" path with the same commands.
- The mock provider exercises the full surface (tools, JSON schema, usage), so CI tests the agent loop,
  prompt assembly and safety wiring deterministically in seconds.
- Because every adapter maps `Tools`/`ToolCalls`, the agent's native mode works on all of them, and
  the ReAct mode is a one-flag fallback (ADR-0006) without callers knowing which provider is behind
  the interface.
- The interface is the natural attachment point for end-user IDs, budgets and audit logging.

**Negative / costs.**
- The adapters are real work: tool-call and image normalisation for four wire formats; recorded-
  response tests per adapter are still to be written, so completion-assisted code can mis-map a
  field unnoticed until a real call.
- Lowest-common-denominator pressure: a provider-only feature (Anthropic prompt caching, OpenAI
  `strict` tool schemas, Gemini native video) either gets an optional field on `Request` that other
  adapters ignore, or stays out. The rule adopted: add the field if at least two providers can
  honour it or if ignoring it is harmless; otherwise keep it out (`JSONSchema` made it in on that
  rule; OpenAI's `strict` did not).
- Error semantics are normalised to `*HTTPError{Status, Body}` with a `Retryable()` method
  (429 and 5xx) and some provider-specific detail is lost; the raw body is retained for debugging.
- There is no streaming: responses are delivered on completion, which keeps the interface and the
  four adapters small at the cost of perceived latency in `copilot chat`.

**Follow-ups.**
- ADR-0009 decides how each adapter talks to its provider (raw HTTP rather than SDKs).
- Token estimation and pricing live in `internal/tokens` and are consulted by callers, not by the
  adapters, to keep the interface free of pricing concerns; the per-model window comes from
  `Provider.ContextWindow`.
- Revisit if a sixth provider arrives with a materially different model (e.g. a pure Responses-API
  shape); the interface should absorb it as another adapter, and if it cannot, that is the signal to
  redesign.

