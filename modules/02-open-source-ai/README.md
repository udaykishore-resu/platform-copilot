# Module 02 · Open-Source AI

> **Roadmap nodes covered:** Open vs Closed Source Models, Popular Open Source Models, Hugging Face, Finding Open Source Models, Hugging Face Tasks, Hugging Face Hub, Using Open Source Models, Inference SDK, Transformers.js, Ollama, Ollama Models, Ollama SDK
>
> **Capstone step:** Add the local provider path: `internal/llm/ollama.go` behind `COPILOT_PROVIDER=ollama` and `internal/embeddings/ollama.go` behind `COPILOT_EMBED_PROVIDER=ollama`, both reading `OLLAMA_HOST`. The whole copilot (`ingest`, `ask`, `search`, `agent`, `chat`) now runs with no external API and no key. Labs: `labs/python/02_hf_inference.py`, `02_transformers_local.py`, `02_ollama_sdk.py`, and `labs/js/transformers-js/index.html`.
>
> **Time:** ~5 hours · **Prerequisites:** Module 01

## Why this module exists

After Module 01 the copilot talks to three frontier vendors, which is exactly the point at which a platform team's security review asks the question that decides the project: does any of this have to leave the building? Postmortems name people and systems. Runbooks contain hostnames and sometimes credentials that should not be there but are. Terraform state references account IDs. An engineer who can only answer "the vendor's terms say they don't train on it" has not answered the question. An engineer who can say "set two environment variables and it runs on our own GPU node, here is the accuracy difference on our eval set" has.

This module builds that answer. Open-weights models from Meta, Mistral, Alibaba, Google and others can be pulled and served locally with Ollama in one command; Hugging Face is where those models, their licences and their evaluation results live; the `transformers` library runs them in-process; Transformers.js runs small ones in the browser. Because Module 01 put every model behind `internal/llm.Provider` and every embedder behind `internal/embeddings.Embedder`, adding the local path is two adapter files and no change to retrieval, prompting or the agent.

The module is also about honesty. Open models are not free: someone pays for the GPU, the upgrades and the quality gap to the frontier. The concept cards compare each choice against its hosted alternative and the lab measures the difference on the same questions, so the recommendation to a CTO is a number, not a preference.

## Concept cards

### Open vs Closed Source Models

**What it is.** A closed model is available only through a vendor's API; its weights, training data and architecture details are private. An "open" model publishes its weights for download under a licence; a smaller set also publishes training data and code. Licences vary materially: some are permissive (Apache 2.0, MIT), some are custom with use restrictions or user-count thresholds (Meta's Llama licence), some are research-only. "Open weights" is the accurate term for most of what is called open source in this field, because the training data and recipe are usually not released.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Open-weights model, self-hosted | Data never leaves; no per-token fee; no deprecation without consent; inspectable and fine-tunable locally | Quality gap to frontier at the same task; GPU cost whether busy or idle; you own security patches and upgrades; licence review | Data residency, air-gapped environments, very high sustained volume, or a need to modify the model |
| Closed model via API | Frontier quality; zero infrastructure; continuous improvement | Data leaves the network; vendor controls price and lifecycle | Default when policy allows |
| Open-weights model via a hosted provider (Together, Fireworks, Bedrock, Azure) | Open-model economics without running GPUs; often the cheapest tokens available | Data still leaves; provider-specific quantisation and behaviour | Cost optimisation without residency constraints |

**Why it wins (and when it doesn't).** Open weights win when the constraint is where the data can go or who controls the lifecycle. They lose when the task needs the last ten points of reasoning quality and the team does not want to run GPUs. The right frame is not ideological; it is a row in an ADR with the eval-set accuracy and the monthly cost for each option.

**Problem it solves → value added.** The capstone's default provider is hosted because it is faster to build with; the Ollama path exists so the same binary can be deployed into an environment where postmortems cannot leave, with a measured and accepted accuracy difference.

**In the capstone.** ADR on provider choice in `adr/`; `COPILOT_PROVIDER=ollama` and `COPILOT_EMBED_PROVIDER=ollama` as the fully local configuration.

### Popular Open Source Models

**What it is.** The open-weights landscape is organised by family and size. Meta's Llama series (instruction-tuned chat models from a few billion to several hundred billion parameters), Mistral's dense and mixture-of-experts models, Alibaba's Qwen series (strong multilingual and coding variants), Google's Gemma (small, efficient), DeepSeek's reasoning-focused models, and Microsoft's Phi (small models trained on curated data). For embeddings: `nomic-embed-text`, BAAI's `bge` family, `all-MiniLM` and `e5`. Size is measured in parameters; a 7 to 9 billion parameter model in 4-bit quantisation fits in about 5 GB of memory and runs usefully on a laptop; 70 billion needs a serious GPU.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Small model (3 to 9B) locally | Runs on a laptop or a small GPU; fast; fine for RAG answers over good context and for classification | Weaker multi-step reasoning and tool use; shorter practical context | Development, edge, RAG with strong retrieval |
| Large open model (70B and up) on a GPU node | Approaches frontier on many tasks | Expensive hardware; slower | Production self-hosting where quality matters |
| Frontier closed model | Best reasoning and agent behaviour | Hosted only | Agent planning |

**Why it wins (and when it doesn't).** For the capstone, a small open model answers runbook questions well because retrieval does the hard part; it struggles as the agent planner, where tool-argument errors rise. The honest configuration is a local small model for `ask` and a hosted or large model for `agent`, unless policy forbids hosted entirely.

**Problem it solves → value added.** Knowing the families and sizes lets the team choose a model that fits the GPU it actually has, rather than discovering at deploy time that the chosen model does not fit in memory.

**In the capstone.** `ollama pull` targets in the lab; `COPILOT_MODEL` when `COPILOT_PROVIDER=ollama`; embedding model in `COPILOT_EMBED_MODEL`.

### Hugging Face

**What it is.** Hugging Face is the company and community hub at the centre of open machine learning: the Hub (models, datasets, demo Spaces), the `transformers` library (a unified Python API for running and training models), `datasets`, `tokenizers`, `sentence-transformers` (maintained in collaboration), the hosted Inference API and dedicated Inference Endpoints, and Transformers.js for the browser. Model cards on the Hub document licence, intended use, training data and evaluation results.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Hugging Face ecosystem | One place for discovery, licences, weights, libraries and hosted inference; standard model format | Quality varies; you must read model cards; Python-centric | Any open-model work |
| Vendor-specific model zoos (NVIDIA NGC, cloud catalogues) | Curated and optimised for one platform | Narrow; lock-in | Deploying on that platform |
| Ollama library | Curated, one-command local use | Smaller catalogue; chat and embedding focus | Local development |

**Why it wins (and when it doesn't).** The Hub is the source of truth for open models even when you run them elsewhere; Ollama's models are repackaged Hub weights. For production serving of LLMs, dedicated servers such as vLLM or Ollama are usually preferred to raw `transformers`.

**Problem it solves → value added.** The capstone's embedding and classification models are chosen by reading model cards and leaderboards on the Hub, which is how the team knows the licence permits commercial use and how the model ranks on retrieval benchmarks before adopting it.

**In the capstone.** `labs/python/02_hf_inference.py`, `02_transformers_local.py`; embedding model selection in Module 03.

### Finding Open Source Models

**What it is.** Finding a model on the Hub is a filtering exercise: by task (text generation, feature extraction for embeddings, zero-shot classification, summarisation), by library (`transformers`, `sentence-transformers`, `gguf` for Ollama and llama.cpp), by licence, by language, by size, and by popularity (downloads and likes). Leaderboards (the MTEB embedding benchmark, open LLM leaderboards) rank models on standardised tasks. The model card is read last but decides: licence, intended use, known limitations, evaluation table, and whether the weights are the original or a quantised derivative.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Hub search plus leaderboards plus model card | Transparent, comparable, licence-aware | Leaderboards can be gamed; benchmarks are not your task | Selecting a candidate set |
| Evaluate candidates on your own eval set | Measures what matters | Effort per candidate | Final choice, always |
| Take a popular default from a blog post | Fast | Often stale or wrong for your task or licence | Never for production |

**Why it wins (and when it doesn't).** Leaderboards narrow the field to three or four candidates; the capstone's evaluation set picks the winner. A model at the top of a general leaderboard can lose to a smaller one on runbook retrieval because the corpus mixes YAML and prose.

**Problem it solves → value added.** Choosing an embedding model by MTEB retrieval score and then validating on the copilot's question set found a model two-thirds the size of the default with equal recall, halving embedding latency at ingest.

**In the capstone.** Conceptual; the selection procedure is documented in Module 03 with `copilot embed` used to compare models.

### Hugging Face Tasks

**What it is.** The Hub organises models by task, a standard vocabulary that also defines the input and output shape: text generation (chat), text2text generation, summarisation, question answering (extractive), zero-shot classification (label a text against arbitrary candidate labels using an entailment model), token classification (named entities), feature extraction (embeddings), sentence similarity, automatic speech recognition, image classification, object detection, image-to-text, and more. Each task has a `pipeline()` in `transformers` and a method on the Inference client.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Task-specific small model (zero-shot classifier, NER, summariser) | Milliseconds; runs on CPU; deterministic; cheap at scale | Fixed task; no reasoning | High-volume structured steps: classify every alert, extract hostnames |
| Prompt a general LLM to do the task | Flexible; handles edge cases and explanation | Seconds and cents per call | Low volume or when judgment is needed |
| Rules and regex | Exact, free | Brittle on language | Formats you control |

**Why it wins (and when it doesn't).** The task vocabulary is how an AI engineer spots the parts of a pipeline that do not need an LLM. Classifying incoming alerts into "needs-runbook", "known-noise", "escalate" is a zero-shot classification task that a 400 MB model does on CPU for free; sending every alert to a frontier model for that is waste.

**Problem it solves → value added.** In the capstone, routing alerts with a zero-shot classifier before invoking the LLM cut LLM calls for the alert path by roughly the share of alerts that are known noise, with no loss of quality on the ones that matter.

**In the capstone.** `labs/python/02_hf_inference.py` (text generation, summarisation, zero-shot classification of an alert); `labs/python/02_transformers_local.py`.

### Hugging Face Hub

**What it is.** The Hub is a Git-backed repository host for models, datasets and Spaces, with an HTTP API and the `huggingface_hub` Python library for searching, downloading (with a local cache), uploading and running hosted inference. Repositories have revisions (branches and commits), so a model can be pinned to an exact commit. Gated models require accepting a licence with a logged-in account and a token; private repositories and organisations support team use. Downloads are content-addressed and cached under `~/.cache/huggingface` by default.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Hub with pinned revisions | Reproducible; versioned; shared cache | Large downloads; token management for gated models | Any production use of open models |
| Vendor model registry (SageMaker, Vertex, MLflow) | Integrated with deployment and audit | Another system; usually seeded from the Hub | Organisations with an ML platform |
| Copy weights into an artifact store yourself | Full control; air-gap friendly | You rebuild provenance and caching | Air-gapped clusters |

**Why it wins (and when it doesn't).** Pinning a model to a Hub revision is the equivalent of pinning a container image by digest and is non-negotiable for reproducible evaluation. For air-gapped deployments, mirror the pinned revision into an internal registry.

**Problem it solves → value added.** An unpinned embedding model updated upstream would silently invalidate every vector in the store; pinning the revision and recording it alongside the index prevents mixed-model vectors.

**In the capstone.** `labs/python/02_hf_inference.py` uses `huggingface_hub.InferenceClient`; Module 03 notes that `internal/vectorstore.Memory` rejects a query whose vector dimension differs from the index, so changing the embedding model means re-running `copilot ingest`.

### Using Open Source Models

**What it is.** There are four ways to run an open model: in-process with `transformers` (load weights into a Python process; simplest for experiments and task-specific small models), through a dedicated inference server (Ollama for local and small deployments, vLLM or TGI for throughput on GPUs; both expose HTTP APIs, often OpenAI-compatible), through a hosted provider of open models, or in the browser with Transformers.js. Quantisation (reducing weights from 16-bit to 8- or 4-bit, for example the GGUF format used by Ollama) cuts memory and increases speed at a small quality cost.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Ollama (local server) | One command to pull and serve; GGUF quantisation; OpenAI-compatible endpoint; Go-friendly HTTP API | Single-node; modest concurrency | Development, small teams, edge |
| vLLM / TGI on Kubernetes GPU nodes | High throughput with continuous batching and paged attention; production-grade | GPU sizing; operational burden | Production self-hosting at volume |
| `transformers` in-process | Full flexibility; every task type | Python process; no batching across requests; slow for chat | Labs, small task models, research |
| Transformers.js in the browser | Zero server cost; data stays on the device | Small models only; first-load download | Client-side embeddings and classification |

**Why it wins (and when it doesn't).** The capstone uses Ollama because a Go service talking HTTP to a local server is the simplest robust shape and Ollama runs anywhere from a laptop to a GPU node pool. At production volume the same `Provider` adapter points at vLLM's OpenAI-compatible endpoint instead.

**Problem it solves → value added.** A serving layer turns "we have weights" into "we have an API with a stable contract", which is what lets `internal/llm/ollama.go` stay under a few hundred lines and lets the copilot run with no internet connection.

**In the capstone.** `internal/llm/ollama.go`, `internal/embeddings/ollama.go`, `OLLAMA_HOST`; `labs/python/02_transformers_local.py`.

### Inference SDK

**What it is.** The Hugging Face Inference SDK (`huggingface_hub.InferenceClient` in Python, `@huggingface/inference` in JavaScript) is a client for running models without downloading them: it calls the hosted serverless Inference API for popular models (rate-limited, free tier available), dedicated Inference Endpoints you deploy, or third-party inference providers routed through the Hub. The same client object exposes task methods (`text_generation`, `chat_completion`, `summarization`, `zero_shot_classification`, `feature_extraction`), so swapping the model is one string.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| `InferenceClient` against serverless API | No infrastructure; try any supported model in one line | Rate limits; cold starts; not every model is served; availability varies | Prototyping, low volume |
| `InferenceClient` against a dedicated endpoint | Predictable latency and capacity | Hourly billing | Production on one open model without your own GPUs |
| Local `transformers` or Ollama | Private; no limits | You run it | Private or high-volume |

**Why it wins (and when it doesn't).** The Inference SDK is the fastest way to try three candidate models for alert classification in an afternoon. It is not the capstone's production path because the data would leave the network and the serverless tier is not an SLO.

**Problem it solves → value added.** Trying a zero-shot classifier on real alert text before committing to download and host it saves the hour of setup for each candidate that turns out to be wrong for the task.

**In the capstone.** `labs/python/02_hf_inference.py`.

### Transformers.js

**What it is.** Transformers.js is a JavaScript port of the `transformers` pipeline API that runs ONNX-converted models in the browser or in Node using WebAssembly or WebGPU. Models are downloaded from the Hub on first use and cached by the browser. It is practical for small models: embeddings (`all-MiniLM-L6-v2` is about 23 MB quantised), classification, small speech and vision models. It needs no server, no build step when loaded from a CDN as an ES module, and no data ever leaves the device.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Transformers.js in-browser | Zero inference cost; privacy; works offline after first load | Model download on first use; limited to small models; CPU/GPU of the user's device | Client-side ranking, dedup and classification of text the user already has |
| Call a server-side embedding API | Any model size; consistent results | Server cost; round trip; data leaves the device | Indexing a corpus |
| ONNX Runtime Web directly | Full control | More code | Custom models |

**Why it wins (and when it doesn't).** For a dashboard that shows a hundred alerts and wants to group near-duplicates instantly, in-browser embeddings beat a server round trip and cost nothing per user. For indexing the runbook corpus, a server-side embedder is correct because the corpus is large and results must be consistent across users.

**Problem it solves → value added.** The lab ranks alert messages by similarity to a query entirely in the browser; the same technique dedupes an alert storm on the client before anything is sent to the copilot's `ask` endpoint, reducing server load and token spend.

**In the capstone.** `labs/js/transformers-js/index.html`.

### Ollama

**What it is.** Ollama is a local model server: a single binary that downloads quantised GGUF models from its library (or from the Hub), manages them on disk, loads them on demand into CPU or GPU memory, and serves an HTTP API on port 11434 with endpoints for chat, generation and embeddings, plus an OpenAI-compatible `/v1` surface. It handles model lifecycle (keep-alive, unload after idle), context length configuration, and concurrent requests within one node. It runs on macOS, Linux and Windows and as a container, which makes it a natural Kubernetes workload on a GPU node pool.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Ollama | Simplest local serving; one command per model; stable HTTP API; small footprint; Kubernetes-friendly | Single-node; throughput below vLLM under heavy concurrency; fewer serving knobs | Development, small teams, edge and residency-constrained deployments |
| vLLM | Highest throughput on GPUs; production features | Heavier; GPU required; more configuration | Production volume |
| llama.cpp server directly | Minimal; what Ollama wraps | You manage models and formats | Embedded or constrained environments |
| LM Studio / desktop apps | GUI | Not a service | Personal exploration |

**Why it wins (and when it doesn't).** Ollama is the capstone's local provider because it runs the same way on a developer laptop and on an EKS GPU node, so the fully offline configuration is tested every day by developers, not discovered at deployment. Under sustained multi-user load, migrate to vLLM behind the same adapter.

**Problem it solves → value added.** With Ollama installed, `go test ./...` and the full lab run with no key and no internet, and the security review's question has a demonstrable answer.

**In the capstone.** `internal/llm/ollama.go` (chat endpoint with tool support, non-streaming), `internal/embeddings/ollama.go` (embeddings endpoint), `OLLAMA_HOST`, `COPILOT_PROVIDER=ollama`.

### Ollama Models

**What it is.** Ollama's library is a curated catalogue of open models in GGUF format with tags for size and quantisation (for example a `:8b` tag for an 8-billion-parameter variant, with quantisation suffixes such as `q4_K_M`), including chat models (Llama, Mistral, Qwen, Gemma, Phi, DeepSeek), coding models, vision-capable models, and embedding models (`nomic-embed-text`, `mxbai-embed-large`, `all-minilm`). A `Modelfile` lets you derive a model with a custom system prompt, parameters (context length, temperature) or a different quantisation, and Ollama can import GGUF files from the Hub directly.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Library default tag (usually 4-bit quantised mid-size) | Best speed/memory/quality balance for most hardware | Slight quality loss vs full precision | Default |
| Higher-precision tag (8-bit or fp16) | Closer to original quality | Two to four times the memory; slower | GPU memory is plentiful and quality is measured to matter |
| Custom `Modelfile` with baked system prompt | Shorter requests; consistent behaviour | Another artefact to version | Fixed-role deployments |
| Import a Hub GGUF | Any model | You check licence and format | Model not in the library |

**Why it wins (and when it doesn't).** Pick the model by the memory you have and the task: an 8B chat model and a small embedding model fit comfortably on a 16 GB laptop; a 70B model needs a 48 GB or larger GPU. Measure on the eval set before choosing a higher-precision tag; often the difference is within noise for RAG answers.

**Problem it solves → value added.** Pinning the full tag (`name:size-quant`) in `COPILOT_MODEL` and `COPILOT_EMBED_MODEL` makes the local configuration reproducible across laptops and the cluster, which is what lets evaluation results be compared.

**In the capstone.** `ollama pull llama3.2`, `ollama pull nomic-embed-text` in the lab; `COPILOT_MODEL`, `COPILOT_EMBED_MODEL`.

### Ollama SDK

**What it is.** Ollama ships official client libraries for Python (`ollama`) and JavaScript (`ollama`) that wrap the HTTP API: `chat` (messages with roles, tool definitions, streaming), `generate`, `embed` (batch embeddings), `pull`, `list`, `show`. Community clients exist for Go, and the API is small enough that the capstone talks to it with `net/http` directly. Because Ollama also exposes an OpenAI-compatible endpoint, the OpenAI SDKs work against it by changing the base URL.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Official `ollama` SDK (Python/JS) | Exposes Ollama-specific features (model management, keep-alive, options) | Ollama-only | Scripts and labs that manage models |
| OpenAI SDK pointed at Ollama's `/v1` | One client for hosted and local | Misses Ollama-specific options; tool-call support depends on model | Applications that already speak OpenAI's shape |
| Raw HTTP (what the Go capstone does) | No dependency; exact control; trivial in Go | You write the JSON types | Go services |

**Why it wins (and when it doesn't).** In Go, the native `/api/chat` and `/api/embed` endpoints are a few structs; the adapter is clearer than routing through OpenAI compatibility and keeps Ollama-specific options (context length, keep-alive) available. In Python the official SDK is the right call for labs and tooling.

**Problem it solves → value added.** The SDK lab demonstrates chat and embeddings against the same local server the Go copilot uses, which lets a developer confirm a model works before debugging the Go adapter.

**In the capstone.** `labs/python/02_ollama_sdk.py`; the Go equivalent is `internal/llm/ollama.go` and `internal/embeddings/ollama.go`.

## Lab

**1. Install Ollama and pull a chat model and an embedding model.**

```bash
# macOS: brew install ollama ; Linux: curl -fsSL https://ollama.com/install.sh | sh
ollama serve &            # skip if running as a service
ollama pull llama3.2
ollama pull nomic-embed-text
ollama list
```

Expected: `ollama list` shows both models with sizes (the chat model around 2 GB, the embedding model under 300 MB).

**2. Run the copilot fully offline.**

```bash
go build -o bin/copilot ./cmd/copilot
export COPILOT_PROVIDER=ollama COPILOT_MODEL=llama3.2
export COPILOT_EMBED_PROVIDER=ollama COPILOT_EMBED_MODEL=nomic-embed-text
./bin/copilot models
./bin/copilot ingest ./data/knowledge
./bin/copilot ask "why is the payments pod CrashLooping and what is the first thing to check?"
```

Expected: `models` prints the price table with an `Active: provider=ollama model=llama3.2 embed=ollama/nomic-embed-text store=memory` line. `ingest` re-embeds the corpus (the store refuses to search across a dimension mismatch, so this rewrites `.copilot/index.json`). `ask` returns a cited answer with a trailer `model=ollama/llama3.2 … cost=$0 (local/mock)`; latency depends on hardware, typically a few seconds on a laptop.

`internal/llm/ollama.go` posts to `/api/chat` with the message list and optional tools (`stream: false`); `internal/embeddings/ollama.go` posts to `/api/embed` in batches. Nothing else in the pipeline changed.

**3. Compare with the hosted answer.**

```bash
COPILOT_PROVIDER=openai OPENAI_API_KEY=sk-... ./bin/copilot ask "why is the payments pod CrashLooping and what is the first thing to check?"
```

Expected: the same citations (retrieval still uses the local embedder unless you change `COPILOT_EMBED_PROVIDER`) with a typically more polished explanation. Record both answers; Module 09 scores them.

**4. Try the agent locally.**

```bash
./bin/copilot agent "is anything in the payments namespace restarting, and does a runbook cover it?"
```

Expected: a trace of tool calls (`kubectl_get`, `search_docs`) and a final answer. With a small model, watch for malformed tool arguments; this is the measured trade-off discussed in the "Popular Open Source Models" card. If no cluster is available, the `kubectl_get` tool reports that and the agent falls back to the documents.

**5. Python labs.**

```bash
cd labs/python && source .venv/bin/activate   # created in Module 01
export HF_TOKEN=hf_...                          # optional; raises rate limits and unlocks gated models
python 02_hf_inference.py
python 02_transformers_local.py
python 02_ollama_sdk.py
```

Expected: `02_hf_inference.py` prints a generated explanation, a summary of a runbook excerpt, and a zero-shot classification of an alert into labels with scores, skipping gracefully if the hosted API is unavailable. `02_transformers_local.py` downloads a small model on first run and runs the same classification and summarisation tasks on CPU. `02_ollama_sdk.py` chats with the local model and prints embedding dimensions and a cosine similarity between two alert strings.

**6. Browser lab.**

```bash
cd labs/js/transformers-js
python -m http.server 8080
# open http://localhost:8080
```

Expected: the page downloads a small embedding model on first load (progress shown), embeds a list of alert messages, and ranks them by cosine similarity to whatever query you type, entirely in the browser. Open the network tab to confirm no request leaves the page after the model is cached.

## Production notes

- Run Ollama (or vLLM) as a Deployment on a GPU node pool with a PersistentVolume for the model cache; pull models in an init container so a pod restart does not re-download gigabytes. Set resource requests to the model's memory footprint plus headroom.
- Pin model tags and Hub revisions exactly and record them in the index metadata; changing the embedding model requires a full re-ingest and the store should refuse mixed vectors.
- Ollama has no authentication by default; keep it on a cluster-internal Service with NetworkPolicy restricting ingress to the copilot, and never expose port 11434 beyond the cluster.
- Measure, do not assume, the quality gap: run the evaluation set against the local and hosted configurations on every model upgrade and publish the numbers with the cost comparison.
- Throughput: a single Ollama node serialises heavily under concurrency; for multi-user production move to vLLM with continuous batching, keeping the `Provider` adapter and changing only the endpoint.
- Licences are part of the dependency review: record the licence of every model in the ADR and check the terms on upgrade; some families change licence between versions.
- Browser-side models ship weights to every user; keep them small, version the model URL, and provide a server fallback for devices without WebAssembly SIMD or WebGPU.
- Supply-chain hygiene applies to model weights: pull from known organisations on the Hub, verify checksums, and mirror into an internal registry for air-gapped deployments.

## Check your understanding

1. Security asks you to guarantee that no postmortem text leaves the company network. What configuration do you ship, what do you re-run, and what do you tell them about accuracy?
2. You are choosing between an 8B local model and a hosted mid-tier model for the `ask` path. What evidence decides it, and which path do you still keep hosted?
3. Every incoming alert is currently sent to a frontier model to decide whether a runbook applies. What do you replace that with and why?
4. An engineer upgrades `nomic-embed-text` on the Ollama node and the copilot's answers get worse overnight. What happened and what prevents it?
5. The product team wants alert deduplication in the web dashboard with no server cost. What do you propose and what is its limit?

<details>
<summary>Answers</summary>

1. `COPILOT_PROVIDER=ollama`, `COPILOT_EMBED_PROVIDER=ollama`, `OLLAMA_HOST` pointing at a cluster-internal Service with NetworkPolicy; re-run `copilot ingest` because vectors from the hosted embedder are not comparable. Report the eval-set accuracy delta versus the hosted configuration as a number and the GPU cost, and note that the agent path degrades more than the RAG path.
2. The evaluation set accuracy and the monthly cost for each, plus latency on the target hardware. For RAG answers over good retrieval the small model is often close enough; the agent planner usually stays on a stronger model because tool-argument errors are expensive.
3. A zero-shot classification model (a few hundred MB, CPU) from the Hub, run locally or via Ollama-adjacent infra, to route alerts into a few labels; only alerts that need explanation go to the LLM. It is milliseconds, free per call, and deterministic.
4. The embedding model changed, so new query vectors live in a different space from the stored vectors. Prevent it by pinning the full model tag in `COPILOT_EMBED_MODEL`, recording the model in index metadata, and having the store refuse queries from a mismatched model with an instruction to re-ingest.
5. Transformers.js with a small sentence-embedding model loaded from a CDN, embedding alerts in the browser and clustering by cosine similarity, as in `labs/js/transformers-js/index.html`. Limits: a first-load download of tens of megabytes, small-model quality, and device CPU; keep a server fallback.

</details>

## References

- Hugging Face Hub documentation: https://huggingface.co/docs/hub
- `huggingface_hub` Inference client: https://huggingface.co/docs/huggingface_hub/guides/inference
- `transformers` pipelines: https://huggingface.co/docs/transformers/main_classes/pipelines
- Hugging Face Tasks: https://huggingface.co/tasks
- Transformers.js documentation: https://huggingface.co/docs/transformers.js
- MTEB leaderboard: https://huggingface.co/spaces/mteb/leaderboard
- Ollama documentation and API reference: https://github.com/ollama/ollama/tree/main/docs and https://github.com/ollama/ollama/blob/main/docs/api.md
- Ollama model library: https://ollama.com/library
- Ollama Python SDK: https://github.com/ollama/ollama-python
- vLLM documentation: https://docs.vllm.ai/
- Meta Llama licence: https://www.llama.com/llama-downloads/ (see licence text per version)
- Mistral AI models and licences: https://docs.mistral.ai/getting-started/models/
- GGUF format specification: https://github.com/ggerganov/ggml/blob/master/docs/gguf.md
