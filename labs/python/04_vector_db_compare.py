#!/usr/bin/env python3
"""
Module 04 lab — the same 50 chunks, four vector stores, one honest comparison.

Indexes 50 chunks from data/knowledge/*.md (embedded once, locally, with
all-MiniLM-L6-v2) into:

  * numpy brute force          — the ground truth; exact cosine over a (50, 384) matrix
  * FAISS IndexFlatIP          — exact, in-process, C++
  * FAISS IndexHNSWFlat        — approximate graph index (HNSW), in-process
  * Chroma (EphemeralClient)   — embedded database with metadata filtering, HNSW inside
  * Qdrant                     — only if QDRANT_URL is set (docker run -p 6333:6333 qdrant/qdrant)

and reports, for 12 on-call questions:

  * recall@5 against brute force  (what fraction of the true top-5 did the index return)
  * mean and p95 query latency    (wall clock, includes client overhead for Chroma/Qdrant)

The point is not that one wins. At 50 vectors every index returns the exact answer and
brute force is the fastest thing in the room. The point is to make you *measure* that,
so when the corpus is 5 million chunks you know which number to watch and what knob
(efSearch, M, quantisation, filtering strategy) moves it. Re-run with --scale 200 to
replicate the chunks with noise and watch the numbers start to separate.

    python labs/python/04_vector_db_compare.py
    python labs/python/04_vector_db_compare.py --scale 200 --k 5
    QDRANT_URL=http://localhost:6333 python labs/python/04_vector_db_compare.py

The Go capstone's `internal/vectorstore/memory.go` is the brute-force column of this
table, `qdrant.go` and `chroma.go` are the HTTP adapters, and COPILOT_VECTORSTORE picks
between them. This lab is the evidence behind ADR "start with memory, swap to Qdrant
when N > ~100k or when you need filtering at scale".
"""

from __future__ import annotations

import argparse
import os
import re
import statistics
import sys
import textwrap
import time
import uuid
from dataclasses import dataclass, field
from pathlib import Path

import numpy as np

KNOWLEDGE = Path(__file__).resolve().parents[2] / "data" / "knowledge"
N_CHUNKS = 50
DIM = 384

QUERIES = [
    "payments-api OOMKilled remediation",
    "roll back ConfigMap v42 to v41",
    "secondary on-call paged after how many minutes",
    "node out of ENI IP addresses",
    "PLEG is not healthy containerd",
    "drain blocked by PodDisruptionBudget",
    "duplicate authorizations caused by Redis eviction",
    "maxmemory-policy volatile-lru",
    "payments-general instance type and node count",
    "readiness probe depends on Redis",
    "HPA maxReplicas pgbouncer connection ceiling",
    "change freeze during SEV2",
]


# --------------------------------------------------------------------------------------
# Chunking: sliding window over words. Module 05 does this properly; here we only need
# 50 reasonably sized pieces with a `source` metadata field to filter on.
# --------------------------------------------------------------------------------------
@dataclass
class Chunk:
    id: str
    source: str
    kind: str  # runbook | postmortem | manifest | policy
    text: str


def kind_of(name: str) -> str:
    if name.startswith("runbook"):
        return "runbook"
    if name.startswith("postmortem"):
        return "postmortem"
    if name.endswith((".yaml", ".tf")):
        return "manifest"
    return "policy"


def load_chunks(n: int, window: int = 110, stride: int = 80) -> list[Chunk]:
    if not KNOWLEDGE.exists():
        sys.exit(f"knowledge base not found at {KNOWLEDGE}")
    per_file: dict[str, list[str]] = {}
    for path in sorted(KNOWLEDGE.iterdir()):
        if not path.is_file():
            continue
        words = re.sub(r"\s+", " ", path.read_text(encoding="utf-8")).split(" ")
        per_file[path.name] = [" ".join(words[i:i + window]) for i in range(0, max(1, len(words) - window + 1), stride)]
    # Round-robin across files so all six sources are represented in the first 50.
    chunks: list[Chunk] = []
    i = 0
    while len(chunks) < n and any(per_file.values()):
        for name, pieces in per_file.items():
            if i < len(pieces):
                chunks.append(Chunk(id=f"{name}#{i}", source=name, kind=kind_of(name), text=pieces[i]))
                if len(chunks) == n:
                    break
        i += 1
    return chunks


def scale_up(vecs: np.ndarray, chunks: list[Chunk], factor: int, rng: np.random.Generator) -> tuple[np.ndarray, list[Chunk]]:
    """Replicate the corpus with Gaussian noise so ANN behaviour becomes visible.

    Synthetic, and the noise makes neighbours unrealistically close to each other, which
    is the *hard* case for HNSW — good for seeing recall drop, not for estimating prod.
    """
    if factor <= 1:
        return vecs, chunks
    reps = [vecs]
    new_chunks = list(chunks)
    for r in range(1, factor):
        noisy = vecs + rng.normal(0, 0.05, vecs.shape).astype(np.float32)
        noisy /= np.linalg.norm(noisy, axis=1, keepdims=True)
        reps.append(noisy)
        new_chunks += [Chunk(id=f"{c.id}~{r}", source=c.source, kind=c.kind, text=c.text) for c in chunks]
    return np.vstack(reps), new_chunks


# --------------------------------------------------------------------------------------
# Each backend exposes search(query_vec, k) -> list[str] of chunk ids.
# --------------------------------------------------------------------------------------
@dataclass
class Result:
    name: str
    recall: float
    mean_ms: float
    p95_ms: float
    build_ms: float
    notes: str = ""
    filtered_ok: bool | None = None


def timed(fn, *args):
    t0 = time.perf_counter()
    out = fn(*args)
    return out, (time.perf_counter() - t0) * 1000


def recall_at_k(truth: list[list[str]], got: list[list[str]], k: int) -> float:
    return statistics.mean(len(set(t[:k]) & set(g[:k])) / k for t, g in zip(truth, got))


def bench(name: str, search, queries: np.ndarray, truth: list[list[str]], k: int, repeats: int = 5) -> tuple[list[list[str]], float, float]:
    got: list[list[str]] = []
    lat: list[float] = []
    for q in queries:
        ids = None
        for _ in range(repeats):
            ids, ms = timed(search, q, k)
            lat.append(ms)
        got.append(ids)
    lat.sort()
    return got, statistics.mean(lat), lat[int(0.95 * (len(lat) - 1))]


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--k", type=int, default=5)
    ap.add_argument("--scale", type=int, default=1, help="replicate corpus N times with noise")
    ap.add_argument("--ef", type=int, default=16, help="HNSW efSearch for FAISS (higher = more recall, slower)")
    args = ap.parse_args()
    k = args.k
    rng = np.random.default_rng(7)

    from sentence_transformers import SentenceTransformer
    model = SentenceTransformer("sentence-transformers/all-MiniLM-L6-v2")

    chunks = load_chunks(N_CHUNKS)
    print(f"loaded {len(chunks)} chunks from {len({c.source for c in chunks})} files")
    vecs = model.encode([c.text for c in chunks], normalize_embeddings=True).astype(np.float32)
    vecs, chunks = scale_up(vecs, chunks, args.scale, rng)
    qvecs = model.encode(QUERIES, normalize_embeddings=True).astype(np.float32)
    ids = [c.id for c in chunks]
    n = len(chunks)
    print(f"corpus: {n} vectors x {DIM} dims = {n * DIM * 4 / 1e6:.2f} MB float32; {len(QUERIES)} queries; k={k}")

    results: list[Result] = []

    # ---- 0. numpy brute force: the truth -------------------------------------------
    def np_search(q: np.ndarray, kk: int) -> list[str]:
        sims = vecs @ q
        top = np.argpartition(-sims, kk)[:kk]
        return [ids[i] for i in top[np.argsort(-sims[top])]]

    truth, mean_ms, p95 = bench("numpy", np_search, qvecs, [[]] * len(QUERIES), k)
    results.append(Result("numpy brute force (truth)", 1.0, mean_ms, p95, 0.0,
                          "exact; this is internal/vectorstore/memory.go"))

    # ---- 1. FAISS flat ---------------------------------------------------------------
    try:
        import faiss
    except ImportError:
        print("faiss-cpu not installed; skipping FAISS")
        faiss = None
    if faiss:
        t0 = time.perf_counter()
        flat = faiss.IndexFlatIP(DIM)  # inner product == cosine on unit vectors
        flat.add(vecs)
        build = (time.perf_counter() - t0) * 1000

        def faiss_flat_search(q: np.ndarray, kk: int) -> list[str]:
            _, idx = flat.search(q[None, :], kk)
            return [ids[i] for i in idx[0]]

        got, mean_ms, p95 = bench("faiss-flat", faiss_flat_search, qvecs, truth, k)
        results.append(Result("FAISS IndexFlatIP", recall_at_k(truth, got, k), mean_ms, p95, build,
                              "exact; SIMD brute force, no filtering, no persistence of metadata"))

        # ---- 2. FAISS HNSW -----------------------------------------------------------
        t0 = time.perf_counter()
        hnsw = faiss.IndexHNSWFlat(DIM, 32, faiss.METRIC_INNER_PRODUCT)  # M=32 links per node
        hnsw.hnsw.efConstruction = 200
        hnsw.add(vecs)
        hnsw.hnsw.efSearch = args.ef
        build = (time.perf_counter() - t0) * 1000

        def faiss_hnsw_search(q: np.ndarray, kk: int) -> list[str]:
            _, idx = hnsw.search(q[None, :], kk)
            return [ids[i] for i in idx[0] if i >= 0]

        got, mean_ms, p95 = bench("faiss-hnsw", faiss_hnsw_search, qvecs, truth, k)
        results.append(Result(f"FAISS IndexHNSWFlat M=32 ef={args.ef}", recall_at_k(truth, got, k), mean_ms, p95, build,
                              "approximate; recall is a knob (efSearch), memory ~ N*(4*dim + 8*M) bytes"))

    # ---- 3. Chroma (embedded) ---------------------------------------------------------
    try:
        import chromadb
    except ImportError:
        print("chromadb not installed; skipping Chroma")
        chromadb = None
    if chromadb:
        client = chromadb.EphemeralClient()
        t0 = time.perf_counter()
        col = client.create_collection(name=f"kb_{uuid.uuid4().hex[:8]}", metadata={"hnsw:space": "cosine"})
        # We pass our own embeddings so the comparison is about the store, not the model.
        for start in range(0, n, 500):  # Chroma caps batch size; 500 is safe everywhere
            sl = slice(start, start + 500)
            col.add(ids=ids[sl], embeddings=vecs[sl].tolist(), documents=[c.text for c in chunks[sl]],
                    metadatas=[{"source": c.source, "kind": c.kind} for c in chunks[sl]])
        build = (time.perf_counter() - t0) * 1000

        def chroma_search(q: np.ndarray, kk: int) -> list[str]:
            return col.query(query_embeddings=[q.tolist()], n_results=kk)["ids"][0]

        got, mean_ms, p95 = bench("chroma", chroma_search, qvecs, truth, k)
        # Metadata filtering — the thing FAISS cannot do for you.
        filtered = col.query(query_embeddings=[qvecs[0].tolist()], n_results=k, where={"kind": "runbook"})
        filtered_ok = all(m["kind"] == "runbook" for m in filtered["metadatas"][0])
        results.append(Result("Chroma EphemeralClient (HNSW)", recall_at_k(truth, got, k), mean_ms, p95, build,
                              "embedded; where={} filtering; PersistentClient for disk; HTTP server mode for Go",
                              filtered_ok))

    # ---- 4. Qdrant (only if reachable) -------------------------------------------------
    qdrant_url = os.environ.get("QDRANT_URL")
    if qdrant_url:
        try:
            from qdrant_client import QdrantClient
            from qdrant_client.models import Distance, FieldCondition, Filter, MatchValue, PointStruct, VectorParams
            qc = QdrantClient(url=qdrant_url, timeout=10)
            cname = f"kb_{uuid.uuid4().hex[:8]}"
            t0 = time.perf_counter()
            qc.create_collection(cname, vectors_config=VectorParams(size=DIM, distance=Distance.COSINE))
            points = [PointStruct(id=i, vector=vecs[i].tolist(),
                                  payload={"chunk_id": ids[i], "source": chunks[i].source, "kind": chunks[i].kind})
                      for i in range(n)]
            for start in range(0, n, 256):
                qc.upsert(cname, points=points[start:start + 256], wait=True)
            build = (time.perf_counter() - t0) * 1000

            def qdrant_search(q: np.ndarray, kk: int, flt: Filter | None = None) -> list[str]:
                if hasattr(qc, "query_points"):  # qdrant-client >= 1.10
                    pts = qc.query_points(cname, query=q.tolist(), limit=kk, query_filter=flt, with_payload=True).points
                else:  # older clients
                    pts = qc.search(cname, query_vector=q.tolist(), limit=kk, query_filter=flt, with_payload=True)
                return [p.payload["chunk_id"] for p in pts]

            got, mean_ms, p95 = bench("qdrant", qdrant_search, qvecs, truth, k)
            flt = Filter(must=[FieldCondition(key="kind", match=MatchValue(value="runbook"))])
            filtered_ids = qdrant_search(qvecs[0], k, flt)
            filtered_ok = all(i.startswith("runbook") for i in filtered_ids)
            results.append(Result(f"Qdrant @ {qdrant_url}", recall_at_k(truth, got, k), mean_ms, p95, build,
                                  "server; HNSW + payload filter inside the graph walk; gRPC Go client; latency = network",
                                  filtered_ok))
            qc.delete_collection(cname)
        except Exception as exc:  # noqa: BLE001 — report and continue; this is a lab
            print(f"Qdrant at {qdrant_url} failed: {exc}")
    else:
        print("QDRANT_URL not set; skipping Qdrant (docker run -p 6333:6333 qdrant/qdrant)")

    # ---- Report ----------------------------------------------------------------------
    print("\n" + "-" * 118)
    print(f"{'backend':<38}{'recall@%d' % k:>10}{'mean ms':>10}{'p95 ms':>10}{'build ms':>11}  {'filter':<7} notes")
    print("-" * 118)
    for r in results:
        filt = "-" if r.filtered_ok is None else ("ok" if r.filtered_ok else "FAIL")
        print(f"{r.name:<38}{r.recall:>10.3f}{r.mean_ms:>10.3f}{r.p95_ms:>10.3f}{r.build_ms:>11.1f}  {filt:<7} {r.notes}")
    print("-" * 118)
    print(textwrap.dedent(f"""
        How to read this at N={n}:
          - Everything is exact and sub-millisecond. Brute force wins because the matrix fits in
            L2 cache; the Go in-memory store is the right default for a team-sized knowledge base.
          - The HNSW/Chroma/Qdrant rows are paying fixed overhead (graph walk, Python<->C++,
            HTTP/gRPC) that only amortises when N is large. Run with --scale 200 (N=10k) and
            then --ef 8 vs --ef 64 to see recall move while brute force latency climbs linearly.
          - "filter" is the column that decides real systems. Filtering *after* an ANN search
            (post-filter) silently returns fewer than k results when the filter is selective;
            Qdrant and Chroma filter inside the search. FAISS makes you do it yourself.
          - Qdrant's latency here is mostly network. That is the price of a store that several
            copilot replicas can share; FAISS and Chroma-embedded live and die with one process.
    """))
    return 0


if __name__ == "__main__":
    sys.exit(main())
