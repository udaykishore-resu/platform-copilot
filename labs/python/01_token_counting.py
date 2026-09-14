#!/usr/bin/env python3
"""
Module 01 lab · Token counting: exact (tiktoken) vs heuristic, and what a runbook costs.

Reads a file (a runbook, a Kubernetes manifest, a Terraform module, anything text),
then prints:

  1. Exact token counts from tiktoken for the encodings OpenAI models use.
  2. The naive heuristic  len(text) / 4  and a per-content-type heuristic, with the
     percentage error against the exact count. This is the experiment that justifies
     the safety margin in the Go capstone's `internal/tokens.Estimate`, which cannot
     depend on a vendor tokeniser because it has to work with every provider
     including the mock.
  3. The cost of sending that file as *input* to a handful of models, using a
     relative pricing table (see the capture date; verify on the vendor page).

Usage:
    python 01_token_counting.py ../../data/knowledge/runbook-payments-api-crashloop.md
    python 01_token_counting.py path/to/deployment.yaml --output-tokens 400

No API key is needed; tiktoken downloads its encoding files on first use.
"""

from __future__ import annotations

import argparse
import pathlib
import sys

import tiktoken

# USD per 1M tokens, (input, output). Relative guide only; see vendor pricing pages.
PRICING_CAPTURED = "2025-09"
PRICING_USD_PER_1M: dict[str, tuple[float, float]] = {
    "gpt-4o": (2.50, 10.00),
    "gpt-4o-mini": (0.15, 0.60),
    "gpt-4.1": (2.00, 8.00),
    "gpt-4.1-nano": (0.10, 0.40),
    "claude-sonnet-4-20250514": (3.00, 15.00),
    "gemini-2.0-flash": (0.10, 0.40),
}

# Characters-per-token heuristic by content type. Prose is ~4 chars/token in English;
# YAML, HCL and JSON carry more punctuation and indentation, so fewer chars per token.
# These factors are what internal/tokens.Estimate uses; this lab is how they were measured.
CHARS_PER_TOKEN: dict[str, float] = {
    ".md": 4.0,
    ".txt": 4.0,
    ".yaml": 3.0,
    ".yml": 3.0,
    ".json": 2.8,
    ".tf": 3.2,
    ".hcl": 3.2,
    ".go": 3.3,
    ".py": 3.4,
}
DEFAULT_CHARS_PER_TOKEN = 3.5

# Encodings: o200k_base is used by gpt-4o and later; cl100k_base by gpt-4 / gpt-3.5 era
# models and text-embedding-3-*. Non-OpenAI vendors use their own tokenisers, so for
# them these counts are an approximation (usually within ~10-15%).
ENCODINGS = ["o200k_base", "cl100k_base"]


def exact_counts(text: str) -> dict[str, int]:
    out: dict[str, int] = {}
    for name in ENCODINGS:
        enc = tiktoken.get_encoding(name)
        out[name] = len(enc.encode(text, disallowed_special=()))
    return out


def heuristic_counts(text: str, suffix: str) -> dict[str, int]:
    naive = round(len(text) / 4)
    factor = CHARS_PER_TOKEN.get(suffix.lower(), DEFAULT_CHARS_PER_TOKEN)
    typed = round(len(text) / factor)
    return {"len/4": naive, f"len/{factor} ({suffix or 'default'})": typed}


def pct_err(estimate: int, truth: int) -> str:
    if truth == 0:
        return "n/a"
    return f"{(estimate - truth) / truth * 100:+.1f}%"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("path", type=pathlib.Path, help="text file to count (runbook, manifest, .tf, ...)")
    ap.add_argument("--output-tokens", type=int, default=300,
                    help="assumed answer length when pricing a full request (default 300)")
    ap.add_argument("--show-tokens", type=int, default=0,
                    help="print the first N tokens of the o200k_base encoding to see how text splits")
    args = ap.parse_args()

    text = args.path.read_text(encoding="utf-8", errors="replace")
    suffix = args.path.suffix
    chars = len(text)
    words = len(text.split())
    lines = text.count("\n") + 1

    print(f"file: {args.path}  ({chars} chars, {words} words, {lines} lines)\n")

    exact = exact_counts(text)
    # Use the modern encoding as "truth" for the error column.
    truth = exact["o200k_base"]

    print("exact (tiktoken)")
    for name, n in exact.items():
        print(f"  {name:<14} {n:>8} tokens   ({chars / n:.2f} chars/token, {words / n:.2f} words/token)")

    print("\nheuristics vs o200k_base")
    for label, n in heuristic_counts(text, suffix).items():
        print(f"  {label:<22} {n:>8} tokens   error {pct_err(n, truth)}")

    print("\nwhy this matters: a heuristic that undercounts on YAML/HCL lets a prompt overflow the")
    print("context window silently. The Go budgeter adds a margin on top of the typed heuristic.")

    if args.show_tokens > 0:
        enc = tiktoken.get_encoding("o200k_base")
        ids = enc.encode(text, disallowed_special=())[: args.show_tokens]
        pieces = [enc.decode([i]) for i in ids]
        print(f"\nfirst {len(pieces)} tokens (o200k_base):")
        print("  " + " | ".join(repr(p) for p in pieces))

    # Cost: the file as input, once per request, plus an assumed answer length.
    # Embedding the file (Module 03) costs far less per token than chatting about it.
    print(f"\ncost to send this file as input + {args.output_tokens} output tokens (pricing captured {PRICING_CAPTURED})")
    print(f"  {'model':<28} {'input $':>10} {'output $':>10} {'total $':>10}   {'x1000 requests':>14}")
    for model, (in_p, out_p) in PRICING_USD_PER_1M.items():
        in_cost = truth * in_p / 1_000_000
        out_cost = args.output_tokens * out_p / 1_000_000
        total = in_cost + out_cost
        print(f"  {model:<28} {in_cost:>10.5f} {out_cost:>10.5f} {total:>10.5f}   {total * 1000:>13.2f}")

    print("\nreading the table: output tokens cost 3-5x input tokens, and the flagship tier costs")
    print("an order of magnitude more than the small tier. Retrieval exists so that you send three")
    print("relevant chunks, not the whole file, on every question.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
