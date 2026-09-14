#!/usr/bin/env python3
"""
Module 01 lab · Same prompt, four providers.

Sends one prompt to OpenAI, Anthropic, Google Gemini and a local Ollama server
through each vendor's official SDK and prints, per provider:

    model · latency (s) · input tokens · output tokens · estimated cost (USD)

Every provider is optional. If its API key is missing (or the Ollama server is
not reachable) that row is skipped with a note instead of failing the run, so
the script is useful with any subset of credentials.

This is the scripted version of a Playground side-by-side comparison, and the
Python mirror of what `internal/llm/{openai,anthropic,gemini,ollama}.go` do in
the Go capstone: one request shape, four adapters, usage captured on every call.

Usage:
    python 01_chat_completions_compare.py
    python 01_chat_completions_compare.py --prompt "Explain CrashLoopBackOff in two sentences."
    python 01_chat_completions_compare.py --max-tokens 300 --only openai,ollama

Environment:
    OPENAI_API_KEY      OPENAI_MODEL     (default: gpt-4o-mini)
    ANTHROPIC_API_KEY   ANTHROPIC_MODEL  (default: claude-sonnet-4-20250514)
    GEMINI_API_KEY      GEMINI_MODEL     (default: gemini-2.0-flash)
    OLLAMA_HOST         OLLAMA_MODEL     (default: http://localhost:11434 / llama3.2)
"""

from __future__ import annotations

import argparse
import os
import sys
import time
from dataclasses import dataclass
from typing import Callable, Optional

# ---------------------------------------------------------------------------
# Pricing. USD per 1M tokens, (input, output). These numbers go stale; they are
# here so the script prints a *relative* cost, not an invoice. Check the vendor
# pricing page before quoting anything to finance and update the capture date.
#   https://openai.com/api/pricing/
#   https://www.anthropic.com/pricing
#   https://ai.google.dev/pricing
# The Go capstone keeps the same table in internal/tokens (Price, Cost).
# ---------------------------------------------------------------------------
PRICING_CAPTURED = "2025-09"
PRICING_USD_PER_1M: dict[str, tuple[float, float]] = {
    # OpenAI
    "gpt-4o": (2.50, 10.00),
    "gpt-4o-mini": (0.15, 0.60),
    "gpt-4.1": (2.00, 8.00),
    "gpt-4.1-mini": (0.40, 1.60),
    "gpt-4.1-nano": (0.10, 0.40),
    # Anthropic
    "claude-sonnet-4-20250514": (3.00, 15.00),
    "claude-3-5-haiku-latest": (0.80, 4.00),
    "claude-opus-4-20250514": (15.00, 75.00),
    # Google
    "gemini-2.0-flash": (0.10, 0.40),
    "gemini-2.5-flash": (0.30, 2.50),
    "gemini-2.5-pro": (1.25, 10.00),
}

SYSTEM_PROMPT = (
    "You are a platform/SRE copilot. Answer precisely and briefly for an "
    "experienced Kubernetes engineer. If you are not sure, say so."
)

DEFAULT_PROMPT = (
    "A Kubernetes pod is in CrashLoopBackOff with last exit code 137. "
    "In at most four sentences, explain what that means and the first "
    "thing an on-call engineer should check."
)


@dataclass
class Result:
    provider: str
    model: str
    latency_s: float
    input_tokens: Optional[int]
    output_tokens: Optional[int]
    text: str

    def cost_usd(self) -> Optional[float]:
        """Estimate cost from the pricing table; None if unknown or free (local)."""
        price = PRICING_USD_PER_1M.get(self.model)
        if price is None or self.input_tokens is None or self.output_tokens is None:
            return None
        in_price, out_price = price
        return (self.input_tokens * in_price + self.output_tokens * out_price) / 1_000_000


# ---------------------------------------------------------------------------
# Provider adapters. Each returns a Result or raises; the caller decides whether
# the failure is "skip" (no key) or "error" (call failed).
# ---------------------------------------------------------------------------

def run_openai(prompt: str, max_tokens: int) -> Result:
    from openai import OpenAI  # imported lazily so a missing SDK only affects this row

    model = os.environ.get("OPENAI_MODEL", "gpt-4o-mini")
    client = OpenAI()  # reads OPENAI_API_KEY
    t0 = time.perf_counter()
    resp = client.chat.completions.create(
        model=model,
        messages=[
            {"role": "system", "content": SYSTEM_PROMPT},
            {"role": "user", "content": prompt},
        ],
        # Newer OpenAI models require max_completion_tokens; the SDK maps it for
        # current models. The Go adapter sets the same field from Request.MaxTokens.
        max_completion_tokens=max_tokens,
        temperature=0.2,
        # End-user ID for abuse monitoring (Module 08). Any stable opaque string.
        user="lab-01-compare",
    )
    latency = time.perf_counter() - t0
    choice = resp.choices[0]
    usage = resp.usage
    text = choice.message.content or ""
    if choice.finish_reason == "length":
        text += "  [truncated: finish_reason=length]"
    return Result("openai", model, latency, usage.prompt_tokens, usage.completion_tokens, text)


def run_anthropic(prompt: str, max_tokens: int) -> Result:
    import anthropic

    model = os.environ.get("ANTHROPIC_MODEL", "claude-sonnet-4-20250514")
    client = anthropic.Anthropic()  # reads ANTHROPIC_API_KEY
    t0 = time.perf_counter()
    # Differences from OpenAI's shape, which internal/llm/anthropic.go also handles:
    # system is a top-level parameter, max_tokens is required, content is a list of blocks.
    resp = client.messages.create(
        model=model,
        max_tokens=max_tokens,
        temperature=0.2,
        system=SYSTEM_PROMPT,
        messages=[{"role": "user", "content": prompt}],
        metadata={"user_id": "lab-01-compare"},
    )
    latency = time.perf_counter() - t0
    text = "".join(block.text for block in resp.content if getattr(block, "type", "") == "text")
    if resp.stop_reason == "max_tokens":
        text += "  [truncated: stop_reason=max_tokens]"
    return Result("anthropic", model, latency, resp.usage.input_tokens, resp.usage.output_tokens, text)


def run_gemini(prompt: str, max_tokens: int) -> Result:
    from google import genai
    from google.genai import types

    model = os.environ.get("GEMINI_MODEL", "gemini-2.0-flash")
    client = genai.Client(api_key=os.environ["GEMINI_API_KEY"])
    t0 = time.perf_counter()
    # Gemini: "contents" + "parts"; system prompt is a config field, not a message.
    resp = client.models.generate_content(
        model=model,
        contents=prompt,
        config=types.GenerateContentConfig(
            system_instruction=SYSTEM_PROMPT,
            max_output_tokens=max_tokens,
            temperature=0.2,
        ),
    )
    latency = time.perf_counter() - t0
    usage = resp.usage_metadata
    in_tok = getattr(usage, "prompt_token_count", None)
    out_tok = getattr(usage, "candidates_token_count", None)
    return Result("gemini", model, latency, in_tok, out_tok, resp.text or "")


def run_ollama(prompt: str, max_tokens: int) -> Result:
    import ollama

    host = os.environ.get("OLLAMA_HOST", "http://localhost:11434")
    model = os.environ.get("OLLAMA_MODEL", "llama3.2")
    client = ollama.Client(host=host)
    t0 = time.perf_counter()
    resp = client.chat(
        model=model,
        messages=[
            {"role": "system", "content": SYSTEM_PROMPT},
            {"role": "user", "content": prompt},
        ],
        # Ollama's name for the output cap is num_predict.
        options={"num_predict": max_tokens, "temperature": 0.2},
    )
    latency = time.perf_counter() - t0
    # Ollama reports prompt_eval_count / eval_count; both may be absent on cache hits.
    in_tok = resp.get("prompt_eval_count")
    out_tok = resp.get("eval_count")
    return Result("ollama", model, latency, in_tok, out_tok, resp["message"]["content"])


def ollama_reachable() -> bool:
    """Cheap liveness probe so a missing local server is a skip, not a traceback."""
    import urllib.request

    host = os.environ.get("OLLAMA_HOST", "http://localhost:11434").rstrip("/")
    try:
        with urllib.request.urlopen(f"{host}/api/tags", timeout=2) as r:  # noqa: S310
            return r.status == 200
    except Exception:
        return False


# provider name -> (precondition, runner). Precondition returns a skip reason or None.
PROVIDERS: dict[str, tuple[Callable[[], Optional[str]], Callable[[str, int], Result]]] = {
    "openai": (lambda: None if os.environ.get("OPENAI_API_KEY") else "OPENAI_API_KEY not set", run_openai),
    "anthropic": (lambda: None if os.environ.get("ANTHROPIC_API_KEY") else "ANTHROPIC_API_KEY not set", run_anthropic),
    "gemini": (lambda: None if os.environ.get("GEMINI_API_KEY") else "GEMINI_API_KEY not set", run_gemini),
    "ollama": (lambda: None if ollama_reachable() else "Ollama not reachable at OLLAMA_HOST", run_ollama),
}


def fmt_cost(c: Optional[float], provider: str) -> str:
    if provider == "ollama":
        return "$0.0000 (local)"
    if c is None:
        return "n/a (model not in table)"
    return f"${c:.4f}"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--prompt", default=DEFAULT_PROMPT, help="user prompt to send to every provider")
    ap.add_argument("--max-tokens", type=int, default=200, help="output token cap per call")
    ap.add_argument("--only", default="", help="comma-separated subset of: openai,anthropic,gemini,ollama")
    ap.add_argument("--show-text", action="store_true", help="print each provider's answer text")
    args = ap.parse_args()

    wanted = [p.strip() for p in args.only.split(",") if p.strip()] or list(PROVIDERS)
    results: list[Result] = []
    skipped: list[tuple[str, str]] = []

    print(f"prompt: {args.prompt!r}\nmax_tokens: {args.max_tokens}\n")

    for name in wanted:
        if name not in PROVIDERS:
            print(f"unknown provider {name!r}; choose from {', '.join(PROVIDERS)}", file=sys.stderr)
            continue
        precondition, runner = PROVIDERS[name]
        reason = precondition()
        if reason:
            skipped.append((name, reason))
            continue
        try:
            results.append(runner(args.prompt, args.max_tokens))
        except Exception as exc:  # a provider error should not abort the comparison
            skipped.append((name, f"call failed: {type(exc).__name__}: {exc}"))

    # Table. Plain text on purpose: the output is often pasted into an ADR.
    header = f"{'provider':<10} {'model':<30} {'latency':>8} {'in_tok':>7} {'out_tok':>8}  {'est_cost':<24}"
    print(header)
    print("-" * len(header))
    for r in results:
        print(
            f"{r.provider:<10} {r.model:<30} {r.latency_s:>7.2f}s "
            f"{(r.input_tokens if r.input_tokens is not None else '?'):>7} "
            f"{(r.output_tokens if r.output_tokens is not None else '?'):>8}  "
            f"{fmt_cost(r.cost_usd(), r.provider):<24}"
        )
    for name, reason in skipped:
        print(f"{name:<10} skipped: {reason}")

    print(f"\npricing table captured {PRICING_CAPTURED}; verify against vendor pricing pages before quoting.")

    if args.show_text:
        for r in results:
            print(f"\n=== {r.provider} / {r.model} ===\n{r.text.strip()}")

    return 0 if results else 1


if __name__ == "__main__":
    sys.exit(main())
