# ADR-0002 · Go for the capstone, Python for the labs

**Status:** Accepted · **Date:** 2025-09 · **Owner:** Udaykishore Resu · **Modules:** all; labs in
02, 03, 05, 06, 07, 08

## Context

The roadmap.sh AI Engineer roadmap is language-neutral, but its ecosystem is not: the reference
SDKs, LangChain, LlamaIndex, sentence-transformers, `transformers`, fine-tuning scripts and most
evaluation tooling are Python-first, and several roadmap nodes (Sentence Transformers, LangChain,
LlamaIndex, Hugging Face Inference SDK, fine-tuning) are effectively Python library nodes. A course
that ignored that would misrepresent the field.

At the same time, the capstone is a *platform/SRE copilot*: it reads Kubernetes manifests and
Terraform, calls `kubectl` read-only, queries Prometheus, and is meant to run as a sidecar or
internal service inside infrastructure that is overwhelmingly written in Go. The author's background
and the target audience — experienced backend and platform engineers moving into AI engineering —
are Go and Kubernetes people. The product value of the capstone to that audience depends on it
fitting their operational world: a single static binary, context propagation, structured logging,
predictable memory, and no Python runtime on the node. Recruiters and hiring managers reading the
repo will also weigh whether the author can build production-grade systems in the language their
platform teams use.

There is also a pedagogical force. Writing the RAG pipeline, the agent loop and the safety layer
*without* a framework — which Go forces, because the frameworks are thin or absent — exposes the
mechanism the roadmap is trying to teach. Using LangChain for everything would teach LangChain.

## Decision

- The capstone (`cmd/copilot`, `internal/*`) is written in Go with the standard library plus a
  minimal set of dependencies (HTTP client, JSON, a JSON-Schema validator, a tokenizer
  approximation). It is the system of record for every roadmap node that has a code implementation.
- Python is used in `labs/python/` for nodes whose substance is a Python library or a hosted
  workflow best demonstrated in Python: Hugging Face Inference and `transformers`,
  sentence-transformers, LangChain and LlamaIndex (RAG and multimodal), the Assistants API, OpenAI
  fine-tuning jobs, and the Python side-by-side versions of the agent and safety suites. Every lab
  is a single file, runnable from a clean virtualenv, and skips gracefully with no keys.
- A small JavaScript lab (`labs/js/`) covers Transformers.js because that node is browser-specific.
- Where the same idea exists in both (the agent loop, the injection suite, the vision reading), the
  Python lab mirrors the Go structure so the comparison is the lesson.

## Alternatives considered

| Option | Why not |
|---|---|
| **Everything in Python** (FastAPI service, LangChain/LlamaIndex throughout) | Fastest path to a demo and closest to the ecosystem, but the capstone would not be credible as platform infrastructure to its audience, and framework abstractions would hide the mechanisms the course must teach. The author's differentiation — production Go systems — would be invisible. |
| **Everything in Go, no Python** | Honest about the author's stack but dishonest about the field: several roadmap nodes cannot be covered faithfully (LangChain, LlamaIndex, sentence-transformers, fine-tuning scripts) and the "Continue learning" tracks assume Python literacy. Also forfeits the side-by-side comparisons that make the design decisions legible. |
| **TypeScript for the capstone** | Strong SDK support (OpenAI, Anthropic, LangChain.js) and a plausible choice for many AI engineers, but a poor fit for a `kubectl`/Prometheus sidecar and for the target audience. |
| **Go capstone with Python microservices for ML pieces** (e.g. an embedding sidecar) | Reasonable in production for local embedding models, but splits the capstone into two runtimes and complicates "runs from a clean clone with no keys". Kept as a documented production option in Module 03 (Ollama covers the local-embedding need without Python). |
| **Rust** | Performance is not the bottleneck (the model call is), and the audience is Go-first. |

## Consequences

**Positive.**
- The capstone is a single binary with `make build`; `COPILOT_PROVIDER=mock` runs everything
  offline. It looks like the infrastructure its users already run.
- Mechanisms are visible: the chunker, the BM25 scorer, the RRF fusion, the ReAct parser, the
  injection heuristics and the output guard are each a short Go file a reviewer can read top to
  bottom.
- Python labs cover the ecosystem nodes faithfully and let the reader judge the frameworks against
  the hand-rolled versions with the same inputs.
- Hiring signal: the repo demonstrates both production Go engineering and fluency with the Python AI
  ecosystem, with explicit reasoning about when to use which.

**Negative / costs.**
- Go's AI ecosystem is thin, so the project re-implements things Python gets for free: a tokenizer
  approximation (exact tiktoken parity is out of scope; `internal/tokens.Estimate` is calibrated
  against real usage reports), BM25, RRF, JSON-Schema-constrained output validation, and every
  provider adapter (ADR-0009).
- Two toolchains to maintain: `go test` and a Python virtualenv with pinned requirements. CI runs
  both; the Python labs are smoke-tested in no-key mode only.
- Some capabilities stay Python-only by design — local sentence-transformer embeddings, fine-tuning,
  CLIP-based image retrieval. The Go capstone reaches local models through Ollama instead, which is
  the right production answer for a platform team but means the Go side has no in-process ML.
- Contributors need to read both languages to follow a module end to end. Each module's "In the
  capstone" lines point to the Go path first and the Python lab second to keep the primary thread
  clear.

**Follow-ups.**
- Keep the Python labs dependency-light and version-pinned; re-run them quarterly since LangChain
  and LlamaIndex APIs move.
- If a Go-native embedding path becomes important (air-gapped deployments without Ollama), evaluate
  ONNX Runtime bindings as a separate ADR rather than adding Python to the binary's runtime
  requirements.

