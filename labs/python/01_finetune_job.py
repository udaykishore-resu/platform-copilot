#!/usr/bin/env python3
"""
Module 01 lab · Build a fine-tuning dataset from runbooks and (optionally) submit the job.

What this does
--------------
1. Walks a directory of Markdown runbooks.
2. Turns every `## Heading` section into one chat example:
       system:    the copilot's persona and the *format* we want it to learn
       user:      a question derived from the heading ("How do I <heading>?")
       assistant: the section body, lightly normalised
3. Validates the JSONL against the OpenAI chat fine-tuning format
   (roles, non-empty content, minimum example count, rough token size).
4. Estimates the training token count so you can price the job from the vendor page.
5. DRY RUN BY DEFAULT. Nothing is uploaded unless you pass --submit.
   With --submit it uploads the file and creates a fine-tuning job; with
   --status <job_id> it polls an existing job.

Why this is the *right* thing to fine-tune for, and what it is not
-----------------------------------------------------------------
Fine-tuning teaches format and style reliably; it does NOT reliably teach facts and
it cannot cite sources. The capstone uses retrieval (Module 05) for runbook
*knowledge*. The point of this dataset is that the assistant turns are written in
the team's runbook style (numbered steps, a "Verify" line, exact kubectl commands),
so a fine-tuned small model learns to answer in that shape with a one-line system
prompt instead of a 2,000-token few-shot prompt on every call.

Usage:
    python 01_finetune_job.py --corpus ../../data/knowledge --out runbook-qa.jsonl
    python 01_finetune_job.py --corpus ../../data/knowledge --out runbook-qa.jsonl --submit
    python 01_finetune_job.py --status ftjob-abc123

Environment (only for --submit / --status):
    OPENAI_API_KEY
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import re
import sys
import time
from typing import Iterator

SYSTEM_PROMPT = (
    "You are the platform team's SRE copilot. Answer runbook questions as a short numbered "
    "procedure with exact commands, then a single line starting with 'Verify:'. "
    "If the runbook does not cover the question, say so."
)

# OpenAI requires at least 10 examples; 50-100 is a realistic minimum for a format task.
MIN_EXAMPLES = 10
RECOMMENDED_EXAMPLES = 50
# Keep assistant turns bounded; very long sections are split or truncated.
MAX_ANSWER_CHARS = 2500
MIN_ANSWER_CHARS = 80

HEADING_RE = re.compile(r"^(#{1,3})\s+(.*?)\s*$", re.MULTILINE)


def iter_sections(md: str) -> Iterator[tuple[str, str]]:
    """Yield (heading, body) for every ##/### section; the H1 is used as document title context."""
    matches = list(HEADING_RE.finditer(md))
    title = ""
    for i, m in enumerate(matches):
        level, heading = len(m.group(1)), m.group(2).strip()
        start = m.end()
        end = matches[i + 1].start() if i + 1 < len(matches) else len(md)
        body = md[start:end].strip()
        if level == 1:
            title = heading
            continue
        if not body:
            continue
        # Prefix the heading with the document title so questions are specific:
        # "CrashLoopBackOff: Check container exit code" rather than "Check container exit code".
        yield (f"{title}: {heading}" if title else heading), body


def heading_to_question(heading: str) -> str:
    """Turn a runbook heading into a natural question. Crude on purpose; curate the JSONL by hand."""
    h = heading.strip().rstrip(".:")
    lower = h.lower()
    if lower.startswith(("how ", "what ", "why ", "when ", "where ")):
        return h if h.endswith("?") else h + "?"
    if ":" in h:
        doc, step = h.split(":", 1)
        return f"For {doc.strip()}, how do I {step.strip()[0].lower() + step.strip()[1:]}?"
    return f"How do I {h[0].lower() + h[1:]}?"


def normalise_answer(body: str) -> str:
    """Strip HTML comments and collapse blank runs; keep code fences intact."""
    body = re.sub(r"<!--.*?-->", "", body, flags=re.DOTALL)
    body = re.sub(r"\n{3,}", "\n\n", body).strip()
    if len(body) > MAX_ANSWER_CHARS:
        body = body[:MAX_ANSWER_CHARS].rsplit("\n", 1)[0].rstrip() + "\n\n(continued in runbook)"
    return body


def build_examples(corpus: pathlib.Path) -> list[dict]:
    examples: list[dict] = []
    files = sorted(corpus.rglob("*.md"))
    if not files:
        raise SystemExit(f"no .md files under {corpus}")
    for path in files:
        md = path.read_text(encoding="utf-8", errors="replace")
        for heading, body in iter_sections(md):
            answer = normalise_answer(body)
            if len(answer) < MIN_ANSWER_CHARS:
                continue  # a heading with one line under it is not a useful example
            examples.append(
                {
                    "messages": [
                        {"role": "system", "content": SYSTEM_PROMPT},
                        {"role": "user", "content": heading_to_question(heading)},
                        {"role": "assistant", "content": answer},
                    ],
                    # Metadata keys outside "messages" are ignored by the API but are
                    # useful for auditing which runbook produced which example.
                    "_source": str(path.relative_to(corpus)),
                }
            )
    return examples


def validate(examples: list[dict]) -> list[str]:
    """Return a list of problems; empty means the dataset is acceptable."""
    problems: list[str] = []
    if len(examples) < MIN_EXAMPLES:
        problems.append(f"only {len(examples)} examples; the API requires at least {MIN_EXAMPLES}")
    elif len(examples) < RECOMMENDED_EXAMPLES:
        problems.append(f"warning: {len(examples)} examples; {RECOMMENDED_EXAMPLES}+ recommended for a stable format")
    allowed_roles = {"system", "user", "assistant"}
    for i, ex in enumerate(examples):
        msgs = ex.get("messages")
        if not isinstance(msgs, list) or len(msgs) < 2:
            problems.append(f"example {i}: 'messages' must be a list with at least user+assistant")
            continue
        roles = [m.get("role") for m in msgs]
        if any(r not in allowed_roles for r in roles):
            problems.append(f"example {i}: bad role in {roles}")
        if "assistant" not in roles:
            problems.append(f"example {i}: no assistant turn")
        for m in msgs:
            if not isinstance(m.get("content"), str) or not m["content"].strip():
                problems.append(f"example {i}: empty content for role {m.get('role')}")
    return problems


def estimate_tokens(examples: list[dict]) -> int:
    """Exact if tiktoken is available, otherwise chars/4. Training cost = tokens x epochs x price."""
    text = "\n".join(m["content"] for ex in examples for m in ex["messages"])
    try:
        import tiktoken

        return len(tiktoken.get_encoding("o200k_base").encode(text, disallowed_special=()))
    except Exception:
        return len(text) // 4


def write_jsonl(examples: list[dict], out: pathlib.Path) -> None:
    with out.open("w", encoding="utf-8") as f:
        for ex in examples:
            # Drop our private metadata before upload; keep the file strictly to the API schema.
            f.write(json.dumps({"messages": ex["messages"]}, ensure_ascii=False) + "\n")


def submit(out: pathlib.Path, base_model: str, suffix: str, epochs: int | None) -> str:
    from openai import OpenAI

    client = OpenAI()
    with out.open("rb") as f:
        uploaded = client.files.create(file=f, purpose="fine-tune")
    print(f"uploaded training file: {uploaded.id}")
    kwargs: dict = {"training_file": uploaded.id, "model": base_model, "suffix": suffix}
    if epochs:
        # The `method` field selects supervised fine-tuning; hyperparameters are optional
        # and the API picks sensible defaults when the whole field is omitted.
        kwargs["method"] = {"type": "supervised", "supervised": {"hyperparameters": {"n_epochs": epochs}}}
    job = client.fine_tuning.jobs.create(**kwargs)
    print(f"created fine-tuning job: {job.id} (status={job.status}, base={base_model})")
    print(f"poll with: python {pathlib.Path(__file__).name} --status {job.id}")
    return job.id


def status(job_id: str, follow: bool) -> None:
    from openai import OpenAI

    client = OpenAI()
    while True:
        job = client.fine_tuning.jobs.retrieve(job_id)
        print(f"{job.id}: status={job.status} model={job.fine_tuned_model or '-'} trained_tokens={job.trained_tokens or '-'}")
        events = client.fine_tuning.jobs.list_events(fine_tuning_job_id=job_id, limit=5)
        for ev in reversed(list(events.data)):
            print(f"  {time.strftime('%H:%M:%S', time.gmtime(ev.created_at))}  {ev.message}")
        if not follow or job.status in {"succeeded", "failed", "cancelled"}:
            if job.status == "succeeded":
                print(f"\nuse it: COPILOT_PROVIDER=openai COPILOT_MODEL={job.fine_tuned_model} ./bin/copilot ask ...")
            return
        time.sleep(30)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--corpus", type=pathlib.Path, help="directory of Markdown runbooks")
    ap.add_argument("--out", type=pathlib.Path, default=pathlib.Path("runbook-qa.jsonl"), help="JSONL output path")
    ap.add_argument("--submit", action="store_true", help="upload and create the job (default is dry run)")
    ap.add_argument("--base-model", default=os.environ.get("FINETUNE_BASE_MODEL", "gpt-4o-mini-2024-07-18"),
                    help="fine-tunable base model id; check the vendor docs for the current list")
    ap.add_argument("--suffix", default="platform-copilot", help="suffix for the fine-tuned model name")
    ap.add_argument("--epochs", type=int, default=None, help="override n_epochs (default: vendor auto)")
    ap.add_argument("--status", metavar="JOB_ID", help="poll an existing job instead of building a dataset")
    ap.add_argument("--follow", action="store_true", help="with --status, keep polling until terminal")
    args = ap.parse_args()

    if args.status:
        if not os.environ.get("OPENAI_API_KEY"):
            print("OPENAI_API_KEY not set", file=sys.stderr)
            return 2
        status(args.status, args.follow)
        return 0

    if not args.corpus:
        ap.error("--corpus is required unless --status is given")

    examples = build_examples(args.corpus)
    problems = validate(examples)
    write_jsonl(examples, args.out)

    tokens = estimate_tokens(examples)
    sources = sorted({ex["_source"] for ex in examples})
    print(f"wrote {len(examples)} examples from {len(sources)} runbooks to {args.out}")
    print(f"estimated training tokens per epoch: {tokens:,}")
    print("  training cost = tokens x epochs x per-token training price (see https://openai.com/api/pricing/)")
    print("  inference on the fine-tuned model is also priced higher than the base; budget both.")

    if problems:
        print("\nvalidation:")
        for p in problems:
            print(f"  - {p}")
    hard = [p for p in problems if not p.startswith("warning")]

    print("\nnext: open the JSONL and curate it. Delete examples whose question is awkward, fix answers")
    print("that do not end with a 'Verify:' line, and hold out ~10% as a validation file.")

    if not args.submit:
        print("\ndry run: nothing uploaded. Re-run with --submit to create the job.")
        return 1 if hard else 0

    if hard:
        print("\nrefusing to submit: fix the validation errors above first.", file=sys.stderr)
        return 1
    if not os.environ.get("OPENAI_API_KEY"):
        print("\nOPENAI_API_KEY not set; cannot submit.", file=sys.stderr)
        return 2
    submit(args.out, args.base_model, args.suffix, args.epochs)
    return 0


if __name__ == "__main__":
    sys.exit(main())
