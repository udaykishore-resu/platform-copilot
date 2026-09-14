# ADR-0003 · Retrieval-augmented generation, not fine-tuning, for runbook knowledge

**Status:** Accepted · **Date:** 2025-09 · **Owner:** Udaykishore Resu · **Modules:** 01
(Fine-tuning), 05 (RAG vs Fine-tuning)

## Context

The copilot's core job is to answer questions about a team's own operational knowledge: runbooks,
Kubernetes manifests, Terraform, postmortems, alert definitions. That corpus has four properties
that matter for how knowledge gets into the model:

1. **It changes daily.** Manifests change on every deploy; runbooks are edited after every incident.
   An answer based on last month's rollback procedure can be wrong in a way that causes an outage.
2. **Provenance is required.** An on-call engineer acting at 3 a.m. needs to see *which* runbook
   section the answer came from, to verify it before acting. "The model says" is not an acceptable
   citation.
3. **It is confidential and access-controlled.** Some documents should only be visible to some
   teams; some contain hostnames and internal identifiers that must not be baked into a model
   artefact that might be shared or exported.
4. **It is small and specific.** Tens of thousands of chunks, not billions of tokens; the knowledge
   is facts and procedures, not a style or a skill.

The roadmap lists both "Fine-tuning" (Module 01) and "RAG vs Fine-tuning" (Module 05) as nodes, and
a credible course must take a position rather than list pros and cons. Fine-tuning is genuinely the
right tool for some things — teaching a model a consistent output format, a house style, a domain's
vocabulary, or compressing a long system prompt — and the course covers it with a lab
(`labs/python/01_finetune_job.py`) so the comparison is concrete.

## Decision

Operational knowledge enters the copilot **only** through retrieval. `internal/rag` ingests
documents (`Ingester.Ingest`), chunks them with format-aware chunkers (`Chunker.Fixed`, `Recursive`,
`Markdown`), embeds chunks (`internal/embeddings`), stores them (`internal/vectorstore`), retrieves
the top-k with a similarity threshold (`Retriever`), assembles a prompt with numbered citations
(`BuildPrompt`), and generates an answer that must cite (`Pipeline.Ask`; the `SystemPrompt` in
`internal/rag/prompt.go` requires a `[n]` on every factual statement and an explicit abstention
otherwise).

Fine-tuning is **not** used to inject knowledge. It is documented as appropriate for behaviour
shaping — output format, terse SRE tone, tool-calling reliability on small open models — and the lab
shows how to run a job, but no fine-tuned model is part of the capstone's default configuration.

## Alternatives considered

| Option | Why not |
|---|---|
| **Fine-tune a model on the runbooks** (supervised fine-tuning on Q&A pairs generated from the corpus) | Knowledge goes stale on the next deploy and retraining per change is impractical; fine-tuned models cannot cite a source; memorisation of specific facts via SFT is unreliable (models learn style far more readily than facts); access control is impossible once facts are in weights; confidential data is exported into a vendor-hosted artefact. Cost and latency of training cycles also dwarf re-indexing. |
| **Long-context stuffing** (put the whole corpus in the prompt every time) | Works for a handful of documents and gets simpler as context windows grow, but costs scale linearly with corpus size on every question, quality degrades with distractors ("lost in the middle"), and there is still no access control or selective citation. Used as a fallback in the capstone only when the corpus fits a budget set by `tokens.Budget` — effectively never for a real team. |
| **RAG plus a fine-tuned generator** | Legitimate combination: retrieval for facts, fine-tuning for format and tone. Deferred; the system prompt achieves the format goals at present and a fine-tuned generator would tie the capstone to one provider's fine-tuning API, against ADR-0001. Revisit if prompt-level format control proves insufficient on small local models. |
| **Hosted retrieval (OpenAI Assistants file search, Bedrock Knowledge Bases)** | Removes the ingestion pipeline but gives up control over chunking (YAML and Terraform need structure-aware chunking), hybrid search (ADR-0005), access-control filters, and portability. Covered as "RAG Alternative: OpenAI Assistant API" in Module 05 with a lab for honest comparison. |
| **Knowledge graph / GraphRAG** | Valuable for questions that span many documents ("which services depend on the thing that depends on etcd?"), but a large additional system; the dependency graph use case is better served by a `PromQL`/service-catalogue tool in the agent. Noted as future work. |

## Consequences

**Positive.**
- Freshness is a property of `make ingest`, not of a training run; a CI job re-indexes on merge to
  the runbook repo.
- Every answer carries `[n]` citations resolved to file path and heading; the CLI prints them and
  `Guard` rejects uncited factual answers when sources were retrieved.
- Access control is a metadata filter at retrieval time (`Store.Search(ctx, vector, k, filter)`), so one index
  can serve several teams safely.
- Confidential data stays in the team's vector store (in-memory JSON, Qdrant or Chroma — ADR-0004)
  and is sent to the model only as needed, after `RedactPII`.
- Provider-agnostic: the same index serves all five providers.

**Negative / costs.**
- Answer quality is bounded by retrieval quality. Chunking strategy, embedding model choice and
  hybrid search (ADR-0005) become first-class engineering concerns with their own eval metrics
  (recall@k, MRR in `internal/rag/eval`).
- Prompt tokens per question are higher than with a fine-tuned model that "knows" the material:
  typically several thousand tokens of context per answer, budgeted by `tokens.Budget` and reported
  as cost.
- The model's *behaviour* (format, tone, refusal style) is controlled by prompting alone, which is
  less stable across model versions than fine-tuning would be; prompts are versioned and
  golden-tested to compensate.
- RAG does not teach the model the domain's vocabulary; it can misread "SLO" or a service name in
  the question. Mitigated by hybrid BM25 retrieval, which matches exact tokens the embedder may
  blur.

**Follow-ups.**
- Keep the fine-tuning lab current and extend Module 01 with a worked example of fine-tuning for
  *format* on a small open model via Ollama, as the one case where it earns its place.
- Add an ingestion webhook path (`copilot serve` → `/v1/ingest`) so repo pushes trigger re-indexing
  without a cron.
- Re-evaluate long-context stuffing as a per-team option when the corpus is small and a provider
  with cached long prompts makes it cheaper than retrieval; the `Pipeline` already has the budget
  hook to enable it.

