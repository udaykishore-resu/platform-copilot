#!/usr/bin/env python3
"""
Module 05 lab — the same RAG pipeline, written with LlamaIndex.

LlamaIndex is organised around the *index* rather than the *chain*. The mental model:

    Documents  --(node parser / splitter)-->  Nodes  --(embed)-->  Index  --(query engine)-->  Response

Map from 05_rag_raw_sdk.py:

    chunk_markdown()   -> SimpleDirectoryReader (MarkdownReader splits on headings) + SentenceSplitter
    embed_texts()      -> Settings.embed_model = OpenAIEmbedding(...)
    numpy matrix       -> VectorStoreIndex (SimpleVectorStore in memory; swap for Qdrant/Chroma)
    retrieve()         -> index.as_retriever(similarity_top_k=5) + SimilarityPostprocessor(cutoff)
    build_prompt()+LLM -> CitationQueryEngine, which numbers sources and instructs the LLM to cite [n]
    print sources      -> response.source_nodes (each carries metadata + score)

CitationQueryEngine is the one piece of "free" citation machinery in either framework:
it re-splits retrieved nodes into smaller citation units, labels them "Source 1:", and
uses a prompt that demands [n] references. It is still prompt engineering under the
hood — look at CITATION_QA_TEMPLATE in llama_index.core.query_engine.citation_query_engine.

    export OPENAI_API_KEY=sk-...
    python labs/python/05_rag_llamaindex.py "what is the OOMKilled remediation for payments-api?"
"""

from __future__ import annotations

import os
import sys
import textwrap
from pathlib import Path

from llama_index.core import Settings, SimpleDirectoryReader, VectorStoreIndex
from llama_index.core.node_parser import SentenceSplitter
from llama_index.core.postprocessor import SimilarityPostprocessor
from llama_index.core.query_engine import CitationQueryEngine
from llama_index.embeddings.openai import OpenAIEmbedding
from llama_index.llms.openai import OpenAI

ROOT = Path(__file__).resolve().parents[2]
KNOWLEDGE = ROOT / "data" / "knowledge"
CHAT_MODEL = os.environ.get("RAG_CHAT_MODEL", "gpt-4o-mini")
EMBED_MODEL = os.environ.get("RAG_EMBED_MODEL", "text-embedding-3-small")
TOP_K = int(os.environ.get("RAG_TOP_K", "5"))
MIN_SCORE = float(os.environ.get("RAG_MIN_SCORE", "0.25"))

DEFAULT_QUESTIONS = [
    "What is the OOMKilled remediation for payments-api?",
    "After how many minutes is the secondary on-call paged for a SEV2?",
    "Why did customers see duplicate authorization holds in March 2026?",
    "What instance type does the payments-general node group use and what are its min/max sizes?",
    "How do I rotate the TLS certificate for the ingress controller?",  # not in the KB
]


def main() -> int:
    if not os.environ.get("OPENAI_API_KEY"):
        sys.exit("OPENAI_API_KEY is required")

    # Global defaults. Every component reads from Settings unless given an explicit model,
    # which is convenient for a lab and a footgun in production (two indexes built with
    # different embed models silently return garbage). Pin them per index in real code.
    Settings.llm = OpenAI(model=CHAT_MODEL, temperature=0)
    Settings.embed_model = OpenAIEmbedding(model=EMBED_MODEL)
    # chunk_size is in tokens here, not characters (LangChain's default splitter counts
    # characters). 300 tokens ~ 1200 characters, matching the other two labs.
    Settings.node_parser = SentenceSplitter(chunk_size=300, chunk_overlap=40)

    # 1. Load. .md goes through MarkdownReader (one Document per heading section, heading
    #    text kept in the body); .yaml/.tf are read as plain text. filename_as_id makes
    #    re-ingestion idempotent when you later persist the index.
    docs = SimpleDirectoryReader(
        input_dir=str(KNOWLEDGE),
        required_exts=[".md", ".yaml", ".tf"],
        filename_as_id=True,
    ).load_data()
    print(f"[ingest] {len(docs)} documents (markdown sections + files) from {KNOWLEDGE}")

    # 2+3. Chunk -> embed -> in-memory SimpleVectorStore. To use a database:
    #    from llama_index.vector_stores.qdrant import QdrantVectorStore
    #    storage_context = StorageContext.from_defaults(vector_store=QdrantVectorStore(...))
    #    VectorStoreIndex.from_documents(docs, storage_context=storage_context)
    index = VectorStoreIndex.from_documents(docs, show_progress=False)
    n_nodes = len(index.docstore.docs)
    print(f"[ingest] {n_nodes} nodes indexed with {EMBED_MODEL}")

    # 4. Query engine with citations. similarity_top_k is the retriever's k; the
    #    postprocessor drops hits below the cosine cutoff so the LLM sees "no context"
    #    instead of junk. citation_chunk_size re-splits each hit into smaller numbered units,
    #    which makes [n] point at a paragraph rather than a whole section.
    engine = CitationQueryEngine.from_args(
        index,
        similarity_top_k=TOP_K,
        citation_chunk_size=512,
        node_postprocessors=[SimilarityPostprocessor(similarity_cutoff=MIN_SCORE)],
    )

    for question in sys.argv[1:] or DEFAULT_QUESTIONS:
        print("\n" + "=" * 88 + f"\nQ: {question}\n" + "=" * 88)
        response = engine.query(question)
        if not response.source_nodes:
            print("A: The knowledge base does not cover this (no node above the similarity cutoff).")
            continue
        print("A:", str(response).strip())
        print("\nSources:")
        for i, sn in enumerate(response.source_nodes, 1):
            meta = sn.node.metadata
            preview = sn.node.get_content()[:80].replace("\n", " ")
            print(f"  [{i}] {meta.get('file_name', '?')}  (score {sn.score:.3f})  {preview}...")

    print(textwrap.dedent("""
        Compared with the LangChain version, LlamaIndex made two decisions for us: the
        markdown reader split on headings without being asked, and CitationQueryEngine
        owns the citation prompt. That is the trade: faster to a good default, more
        digging when the default is wrong (e.g. you want [n] to mean a file, not a
        512-token sub-chunk). The raw SDK version is where you go to see what either
        framework is actually sending to the model — LlamaIndex exposes it with
        `Settings.callback_manager` / `set_global_handler("simple")` if you prefer.
    """))
    return 0


if __name__ == "__main__":
    sys.exit(main())
