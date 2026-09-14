# ADR-0004 · An in-memory vector store first; Qdrant (and Chroma) behind the same interface for production

**Status:** Accepted · **Date:** 2025-09 · **Owner:** Udaykishore Resu · **Modules:** 04 (Vector
Databases), 05

## Context

Module 04 of the roadmap lists eight vector databases (Chroma, Pinecone, Weaviate, FAISS, LanceDB,
Qdrant, Supabase pgvector, MongoDB Atlas) and asks the learner to "pick one". The capstone has to
make that choice while still teaching what a vector store *is* — an index over high-dimensional
vectors supporting nearest-neighbour search with metadata filtering — rather than how to drive one
vendor's client.

The operational facts for this product: a platform team's corpus is tens of thousands of chunks at
most (a large runbook repo plus manifests and a few hundred postmortems), embedded at 768–1536
dimensions, which is a few hundred megabytes of float32 and brute-force searchable in single-digit
milliseconds on a laptop. The copilot must run from a clean clone with no services
(`COPILOT_PROVIDER=mock`, no Docker) so every lab and CI job works offline. In production, the same
team will want persistence that survives restarts, concurrent writers (the ingest webhook and the
CLI), metadata filtering for access control (ADR-0003), and eventually a corpus shared by several
copilot replicas behind `copilot serve`.

The roadmap's "Implementing Vector Search", "Indexing Embeddings" and "Performing Similarity Search"
nodes are best taught by implementing the naive version — it makes the point that approximate
indexes (HNSW, IVF) are an optimisation you adopt when brute force stops being fast enough, not a
prerequisite for vector search.

## Decision

`internal/vectorstore` defines one interface, `Store`, with `Name()`, `Upsert(ctx, []Document)`,
`Search(ctx, vector, k, filter) ([]Hit, error)`, `Count(ctx)`, `All(ctx)` (used to build the BM25
index) and `Delete(ctx, ids)`, where `Document{ID, Text, Metadata, Vector}` and `Hit{Document,
Score}`.

Three implementations:

- **`Memory`** — the default (`COPILOT_VECTORSTORE=memory`). Brute-force cosine search over an
  in-process slice, with metadata filtering and JSON persistence to `COPILOT_INDEX_PATH` (default
  `.copilot/index.json`) on every upsert and delete. The BM25 index used for hybrid search
  (ADR-0005) is built lazily by the `Retriever` from `All()`.
- **`Qdrant`** — the recommended production store (`COPILOT_VECTORSTORE=qdrant`, `QDRANT_URL`).
  Implemented against Qdrant's REST API over raw HTTP (ADR-0009): collection creation with cosine
  distance and `hnsw_config`, batched point upserts with the chunk metadata as payload, filtered
  search, and scroll for `All()`.
- **`Chroma`** — a second external adapter (`COPILOT_VECTORSTORE=chroma`, `CHROMA_URL`) to prove the
  interface is not shaped around one vendor, and because Chroma is the store most Python-first teams
  already run.

The chunker, embedder, retriever and RAG pipeline depend only on `Store`. `make ingest` and `copilot
search` work identically against all three; the integration tests for Qdrant and Chroma skip when
their URLs are unset.

## Alternatives considered

| Option | Why not (as the first or only choice) |
|---|---|
| **Qdrant from day one** | Adds a running service to every lab and CI job, hides the mechanism the module must teach, and is unnecessary at this corpus size. Chosen for production instead. |
| **Chroma first** | Same service dependency; its Go story is HTTP-only, like Qdrant. Included as a second adapter rather than the default. |
| **FAISS** | Excellent library, but C++/Python with no maintained Go binding; it is an index, not a store (no persistence of payloads, no filtering out of the box). Covered conceptually in Module 04 as the canonical ANN library. |
| **pgvector (Supabase/Postgres)** | The right answer for teams that already run Postgres and want transactional co-location of vectors with relational data. Not chosen as default because it needs a database, and not as the production recommendation because the team in question typically does not want the copilot's index in their production Postgres. Straightforward to add as a fourth adapter. |
| **Pinecone / Weaviate Cloud / MongoDB Atlas Vector Search** | Managed services with good scaling stories, but a hosted dependency and account for a course repo, and for a platform team a data-residency question. Compared in Module 04's tables; not implemented. |
| **LanceDB** | Embedded, columnar, promising for exactly this "no service" case — but Go bindings were immature at decision time. Worth re-evaluating as a replacement for `Memory`'s JSON persistence. |
| **HNSW in-process (e.g. a Go HNSW library)** | Would make `Memory` scale further, but brute force is fast enough at the target size and the extra dependency obscures the teaching point. Documented as the first optimisation to make if `Count()` passes a few hundred thousand. |

## Consequences

**Positive.**
- Zero-dependency default: `git clone && make ingest && copilot ask` works with no Docker, no
  accounts, no keys.
- The naive implementation is the lesson: `Memory.Search` is a loop computing cosine similarity and
  a partial sort, which is exactly what the module's "Performing Similarity Search" card describes.
- Switching to Qdrant for production is a two-variable change (`COPILOT_VECTORSTORE`, `QDRANT_URL`)
  and a re-run of `make ingest`; nothing above the interface changes.
- Metadata filtering in the interface from the start means access control (ADR-0003) and per-source
  eval disaggregation (Module 09) are not retrofits.
- Two external adapters keep the interface honest and document the differences (Qdrant's filter DSL
  vs Chroma's `where`).

**Negative / costs.**
- `Memory` is single-process: concurrent writers from the CLI and `copilot serve` would race on the
  JSON file. Mitigated with a file lock and documented as the reason to move to Qdrant when running
  the server.
- JSON persistence of float32 vectors is large and slow to load for big corpora (hundreds of MB,
  seconds of startup). Acceptable for the target size; the Production notes in Module 04 set the
  threshold at which to move.
- Brute force is O(n) per query; at roughly 500k vectors on a single core, latency becomes
  user-visible. Qdrant's HNSW is the answer, and the interface makes that a configuration change,
  but students must understand the cliff exists.
- Two external adapters to maintain against evolving REST APIs, each with integration tests that
  only run when a service is available; wire-format drift is caught by the nightly CI job, not on
  PRs.
- Feature asymmetry: hybrid BM25 search (ADR-0005) is implemented in-process and therefore works
  with all three stores by fusing a local BM25 index with the store's vector hits, rather than using
  Qdrant's native sparse vectors. Simpler and portable, at the cost of not using the best native
  feature.

**Follow-ups.**
- Add a `pgvector` adapter when a team asks for it; the interface needs no change.
- Benchmark `Memory` with an optional HNSW index at 100k, 500k and 1M vectors and publish the
  numbers in Module 04.
- Evaluate Qdrant's native sparse-vector hybrid search as an alternative implementation of ADR-0005
  once the in-process version's behaviour is the measured baseline.

