#!/usr/bin/env python3
"""
Module 05 lab — the same RAG pipeline as 05_rag_raw_sdk.py, written with LangChain.

Map from the raw version to what LangChain gives you:

    chunk_markdown()   -> MarkdownHeaderTextSplitter + RecursiveCharacterTextSplitter
    embed_texts()      -> OpenAIEmbeddings
    numpy matrix       -> InMemoryVectorStore (swap for Chroma/Qdrant/FAISS with one line)
    retrieve()         -> vector_store.similarity_search_with_score(q, k=5) + threshold, as a RunnableLambda
    build_prompt()     -> ChatPromptTemplate + a small format_docs() function
    chat.completions   -> ChatOpenAI
    glue               -> LCEL: `{"context": retriever | format_docs, "question": ...} | prompt | llm`

Citations: LangChain does not "do" citations. We keep each Document's metadata (source,
heading) through the chain and number the passages ourselves, exactly like the raw SDK
version and like `internal/rag/prompt.go` in the Go capstone. Anyone who tells you a
framework gives you citations for free means "gives you metadata you can print".

    export OPENAI_API_KEY=sk-...
    python labs/python/05_rag_langchain.py "what is the OOMKilled remediation for payments-api?"

Ollama: set OPENAI_BASE_URL=http://localhost:11434/v1 OPENAI_API_KEY=ollama and
RAG_CHAT_MODEL / RAG_EMBED_MODEL as in the raw lab (ChatOpenAI honours OPENAI_BASE_URL).
"""

from __future__ import annotations

import os
import sys
import textwrap
from pathlib import Path

from langchain_core.documents import Document
from langchain_core.output_parsers import StrOutputParser
from langchain_core.prompts import ChatPromptTemplate
from langchain_core.runnables import RunnableLambda, RunnableParallel, RunnablePassthrough
from langchain_core.vectorstores import InMemoryVectorStore
from langchain_openai import ChatOpenAI, OpenAIEmbeddings
from langchain_text_splitters import MarkdownHeaderTextSplitter, RecursiveCharacterTextSplitter

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


# --------------------------------------------------------------------------------------
# 1. Load + chunk. Two splitters: one that understands markdown headings (so every chunk
#    carries its section path as metadata) and one that windows long sections.
# --------------------------------------------------------------------------------------
def load_documents() -> list[Document]:
    header_splitter = MarkdownHeaderTextSplitter(
        headers_to_split_on=[("#", "h1"), ("##", "h2"), ("###", "h3")],
        strip_headers=False,
    )
    window = RecursiveCharacterTextSplitter(chunk_size=1200, chunk_overlap=150,
                                            separators=["\n\n", "\n", ". ", " ", ""])
    docs: list[Document] = []
    for path in sorted(KNOWLEDGE.iterdir()):
        if not path.is_file():
            continue
        text = path.read_text(encoding="utf-8")
        if path.suffix in {".md"}:
            sections = header_splitter.split_text(text)
        else:  # YAML / HCL: no headings, one section
            sections = [Document(page_content=text, metadata={})]
        for sec in sections:
            heading = " > ".join(v for k, v in sec.metadata.items() if k in {"h1", "h2", "h3"}) or "(file)"
            for piece in window.split_documents([sec]):
                piece.metadata.update({"source": path.name, "heading": heading})
                # Same trick as the raw lab: prefix the heading path so the chunk is
                # self-describing when ranked on its own.
                piece.page_content = f"{path.name} > {heading}\n\n{piece.page_content}"
                docs.append(piece)
    return docs


# --------------------------------------------------------------------------------------
# 2+3. Embed + store. InMemoryVectorStore is brute-force cosine, like the Go memory store.
#      To move to a database, replace this one constructor:
#        from langchain_chroma import Chroma;   Chroma.from_documents(docs, embeddings)
#        from langchain_qdrant import QdrantVectorStore; QdrantVectorStore.from_documents(...)
# --------------------------------------------------------------------------------------
def build_store(docs: list[Document]) -> InMemoryVectorStore:
    embeddings = OpenAIEmbeddings(model=EMBED_MODEL)
    return InMemoryVectorStore.from_documents(docs, embeddings)


# --------------------------------------------------------------------------------------
# 4. Prompt with numbered context. format_docs() is where citations are born.
# --------------------------------------------------------------------------------------
PROMPT = ChatPromptTemplate.from_messages([
    ("system", textwrap.dedent("""
        You are an on-call copilot for a platform engineering team. Answer ONLY from the
        numbered context passages. After every sentence that uses a passage, cite it like [2].
        Quote exact values (versions, limits, minutes, instance types) verbatim. If the
        context does not contain the answer, say "The knowledge base does not cover this"
        and stop. Be concise: commands and numbers over prose.
    """).strip()),
    ("human", "Context:\n\n{context}\n\nQuestion: {question}"),
])


def format_docs(docs: list[Document]) -> str:
    return "\n\n".join(
        f"[{i}] (source: {d.metadata['source']} > {d.metadata['heading']})\n{d.page_content}"
        for i, d in enumerate(docs, 1)
    )


def main() -> int:
    if not os.environ.get("OPENAI_API_KEY"):
        sys.exit("OPENAI_API_KEY is required")

    docs = load_documents()
    print(f"[ingest] {len(docs)} chunks from {KNOWLEDGE}")
    store = build_store(docs)

    # `store.as_retriever(search_type="similarity_score_threshold", ...)` is the one-liner,
    # but relevance-score normalisation differs per store class, so we do the threshold
    # explicitly: similarity_search_with_score returns cosine similarity for
    # InMemoryVectorStore, and we keep only hits >= MIN_SCORE. Wrapping it in
    # RunnableLambda makes it a first-class chain step.
    def retrieve(question: str) -> list[Document]:
        hits = store.similarity_search_with_score(question, k=TOP_K)
        kept = []
        for doc, score in hits:
            if score >= MIN_SCORE:
                doc.metadata["score"] = round(float(score), 3)
                kept.append(doc)
        return kept

    retriever = RunnableLambda(retrieve)
    llm = ChatOpenAI(model=CHAT_MODEL, temperature=0)

    # LCEL. The dict step fans the question out to the retriever and passes it through, and
    # we keep the raw `docs` so we can print sources after the answer. Everything here is a
    # Runnable, so `.invoke`, `.stream`, `.batch` and LangSmith tracing all work unchanged.
    chain = (
        RunnableParallel(docs=retriever, question=RunnablePassthrough())
        | RunnablePassthrough.assign(context=lambda x: format_docs(x["docs"]))
        | RunnablePassthrough.assign(
            answer=(lambda x: {"context": x["context"], "question": x["question"]}) | PROMPT | llm | StrOutputParser()
        )
    )

    for question in sys.argv[1:] or DEFAULT_QUESTIONS:
        print("\n" + "=" * 88 + f"\nQ: {question}\n" + "=" * 88)
        out = chain.invoke(question)
        if not out["docs"]:
            print("A: The knowledge base does not cover this (no chunk above the similarity threshold).")
            continue
        print("A:", out["answer"].strip())
        print("\nSources:")
        for i, d in enumerate(out["docs"], 1):
            print(f"  [{i}] {d.metadata['source']} > {d.metadata['heading']}  (cosine {d.metadata['score']})")

    print(textwrap.dedent("""
        What LangChain bought us over the raw version: the markdown-aware splitter, a store
        interface we can swap without touching the chain, and a chain object we can stream,
        batch and trace. What it did not change: chunk size, the threshold, the prompt's
        "do not guess" rule, or the fact that citations are our own numbering of metadata.
        Those are where answer quality lives, and they are framework-independent.
    """))
    return 0


if __name__ == "__main__":
    sys.exit(main())
