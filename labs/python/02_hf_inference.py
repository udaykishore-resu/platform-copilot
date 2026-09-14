#!/usr/bin/env python3
"""
Module 02 lab · Hugging Face Inference SDK: three tasks, zero downloads.

Uses `huggingface_hub.InferenceClient` to run hosted models for three Hugging Face
*tasks* that a platform copilot actually needs:

  1. text generation (chat)       explain an error to an engineer
  2. summarization                condense a runbook excerpt
  3. zero-shot classification     route an alert into a small label set

Task 3 is the important one for judgment: routing alerts is a classification
problem, and a ~400 MB entailment model does it in milliseconds for free. Sending
every alert to a frontier chat model for the same decision is waste. The copilot
uses an LLM only for the alerts that need an explanation.

Each task is wrapped so that a rate limit, a cold start, or a model that the
serverless API is not currently serving produces a clear "skipped" line rather
than a traceback. The hosted serverless API is for prototyping; it is not an SLO.

Usage:
    python 02_hf_inference.py
    python 02_hf_inference.py --alert "ingress-nginx 5xx ratio above 5% for 10m"

Environment:
    HF_TOKEN   optional. Raises rate limits and unlocks gated models.
               Create one at https://huggingface.co/settings/tokens (read scope).
    HF_CHAT_MODEL, HF_SUMMARY_MODEL, HF_ZEROSHOT_MODEL   override the defaults below.
"""

from __future__ import annotations

import argparse
import os
import sys
import time
from typing import Callable

from huggingface_hub import InferenceClient

# Defaults chosen for availability on the serverless API and modest size.
# Model cards: read the licence and evaluation table before adopting any of them.
DEFAULT_CHAT_MODEL = os.environ.get("HF_CHAT_MODEL", "Qwen/Qwen2.5-7B-Instruct")
DEFAULT_SUMMARY_MODEL = os.environ.get("HF_SUMMARY_MODEL", "facebook/bart-large-cnn")
DEFAULT_ZEROSHOT_MODEL = os.environ.get("HF_ZEROSHOT_MODEL", "facebook/bart-large-mnli")

RUNBOOK_EXCERPT = """\
CrashLoopBackOff means the kubelet started the container, it exited, and Kubernetes is
backing off exponentially before the next restart (10s, 20s, 40s, up to 5 minutes).
First read the previous container's logs with `kubectl logs <pod> -c <container> --previous`,
because the current container may not have logged anything yet. Then check the last
termination reason and exit code with `kubectl describe pod <pod>`: exit code 137 with
reason OOMKilled means the memory limit is too low or the process leaks; exit code 1 with
an application stack trace means a configuration or dependency problem; exit code 0 in a
loop usually means the entrypoint finishes immediately and the container has no long-running
process. Do not raise the memory limit blindly; compare `container_memory_working_set_bytes`
against the limit in Grafana for the last hour. Verify: the pod reaches Running with
RESTARTS no longer incrementing for at least two back-off periods.
"""

DEFAULT_ALERT = (
    "[FIRING] KubePodCrashLooping: pod payments-api-7d9f4 in namespace payments "
    "is restarting 4.2 times / 10 minutes. Last exit code 137."
)

# Label set for alert routing. Small and operationally meaningful; the classifier scores
# the alert against each label as a hypothesis ("this text is about <label>").
ALERT_LABELS = [
    "application crash",
    "out of memory",
    "network or ingress error",
    "certificate expiry",
    "disk or storage pressure",
    "noise or test alert",
]


def run_task(name: str, fn: Callable[[], None]) -> None:
    """Run one task, timing it, and turn any failure into a readable skip line."""
    print(f"\n== {name} ==")
    t0 = time.perf_counter()
    try:
        fn()
        print(f"   ({time.perf_counter() - t0:.2f}s)")
    except Exception as exc:
        msg = str(exc).strip().splitlines()[0] if str(exc).strip() else type(exc).__name__
        print(f"   skipped: {type(exc).__name__}: {msg}")
        print("   (hosted serverless models are rate-limited and not all are loaded; set HF_TOKEN,")
        print("    pick another model with HF_*_MODEL, or run 02_transformers_local.py instead)")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--alert", default=DEFAULT_ALERT, help="alert text to classify")
    ap.add_argument("--question", default="Explain exit code 137 in a Kubernetes pod in two sentences.",
                    help="question for the chat model")
    ap.add_argument("--max-tokens", type=int, default=120)
    args = ap.parse_args()

    token = os.environ.get("HF_TOKEN")
    if not token:
        print("note: HF_TOKEN not set; using anonymous access with lower rate limits.")
    client = InferenceClient(token=token, timeout=60)

    # 1. Text generation via the chat-completion task. The serverless API exposes an
    #    OpenAI-compatible chat shape for instruct models, which is what the capstone's
    #    Provider interface models too.
    def chat() -> None:
        resp = client.chat_completion(
            model=DEFAULT_CHAT_MODEL,
            messages=[
                {"role": "system", "content": "You are a concise SRE copilot for Kubernetes engineers."},
                {"role": "user", "content": args.question},
            ],
            max_tokens=args.max_tokens,
            temperature=0.2,
        )
        print(f"   model: {DEFAULT_CHAT_MODEL}")
        print("   " + resp.choices[0].message.content.strip().replace("\n", "\n   "))
        if resp.usage:
            print(f"   tokens: prompt={resp.usage.prompt_tokens} completion={resp.usage.completion_tokens}")

    # 2. Summarisation with an encoder-decoder model trained for the task. Much smaller and
    #    cheaper than a chat model for this one job; less flexible (no instructions).
    def summarise() -> None:
        out = client.summarization(RUNBOOK_EXCERPT, model=DEFAULT_SUMMARY_MODEL)
        text = out.summary_text if hasattr(out, "summary_text") else str(out)
        print(f"   model: {DEFAULT_SUMMARY_MODEL}")
        print("   " + text.strip())

    # 3. Zero-shot classification. An NLI model scores "premise entails 'this is about X'".
    #    No training data, arbitrary labels, CPU-friendly. This is the alert router.
    def classify() -> None:
        results = client.zero_shot_classification(
            args.alert,
            candidate_labels=ALERT_LABELS,
            multi_label=False,  # exactly one primary cause; use True when labels can co-occur
            model=DEFAULT_ZEROSHOT_MODEL,
        )
        print(f"   model: {DEFAULT_ZEROSHOT_MODEL}")
        print(f"   alert: {args.alert}")
        # The SDK returns a list of objects with .label and .score (sorted desc).
        for r in results:
            label = getattr(r, "label", None) or r["label"]
            score = getattr(r, "score", None) or r["score"]
            bar = "#" * int(score * 40)
            print(f"   {label:<26} {score:6.3f}  {bar}")
        top = results[0]
        top_label = getattr(top, "label", None) or top["label"]
        top_score = getattr(top, "score", None) or top["score"]
        # Routing decision the copilot would make. Thresholds come from an eval set, not vibes.
        if top_label == "noise or test alert" and top_score > 0.6:
            print("   route: drop (known noise)")
        elif top_score < 0.4:
            print("   route: escalate to LLM for explanation (low-confidence classification)")
        else:
            print(f"   route: search runbooks for '{top_label}' and answer with citations")

    run_task("text generation (chat)", chat)
    run_task("summarization", summarise)
    run_task("zero-shot classification (alert routing)", classify)

    print("\nlesson: only the first task needs an LLM. The other two are cheaper, faster task models")
    print("that the copilot runs before deciding whether to spend chat tokens at all.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
