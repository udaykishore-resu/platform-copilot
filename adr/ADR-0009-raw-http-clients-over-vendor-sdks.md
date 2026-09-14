# ADR-0009 · Provider adapters use raw `net/http`, not vendor SDKs

**Status:** Accepted · **Date:** 2025-09 · **Owner:** Udaykishore Resu · **Modules:** 01 (OpenAI
API), 02, 04, 07

## Context

ADR-0001 commits to one `llm.Provider` interface with an adapter per provider; ADR-0004 does the
same for vector stores. Each adapter has to talk to a remote API, and there are two ways to do that
in Go: import the vendor's official or community SDK (`openai-go`, `anthropic-sdk-go`, the Google
`genai` module, `ollama/api`, `go-client` for Qdrant, a community Chroma client), or write a thin
HTTP client against the documented REST endpoints.

The SDK route is the default in most application code and has real advantages: typed request and
response structs maintained by the vendor, built-in retries with backoff, pagination helpers,
streaming parsers, and faster adoption of new API features. The costs are specific to this project.
Each SDK pulls its own dependency tree (several of them include generated code, gRPC, or protobuf),
each has its own idioms for context, options and errors that leak into the adapter, and each changes
on its own schedule — the OpenAI and Anthropic Go SDKs both made breaking changes during the period
this repo was being written, and the Gemini client moved between modules. The Qdrant Go client is
gRPC-first, which adds a transport the rest of the project does not use.

There is also the teaching force. The roadmap's "OpenAI API" and "Chat Completions API" nodes are
about the *wire protocol*: messages, roles, tools, usage, finish reasons, the shape of a streaming
chunk, the headers that carry rate-limit information. A student who has only seen
`client.Chat.Completions.New(...)` has not seen the API. Finally, the same mechanism must support
the mock provider, recorded-response golden tests, and pointing the OpenAI adapter at any
OpenAI-compatible endpoint (Ollama, vLLM, a gateway) by changing a base URL — all simplest when the
adapter owns the HTTP call.

## Decision

Every adapter in `internal/llm`, `internal/embeddings`, `internal/vectorstore` and
`internal/multimodal` is implemented with the standard library: `net/http` with a shared
`*http.Client` (timeouts, connection reuse), `encoding/json` for the documented request and response
bodies, hand-written structs for exactly the fields the project uses, and small per-package helpers
rather than a shared HTTP package:

- `internal/llm/provider.go → postJSON` (context-aware JSON POST with per-provider headers —
  `Authorization: Bearer`, `x-api-key` + `anthropic-version`, `x-goog-api-key`) and `HTTPError{
  Status, Body}` with `Retryable()` for 429 and 5xx;
- `internal/llm/provider.go → WithRetry` wrapping any `Provider` with exponential backoff on
  retryable errors;
- the same pattern repeated in `internal/embeddings`, `internal/vectorstore` (`do` helpers on the
  Qdrant and Chroma adapters) and `internal/multimodal/audio.go` (multipart upload for Whisper);
- no streaming (ADR-0001), so no SSE parsing.

Base URLs are configurable per provider so the OpenAI adapter also serves Ollama's and vLLM's
OpenAI-compatible endpoints and any internal gateway. The shared behaviour (retry, structured output, tool-call
scripting) is tested against the mock in `internal/llm/mock_test.go`; recorded-response tests per
adapter, which would catch a wrong JSON tag or a missed content-block type without network access,
are the first follow-up.

Vendor SDKs are not imported anywhere in the Go module. The Python labs use the official `openai`,
LangChain and LlamaIndex packages freely, because there the point is the ecosystem (ADR-0002).

## Alternatives considered

| Option | Why not |
|---|---|
| **Official vendor SDKs for each provider** | Five dependency trees with independent breaking-change schedules; SDK idioms leak into adapters and complicate the uniform normalisation ADR-0001 needs; the Qdrant client brings gRPC; hides the protocol the course teaches; harder to point at compatible endpoints and to golden-test. |
| **One SDK (OpenAI) plus OpenAI-compatible endpoints for everything else** | Anthropic and Gemini are not OpenAI-compatible natively (their compatibility shims lag on tools and vision); would either exclude them or route through a gateway, moving the seam out of the repo. |
| **A gateway (LiteLLM, Portkey, an internal proxy) as the only client** | Good production practice and fully compatible with this decision — the OpenAI adapter can target it — but as the *only* path it adds a service to the no-keys lab flow and hides exactly the differences the modules compare. Documented as a deployment option. |
| **Generated clients from OpenAPI specs** | Produces large, churny code for the small slice of each API used; hand-written structs for the used fields are smaller and more readable. |
| **Community multi-provider Go libraries** | Uneven maintenance and coverage (tools and vision in particular); would be a dependency on someone else's version of ADR-0001. |

## Consequences

**Positive.**
- The Go module's dependency list stays short and auditable — a property platform teams care about
  and one that recruiters reading `go.mod` will notice.
- Adapters are legible: a reader sees the actual JSON OpenAI, Anthropic and Gemini exchange, which
  is the roadmap node.
- Uniform behaviour across providers for retries, timeouts, error classes, logging and end-user ID
  propagation, because it is one helper package rather than five SDK configurations.
- Base-URL configurability gives Ollama, vLLM and gateway support for free, and golden-file tests
  run offline in milliseconds.
- No SDK upgrade can change runtime behaviour under the project; changes arrive only when the
  project chooses to follow an API change.

**Negative / costs.**
- The project owns what SDKs give away: streaming parsers, multipart uploads, retry policy, and
  keeping structs current when providers add fields. The `httpx` helper concentrates most of this,
  but new API features (a new content-block type, a new tool-choice mode) need a deliberate change
  rather than a version bump.
- Risk of subtle wire-format bugs in hand-written structs, especially when code completion tools
  suggest plausible but wrong field names (Module 09). The golden-file tests exist for this reason
  and must be re-recorded when providers change formats; the nightly CI job against live providers
  detects drift.
- Slower adoption of brand-new provider features than SDK users enjoy; acceptable for a copilot
  whose feature set is deliberately stable.
- Some vendor conveniences are lost — automatic pagination on list endpoints, typed enums for every
  option — and the adapters expose only the subset the project uses.
- Contributors used to SDKs may find the style unusual; `CONTRIBUTING.md` and the adapter template
  in `internal/llm/mock.go` set the pattern.

**Follow-ups.**
- Record and commit golden responses for every provider's tool-call and vision shapes, including
  edge cases (parallel calls, refusals, truncated outputs).
- Add a `COPILOT_GATEWAY_URL` convenience that points all OpenAI-compatible traffic at one gateway
  for production deployments.
- Re-evaluate only if a provider's REST surface becomes unsupported in favour of a transport (gRPC,
  WebSocket-only realtime) that `net/http` cannot reasonably serve; at that point an adapter-local
  dependency would be justified by its own ADR.

