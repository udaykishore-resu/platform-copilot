#!/usr/bin/env python3
"""
Module 02 lab · `transformers` pipelines, fully local, CPU is fine.

Same three tasks as 02_hf_inference.py, but the models are downloaded from the
Hugging Face Hub once (cached under ~/.cache/huggingface) and run in this process.
No API key, no network after the first run, no data leaves the machine.

Models are deliberately small so the lab runs on a laptop:
  zero-shot classification   typeform/distilbert-base-uncased-mnli   (~250 MB)
  summarization              Falconsai/text_summarization            (~240 MB, T5-small)
  text generation            HuggingFaceTB/SmolLM2-360M-Instruct     (~720 MB)

Every model is pinned to a Hub revision via --revision-* flags (default "main").
In production pin a commit hash; it is the model-weights equivalent of pinning a
container image by digest, and it is what keeps the capstone's evaluation
reproducible.

Usage:
    python 02_transformers_local.py
    python 02_transformers_local.py --skip-generation        # classification + summary only
    python 02_transformers_local.py --alert "TLS certificate for api.example.com expires in 6 days"

Set TRANSFORMERS_OFFLINE=1 after the first run to prove nothing is fetched.
"""

from __future__ import annotations

import argparse
import os
import sys
import time

# Quieter logs; the pipelines print a lot of advisory warnings by default.
os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")
os.environ.setdefault("TRANSFORMERS_VERBOSITY", "error")

from transformers import pipeline  # noqa: E402

ZEROSHOT_MODEL = os.environ.get("LOCAL_ZEROSHOT_MODEL", "typeform/distilbert-base-uncased-mnli")
SUMMARY_MODEL = os.environ.get("LOCAL_SUMMARY_MODEL", "Falconsai/text_summarization")
GENERATION_MODEL = os.environ.get("LOCAL_GENERATION_MODEL", "HuggingFaceTB/SmolLM2-360M-Instruct")

ALERT_LABELS = [
    "application crash",
    "out of memory",
    "network or ingress error",
    "certificate expiry",
    "disk or storage pressure",
    "noise or test alert",
]

DEFAULT_ALERT = (
    "[FIRING] KubePodCrashLooping: pod payments-api-7d9f4 in namespace payments "
    "is restarting 4.2 times / 10 minutes. Last exit code 137."
)

RUNBOOK_EXCERPT = (
    "CrashLoopBackOff means the kubelet started the container, it exited, and Kubernetes is "
    "backing off exponentially before the next restart. First read the previous container's "
    "logs with kubectl logs --previous, because the current container may not have logged "
    "anything yet. Then check the last termination reason and exit code with kubectl describe "
    "pod. Exit code 137 with reason OOMKilled means the memory limit is too low or the process "
    "leaks. Exit code 1 with a stack trace means a configuration or dependency problem. Do not "
    "raise the memory limit blindly; compare the working set against the limit in Grafana for "
    "the last hour. Verify that the pod reaches Running with RESTARTS no longer incrementing."
)


def timed(label: str):
    """Tiny context manager that prints wall time; model load time dominates the first run."""
    class _T:
        def __enter__(self):
            self.t0 = time.perf_counter()
            return self

        def __exit__(self, *exc):
            print(f"   ({time.perf_counter() - self.t0:.2f}s {label})")

    return _T()


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--alert", default=DEFAULT_ALERT)
    ap.add_argument("--question", default="Explain Kubernetes exit code 137 in two sentences.")
    ap.add_argument("--skip-generation", action="store_true", help="skip the ~720 MB chat model")
    ap.add_argument("--revision-zeroshot", default="main")
    ap.add_argument("--revision-summary", default="main")
    ap.add_argument("--revision-generation", default="main")
    ap.add_argument("--device", default=None, help='e.g. "cpu", "cuda", "mps"; default auto (cpu)')
    args = ap.parse_args()

    device_kw = {"device": args.device} if args.device else {}

    # 1. Zero-shot classification: the alert router. `pipeline()` downloads the model and
    #    tokenizer, picks the right head for the task, and handles batching and post-processing.
    print("\n== zero-shot classification (alert routing) ==")
    with timed("load"):
        clf = pipeline("zero-shot-classification", model=ZEROSHOT_MODEL,
                       revision=args.revision_zeroshot, **device_kw)
    with timed("infer"):
        out = clf(args.alert, candidate_labels=ALERT_LABELS, multi_label=False)
    print(f"   model: {ZEROSHOT_MODEL}")
    print(f"   alert: {args.alert}")
    for label, score in zip(out["labels"], out["scores"]):
        print(f"   {label:<26} {score:6.3f}  {'#' * int(score * 40)}")
    top_label, top_score = out["labels"][0], out["scores"][0]
    if top_label == "noise or test alert" and top_score > 0.6:
        print("   route: drop (known noise)")
    elif top_score < 0.4:
        print("   route: escalate to LLM (low confidence)")
    else:
        print(f"   route: search runbooks for '{top_label}'")

    # 2. Summarisation with a small T5 fine-tuned for the task. Encoder-decoder models are
    #    compact and deterministic for this; they cannot follow instructions like a chat model.
    print("\n== summarization ==")
    with timed("load"):
        summ = pipeline("summarization", model=SUMMARY_MODEL, revision=args.revision_summary, **device_kw)
    with timed("infer"):
        s = summ(RUNBOOK_EXCERPT, max_length=80, min_length=25, do_sample=False)
    print(f"   model: {SUMMARY_MODEL}")
    print("   " + s[0]["summary_text"].strip())

    # 3. Text generation with a small instruct model. Expect a noticeable quality gap versus
    #    a 7B+ model or a hosted frontier model; this is the trade-off the module is about.
    if args.skip_generation:
        print("\n== text generation == skipped (--skip-generation)")
    else:
        print("\n== text generation (chat) ==")
        with timed("load"):
            gen = pipeline("text-generation", model=GENERATION_MODEL,
                           revision=args.revision_generation, **device_kw)
        messages = [
            {"role": "system", "content": "You are a concise SRE copilot for Kubernetes engineers."},
            {"role": "user", "content": args.question},
        ]
        with timed("infer"):
            # Chat-formatted input: the pipeline applies the model's chat template. `max_new_tokens`
            # is the local equivalent of max_tokens and bounds latency the same way.
            result = gen(messages, max_new_tokens=120, do_sample=False, return_full_text=False)
        text = result[0]["generated_text"]
        if isinstance(text, list):  # some versions return the appended message list
            text = text[-1]["content"]
        print(f"   model: {GENERATION_MODEL}")
        print("   " + str(text).strip().replace("\n", "\n   "))

    print("\nlesson: task models (classification, summarisation) are small, fast and private.")
    print("For chat quality, a small local model is the floor; measure it against the hosted")
    print("configuration on the eval set before deciding where the copilot's `ask` path runs.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
