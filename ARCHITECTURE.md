# Architecture

`platform-copilot` is a knowledge copilot for platform / SRE teams. It ingests runbooks, Kubernetes manifests, Terraform, and incident postmortems, answers questions with citations (RAG), runs tool-using agents that can look at a cluster read-only, and understands dashboard screenshots. Every component maps to a section of the **AI Engineer** roadmap — the repo is the roadmap, built.

```mermaid
%%{init: {'theme':'base','themeVariables':{'fontFamily':'Roboto, Helvetica, Arial, sans-serif','lineColor':'#607D8B','textColor':'#263238','clusterBkg':'#FAFAFA','clusterBorder':'#B0BEC5','edgeLabelBackground':'#FFFFFF','primaryColor':'#E8EAF6','primaryTextColor':'#1A237E','primaryBorderColor':'#3F51B5','actorBkg':'#E8EAF6','actorBorder':'#3F51B5','actorTextColor':'#1A237E','signalColor':'#455A64','signalTextColor':'#263238','labelBoxBkgColor':'#E8EAF6','labelBoxBorderColor':'#3F51B5','noteBkgColor':'#FFF8E1','noteBorderColor':'#FFB300','noteTextColor':'#FF6F00'}}}%%
flowchart TD
  CLI["cmd/copilot<br/>ingest · search · ask · chat · agent · vision<br/>transcribe · speak · diagram · models · tokens · embed · moderate · eval · serve"]
  SRV["internal/server<br/>POST /v1/ask · POST /v1/agent · /healthz<br/>end-user IDs · request IDs · audit log"]
  RAG["internal/rag<br/>chunk → embed → upsert<br/>retrieve → assemble → generate"]
  AG["internal/agent<br/>ReAct loop · native tool calling<br/>search_docs · kubectl (read-only) · promql · calc"]
  EMB["internal/embeddings<br/>openai · ollama · cohere · mock"]
  VST[("internal/vectorstore<br/>memory · qdrant · chroma<br/>BM25 + RRF")]
  LLM["internal/llm<br/>Provider → openai · anthropic · gemini · ollama · mock"]
  SAF["internal/safety<br/>injection · PII<br/>moderation · guard"]
  TOK["internal/tokens<br/>estimate · budget<br/>pricing"]
  MM["internal/multimodal<br/>vision · whisper<br/>images · tts · video"]
  EVAL["internal/rag/eval<br/>hit@k · MRR<br/>answer + abstention accuracy"]
  CFG["internal/config<br/>env → Config<br/>mock is the zero-key default"]

  CLI --> RAG
  CLI --> AG
  CLI --> MM
  CLI --> SRV
  CLI --> EVAL
  SRV --> RAG
  SRV --> AG
  RAG --> EMB
  RAG --> VST
  RAG --> LLM
  AG --> LLM
  AG -->|"search_docs"| RAG
  MM --> LLM
  EVAL --> RAG
  SAF -.->|"guards every request and response"| RAG
  SAF -.-> AG
  TOK -.->|"fits the prompt to the window"| RAG
  CFG -.->|"wires every component"| CLI
  classDef entry fill:#E8EAF6,stroke:#3F51B5,stroke-width:2px,color:#1A237E
  classDef core fill:#E0F2F1,stroke:#00897B,stroke-width:2px,color:#004D40
  classDef data fill:#E3F2FD,stroke:#1E88E5,stroke-width:2px,color:#0D47A1
  classDef model fill:#F3E5F5,stroke:#8E24AA,stroke-width:2px,color:#4A148C
  classDef safety fill:#FBE9E7,stroke:#FF5722,stroke-width:2px,color:#BF360C
  classDef ext fill:#ECEFF1,stroke:#607D8B,stroke-width:2px,color:#263238
  classDef out fill:#E8F5E9,stroke:#43A047,stroke-width:2px,color:#1B5E20
  class CLI,SRV entry
  class RAG,AG,EVAL core
  class EMB,LLM,MM model
  class VST data
  class SAF safety
  class TOK,CFG ext
```

## Package contract

| Package | Responsibility | Key types / functions | Roadmap section |
|---|---|---|---|
| `cmd/copilot` | CLI entry point; one subcommand per capability | `main.go` | all |
| `internal/config` | Environment-driven configuration; `COPILOT_PROVIDER=mock` is the zero-key default | `Load() Config` | Development Tools |
| `internal/llm` | One `Provider` interface over every chat model | `Provider`, `Request`, `Response`, `Message`, `Tool`, `ToolCall`, `Usage`; `New(cfg)`; `openai.go`, `anthropic.go`, `gemini.go`, `ollama.go`, `mock.go` | Pre-trained Models, OpenAI Platform, Open-Source AI |
| `internal/embeddings` | One `Embedder` interface; cosine/dot/euclidean | `Embedder`, `Cosine`, `openai.go`, `ollama.go`, `mock.go` (deterministic hashed bag-of-words so the mock still ranks semantically-ish) | Embeddings |
| `internal/vectorstore` | `Store` interface; in-memory brute-force with JSON persistence; Qdrant and Chroma HTTP adapters; BM25 + reciprocal-rank fusion for hybrid search | `Store`, `Document`, `Hit`, `Memory`, `Qdrant`, `Chroma`, `BM25`, `RRF` | Vector Databases |
| `internal/rag` | Chunkers (fixed, recursive, markdown-aware), ingestion walker, retriever with threshold, citation-aware prompt assembly, pipeline | `Chunker`, `Ingest`, `Retriever`, `BuildPrompt`, `Pipeline.Ask` | RAG & Implementation |
| `internal/tokens` | Token estimation, context-window budgeting, per-model pricing | `Estimate`, `Budget.Fit`, `Price`, `Cost` | OpenAI API (tokens, pricing) |
| `internal/agent` | ReAct loop (text protocol) and native tool-calling loop; tool registry; read-only cluster tools | `Agent.Run`, `Tool`, `Registry`, `SearchDocs`, `KubectlGet`, `PromQL`, `Calc` | AI Agents |
| `internal/safety` | Prompt-injection heuristics, PII redaction, moderation (OpenAI API + local fallback), input/output constraints, JSON-schema output guard | `DetectInjection`, `RedactPII`, `Moderate`, `Guard` | AI Safety & Ethics |
| `internal/multimodal` | Image → vision request, Whisper transcription, image generation, TTS | `ImagePart`, `Transcribe`, `GenerateImage`, `Speak` | Multimodal AI |
| `internal/server` | HTTP API (`/v1/ask`, `/v1/agent`, `/healthz`) with end-user IDs passed through to providers | `New(deps).ListenAndServe` | Safety (end-user IDs), Dev Tools |
| `labs/python` | Hugging Face Inference, sentence-transformers, LangChain and LlamaIndex RAG, fine-tuning job script | `*.py` | Open-Source AI, RAG, Fine-tuning |
| `labs/js` | Transformers.js in-browser embeddings | `index.html` | Open-Source AI |
| `modules/` | The course: one module per roadmap section, one concept card per node | `00-…` to `09-…` | all |
| `adr/` | Architecture Decision Records for the big forks | `ADR-000x-*.md` | all |

## Request flow — `copilot ask "why is the payments pod CrashLooping?"`

```mermaid
%%{init: {'theme':'base','themeVariables':{'fontFamily':'Roboto, Helvetica, Arial, sans-serif','lineColor':'#607D8B','textColor':'#263238','clusterBkg':'#FAFAFA','clusterBorder':'#B0BEC5','edgeLabelBackground':'#FFFFFF','primaryColor':'#E8EAF6','primaryTextColor':'#1A237E','primaryBorderColor':'#3F51B5','actorBkg':'#E8EAF6','actorBorder':'#3F51B5','actorTextColor':'#1A237E','signalColor':'#455A64','signalTextColor':'#263238','labelBoxBkgColor':'#E8EAF6','labelBoxBorderColor':'#3F51B5','noteBkgColor':'#FFF8E1','noteBorderColor':'#FFB300','noteTextColor':'#FF6F00'}}}%%
sequenceDiagram
  autonumber
  participant U as Engineer
  participant C as cmd/copilot
  participant S as safety.Guard
  participant E as embeddings.Embedder
  participant V as vectorstore.Store
  participant T as tokens.Budget
  participant L as llm.Provider
  U->>C: copilot ask "why is payments-api crashlooping?"
  C->>S: CheckInput(question)
  S-->>C: redacted question (or blocked)
  C->>E: Embed(question)
  E-->>C: query vector
  C->>V: Search(vector, k, filter)
  V-->>C: top-k chunks (+ BM25 fused by RRF)
  C->>T: fit system + context + history to the window
  T-->>C: kept chunks, trimmed history
  C->>L: Complete(messages, user=end-user ID)
  L-->>C: answer + usage
  C->>S: CheckOutput(answer)
  S-->>C: redacted answer (or withheld)
  C-->>U: answer with [n] citations, tokens, cost, latency
```

The same flow in words, with what each step actually calls:

1. `safety.DetectInjection` + `safety.RedactPII` screen the question.
2. `embeddings.Embedder.Embed` turns the question into a vector (mock: hashed bag-of-words; real: `text-embedding-3-small`, `nomic-embed-text`, …).
3. `vectorstore.Store.Search` returns the top-k chunks; when hybrid search is on, BM25 hits are fused with RRF.
4. `rag.BuildPrompt` assembles system + context (numbered `[1]`, `[2]` citations) + question, trimmed by `tokens.Budget` to the model's context window.
5. `llm.Provider.Complete` calls the configured model with the request's `User` (end-user ID) attached.
6. `safety.Guard` checks the output (length, banned patterns, optional JSON schema) and the CLI prints the answer with its sources and the estimated cost.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `COPILOT_PROVIDER` | `mock` | `mock` · `openai` · `anthropic` · `gemini` · `ollama` |
| `COPILOT_MODEL` | provider default | chat model id |
| `COPILOT_EMBED_PROVIDER` | `mock` | `mock` · `openai` · `ollama` |
| `COPILOT_EMBED_MODEL` | provider default | embedding model id |
| `COPILOT_VECTORSTORE` | `memory` | `memory` · `qdrant` · `chroma` |
| `COPILOT_INDEX_PATH` | `.copilot/index.json` | persistence for the in-memory store |
| `COPILOT_HYBRID` | `true` | fuse BM25 with vector search |
| `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` / `GEMINI_API_KEY` | — | provider keys |
| `OLLAMA_HOST` | `http://localhost:11434` | local models |
| `QDRANT_URL` / `CHROMA_URL` | `http://localhost:6333` / `http://localhost:8000` | external stores |
