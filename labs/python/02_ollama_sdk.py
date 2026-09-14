#!/usr/bin/env python3
"""
Module 02 lab · Ollama Python SDK: chat (streaming and not), tool definitions, embeddings.

Talks to the same local Ollama server the Go capstone uses
(`internal/llm/ollama.go`, `internal/embeddings/ollama.go`, OLLAMA_HOST), so this is
the quickest way to confirm a model works before debugging the Go adapter.

What it shows:
  1. list      which models are pulled, their size and quantisation
  2. chat      a one-shot answer with token counts and tokens/second
  3. stream    the same call streamed token by token (what `copilot chat` does)
  4. tools     a tool definition in the request and whether the model emits a tool call
  5. embed     batch embeddings for alert strings + cosine similarity, no API key, no cost

Usage:
    ollama pull llama3.2 && ollama pull nomic-embed-text
    python 02_ollama_sdk.py
    python 02_ollama_sdk.py --model qwen2.5:7b --embed-model mxbai-embed-large

Environment:
    OLLAMA_HOST   default http://localhost:11434
"""

from __future__ import annotations

import argparse
import math
import os
import sys
import time

import ollama

SYSTEM_PROMPT = "You are a concise SRE copilot for Kubernetes engineers. Answer in at most three sentences."

ALERTS = [
    "KubePodCrashLooping: payments-api restarting 4 times / 10m, exit code 137",
    "Container OOMKilled in namespace payments, memory limit 512Mi exceeded",
    "ingress-nginx 5xx error ratio above 5% for 10 minutes on api.example.com",
    "TLS certificate for api.example.com expires in 6 days",
    "Node disk pressure on ip-10-0-3-17, ephemeral storage 91% used",
    "Test alert from Alertmanager smoke check, please ignore",
]

# A read-only tool definition in the OpenAI-style schema Ollama accepts. The capstone's
# agent registers KubectlGet exactly like this; the host executes it, never the model.
KUBECTL_GET_TOOL = {
    "type": "function",
    "function": {
        "name": "kubectl_get",
        "description": "Read-only: list Kubernetes resources of a kind in a namespace.",
        "parameters": {
            "type": "object",
            "properties": {
                "kind": {"type": "string", "description": "pods, deployments, events, ..."},
                "namespace": {"type": "string"},
            },
            "required": ["kind", "namespace"],
        },
    },
}


def cosine(a: list[float], b: list[float]) -> float:
    dot = sum(x * y for x, y in zip(a, b))
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(y * y for y in b))
    return dot / (na * nb) if na and nb else 0.0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--model", default=os.environ.get("OLLAMA_MODEL", "llama3.2"))
    ap.add_argument("--embed-model", default=os.environ.get("OLLAMA_EMBED_MODEL", "nomic-embed-text"))
    ap.add_argument("--question", default="A pod is CrashLooping with exit code 137. What is the first thing to check?")
    ap.add_argument("--max-tokens", type=int, default=150)
    args = ap.parse_args()

    host = os.environ.get("OLLAMA_HOST", "http://localhost:11434")
    client = ollama.Client(host=host)

    # 1. List models. Fails fast with a clear message if the server is down.
    print(f"== models on {host} ==")
    try:
        listing = client.list()
    except Exception as exc:
        print(f"cannot reach Ollama at {host}: {exc}")
        print("start it with `ollama serve` (or install from https://ollama.com) and pull a model.")
        return 2
    names = []
    for m in listing.get("models", []):
        name = m.get("model") or m.get("name")
        names.append(name)
        size_gb = (m.get("size") or 0) / 1e9
        details = m.get("details") or {}
        print(f"   {name:<28} {size_gb:5.2f} GB  {details.get('parameter_size', ''):>6} {details.get('quantization_level', '')}")
    for needed in (args.model, args.embed_model):
        if not any(n == needed or n.split(":")[0] == needed.split(":")[0] for n in names):
            print(f"\nmodel {needed!r} is not pulled. Run: ollama pull {needed}")
            return 2

    # 2. One-shot chat with usage. Ollama reports prompt_eval_count / eval_count and
    #    eval_duration (ns), so tokens/second is a one-liner.
    print(f"\n== chat ({args.model}) ==")
    t0 = time.perf_counter()
    resp = client.chat(
        model=args.model,
        messages=[{"role": "system", "content": SYSTEM_PROMPT}, {"role": "user", "content": args.question}],
        options={"num_predict": args.max_tokens, "temperature": 0.2},
    )
    wall = time.perf_counter() - t0
    print("   " + resp["message"]["content"].strip().replace("\n", "\n   "))
    in_tok, out_tok = resp.get("prompt_eval_count"), resp.get("eval_count")
    eval_ns = resp.get("eval_duration") or 0
    tps = (out_tok / (eval_ns / 1e9)) if out_tok and eval_ns else float("nan")
    print(f"   tokens: prompt={in_tok} completion={out_tok}  wall={wall:.2f}s  decode={tps:.1f} tok/s  cost=$0 (local)")

    # 3. Streaming: the perceived latency becomes time-to-first-token.
    print(f"\n== chat, streamed ==")
    t0 = time.perf_counter()
    first = None
    print("   ", end="", flush=True)
    for chunk in client.chat(
        model=args.model,
        messages=[{"role": "system", "content": SYSTEM_PROMPT},
                  {"role": "user", "content": "Give one PromQL query to see container restarts in a namespace."}],
        options={"num_predict": args.max_tokens, "temperature": 0.2},
        stream=True,
    ):
        if first is None:
            first = time.perf_counter() - t0
        print(chunk["message"]["content"], end="", flush=True)
    print(f"\n   time-to-first-token={first:.2f}s  total={time.perf_counter() - t0:.2f}s")

    # 4. Tool calling. Not every small model supports tools; the SDK raises or the model
    #    answers in plain text. The capstone handles both (native tool calls when the
    #    model supports them, ReAct text protocol otherwise).
    print(f"\n== tool calling ==")
    try:
        tresp = client.chat(
            model=args.model,
            messages=[{"role": "user", "content": "Which pods are restarting in the payments namespace?"}],
            tools=[KUBECTL_GET_TOOL],
            options={"temperature": 0},
        )
        calls = tresp["message"].get("tool_calls") or []
        if calls:
            for c in calls:
                fn = c["function"]
                print(f"   model requested: {fn['name']}({fn.get('arguments')})")
            print("   (the host would run a read-only kubectl here and send the result back as a 'tool' message)")
        else:
            print("   no tool call emitted; model answered in text:")
            print("   " + (tresp["message"]["content"] or "").strip()[:300].replace("\n", "\n   "))
    except Exception as exc:
        print(f"   tools unsupported by {args.model}: {type(exc).__name__}: {str(exc).splitlines()[0]}")

    # 5. Embeddings. `embed` takes a list and returns one vector per input. Dimensions depend
    #    on the model (nomic-embed-text: 768). Vectors from different models are NOT comparable,
    #    which is why the capstone refuses to mix them in one index.
    print(f"\n== embeddings ({args.embed_model}) ==")
    t0 = time.perf_counter()
    emb = client.embed(model=args.embed_model, input=ALERTS)
    vectors = emb["embeddings"]
    print(f"   {len(vectors)} vectors x {len(vectors[0])} dims in {time.perf_counter() - t0:.2f}s")

    # Nearest neighbours of the first alert. The OOMKilled alert should rank first: same
    # meaning, different words, which is exactly what lexical search misses.
    query_idx = 0
    sims = sorted(
        ((cosine(vectors[query_idx], v), i) for i, v in enumerate(vectors) if i != query_idx),
        reverse=True,
    )
    print(f"   query: {ALERTS[query_idx]}")
    for score, i in sims:
        print(f"   {score:6.3f}  {ALERTS[i]}")

    print("\nlesson: chat + embeddings from one local server is the whole `COPILOT_PROVIDER=ollama`")
    print("configuration. The quality gap versus hosted models is real and should be measured, not assumed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
