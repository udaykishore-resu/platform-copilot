# ADR-0005 · Hybrid retrieval: BM25 fused with vector search by reciprocal rank fusion

**Status:** Accepted · **Date:** 2025-09 · **Owner:** Udaykishore Resu · **Modules:** 03 (Semantic
Search), 04, 05 (Retrieval Process)

## Context

The capstone's corpus is unlike the prose corpora most RAG tutorials assume. Roughly half of it is
structured text — Kubernetes YAML, Helm values, Terraform HCL, PromQL alert rules — where the
information an engineer needs is carried by exact identifiers: `payments-api`, `readinessProbe`,
`kube_pod_container_status_restarts_total`, `v2.31.0`, an IAM role ARN. The other half is runbooks
and postmortems, where questions are phrased differently from the text that answers them ("why does
the pod keep dying?" vs "CrashLoopBackOff remediation").

Dense embedding retrieval is excellent at the second kind of match and mediocre at the first:
embedding models tokenise identifiers into fragments, and two manifests that differ only in a
service name embed to nearly the same vector. During Module 05's evaluation on the sample corpus,
pure vector retrieval returned the wrong service's Deployment for a question that named the service,
because the surrounding YAML dominated the embedding. Lexical retrieval (BM25) has the opposite
profile: it nails exact identifiers and version strings and fails on paraphrase.

There is also the mock embedder to consider. `internal/embeddings/mock.go` is a deterministic hashed
bag-of-words, which makes `COPILOT_EMBED_PROVIDER=mock` rank "semantically-ish" but far worse than a
real model; labs and CI run on it. Lexical retrieval keeps the offline experience useful rather than
embarrassing.

Finally, the retriever must work with all three stores (ADR-0004), two of which are external
services with different native hybrid-search features (Qdrant has sparse vectors; Chroma has
full-text `where_document` filtering but no ranking fusion).

## Decision

Retrieval is hybrid by default (`COPILOT_HYBRID=true`):

1. The query is embedded and searched in the configured `vectorstore.Store` for the top `k_vec`
   hits.
2. The same query is tokenised and scored against an in-process **BM25** index
   (`internal/vectorstore/bm25.go`, Okapi BM25 with `k1=1.2`, `b=0.75`, a tokenizer (`Tokenize`) that lower-cases and
   keeps identifiers like `payments-api` and `v1.9.0` intact) for the top
   `k_lex` hits.
3. The two ranked lists are merged with **reciprocal rank fusion** (`internal/vectorstore/rrf.go`,
   `score = Σ 1/(60 + rank)`), deduplicated by chunk ID, and the top `k` fused hits are returned by
   the `Retriever`, which applies `MinScore` to the dense list before fusion (BM25 scores are not
   comparable across queries) and records `score_vector` / `score_bm25` in each hit's metadata.

The BM25 index is built in-process by the `Retriever` on first use from `Store.All()`, so it works
the same over `memory`, `qdrant` and `chroma` and nothing extra is persisted. `copilot search
--explain` prints each hit's vector and BM25 scores next to the fused score so the behaviour is
inspectable; `--dense-only` turns fusion off for one query and `COPILOT_HYBRID=false` globally.

## Alternatives considered

| Option | Why not |
|---|---|
| **Vector-only retrieval** | Simplest and what most tutorials show. Measurably worse on this corpus: recall@5 on the Module 05 eval set dropped noticeably when `COPILOT_HYBRID=false`, with the misses concentrated on questions naming a specific resource or version. Also degrades badly on the mock embedder. |
| **BM25-only retrieval** | Cheap, deterministic, no embedding cost — and a surprisingly strong baseline on manifests. Fails on paraphrased questions against runbooks, which is the majority of real questions. Not exposed as a mode today; the mock embedder plus BM25 fusion (`COPILOT_HYBRID=true`) is the air-gapped path with no real embedder. |
| **Weighted score fusion** (normalise both scores to [0,1] and combine with α) | Requires calibrating α per corpus and per embedding model, and BM25 score ranges shift with query length. RRF needs no tuning and is robust to scale differences; the literature and practice (Elasticsearch, Weaviate, Azure AI Search all default to RRF) agree. |
| **Store-native hybrid search** (Qdrant sparse vectors + fusion, Weaviate hybrid, Elasticsearch) | Uses the best available implementation per store, but makes behaviour differ across stores and leaves the `Memory` store without hybrid search. The in-process approach is portable and is the measured baseline; native hybrid is listed as a follow-up. |
| **Cross-encoder re-ranking after retrieval** (Cohere Rerank, bge-reranker) | Improves precision and is compatible with this decision — it sits after fusion. Deferred: it adds a model call (latency, cost or a local model) and the fused top-k was already good enough for the eval set. Hook left in `Retriever` as an optional `Reranker`. |
| **Query expansion / HyDE** (generate a hypothetical answer and embed that) | Helps paraphrase recall but doubles model calls per question and does nothing for exact identifiers. Not adopted. |
| **Metadata-first routing** (detect a resource name in the query, filter the store by it) | Powerful when it works, brittle when the detector misses. BM25 achieves most of the benefit without a detector. |

## Consequences

**Positive.**
- Retrieval quality on mixed prose-plus-config corpora improves without tuning; exact identifiers
  and versions are found reliably and paraphrased questions still work.
- Offline and CI retrieval on the mock embedder is genuinely useful because BM25 carries it.
- Identical behaviour across `memory`, `qdrant` and `chroma` stores; `copilot search --explain`
  makes the fusion visible, which is the teaching payoff for Module 05's "Retrieval Process" node.
- A deterministic lexical path provides a fallback when the embedding provider is down: `Retriever`
  degrades to BM25-only and marks the answer as degraded.

**Negative / costs.**
- Two indexes to build and persist; ingest takes longer and the `.copilot/` directory grows. BM25
  postings for the target corpus are small (tens of MB) so this is acceptable.
- The BM25 index is in-process and per-replica; with `copilot serve` scaled out, each replica
  rebuilds it from the store at startup, which adds seconds and memory. The Qdrant-native follow-up
  removes this.
- Two retrieval calls per question add a few milliseconds; negligible against the model call.
- RRF's constant (60) and the `k_vec`/`k_lex` split (default 20/20 fused to `k=6`) are defaults from
  the literature, not tuned to this corpus; the eval harness exists to tune them if needed.
- The similarity threshold applies only to the vector score, so a chunk surfaced by BM25 alone can
  reach the prompt with a low vector score. This is intentional (exact-match hits are usually right)
  but means the "no relevant sources" refusal path relies on BM25 returning nothing, which it does
  for nonsense queries but not for queries sharing common tokens with the corpus.

**Follow-ups.**
- Implement the Qdrant-native hybrid path (sparse vectors with a SPLADE or BM25-derived encoder) as
  an alternative and compare on the eval set.
- Add the optional cross-encoder `Reranker` and measure precision@3 before and after.
- Tokenizer improvements for Terraform (`module.x.y` paths) and PromQL (label matchers) once the
  eval set has enough such questions to measure the effect.

