#!/usr/bin/env python3
"""
Module 03 lab — open-source embeddings with sentence-transformers.

What this script demonstrates, in order:

  1. Embedding runbook snippets with `all-MiniLM-L6-v2` (384 dimensions, runs on CPU).
  2. Semantic search: rank snippets by cosine similarity to an on-call question.
  3. Data classification: a k-nearest-neighbour classifier that assigns an alert
     severity (SEV1/SEV2/SEV3) from a handful of labelled examples — no training loop.
  4. Anomaly detection: flag log lines whose embedding is far from the centroid of
     "normal" lines.

Everything runs offline after the first model download (~90 MB). No API key required.

    pip install -r labs/python/requirements.txt
    python labs/python/03_sentence_transformers.py

The same four ideas appear in the Go capstone as `internal/embeddings` (the Embedder
interface and `Cosine`) and `internal/vectorstore/memory.go` (brute-force search).
The Go side uses OpenAI or Ollama for the vectors; this lab shows what those vectors
are and what you can do with them before any database or LLM is involved.
"""

from __future__ import annotations

import sys
import textwrap
import time
from collections import Counter
from pathlib import Path

import numpy as np
from sentence_transformers import SentenceTransformer

MODEL_NAME = "sentence-transformers/all-MiniLM-L6-v2"

# --------------------------------------------------------------------------------------
# 1. Corpus: short snippets lifted from data/knowledge/*.md. Kept inline so the lab has
#    no file dependencies, but we also pull the real files in if they exist so you can
#    see the model work on full paragraphs.
# --------------------------------------------------------------------------------------
SNIPPETS = [
    # (id, text)
    ("crashloop/oom",
     "If the container's Last State reason is OOMKilled (exit 137), raise the memory limit "
     "to 1.5Gi and roll back ConfigMap payments-api-config from v42 to v41 "
     "(LEDGER_BATCH_SIZE 5000 -> 500)."),
    ("crashloop/secret",
     "Exit code 1 within two seconds of start with 'required key PSP_API_KEY not set' means "
     "the External Secrets Operator has not synced payments-api-secrets from AWS Secrets Manager."),
    ("crashloop/readiness",
     "Pods Running but 0/1 Ready: /readyz returns 503 when Postgres via pgbouncer is unreachable "
     "or the connection pool is exhausted; scale HPA maxReplicas down to 14 as a stopgap."),
    ("crashloop/rollback",
     "If the crash loop began within 30 minutes of a deploy, kubectl rollout undo "
     "deployment/payments-api first. Previous known-good image is v2.14.2."),
    ("node/pleg",
     "kubelet log 'PLEG is not healthy' means containerd is wedged; restart containerd and "
     "kubelet, and if it recurs cordon and replace the node."),
    ("node/disk",
     "DiskPressure on payments-general nodes: root volume is 100 GiB gp3. Prune images with "
     "crictl rmi --prune; ledger-worker debug logging is the usual cause."),
    ("node/eni",
     "NodeENIExhausted: an m6i.2xlarge has 58 usable pod IPs. Cordon the node; prefix "
     "delegation (PLAT-1790) is the medium-term fix."),
    ("node/drain",
     "Drain a node with --ignore-daemonsets --delete-emptydir-data. The payments-api PDB "
     "minAvailable 4 blocks the drain unless at least 5 replicas are ready elsewhere."),
    ("redis/rootcause",
     "ledger-worker v1.9.0 cached event payloads in redis-payments without a TTL; allkeys-lru "
     "then evicted payments-api idempotency keys, causing 1,212 duplicate authorizations."),
    ("redis/fix",
     "Change maxmemory-policy to volatile-lru, move idempotency keys to a dedicated "
     "redis-idempotency instance with noeviction, and alert on rate(redis_evicted_keys_total) > 0."),
    ("policy/sev",
     "SEV1: customers cannot pay or money is at risk; acknowledge within 5 minutes. SEV2: "
     "material degradation with a workaround; acknowledge within 15 minutes."),
    ("policy/escalation",
     "Secondary is paged after 10 minutes unacknowledged for SEV2. Engineering manager is "
     "escalated after 30 minutes of SEV2 without a mitigation path, immediately for SEV1."),
    ("tf/nodegroup",
     "Node group payments-general runs m6i.2xlarge, min 3 / desired 6 / max 12, label "
     "nodegroup=payments-general, no taints. platform-system is fixed at 3 m6i.xlarge and tainted."),
    ("deploy/probes",
     "Readiness probe GET /readyz every 5s, failureThreshold 3. Since postmortem action item 5, "
     "slow Redis reports degraded but still 200; only DB or PSP unreachable returns 503."),
]


def load_knowledge_paragraphs(root: Path) -> list[tuple[str, str]]:
    """Split every markdown file under data/knowledge into paragraphs >= 200 chars.

    This is deliberately crude — Module 05 covers real chunking. The point here is
    only to give the model some longer, messier inputs alongside the clean snippets.
    """
    out: list[tuple[str, str]] = []
    for path in sorted(root.glob("*.md")):
        for i, para in enumerate(path.read_text(encoding="utf-8").split("\n\n")):
            para = para.strip()
            if len(para) >= 200 and not para.startswith("|"):  # skip tables
                out.append((f"{path.name}#p{i}", " ".join(para.split())))
    return out


def banner(title: str) -> None:
    print("\n" + "=" * 88)
    print(title)
    print("=" * 88)


# --------------------------------------------------------------------------------------
# 2. Semantic search
# --------------------------------------------------------------------------------------
def semantic_search(model: SentenceTransformer, ids: list[str], matrix: np.ndarray) -> None:
    banner("2. Semantic search — cosine similarity between question and snippet embeddings")
    questions = [
        "payments pod keeps getting OOMKilled, what do I do?",
        "how long before the secondary on-call gets paged?",
        "node is out of IP addresses for pods",
        "why did customers see duplicate pending charges in March?",
        "what instance type does the payments node group use?",
    ]
    # normalize_embeddings=True makes dot product == cosine similarity. The Go capstone
    # does the same thing in embeddings.Cosine; normalising once at index time is cheaper
    # than dividing by norms on every query.
    q = model.encode(questions, normalize_embeddings=True)
    scores = q @ matrix.T  # (n_questions, n_snippets)
    for qi, question in enumerate(questions):
        top = np.argsort(-scores[qi])[:3]
        print(f"\nQ: {question}")
        for rank, idx in enumerate(top, 1):
            print(f"   {rank}. {scores[qi, idx]:.3f}  {ids[idx]}")
    print(textwrap.dedent("""
        Read the scores, not just the ranks. MiniLM cosine scores for a good match sit
        around 0.5-0.7; anything under ~0.3 is usually noise. The capstone's Retriever
        applies a threshold for exactly this reason (internal/rag/retrieve.go) so the LLM
        is told "no relevant documents" instead of being handed junk context.
    """))


# --------------------------------------------------------------------------------------
# 3. kNN classifier for alert severity
# --------------------------------------------------------------------------------------
LABELLED_ALERTS = [
    # Text that might arrive from Alertmanager, with the severity the policy doc assigns.
    ("checkout success rate below 99% for 6 minutes", "SEV1"),
    ("payments-api has 1 ready endpoint out of 6", "SEV1"),
    ("confirmed duplicate capture reported by PSP", "SEV1"),
    ("payments-api CrashLoopBackOff on 3 pods", "SEV2"),
    ("redis-idempotency memory above 80 percent", "SEV2"),
    ("two payments-general nodes NotReady", "SEV2"),
    ("platform-system node ip-10-0-3-41 NotReady", "SEV2"),
    ("ledger consumer lag 14 minutes", "SEV2"),
    ("single node NotReady, workloads rescheduled", "SEV3"),
    ("one ledger-worker pod restarted once and recovered", "SEV3"),
    ("staging payments-api deploy failed", "SEV3"),
    ("grafana payments dashboard panel shows no data", "SEV3"),
]

UNLABELLED_ALERTS = [
    "payments-api readiness failing on 4 of 6 pods",
    "PSP reports a second capture for order 88213",
    "disk pressure on a single payments-general node",
    "ledger-worker lag is 22 minutes and growing",
    "alert rule KubeNodeNotReady has no runbook link",
]


def knn_classify(model: SentenceTransformer, k: int = 3) -> None:
    banner(f"3. Data classification — {k}-NN severity classifier on {len(LABELLED_ALERTS)} labelled alerts")
    texts, labels = zip(*LABELLED_ALERTS)
    train = model.encode(list(texts), normalize_embeddings=True)
    test = model.encode(UNLABELLED_ALERTS, normalize_embeddings=True)
    sims = test @ train.T
    for i, alert in enumerate(UNLABELLED_ALERTS):
        nn = np.argsort(-sims[i])[:k]
        votes = Counter(labels[j] for j in nn)
        # Weight votes by similarity so one very close neighbour beats two distant ones.
        weighted = Counter()
        for j in nn:
            weighted[labels[j]] += float(sims[i, j])
        predicted, confidence = weighted.most_common(1)[0]
        print(f"\n  {alert}")
        print(f"    -> {predicted}  (weighted score {confidence:.2f}; votes {dict(votes)})")
        for j in nn:
            print(f"       {sims[i, j]:.3f} [{labels[j]}] {texts[j]}")
    print(textwrap.dedent("""
        Twelve examples and no training step. This is "classification with embeddings":
        you get a usable triage hint in minutes, and adding a label is adding a row.
        When it is not enough: severity depends on numbers ("< 99.0% for 5 minutes")
        that embeddings blur. Production triage should parse thresholds deterministically
        and use kNN only for the free-text remainder. The capstone keeps severity rules in
        the knowledge base and lets RAG cite them rather than guessing (module 05).
    """))


# --------------------------------------------------------------------------------------
# 4. Anomaly detection by distance to centroid
# --------------------------------------------------------------------------------------
NORMAL_LOGS = [
    "POST /v1/authorize 200 41ms order=77120 psp=stripe",
    "POST /v1/authorize 200 38ms order=77121 psp=stripe",
    "POST /v1/capture 200 52ms order=77098 psp=adyen",
    "GET /readyz 200 1ms",
    "GET /healthz 200 1ms",
    "ledger batch flushed size=500 topic=payments.ledger.events took=120ms",
    "POST /v1/refund 200 66ms order=76990 psp=stripe",
    "idempotency hit key=idem:77120 ttl=86399",
    "POST /v1/authorize 200 44ms order=77122 psp=adyen",
    "ledger batch flushed size=500 topic=payments.ledger.events took=131ms",
    "GET /metrics 200 3ms",
    "POST /v1/capture 200 49ms order=77101 psp=stripe",
]

CANDIDATE_LOGS = [
    "POST /v1/authorize 200 40ms order=77123 psp=stripe",            # normal
    "ledger batch flushed size=500 topic=payments.ledger.events took=118ms",  # normal
    "panic: runtime error: invalid memory address or nil pointer dereference",  # anomaly
    "redis: connection refused redis-idempotency.platform.svc.cluster.local:6379",  # anomaly
    "config: LEDGER_BATCH_SIZE must be between 1 and 5000",            # anomaly
    "GET /readyz 503 2ms degraded=redis",                              # borderline
    "pgbouncer: no more connections allowed (max_client_conn)",        # anomaly
]


def anomaly_detection(model: SentenceTransformer) -> None:
    banner("4. Anomaly detection — distance from the centroid of normal log lines")
    normal = model.encode(NORMAL_LOGS, normalize_embeddings=True)
    centroid = normal.mean(axis=0)
    centroid /= np.linalg.norm(centroid)
    # Calibrate a threshold from the normal set itself: mean + 2 std of the normal
    # lines' own distances. In production you would fit this on a day of healthy logs
    # and re-fit after every release, because "normal" drifts with the code.
    normal_dist = 1.0 - normal @ centroid
    threshold = normal_dist.mean() + 2.0 * normal_dist.std()
    print(f"  centroid fitted on {len(NORMAL_LOGS)} lines; "
          f"normal distance mean={normal_dist.mean():.3f} std={normal_dist.std():.3f} "
          f"-> threshold={threshold:.3f}\n")
    cand = model.encode(CANDIDATE_LOGS, normalize_embeddings=True)
    dist = 1.0 - cand @ centroid
    for line, d in sorted(zip(CANDIDATE_LOGS, dist), key=lambda t: -t[1]):
        flag = "ANOMALY" if d > threshold else "ok     "
        print(f"  {flag}  {d:.3f}  {line}")
    print(textwrap.dedent("""
        This catches "a kind of line we have never seen", which is exactly what a regex
        alert cannot. It does not catch a normal-looking line with an abnormal number
        (latency 40ms vs 4000ms embeds almost identically). Pair it with metrics.
        The same trick gives you deduplication for alert storms: cluster incoming alerts
        by embedding and page once per cluster.
    """))


def main() -> int:
    banner(f"1. Loading {MODEL_NAME}")
    t0 = time.perf_counter()
    model = SentenceTransformer(MODEL_NAME)
    print(f"  loaded in {time.perf_counter() - t0:.1f}s; "
          f"dimension={model.get_sentence_embedding_dimension()}, "
          f"max_seq_length={model.max_seq_length} tokens")
    print("  Note: max_seq_length is a hard truncation. Anything past ~256 word pieces is "
          "silently dropped — this is why Module 05 chunks before embedding.")

    ids = [s[0] for s in SNIPPETS]
    texts = [s[1] for s in SNIPPETS]
    knowledge_root = Path(__file__).resolve().parents[2] / "data" / "knowledge"
    if knowledge_root.exists():
        extra = load_knowledge_paragraphs(knowledge_root)
        ids += [e[0] for e in extra]
        texts += [e[1] for e in extra]
        print(f"  added {len(extra)} paragraphs from {knowledge_root}")

    t0 = time.perf_counter()
    matrix = model.encode(texts, normalize_embeddings=True, batch_size=32, show_progress_bar=False)
    dt = time.perf_counter() - t0
    print(f"  embedded {len(texts)} texts in {dt:.2f}s "
          f"({1000 * dt / len(texts):.1f} ms/text on this CPU); matrix shape={matrix.shape}")

    semantic_search(model, ids, matrix)
    knn_classify(model)
    anomaly_detection(model)
    return 0


if __name__ == "__main__":
    sys.exit(main())
