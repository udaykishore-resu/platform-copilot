#!/usr/bin/env python3
"""
Module 05 lab — the "RAG alternative": let OpenAI host the whole pipeline.

Upload data/knowledge/* to an OpenAI vector store and ask the same questions through
`file_search`. OpenAI does the chunking (default 800 tokens, 400 overlap), embedding,
storage, hybrid retrieval and re-ranking; you send a question and get an answer with
file citations. You give up control of every one of those knobs, you pay per GB-day of
vector storage plus per-call tool usage, and your documents leave your network.

Two ways to call it, selected with --api:

    responses   (default) the Responses API with the file_search tool. This is the
                supported path going forward.
    assistants  the original Assistants API (assistant -> thread -> run). OpenAI has
                deprecated it in favour of Responses; it is kept here because the
                roadmap node is named after it and because plenty of code still uses it.

Both share one vector store, created at the start and deleted at the end unless you
pass --keep (vector stores are billed per GB-day while they exist).

    export OPENAI_API_KEY=sk-...
    python labs/python/05_openai_assistants_file_search.py
    python labs/python/05_openai_assistants_file_search.py --api assistants "why was HPA max lowered?"

Compare the answers and citations with 05_rag_raw_sdk.py. Same documents, same model
family, zero lines of retrieval code here — and zero ability to tune it when it is wrong.
"""

from __future__ import annotations

import argparse
import os
import sys
import textwrap
import time
from pathlib import Path

from openai import OpenAI

ROOT = Path(__file__).resolve().parents[2]
KNOWLEDGE = ROOT / "data" / "knowledge"
CHAT_MODEL = os.environ.get("RAG_CHAT_MODEL", "gpt-4o-mini")

INSTRUCTIONS = textwrap.dedent("""
    You are an on-call copilot for a platform engineering team. Answer only from the
    uploaded documents and cite them. Quote exact values (versions, limits, minutes,
    instance types) verbatim. If the documents do not contain the answer, say "The
    knowledge base does not cover this" and stop. Be concise.
""").strip()

DEFAULT_QUESTIONS = [
    "What is the OOMKilled remediation for payments-api?",
    "After how many minutes is the secondary on-call paged for a SEV2?",
    "Why did customers see duplicate authorization holds in March 2026?",
    "How do I rotate the TLS certificate for the ingress controller?",  # not in the KB
]


def vector_stores_api(client: OpenAI):
    """openai>=1.66 exposes client.vector_stores; older SDKs have client.beta.vector_stores."""
    return getattr(client, "vector_stores", None) or client.beta.vector_stores


def create_vector_store(client: OpenAI) -> str:
    vs_api = vector_stores_api(client)
    vs = vs_api.create(name=f"platform-copilot-kb-{int(time.time())}",
                       # Chunking is configurable but coarse: only size/overlap, no heading awareness.
                       chunking_strategy={"type": "static",
                                          "static": {"max_chunk_size_tokens": 400, "chunk_overlap_tokens": 50}})
    # .tf and .yaml are not accepted extensions for file_search; .md is. Upload the
    # manifests with a .txt suffix so they are indexed as plain text.
    paths = [p for p in sorted(KNOWLEDGE.iterdir()) if p.is_file()]
    streams = []
    for p in paths:
        name = p.name if p.suffix == ".md" else p.name + ".txt"
        streams.append((name, p.read_bytes()))
    print(f"[upload] {len(streams)} files -> vector store {vs.id}")
    batch = vs_api.file_batches.upload_and_poll(vector_store_id=vs.id, files=streams)
    print(f"[upload] status={batch.status} completed={batch.file_counts.completed} failed={batch.file_counts.failed}")
    return vs.id


# --------------------------------------------------------------------------------------
# Responses API path
# --------------------------------------------------------------------------------------
def ask_responses(client: OpenAI, vs_id: str, question: str) -> None:
    resp = client.responses.create(
        model=CHAT_MODEL,
        instructions=INSTRUCTIONS,
        input=question,
        tools=[{"type": "file_search", "vector_store_ids": [vs_id], "max_num_results": 5}],
        include=["file_search_call.results"],  # return the retrieved chunks, not just the answer
    )
    print("A:", resp.output_text.strip())
    # Citations live as annotations on the message's text parts.
    cited: dict[str, str] = {}
    retrieved = []
    for item in resp.output:
        if item.type == "file_search_call" and getattr(item, "results", None):
            retrieved = item.results
        if item.type == "message":
            for part in item.content:
                for ann in getattr(part, "annotations", []) or []:
                    if ann.type == "file_citation":
                        cited[ann.file_id] = getattr(ann, "filename", ann.file_id)
    if cited:
        print("\nCited files:")
        for fid, name in cited.items():
            print(f"  - {name} ({fid})")
    if retrieved:
        print(f"\nRetrieved {len(retrieved)} chunks (score, file, preview):")
        for r in retrieved[:5]:
            preview = (r.text or "")[:70].replace("\n", " ")
            print(f"  {r.score:.3f}  {r.filename}  {preview}...")
    if resp.usage:
        print(f"\n({resp.usage.input_tokens} input + {resp.usage.output_tokens} output tokens)")


# --------------------------------------------------------------------------------------
# Assistants API path (deprecated; shown for the roadmap node and for legacy code)
# --------------------------------------------------------------------------------------
def ask_assistants(client: OpenAI, assistant_id: str, question: str) -> None:
    thread = client.beta.threads.create(messages=[{"role": "user", "content": question}])
    run = client.beta.threads.runs.create_and_poll(thread_id=thread.id, assistant_id=assistant_id)
    if run.status != "completed":
        print(f"run ended with status={run.status} last_error={run.last_error}")
        return
    messages = client.beta.threads.messages.list(thread_id=thread.id, order="desc", limit=1)
    msg = messages.data[0]
    for part in msg.content:
        if part.type != "text":
            continue
        text = part.text.value
        cited = {}
        # The model writes markers like 【4:0†source】; annotations map them to file ids.
        for i, ann in enumerate(part.text.annotations, 1):
            fc = getattr(ann, "file_citation", None)
            if fc:
                fname = client.files.retrieve(fc.file_id).filename
                cited[ann.text] = f"[{i}] {fname}"
                text = text.replace(ann.text, f" [{i}]")
        print("A:", text.strip())
        if cited:
            print("\nCited files:")
            for label in cited.values():
                print(f"  {label}")
    if run.usage:
        print(f"\n({run.usage.prompt_tokens} prompt + {run.usage.completion_tokens} completion tokens)")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--api", choices=["responses", "assistants"], default="responses")
    ap.add_argument("--keep", action="store_true", help="do not delete the vector store afterwards")
    ap.add_argument("questions", nargs="*")
    args = ap.parse_args()

    if not os.environ.get("OPENAI_API_KEY"):
        sys.exit("OPENAI_API_KEY is required (this lab has no offline mode — that is the point)")
    client = OpenAI()

    vs_id = create_vector_store(client)
    assistant_id = None
    try:
        if args.api == "assistants":
            assistant = client.beta.assistants.create(
                name="platform-copilot (lab)",
                model=CHAT_MODEL,
                instructions=INSTRUCTIONS,
                tools=[{"type": "file_search"}],
                tool_resources={"file_search": {"vector_store_ids": [vs_id]}},
            )
            assistant_id = assistant.id
            print(f"[assistant] {assistant_id} (Assistants API is deprecated; prefer --api responses)")

        for question in args.questions or DEFAULT_QUESTIONS:
            print("\n" + "=" * 88 + f"\nQ: {question}\n" + "=" * 88)
            if args.api == "responses":
                ask_responses(client, vs_id, question)
            else:
                ask_assistants(client, assistant_id, question)
    finally:
        if assistant_id:
            client.beta.assistants.delete(assistant_id)
        if args.keep:
            print(f"\n[keep] vector store {vs_id} left in place — it is billed per GB-day")
        else:
            vs_api = vector_stores_api(client)
            # Delete the files too; vector store deletion does not delete the underlying File objects.
            for f in vs_api.files.list(vector_store_id=vs_id).data:
                try:
                    client.files.delete(f.id)
                except Exception:  # noqa: BLE001
                    pass
            vs_api.delete(vs_id)
            print(f"\n[cleanup] deleted vector store {vs_id} and its files")

    print(textwrap.dedent("""
        What you got: chunking, embedding, hybrid search, re-ranking, citations, and
        storage, with no retrieval code and no database to run. What you gave up:
          - chunk boundaries (no heading awareness — compare the citations with the raw lab
            on the runbook questions; the hosted chunker happily splits a remediation list),
          - the embedding model, the similarity threshold and the retrieval k,
          - the ability to keep documents inside your network or on your own Qdrant,
          - portability: this only works with OpenAI models.
        Use it for a prototype or an internal tool where the documents are already allowed
        to leave the building. The capstone does not, which is why internal/rag exists.
    """))
    return 0


if __name__ == "__main__":
    sys.exit(main())
