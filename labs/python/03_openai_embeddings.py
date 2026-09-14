#!/usr/bin/env python3
"""
Module 03 lab — OpenAI Embeddings API: text-embedding-3-small, the `dimensions`
parameter, cost estimation, and a side-by-side with sentence-transformers.

Requires OPENAI_API_KEY. Without it the script prints the cost estimate (which only
needs tiktoken) and exits 0, so CI can still import and smoke-test it.

    export OPENAI_API_KEY=sk-...
    python labs/python/03_openai_embeddings.py

What to look for in the output:

  * Truncating text-embedding-3-small from 1536 to 256 dimensions (`dimensions=256`)
    barely changes the ranking on this corpus. Matryoshka-trained models front-load
    information into the leading dimensions, so the trade is 6x less storage for a
    small, measurable recall loss — measurable is the key word; the script measures it.
  * The cost of embedding the whole knowledge base is fractions of a cent. The cost
    that matters is re-embedding on every ingest and embedding every query; the
    script separates those.
  * MiniLM and text-embedding-3-small agree on the easy questions and disagree on
    the ones that need domain vocabulary. Neither is "right" — the question is
    whether the difference is worth a network hop and a vendor dependency.

In the Go capstone the equivalent is `internal/embeddings/openai.go` behind the
`Embedder` interface, selected with COPILOT_EMBED_PROVIDER=openai.
"""

from __future__ import annotations

import os
import sys
import textwrap
import time
from pathlib import Path

import numpy as np
import tiktoken

MODEL = "text-embedding-3-small"      # 1536 dims by default; supports dimensions=
LARGE_MODEL = "text-embedding-3-large"  # 3072 dims; ~6.5x the price of -small (see pricing page)

# Pricing is quoted per million input tokens on https://openai.com/api/pricing/ and
# changes. We read it from the environment so the number in this file never goes stale
# silently; the default is only used to show the order of magnitude.
PRICE_PER_MTOK_SMALL = float(os.environ.get("OPENAI_EMBED_SMALL_PRICE_PER_MTOK", "0.02"))

CORPUS = [
    ("crashloop/oom",
     "If Last State reason is OOMKilled, raise the payments-api memory limit to 1.5Gi and roll "
     "back ConfigMap payments-api-config from v42 to v41 (LEDGER_BATCH_SIZE 5000 -> 500)."),
    ("crashloop/secret",
     "Exit code 1 with 'required key PSP_API_KEY not set': External Secrets Operator has not "
     "synced payments-api-secrets from AWS Secrets Manager path prod/payments/payments-api."),
    ("crashloop/readiness",
     "Pods Running but not Ready: /readyz returns 503 when pgbouncer reports no more connections; "
     "scale the HPA maxReplicas down to 14 as a stopgap."),
    ("node/pleg",
     "kubelet 'PLEG is not healthy' means containerd is wedged; restart containerd and kubelet, "
     "and replace the node if it recurs."),
    ("node/eni",
     "NodeENIExhausted: an m6i.2xlarge has 58 usable pod IPs; cordon the node and enable prefix "
     "delegation (PLAT-1790)."),
    ("node/drain",
     "Drain with --ignore-daemonsets --delete-emptydir-data; the payments-api PDB minAvailable 4 "
     "blocks the drain unless 5 replicas are ready elsewhere."),
    ("redis/rootcause",
     "ledger-worker v1.9.0 cached event payloads in redis-payments without TTL; allkeys-lru "
     "evicted idempotency keys and caused 1,212 duplicate authorizations."),
    ("redis/fix",
     "Switch maxmemory-policy to volatile-lru, move idempotency keys to redis-idempotency with "
     "noeviction, alert on rate(redis_evicted_keys_total[5m]) > 0."),
    ("policy/sev",
     "SEV1 means customers cannot pay; acknowledge within 5 minutes. SEV2 is material "
     "degradation with a workaround; acknowledge within 15 minutes."),
    ("policy/escalation",
     "Secondary is paged after 10 minutes unacknowledged for SEV2; the engineering manager is "
     "escalated after 30 minutes without a mitigation path."),
    ("tf/nodegroup",
     "payments-general node group: m6i.2xlarge, min 3 desired 6 max 12, label "
     "nodegroup=payments-general. platform-system: 3 x m6i.xlarge, tainted dedicated=platform-system."),
    ("deploy/hpa",
     "HPA for payments-api scales 6 to 14 replicas on 65% CPU; max was lowered from 20 because "
     "20 pods x 40 connections exceeds the 600-connection RDS ceiling."),
]

QUERIES = [
    ("payments pod OOMKilled remediation", "crashloop/oom"),
    ("who gets paged if the primary does not ack a SEV2", "policy/escalation"),
    ("node ran out of pod IP addresses", "node/eni"),
    ("why were there duplicate card holds in March", "redis/rootcause"),
    ("what EC2 instance type do payments nodes use", "tf/nodegroup"),
    ("how many replicas can the API autoscale to", "deploy/hpa"),
    ("secret missing at startup", "crashloop/secret"),
    ("containerd hung on a worker", "node/pleg"),
]


def banner(title: str) -> None:
    print("\n" + "=" * 88 + "\n" + title + "\n" + "=" * 88)


def count_tokens(texts: list[str]) -> int:
    # text-embedding-3-* use the cl100k_base tokenizer. tiktoken.encoding_for_model knows this.
    enc = tiktoken.encoding_for_model(MODEL)
    return sum(len(enc.encode(t)) for t in texts)


def cost_estimate() -> None:
    banner("Cost estimate (tiktoken only — no API call)")
    corpus_tokens = count_tokens([t for _, t in CORPUS])
    query_tokens = count_tokens([q for q, _ in QUERIES])
    root = Path(__file__).resolve().parents[2] / "data" / "knowledge"
    kb_tokens = 0
    if root.exists():
        kb_tokens = count_tokens([p.read_text(encoding="utf-8") for p in root.iterdir() if p.is_file()])

    def usd(tokens: int) -> str:
        return f"${tokens / 1_000_000 * PRICE_PER_MTOK_SMALL:.6f}"

    print(f"  price assumed for {MODEL}: ${PRICE_PER_MTOK_SMALL}/M tokens "
          f"(override with OPENAI_EMBED_SMALL_PRICE_PER_MTOK; check the pricing page)")
    print(f"  inline corpus:        {corpus_tokens:>7} tokens  {usd(corpus_tokens)}")
    print(f"  8 queries:            {query_tokens:>7} tokens  {usd(query_tokens)}")
    if kb_tokens:
        print(f"  data/knowledge (6 files): {kb_tokens:>5} tokens  {usd(kb_tokens)} per full re-ingest")
        print(f"  ...re-ingested hourly for a year: {usd(kb_tokens * 24 * 365)}")
        print(f"  ...1,000 on-call questions/day for a year at ~12 tokens each: {usd(12 * 1000 * 365)}")
    print(textwrap.dedent("""
        Conclusions you can defend in a design review:
          - Embedding cost is dominated by re-ingestion, not by queries. Hash chunks and skip
            unchanged ones (the capstone's ingest does this by content hash).
          - Storage and search cost scale with dimensions x vectors, and that cost is per
            month forever. `dimensions=` is a storage decision more than an API decision.
          - text-embedding-3-large is several times the price of -small and 2x the vector
            size. Only pay for it after a recall measurement shows -small is the bottleneck.
    """))


def cosine_matrix(a: np.ndarray, b: np.ndarray) -> np.ndarray:
    a = a / np.linalg.norm(a, axis=1, keepdims=True)
    b = b / np.linalg.norm(b, axis=1, keepdims=True)
    return a @ b.T


def evaluate(name: str, doc_vecs: np.ndarray, query_vecs: np.ndarray, ids: list[str]) -> float:
    """Print top-3 per query and return hit@1 across QUERIES."""
    sims = cosine_matrix(query_vecs, doc_vecs)
    hits = 0
    print(f"\n  [{name}]  dims={doc_vecs.shape[1]}")
    for i, (q, expected) in enumerate(QUERIES):
        order = np.argsort(-sims[i])
        top1 = ids[order[0]]
        ok = top1 == expected
        hits += ok
        top3 = ", ".join(f"{ids[j]}({sims[i, j]:.2f})" for j in order[:3])
        print(f"    {'ok ' if ok else 'MISS'} {q!r:<52} -> {top3}")
    print(f"    hit@1 = {hits}/{len(QUERIES)}")
    return hits / len(QUERIES)


def main() -> int:
    cost_estimate()

    if not os.environ.get("OPENAI_API_KEY"):
        print("\nOPENAI_API_KEY not set — skipping the live API section. Export a key to run it.")
        return 0

    from openai import OpenAI  # imported late so the cost section works without the SDK configured
    client = OpenAI()

    ids = [i for i, _ in CORPUS]
    docs = [t for _, t in CORPUS]
    queries = [q for q, _ in QUERIES]

    banner("OpenAI Embeddings API — text-embedding-3-small at 1536 and 256 dimensions")
    results: dict[str, float] = {}
    for dims in (1536, 256):
        t0 = time.perf_counter()
        # One request can carry up to 2048 inputs / 8192 tokens per input. Batch; do not
        # loop one text per request — latency is per round trip, not per token.
        resp = client.embeddings.create(model=MODEL, input=docs + queries, dimensions=dims)
        dt = time.perf_counter() - t0
        vecs = np.array([d.embedding for d in resp.data], dtype=np.float32)
        print(f"\n  {len(docs) + len(queries)} texts, dimensions={dims}: "
              f"{dt * 1000:.0f} ms round trip, {resp.usage.total_tokens} tokens billed")
        # The API returns unit-length vectors, so dot product is already cosine. Truncated
        # vectors (dimensions < 1536) are re-normalised server-side as well.
        print(f"  norm of first vector: {np.linalg.norm(vecs[0]):.4f} (unit length)")
        results[f"{MODEL}@{dims}"] = evaluate(f"{MODEL} dims={dims}", vecs[: len(docs)], vecs[len(docs):], ids)
        print(f"  storage for 100k chunks as float32: {100_000 * dims * 4 / 1e9:.2f} GB")

    banner("Compare: sentence-transformers/all-MiniLM-L6-v2 (local, free, 384 dims)")
    try:
        from sentence_transformers import SentenceTransformer
    except ImportError:
        print("  sentence-transformers not installed; skipping comparison.")
    else:
        st = SentenceTransformer("sentence-transformers/all-MiniLM-L6-v2")
        t0 = time.perf_counter()
        st_vecs = st.encode(docs + queries, normalize_embeddings=True)
        dt = time.perf_counter() - t0
        print(f"  {len(docs) + len(queries)} texts locally: {dt * 1000:.0f} ms on CPU, $0")
        results["all-MiniLM-L6-v2@384"] = evaluate("all-MiniLM-L6-v2", st_vecs[: len(docs)], st_vecs[len(docs):], ids)

    banner("Summary")
    for k, v in results.items():
        print(f"  {k:<32} hit@1 = {v:.2f}")
    print(textwrap.dedent("""
        How to read this: eight queries is a smoke test, not an evaluation. Before picking
        an embedding model for the copilot, build 50-100 (question, expected chunk) pairs
        from real on-call questions and run exactly this loop. The capstone's `copilot
        search` command exists so you can do that against the Go vector store with any
        COPILOT_EMBED_PROVIDER and compare.
    """))
    return 0


if __name__ == "__main__":
    sys.exit(main())
