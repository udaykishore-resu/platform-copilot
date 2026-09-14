#!/usr/bin/env python3
"""
Module 05 lab — RAG with nothing but the openai SDK and numpy.

Read this one first, then 05_rag_langchain.py and 05_rag_llamaindex.py. Every line here
has a one-to-one counterpart in the frameworks (and in the Go capstone); seeing the raw
version is what lets you debug the abstracted ones.

Pipeline (identical to `internal/rag` in the Go code):

    ingest:  walk data/knowledge -> chunk (markdown-aware-ish) -> embed -> store in memory
    ask:     embed question -> cosine top-k with threshold -> numbered context -> chat -> cite

    export OPENAI_API_KEY=sk-...
    python labs/python/05_rag_raw_sdk.py "what is the OOMKilled remediation for payments-api?"
    python labs/python/05_rag_raw_sdk.py            # runs a fixed set of questions

Works against Ollama too, because Ollama exposes an OpenAI-compatible endpoint:

    export OPENAI_BASE_URL=http://localhost:11434/v1 OPENAI_API_KEY=ollama
    export RAG_CHAT_MODEL=llama3.1 RAG_EMBED_MODEL=nomic-embed-text
    python labs/python/05_rag_raw_sdk.py

Embeddings are cached in .cache/ so repeated runs cost nothing (the capstone does the
same with COPILOT_INDEX_PATH=.copilot/index.json).
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import sys
import textwrap
from dataclasses import asdict, dataclass
from pathlib import Path

import numpy as np
from openai import OpenAI

ROOT = Path(__file__).resolve().parents[2]
KNOWLEDGE = ROOT / "data" / "knowledge"
CACHE = ROOT / ".cache" / "raw_sdk_index.json"

CHAT_MODEL = os.environ.get("RAG_CHAT_MODEL", "gpt-4o-mini")
EMBED_MODEL = os.environ.get("RAG_EMBED_MODEL", "text-embedding-3-small")
TOP_K = int(os.environ.get("RAG_TOP_K", "5"))
MIN_SCORE = float(os.environ.get("RAG_MIN_SCORE", "0.25"))   # cosine; tune per embedding model
CHUNK_CHARS = 1200
OVERLAP_CHARS = 150

DEFAULT_QUESTIONS = [
    "What is the OOMKilled remediation for payments-api?",
    "After how many minutes is the secondary on-call paged for a SEV2?",
    "Why did customers see duplicate authorization holds in March 2026, and what changed afterwards?",
    "What instance type and size limits does the payments-general node group use?",
    "What is the maxReplicas of the payments-api HPA and why was it lowered?",
    "How do I rotate the TLS certificate for the ingress controller?",   # not in the KB: must say so
]


# --------------------------------------------------------------------------------------
# 1. Chunking. Markdown-aware in the cheapest way that matters: split on headings first
#    so a chunk never straddles two runbook branches, then window long sections. Module 05
#    README explains why heading-aligned chunks beat fixed windows for runbooks.
# --------------------------------------------------------------------------------------
@dataclass
class Chunk:
    id: str
    source: str
    heading: str
    text: str


def chunk_markdown(path: Path) -> list[Chunk]:
    raw = path.read_text(encoding="utf-8")
    # YAML / HCL files have no headings; treat the whole file as one section and window it.
    if path.suffix in {".yaml", ".yml", ".tf"}:
        sections = [("(file)", raw)]
    else:
        parts = re.split(r"(?m)^(#{1,3} .+)$", raw)
        sections = [("(preamble)", parts[0])] if parts[0].strip() else []
        for i in range(1, len(parts), 2):
            sections.append((parts[i].lstrip("# ").strip(), parts[i + 1]))

    chunks: list[Chunk] = []
    for heading, body in sections:
        body = body.strip()
        if not body:
            continue
        start = 0
        n = 0
        while start < len(body):
            piece = body[start:start + CHUNK_CHARS]
            # Prepend the heading path so the chunk is self-describing when it is ranked
            # alone. This is the single cheapest retrieval-quality win in RAG.
            text = f"{path.name} > {heading}\n\n{piece}"
            chunks.append(Chunk(id=f"{path.name}#{heading}#{n}", source=path.name, heading=heading, text=text))
            if start + CHUNK_CHARS >= len(body):
                break
            start += CHUNK_CHARS - OVERLAP_CHARS
            n += 1
    return chunks


# --------------------------------------------------------------------------------------
# 2. Embedding with a content-hash cache. Re-ingesting unchanged files costs zero tokens.
# --------------------------------------------------------------------------------------
def content_key(chunk: Chunk) -> str:
    return hashlib.sha256(f"{EMBED_MODEL}\n{chunk.text}".encode()).hexdigest()


def embed_texts(client: OpenAI, texts: list[str]) -> np.ndarray:
    out: list[list[float]] = []
    for i in range(0, len(texts), 100):  # batch; one round trip per 100 chunks
        resp = client.embeddings.create(model=EMBED_MODEL, input=texts[i:i + 100])
        out.extend(d.embedding for d in sorted(resp.data, key=lambda d: d.index))
    vecs = np.array(out, dtype=np.float32)
    return vecs / np.linalg.norm(vecs, axis=1, keepdims=True)  # unit length -> dot == cosine


def build_index(client: OpenAI) -> tuple[list[Chunk], np.ndarray]:
    chunks = [c for p in sorted(KNOWLEDGE.iterdir()) if p.is_file() for c in chunk_markdown(p)]
    cache: dict[str, list[float]] = {}
    if CACHE.exists():
        cache = json.loads(CACHE.read_text())
    missing = [c for c in chunks if content_key(c) not in cache]
    if missing:
        print(f"[ingest] embedding {len(missing)} new/changed chunks with {EMBED_MODEL} "
              f"({len(chunks) - len(missing)} cached)")
        vecs = embed_texts(client, [c.text for c in missing])
        for c, v in zip(missing, vecs):
            cache[content_key(c)] = v.tolist()
        CACHE.parent.mkdir(parents=True, exist_ok=True)
        CACHE.write_text(json.dumps(cache))
    else:
        print(f"[ingest] all {len(chunks)} chunks cached")
    matrix = np.array([cache[content_key(c)] for c in chunks], dtype=np.float32)
    return chunks, matrix


# --------------------------------------------------------------------------------------
# 3. Retrieval: brute-force cosine, top-k, threshold. This *is* internal/vectorstore/memory.go.
# --------------------------------------------------------------------------------------
def retrieve(client: OpenAI, chunks: list[Chunk], matrix: np.ndarray, question: str) -> list[tuple[float, Chunk]]:
    q = embed_texts(client, [question])[0]
    sims = matrix @ q
    order = np.argsort(-sims)[:TOP_K]
    return [(float(sims[i]), chunks[i]) for i in order if sims[i] >= MIN_SCORE]


# --------------------------------------------------------------------------------------
# 4. Prompt assembly with numbered citations, then generation.
# --------------------------------------------------------------------------------------
SYSTEM = textwrap.dedent("""
    You are an on-call copilot for a platform engineering team. Answer ONLY from the
    numbered context passages. After every sentence that uses a passage, cite it like [2].
    Quote exact values (versions, limits, minutes, instance types) verbatim. If the
    context does not contain the answer, say "The knowledge base does not cover this"
    and stop — do not guess. Be concise: commands and numbers over prose.
""").strip()


def build_prompt(question: str, hits: list[tuple[float, Chunk]]) -> list[dict]:
    context = "\n\n".join(f"[{i}] (source: {c.source} > {c.heading}, score {s:.2f})\n{c.text}"
                          for i, (s, c) in enumerate(hits, 1))
    user = f"Context:\n\n{context}\n\nQuestion: {question}"
    return [{"role": "system", "content": SYSTEM}, {"role": "user", "content": user}]


def ask(client: OpenAI, chunks: list[Chunk], matrix: np.ndarray, question: str) -> None:
    print("\n" + "=" * 88 + f"\nQ: {question}\n" + "=" * 88)
    hits = retrieve(client, chunks, matrix, question)
    if not hits:
        print("A: The knowledge base does not cover this (no chunk above the similarity threshold).")
        return
    messages = build_prompt(question, hits)
    resp = client.chat.completions.create(model=CHAT_MODEL, messages=messages, temperature=0)
    print("A:", resp.choices[0].message.content.strip())
    print("\nSources:")
    for i, (s, c) in enumerate(hits, 1):
        print(f"  [{i}] {c.source} > {c.heading}  (cosine {s:.3f})")
    u = resp.usage
    print(f"\n({u.prompt_tokens} prompt + {u.completion_tokens} completion tokens, model {CHAT_MODEL})")


def main() -> int:
    if not os.environ.get("OPENAI_API_KEY"):
        sys.exit("OPENAI_API_KEY is required (or OPENAI_BASE_URL + OPENAI_API_KEY=ollama for Ollama)")
    client = OpenAI()
    chunks, matrix = build_index(client)
    print(f"[ingest] {len(chunks)} chunks, matrix {matrix.shape}")
    questions = sys.argv[1:] or DEFAULT_QUESTIONS
    for q in questions:
        ask(client, chunks, matrix, q)
    print(textwrap.dedent("""
        That is the whole thing: ~120 lines of logic. LangChain and LlamaIndex give you
        loaders for 100 file types, splitters that know about code and tables, swappable
        stores and models, and tracing. They do not give you better answers than this by
        default — the quality levers (chunk boundaries, heading prefixes, threshold,
        the system prompt's "do not guess") are the same in all three scripts.
    """))
    return 0


if __name__ == "__main__":
    sys.exit(main())
