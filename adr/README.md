# Architecture Decision Records

Each ADR records one fork in the design of `platform-copilot` — the context that forced a choice, the decision, the alternatives that were honestly considered, and the consequences the project lives with. They are written to be read in a design review: if you disagree with a decision, the "Alternatives considered" table should already contain your proposal and the reason it lost, or the ADR is incomplete.

Format: **Context / Decision / Alternatives considered / Consequences**, 60–120 lines, status and date in the header. Decisions are consistent with [`ARCHITECTURE.md`](../ARCHITECTURE.md); where the code and an ADR disagree, the ADR is wrong and should be superseded, not silently ignored.

| ADR | Decision | Supersedes / depends on | Modules |
|---|---|---|---|
| [ADR-0001](ADR-0001-provider-agnostic-llm-interface.md) | One `llm.Provider` interface (messages, tools, content parts, usage) over OpenAI, Anthropic, Gemini, Ollama and a full-featured mock | — | 01, 02, 06, 07 |
| [ADR-0002](ADR-0002-go-for-the-capstone-python-for-labs.md) | The capstone is Go with minimal dependencies; Python (and one JS file) for labs whose substance is a Python library or hosted workflow | — | all |
| [ADR-0003](ADR-0003-rag-over-fine-tuning-for-runbook-knowledge.md) | Operational knowledge enters only through retrieval with citations; fine-tuning is for behaviour, not facts | — | 01, 05 |
| [ADR-0004](ADR-0004-in-memory-vector-store-first-qdrant-in-production.md) | Brute-force in-memory `Store` with JSON persistence as default; Qdrant (and Chroma) behind the same interface for production | 0003 | 04, 05 |
| [ADR-0005](ADR-0005-hybrid-retrieval-bm25-plus-vectors.md) | Hybrid retrieval: in-process BM25 fused with vector hits by reciprocal rank fusion, on by default | 0004 | 03, 04, 05 |
| [ADR-0006](ADR-0006-react-text-protocol-plus-native-tool-calling.md) | Two agent loops: native tool calling where the provider supports it, ReAct text protocol as the portable fallback; no hosted Assistants loop | 0001 | 06 |
| [ADR-0007](ADR-0007-read-only-agent-tools-and-allowlists.md) | Agent tools are read-only, allowlisted in code with minimal RBAC, and budgeted; adding a tool requires an ADR | 0006 | 06, 08 |
| [ADR-0008](ADR-0008-safety-layer-before-and-after-the-model.md) | One `internal/safety` package invoked by the pipelines before and after every model call; detection and containment reported separately | 0007 | 05, 06, 07, 08 |
| [ADR-0009](ADR-0009-raw-http-clients-over-vendor-sdks.md) | Provider and store adapters use `net/http` and hand-written structs with golden-file tests, not vendor SDKs | 0001, 0004 | 01, 02, 04, 07 |
| [ADR-0010](ADR-0010-end-user-ids-and-audit-logging.md) | HMAC-hashed end-user IDs on every provider request; a structured, PII-free audit log keyed by them | 0001, 0008 | 08, 09 |

## How the decisions fit together

- **Portability** (0001, 0009) makes every feature run on five providers and with no keys, which is what lets the labs and CI work from a clean clone (0002, Module 09).
- **Knowledge** (0003, 0004, 0005) is retrieval with citations over a store you can run in-process or in Qdrant, with lexical search fused in because the corpus is half YAML and Terraform.
- **Agency** (0006, 0007) uses the best loop per provider but never gives the model a write path; the allowlist, not the prompt, is the control.
- **Safety and accountability** (0008, 0010) screen inputs and validate outputs on every path and make each request attributable to a pseudonymous person without sending PII anywhere.

## Writing a new ADR

1. Copy the header and four-section structure from any existing ADR; number it sequentially.
2. State the forces in *Context* as facts about this product, not generalities.
3. Put every serious alternative in the table with the real reason it lost. If you cannot write that reason, the decision is not ready.
4. List negative consequences honestly; an ADR with only upsides is marketing.
5. Link it from this index and from the module(s) whose concept cards point at it. Anything that adds an agent tool, a new data class to the corpus, a new audience, or a write path requires an ADR before code (see 0007 and Module 08).
