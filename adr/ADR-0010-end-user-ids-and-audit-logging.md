# ADR-0010 · Hashed end-user IDs on every provider request, and a structured audit log keyed by them

**Status:** Accepted · **Date:** 2025-09 · **Owner:** Udaykishore Resu · **Modules:** 08 (Adding
end-user IDs, Security and Privacy), 09

## Context

The copilot is a shared tool. Behind `copilot serve` it is used by a whole platform team — and, if
Module 08's threat model is revisited, possibly by other teams — through one set of provider API
keys. Providers run abuse detection on those keys; OpenAI's safety best practices, Anthropic's
`metadata.user_id`, and Gemini's request metadata all exist so a provider can attribute problematic
traffic to *one of your users* rather than to your whole organisation. Without that, a single person
probing the copilot with policy-violating prompts can get the shared key rate-limited or suspended —
during an incident, when the tool matters most.

The same identifier is needed internally. Module 08's controls (ADR-0008) make decisions — injection
flagged, PII redacted, moderation categories, guard rejections — that have to be reviewable
afterwards: who asked what, which documents were retrieved, which tools were called, what it cost.
Module 09's production notes want per-user cost attribution. A postmortem of a bad answer needs to
replay the exact run. All of that needs a stable key per person.

The constraints pull in opposite directions. Attribution requires identity; privacy (Module 08,
"Security and Privacy Concerns") forbids sending emails or names to a third party or storing raw
prompts containing PII indefinitely. The CLI runs on laptops without an SSO session; the server runs
behind SSO. The identifier must be consistent between the two so one person's CLI and browser usage
correlate. And since the provider abstraction (ADR-0001) is the only place every request passes
through, the identifier has to be a field on `llm.Request`.

## Decision

1. **`llm.Request.User` is a required field** carrying a pseudonymous end-user identifier. Each
   provider adapter maps it to its wire field (OpenAI `user`; Anthropic `metadata.user_id`; Gemini
   request labels/metadata; Ollama ignores it; the mock records it for tests). The embeddings and
   moderation clients carry the same value where the API accepts one.
2. **The identifier is opaque, never a raw identity.** In `internal/server` it is the `user` field
   of the request body or the `X-End-User-ID` header set by the authenticating proxy (`userOr` in
   `server.go`; an SSO subject or Slack ID, never an email), falling back to `anonymous`; in
   `cmd/copilot` it is `--user`, else `COPILOT_USER`, else `cli:<os-username>`
   (`config.Config.User`). Hashing the identity before it reaches the copilot — e.g. the first 16
   hex characters of an HMAC-SHA256 with a deployment salt — is the proxy's job, so the same salt
   yields the same ID everywhere.
3. **A structured log line per request** is emitted by `internal/server/middleware.go`
   (`withRequestID`, `withLogging` via `log/slog`): request ID, method, path, status, latency; and
   every `/v1/ask` and `/v1/agent` response carries `request_id`, `model`, `usage`, `cost`,
   `latency_ms` and `warnings` (guard reasons, redaction counts, injection matches) so the caller
   can record them against the user ID it supplied. Full prompts are *not* logged; the question,
   the retrieved chunk IDs (`Answer.Sources`) and the corpus are sufficient to replay a run. A
   dedicated audit sink with retrieved chunk IDs, tool-call hashes and moderation categories is the
   follow-up.
4. **Sinks are pluggable in principle:** `slog` JSON lines to stderr by default (for container log
   collection; `cmd/copilot/main.go → cmdServe`); swapping the handler for an OpenTelemetry exporter
   is a one-line change. Retention is the operator's policy.
5. **Per-user controls hang off the same ID:** the response's `usage` and `cost` are per request and
   per user; rate limits and a daily token budget in `internal/server/middleware.go` are the natural
   next middleware.

## Alternatives considered

| Option | Why not |
|---|---|
| **No end-user identifier** | The simplest thing and the default in most tutorials. Exposes the shared key to collective punishment for one user's behaviour, and leaves no attribution for audit. Acceptable only for a single-user prototype. |
| **Send the raw identity (email or SSO subject)** | Works for attribution but sends PII to a third party on every request and stores it in their logs, contrary to Module 08 and to most privacy regimes. |
| **Random per-session IDs** | Protects privacy but defeats the purpose: a provider's abuse report cannot be tied to a person, and budgets and cost attribution reset every session. |
| **One provider API key per user or team** | Hard isolation and clean billing, but key sprawl, rotation burden, and still no per-person attribution within a team key. Reasonable for per-*team* billing separation; compatible with this decision rather than a replacement. |
| **Full prompt and response logging for debugging** | Maximal replayability, but stores the PII and confidential content the safety layer worked to exclude and creates a high-value target. Replay from question hash plus chunk IDs plus prompt version achieves the debugging goal without it. |
| **Audit logging in the HTTP middleware only** | Misses CLI usage and the agent's internal steps; the pipelines are the only place that sees everything (same reasoning as ADR-0008). |
| **Plain SHA-256 without a salt** | Cheap to compute and to reverse for a small, known set of usernames; HMAC with a deployment secret prevents a provider or a leaked log from de-anonymising users. |

## Consequences

**Positive.**
- Provider abuse signals and enforcement become per-user; the shared key survives one person's bad
  day.
- Every safety decision is reviewable: a bad answer or a flagged request can be traced to a
  pseudonymous user, a prompt version, the exact chunks and tool calls, and replayed against the
  mock or the real provider.
- Per-user cost and rate controls are possible with no additional identity plumbing.
- Privacy is preserved structurally — no raw identity or PII leaves the boundary or enters the log —
  rather than by policy.
- Consistent IDs across CLI and server give one view of a person's usage.

**Negative / costs.**
- Identity at the edge is a deployment requirement: `copilot serve` must sit behind an
  authenticating proxy that sets a trusted `X-End-User-ID` header (or a trusted client that fills the
  `user` field). Misconfiguration — no header, no field — falls back to an `anonymous` ID, which is
  safe but useless for attribution.
- Salt rotation changes every ID and breaks historical correlation; documented as a deliberate, rare
  operation.
- Pseudonymity is not anonymity: anyone with the salt can recompute IDs, so the log is still
  sensitive and needs access control and retention limits. The default stdout sink delegates that to
  the logging platform.
- Replay-from-hash depends on the corpus being versioned; if `make ingest` re-chunks with new IDs,
  old audit records lose their chunk references. Chunk IDs are content hashes (`chunkID` in
  `internal/rag/ingest.go`), so an unchanged passage keeps its ID across re-ingests and a changed
  one is detectable.
- Ollama has no user field, so local deployments get the internal audit benefits but not
  provider-side attribution — irrelevant, since there is no shared third-party key to protect.
- Slightly more code in every adapter and pipeline, and a required field that tests must set; the
  mock provider asserts it is non-empty to catch omissions.

**Follow-ups.**
- Add the OpenTelemetry exporter and a reference dashboard (requests, cost, guard rejections and
  injection flags per user ID) to Module 09's production notes.
- Document the salt distribution and rotation procedure in `docs/deployment.md`.
- When role-based capability profiles land (ADR-0007 follow-up), derive the profile from the same
  SSO claims at the same edge so identity and authority are decided once.

