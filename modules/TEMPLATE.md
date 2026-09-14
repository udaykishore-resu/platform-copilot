# Module NN · <Roadmap section name>

> **Roadmap nodes covered:** <comma-separated list of every roadmap node this module covers — nothing may be skipped>
>
> **Capstone step:** <what this module adds to `platform-copilot`, with the exact Go packages / commands touched>
>
> **Time:** ~N hours · **Prerequisites:** Module MM

## Why this module exists

Two or three paragraphs. What a working engineer is unable to do before this module and is able to do after it. Anchor it in the capstone (the platform/SRE copilot) — the concrete pain this module removes from that product.

## Concept cards

One card per roadmap node, in roadmap order. Every card has exactly these five parts.

### <Node name>

**What it is.** Precise, two to five sentences. Define the term the way a Principal Engineer would in a design review — mechanism, not marketing.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| <the thing itself> | | | |
| <alternative 1> | | | |
| <alternative 2> | | | |

**Why it wins (and when it doesn't).** The honest trade-off. Name the conditions under which an alternative is the better call.

**Problem it solves → value added.** The failure mode you hit without it, and the measurable value (latency, cost, accuracy, safety, developer time) you get with it. Use the capstone as the example.

**In the capstone.** The file(s), function(s), or command(s) in this repo that implement it, e.g. `internal/rag/chunk.go → Chunker.Recursive`, `make ingest`. If a node is conceptual (no code), say so and point to the ADR or lab that exercises it.

## Lab

Step-by-step, runnable from a clean clone. Commands first, expected output second, explanation third. Use `make` targets and the `copilot` CLI. The mock provider must make the lab runnable with no API keys; a "with real models" variant follows.

## Production notes

What changes between the lab and production: scale, cost controls, failure modes, observability, security. Bullet list is acceptable here.

## Check your understanding

Five questions, design-review style ("You are asked to… what do you choose and why?"), with short answers in a collapsed `<details>` block.

## References

Primary sources only (official docs, papers, vendor pricing pages). No tutorials.
