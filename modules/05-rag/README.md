# Module 05 · RAG & Implementation

> **Roadmap nodes covered:** RAG Usecases · RAG vs Fine-tuning · Implementing RAG · Chunking · Embedding · Vector Database · Retrieval Process · Generation · Ways of Implementing RAG · Using SDKs Directly · Langchain · Llama Index · RAG Alternative: OpenAI Assistant API
>
> **Capstone step:** `internal/rag` — `Chunker` (fixed, recursive, markdown-aware) in `chunk.go`, the ingestion walker `Ingester.Ingest`, `Retriever` with score threshold and filters, citation-aware `BuildPrompt` trimmed by `tokens.Budget`, and `Pipeline.Ask` wiring `embeddings.Embedder` → `vectorstore.Store` → `llm.Provider`. Commands: `make ingest`, `copilot ingest`, `copilot ask`, `copilot search`. Python labs: `05_rag_raw_sdk.py`, `05_rag_langchain.py`, `05_rag_llamaindex.py`, `05_openai_assistants_file_search.py`.
>
> **Time:** ~6 hours · **Prerequisites:** Modules 03 (Embeddings) and 04 (Vector Databases)

## Why this module exists

Modules 03 and 04 gave the copilot a way to find the right paragraph. This module makes it answer. Retrieval-Augmented Generation is the pattern where the model is handed the retrieved passages inside the prompt and told to answer from them and cite them, and it is the pattern that turns a chat model with a training cutoff and no knowledge of your cluster into something an on-call engineer can trust at 03:00: "raise the memory limit to 1.5Gi and roll back config v42 [1]" with `[1]` pointing at the runbook section, rather than a plausible paragraph about Kubernetes memory limits in general. Before this module the copilot can search; after it, `copilot ask "why is the payments pod CrashLooping?"` returns an answer, its sources, and its cost, and says "the knowledge base does not cover this" when it should.

The module is also where the engineering judgment lives, because a RAG pipeline has five stages and each one has a failure mode that looks like "the model is dumb" and is not. A chunk that straddles two runbook branches retrieves well and answers wrong. A threshold set too low hands the model junk and it invents. A prompt that does not number its passages cannot be cited. A context window exceeded by two tokens returns an API error instead of an answer. The Go code fixes each of these in a specific file, and the three Python labs build the same pipeline with the raw SDK, LangChain and LlamaIndex so you can see exactly what a framework hides and what it does not. The fourth lab hands the whole pipeline to OpenAI's hosted file search, so the trade between control and convenience is something you have run, not read about.

## Concept cards

### RAG Usecases

**What it is.** RAG fits problems where the answer exists in a corpus that is private, changes often, or is too large for a prompt, and where the user needs to verify the answer against its source. The canonical cases are internal knowledge assistants (runbooks, policies, architecture docs), customer support over product documentation, code and API question answering over a repository, compliance and legal lookups where citation is mandatory, and "what happened before" queries over incident and ticket history. The common thread is that correctness is defined by the documents, not by the model's general knowledge, and that the documents change faster than anyone would retrain a model.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| RAG over a curated corpus | Up to date the moment a document is ingested; citations; access control by filtering; no training | Quality bounded by retrieval; prompt cost per question; requires a corpus worth searching | Answers live in documents and must be traceable |
| Long-context stuffing (put everything in the prompt) | No retrieval failures; trivial to build | Cost and latency scale with the corpus; models degrade on very long contexts; no access control | Corpus is small (tens of pages) and stable |
| Fine-tuning (next card) | Style, format, domain vocabulary baked in | Knowledge goes stale; no citations; expensive to iterate | Behaviour change, not knowledge injection |
| Tool-using agent that queries live systems (Module 06) | Answers from current state (`kubectl get`, PromQL), not documents | Slower, riskier, needs guardrails | The question is "what is happening now" |
| Plain chat model | Zero infrastructure | Hallucinates specifics; cutoff date; no private knowledge | General knowledge questions only |

**Why it wins (and when it doesn't).** RAG wins when the user will act on the answer and needs to check it: an on-call engineer will not run a `kubectl set resources` command because a model said so, but will if `[1]` opens the runbook section that says so. It does not win for live-state questions ("is payments-api healthy right now?"), which Module 06's agent answers with tools, nor for questions whose answer is not in any document, which no retrieval can fix; the honest design routes those to "the knowledge base does not cover this".

**Problem it solves → value added.** Without RAG, the copilot answers "why is the payments pod CrashLooping?" from training data: generic, uncited, and silent about ConfigMap v42. With it, the answer quotes the runbook's Branch A with the exact limit and rollback, cites the file and heading, and costs a few thousand prompt tokens. The measurable values are answer accuracy on a question set with known answers, citation precision (does `[1]` actually support the sentence), and the no-answer rate on out-of-corpus questions.

**In the capstone.** `copilot ask <question>`; `internal/rag` `Pipeline.Ask`; the six documents in `data/knowledge/` are the reference corpus, written so that each contains concrete, askable facts.

### RAG vs Fine-tuning

**What it is.** Fine-tuning continues training a model on your examples so that its weights encode new behaviour: output format, tone, domain vocabulary, a task the base model does poorly. RAG leaves the weights alone and supplies knowledge at inference time through the prompt. They answer different questions. Fine-tuning changes how the model answers; RAG changes what it knows while answering. Fine-tuning on facts is a poor way to inject knowledge (models memorise unevenly, cannot cite, and every document change is a training run), while RAG is a poor way to change style (you can prompt for style, but a fine-tuned model does it without spending tokens on instructions).

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| RAG | Fresh knowledge; citations; per-user access control; iterate by editing documents | Retrieval quality ceiling; prompt tokens per question | Knowledge changes; traceability required |
| Fine-tuning | Consistent format/tone; domain jargon; smaller model can match a larger one on a narrow task; fewer instruction tokens | Stale knowledge; no citations; training data curation and evaluation; cost per iteration | A narrow, stable behaviour with hundreds of examples |
| RAG + fine-tuned model | Fine-tuned for "answer from context with citations", RAG for knowledge | Two pipelines to maintain | High volume where instruction tokens cost real money |
| Prompt engineering only | Zero training; instant iteration | Long prompts; less consistent | Always first; fine-tune when prompts stop working |

**Why it wins (and when it doesn't).** For the copilot, RAG wins outright: runbooks change weekly, a postmortem adds a fact the moment it is merged, and an answer without a citation is not actionable in an incident. Fine-tuning would win for a component such as a severity classifier with thousands of labelled alerts, or to make a small local model reliably emit the copilot's strict citation format; Module 01's fine-tuning node and the `labs/python` fine-tuning script cover that case. The anti-pattern to name in a review is "fine-tune the model on our runbooks so it knows them".

**Problem it solves → value added.** A team that fine-tuned on runbooks would ship a model that confidently describes ConfigMap v42 as current three weeks after v43 rolled out, with no way to tell. RAG ingests v43 in the next `make ingest` and cites it. The value is correctness over time at zero retraining cost; the cost is a few thousand prompt tokens per question, which `copilot ask` prints.

**In the capstone.** Conceptual; decided in the RAG ADR in `adr/`. `internal/rag` is the RAG side; the fine-tuning lab under `labs/python/` is the other side, scoped to behaviour rather than knowledge.

### Implementing RAG

**What it is.** A RAG implementation has two paths. The ingestion path walks sources, splits them into chunks, embeds each chunk, and upserts chunk text, vector and metadata into a store, idempotently. The query path screens and embeds the question, retrieves the top-k chunks above a threshold (optionally filtered and fused with lexical search), assembles a prompt with a system instruction, numbered context passages and the question, trims it to the model's context budget, calls the model, checks the output, and returns the answer with the sources that were cited. Each stage is an interface in the capstone so it can be tested alone and swapped without touching the others.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Own pipeline over narrow interfaces (the capstone) | Every stage visible and testable; runs in CI with mocks; language of your service | You write the loaders and splitters | Production services, especially outside Python |
| Framework (LangChain, LlamaIndex) | Loaders, splitters, stores, tracing out of the box | Abstraction leaks; version churn; Python/JS | Prototypes and Python services |
| Hosted (OpenAI vector stores + file search, Vertex AI Search, Bedrock Knowledge Bases) | No retrieval code | No control over chunking, model, threshold, or data location | Documents may leave; convenience dominates |
| Search-engine backed (Elasticsearch + LLM) | Mature lexical search, aggregations | You still build the prompt side | Lexical search exists already |

**Why it wins (and when it doesn't).** Owning the pipeline wins for a Go service where the frameworks do not reach and for any team that needs the pipeline to run without network in tests; the capstone's `mock` provider and `memory` store mean `go test ./internal/rag/...` exercises chunking, retrieval, prompt assembly and the citation parser with no key. It loses on day one against a framework, which is why the Python labs exist: build it three ways in an afternoon, then port the decisions to Go.

**Problem it solves → value added.** The value of stage separation is in diagnosis. When an answer is wrong, `copilot search` shows whether the right chunk was retrieved (retrieval problem) or retrieved and ignored (prompt or model problem); `copilot tokens` shows whether the context was trimmed (budget problem). Without separation every bug is "the model is bad". With it, the lab's wrong answers are each traceable to one file.

**In the capstone.** `internal/rag/pipeline.go` `Pipeline.Ask` composes `safety` → `Embedder` → `Store.Search` (+ BM25/RRF) → `Retriever` threshold → `BuildPrompt` → `tokens.Budget.FitContext` → `llm.Provider.Complete` → `safety.Guard`. The request flow in `ARCHITECTURE.md` is this function.

### Chunking

**What it is.** Chunking splits documents into units that are embedded and retrieved independently. The unit size is bounded above by the embedding model's input limit and by how much context you want per hit, and below by meaning: a chunk has to be understandable on its own, because that is how it will be ranked. Strategies, in increasing structure-awareness: fixed-size windows by tokens or characters with overlap; recursive splitting on a separator hierarchy (paragraph, line, sentence, word) that prefers natural boundaries; structure-aware splitting on markdown headings, code blocks, or YAML/HCL blocks, with the heading path prepended to each chunk; and semantic chunking that splits where adjacent sentence embeddings diverge. Sizes of 200–500 tokens with 10–15% overlap are a common starting point; the right size is whatever maximises retrieval on your question set.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Fixed window (tokens/chars + overlap) | Predictable size; trivial; works on any text | Splits mid-sentence and mid-list; a remediation step can lose its heading | Unstructured text, baseline experiments |
| Recursive separator splitting | Prefers paragraph/sentence boundaries; still size-bounded | Ignores document structure above the paragraph | Prose without useful headings |
| Markdown/structure-aware + heading path prefix | Chunks align with runbook branches; headings make chunks self-describing; citations point at sections | Section sizes vary; long sections still need windowing | Runbooks, postmortems, docs with headings; the capstone default |
| Semantic chunking | Boundaries follow topic shifts | Embedding cost at ingest; non-deterministic sizes | Long prose without structure |
| Parent-child (small chunks for retrieval, return the parent section) | Precise retrieval, rich context | Two-level store | Large sections with precise questions |

**Why it wins (and when it doesn't).** Heading-aware chunking with the heading path prepended wins on runbooks because the structure is the meaning: "Branch A: Reason is OOMKilled" is the key that a fixed window throws away when the window starts three lines below it. `labs/python/05_rag_raw_sdk.py` splits on `#`/`##`/`###`, windows long sections to 1,200 characters with 150 overlap, and prefixes `file > heading` to every chunk; this one change is the largest retrieval-quality gain in the module. It does not win on YAML or HCL, which have no headings; the capstone falls back to a recursive splitter for those, and a manifest chunk still carries its file name as the prefix.

**Problem it solves → value added.** With fixed 500-token windows over the crashloop runbook, the question "OOMKilled remediation" can retrieve a chunk that begins at step 2 of the remediation and ends inside Branch B, and the model merges two branches into one wrong procedure. With heading-aware chunks, the whole of Branch A is one chunk and the answer is "raise to 1.5Gi, roll back to v41" with one citation. The measurable value is answer accuracy on procedural questions and citation precision; the cost is variable chunk size, handled by the token budget.

**In the capstone.** `internal/rag/chunk.go`: `Chunker` struct (`MaxTokens`, `Overlap`) with `Fixed`, `Recursive` and `Markdown` methods; `Markdown` prepends the heading path and applies `Recursive` inside oversized sections, and `Ingester` uses `Recursive` for non-markdown files. Chunk ids are a SHA-256 of `source + text` (`chunkID` in `ingest.go`), which is what makes ingest idempotent.

### Embedding

**What it is.** In the RAG pipeline, embedding is applied twice with the same model: to every chunk at ingest and to the question at query time. Three pipeline-specific concerns sit on top of Module 03: asymmetry (some models expect different prefixes for queries and passages; OpenAI's and MiniLM do not, e5 and bge do), the text you embed versus the text you show (embedding `file > heading + body` while storing the body for display improves ranking without cluttering the prompt), and ingestion economics (batching, content-hash caching so unchanged chunks are never re-embedded, and recording the model so a change forces a re-index).

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| One embedder for chunks and queries (the capstone) | Simple; correct for symmetric models | Must remember query/passage prefixes for models that need them | Default |
| Embed an enriched chunk (heading path, summary, keywords) | Better ranking for terse chunks | More ingest tokens; summaries need an LLM call | Chunks are short or context-dependent |
| Hypothetical question embeddings (generate questions per chunk, embed those) | Matches question-to-question; strong for FAQ-like corpora | LLM cost per chunk at ingest | Corpus is small and questions predictable |
| Multi-vector / late interaction (ColBERT-style) | Higher precision | Specialised stores; larger indexes | Retrieval precision is the product |

**Why it wins (and when it doesn't).** A single embedder with heading-enriched chunk text wins on cost and simplicity, and on the sample corpus it is enough to answer every in-corpus question correctly. Enrichment with generated questions or summaries pays off when chunks are terse (manifests, config) and questions are prose; the capstone leaves a hook for it but does not enable it by default, because it adds an LLM call per chunk to ingest.

**Problem it solves → value added.** Re-embedding the whole corpus on every `make ingest` would cost tokens linearly in corpus size and time proportional to chunk count; with content-hash caching, the second run embeds nothing, and editing one runbook embeds only its chunks. `labs/python/05_rag_raw_sdk.py` shows the cache in forty lines; the Go ingest does the same through the store.

**In the capstone.** `internal/rag` `Ingester.Ingest` calls `embeddings.Embedder.Embed` in batches and skips chunks whose content-hash id already exists in `vectorstore.Store`; `COPILOT_EMBED_PROVIDER`/`COPILOT_EMBED_MODEL` select the model; the store rejects queries whose dimension no longer matches.

### Vector Database

**What it is.** In the RAG pipeline the store holds chunk text, vector, and metadata (`source`, `heading`, `kind`, hash, ingest time), answers top-k similarity queries with optional filters, and optionally supports a lexical index for hybrid retrieval. The RAG-specific requirements beyond Module 04 are: metadata rich enough to cite (file and heading, not just an id), filters that express the questions users ask ("only runbooks", "only the payments namespace"), and a deletion path so removed or renamed documents disappear from answers.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| `memory` store with JSON persistence (capstone default) | Zero infrastructure; exact search; runs in CI | Single process; corpus bounded by RAM; no server | Team knowledge base, development, tests |
| Qdrant / Chroma via the same `Store` interface | Shared across replicas; ANN; in-store filtering | A service to run | Production with multiple replicas or large corpus |
| Framework in-memory store (LangChain `InMemoryVectorStore`, LlamaIndex `SimpleVectorStore`) | One line in Python | Python process only | Labs and prototypes |
| Hosted vector store (OpenAI) | Nothing to run | No control of chunking or model; data leaves | See the last card |

**Why it wins (and when it doesn't).** The in-memory default wins for this module because nothing about RAG correctness depends on the store being a database; every quality lever is in chunking, retrieval thresholds and prompts. The switch to Qdrant is driven by Module 04's triggers, not by RAG, and `internal/rag` does not change when it happens.

**Problem it solves → value added.** Storing `source` and `heading` with every chunk is what makes `[1]` resolvable to "runbook-payments-api-crashloop.md > Branch A"; a store that keeps only ids forces a second lookup and breaks when ids change. Filters turn "what is the SEV2 ack time?" scoped to `kind=policy` into a one-chunk retrieval instead of five candidates from three documents.

**In the capstone.** `vectorstore.Document` carries `ID` (content hash), `Text`, `Vector` and `Metadata` (`source`, `source_type`, `section`); `copilot ask --source runbook` passes a filter through `Retriever`; `COPILOT_VECTORSTORE` selects the backend.

### Retrieval Process

**What it is.** Retrieval takes the screened question and returns the ordered passages that will be shown to the model. The steps: embed the question; vector search for top-k′ (k′ larger than the final k when fusion or re-ranking follows); optionally BM25 search and RRF fusion; apply metadata filters; drop hits below the similarity threshold; deduplicate near-identical chunks and cap chunks per source so one long document does not fill the context; optionally re-rank with a cross-encoder; return k hits with scores and metadata. The threshold is the step that separates a trustworthy system from a fluent one: with no threshold there is always a top-5, so the model always gets context, so it always answers, including when nothing relevant exists.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Top-k dense with threshold | Simple; fast | Identifier questions; threshold per model | Prose corpora |
| Hybrid dense + BM25 with RRF (capstone default) | Handles identifiers and paraphrase; robust | Second index; fusion constant | Runbooks, configs, tickets |
| Re-ranked two-stage (top-50 → cross-encoder → top-5) | Best top-5 precision | 50–200 ms and a model | Precision matters and budget allows |
| Query transformation (rewrite, multi-query, HyDE) | Better recall on terse questions | Extra LLM call; can drift from intent | Short or ambiguous questions |
| Agentic retrieval (the model decides what to search, iteratively) | Handles multi-hop questions | Latency, cost, unpredictability | Questions span several documents; see Module 06 |

**Why it wins (and when it doesn't).** Hybrid retrieval with a calibrated threshold and a per-source cap is the right default for an on-call copilot because questions mix prose ("why is the pod dying") with identifiers (`v42`, `PLAT-1790`), and because the failure mode of answering confidently from nothing is worse than saying "not covered". Re-ranking is worth adding when measurement shows the right chunk is usually in the top-20 but not the top-5; query rewriting when questions are terse. None of it helps when the document does not exist, which is the most common root cause in practice, and the no-answer rate is how you find those gaps.

**Problem it solves → value added.** The lab's sixth question, "how do I rotate the TLS certificate for the ingress controller?", has no answer in the corpus. Without a threshold, all three Python pipelines retrieve five low-scoring chunks from the node runbook and the model writes a plausible procedure. With the threshold, they print "the knowledge base does not cover this". That sentence is the most valuable output a RAG system produces, and the no-answer rate over a week of real questions is the backlog of documents to write.

**In the capstone.** `internal/rag` `Retriever` (`TopK`, `MinScore`, `Filter`, `Hybrid`), `vectorstore.BM25` + `RRF` behind `COPILOT_HYBRID`; `copilot search` prints the same hits `copilot ask` would use so retrieval can be debugged without generation.

### Generation

**What it is.** Generation assembles the prompt and calls the model. The prompt has a system instruction (answer only from context, cite with `[n]`, quote exact values, say when the context does not cover the question, be concise), the numbered context passages each labelled with their source and heading, and the question. Before the call, the assembled prompt is measured against the model's context window minus the reserved output tokens, and passages are dropped from the bottom of the ranked list until it fits. After the call, the output is checked: length, banned patterns, optional JSON schema, and citation validity (every `[n]` must refer to a passage that was actually supplied). The answer is returned with its sources and the measured token usage and cost.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Single completion with numbered context and citation instruction (capstone) | One call; citations; deterministic at temperature 0 | Long contexts dilute attention; model may still over-answer | Default |
| Map-reduce over chunks (summarise each, then combine) | Handles more context than fits | N+1 calls; slower; loses cross-chunk reasoning | Very long documents must all be considered |
| Structured output (JSON with `answer`, `citations[]`, `confidence`) | Machine-checkable citations; UI-friendly | Slightly stiffer prose; schema maintenance | The answer feeds a UI or another system |
| Streaming tokens to the client | Perceived latency drops | Citations must be validated after the stream ends | Interactive chat (`copilot chat`, `/v1/ask` with SSE) |
| Hosted generation with built-in citations (file search) | Citations come as annotations | Vendor-defined format and behaviour | Last card |

**Why it wins (and when it doesn't).** A single, well-instructed completion at temperature 0 with numbered passages wins for an on-call tool because it is fast, cheap and auditable: the prompt can be printed, the citations checked against the passages, the cost recorded. It does not win when the retrieved material exceeds the window (rare for runbooks, common for long postmortem collections), where map-reduce or a tighter retriever is needed, and it needs post-validation when streaming, because a `[7]` that refers to nothing can only be caught at the end.

**Problem it solves → value added.** Without the budget step, a question that retrieves several long chunks produces a context-length error from the API rather than an answer; with `tokens.Budget.FitContext`, the lowest-ranked passages are dropped and the call succeeds. Without output checks, the model occasionally echoes a secret or a destructive command from a chunk; `safety.Guard.CheckOutput` redacts or withholds it (citation-index validation is a natural addition next to it). The measurable values are the error rate on long retrievals (to zero) and citation precision, which the question set scores.

**In the capstone.** `internal/rag/prompt.go` `BuildPrompt`; `internal/tokens` `Budget.FitContext`/`TrimHistory` and `Estimate`; `internal/llm` `Provider.Complete` with `Request.User` set to the end-user id; `internal/safety` `Guard`; `copilot ask` prints answer, sources and cost. `copilot ask --print-prompt` shows the assembled prompt and `copilot tokens` sizes it.

### Ways of Implementing RAG

**What it is.** There are four practical ways to build the same pipeline: directly against provider SDKs (OpenAI, Anthropic, Ollama) with your own chunking and a vector store client; with LangChain, which models the pipeline as composable Runnables; with LlamaIndex, which models it as documents → nodes → index → query engine; or by delegating retrieval to a hosted service such as OpenAI's vector stores with the file search tool. The four produce the same answers on the sample corpus because the answer quality lives in chunk boundaries, thresholds and prompts, which are your decisions in all four; they differ in how visible those decisions are, how portable the result is, and how much code you own.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| SDKs directly | Everything visible; minimal dependencies; any language (the capstone's Go) | You write loaders, splitters, retries | Services; when you need to debug precisely |
| LangChain | Loaders and splitters for everything; LCEL composition; LangSmith tracing; huge ecosystem | Abstraction layers; API churn between versions; Python/JS | Python prototypes, many data sources |
| LlamaIndex | Index-centric; strong defaults for RAG (node parsers, query engines, citation engine); good evaluation module | Opinionated; global `Settings` footgun; Python | RAG-focused Python apps |
| Hosted (OpenAI file search) | No retrieval code; citations as annotations | No chunking/model/threshold control; data leaves; vendor-bound | Prototypes where documents may leave |

**Why it wins (and when it doesn't).** The course's position is to learn the raw version first and choose a framework second: `05_rag_raw_sdk.py` is about 120 lines of logic and every line maps to a framework concept, so the frameworks become labelled shortcuts rather than magic. For the Go capstone there is no framework decision to make; for a Python service, LlamaIndex is the faster path to a good RAG default and LangChain the better fit when RAG is one step of a larger chain or agent.

**Problem it solves → value added.** Teams that start with a framework often cannot answer "what exactly did we send to the model?", which is the first question in every RAG bug. Having built the raw version, the labs' LangChain and LlamaIndex scripts annotate each framework call with its raw equivalent, so the answer is always available. The value is debugging time.

**In the capstone.** The Go code is the "SDKs directly" path (`internal/rag`, `internal/llm`, `internal/embeddings`, `internal/vectorstore`). The four Python labs are the four ways side by side.

### Using SDKs Directly

**What it is.** Building RAG against the provider SDKs means calling the embeddings endpoint and the chat endpoint yourself, and writing the chunker, the store client, the prompt and the citation parser. The OpenAI Python SDK (`openai`) and Go's community and official clients expose `embeddings.create` and `chat.completions.create` (or `responses.create`); Ollama exposes an OpenAI-compatible endpoint, so the same code runs locally with a base URL change. What you own in return is small: batching, retries with backoff, a content-hash cache, cosine over a numpy matrix or a store client call, and a prompt template.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Provider SDK + numpy/store client (`05_rag_raw_sdk.py`, `internal/rag`) | Complete visibility; ~120 lines; portable via OpenAI-compatible base URLs | No loaders for PDFs/HTML/Confluence; you maintain it | Services, Go, anything you must debug at 03:00 |
| Provider SDK with provider-side retrieval (file search) | Even less code | Control lost | Prototypes |
| Thin helper libraries (LiteLLM for provider switching, Instructor for structured output) | Keep visibility, remove boilerplate | More dependencies | Python services that want portability without a framework |
| Full framework | See the next two cards | | |

**Why it wins (and when it doesn't).** Direct SDK use wins whenever the service language is not Python or JavaScript, and whenever you need deterministic, offline-testable behaviour: the Go capstone's mock provider and mock embedder make the full pipeline unit-testable, which no framework offers for Go. It loses when the corpus arrives in twenty formats from five systems; writing a Confluence loader and a PDF table extractor is where frameworks repay their abstraction.

**Problem it solves → value added.** The raw lab shows that "RAG" is chunk, embed, cosine, prompt, call — about 120 lines — and the Go implementation is the same shape with interfaces. The value is that every engineer on the team can read the entire retrieval path in one sitting, which is what makes the ADR decisions about thresholds and chunk sizes reviewable.

**In the capstone.** All of `internal/rag`; `internal/llm/openai.go`, `anthropic.go`, `gemini.go`, `ollama.go` implement `Provider`; `labs/python/05_rag_raw_sdk.py` is the Python mirror, including the OpenAI-compatible Ollama path via `OPENAI_BASE_URL`.

### Langchain

**What it is.** LangChain is a Python and JavaScript framework whose core abstraction is the Runnable: loaders, splitters, embeddings, vector stores, retrievers, prompts, models and output parsers all implement `invoke`/`stream`/`batch` and compose with `|` into chains (the LangChain Expression Language, LCEL). For RAG it supplies `DirectoryLoader` and dozens of document loaders, `RecursiveCharacterTextSplitter` and `MarkdownHeaderTextSplitter`, `OpenAIEmbeddings` and peers, vector store wrappers for every database in Module 04, `as_retriever()` with search-type options, `ChatPromptTemplate`, and `ChatOpenAI`/`ChatAnthropic`/`ChatOllama`. LangSmith provides tracing of every step; LangGraph extends chains into stateful agent graphs (Module 06).

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| LangChain (LCEL) | Widest integration catalogue; composition, streaming and batching for free; tracing | Several layers between you and the API; frequent breaking changes across minor versions; citations are your own metadata numbering | RAG is one stage in a larger Python application |
| LlamaIndex | Better RAG defaults out of the box | Narrower beyond RAG | RAG is the application |
| Raw SDK | Full control | You write the loaders | Services |
| Haystack | Pipeline graphs, strong evaluation story, production focus | Smaller ecosystem | Python teams who want explicit DAGs |

**Why it wins (and when it doesn't).** LangChain wins when the corpus is heterogeneous and the application is more than question answering: the loader catalogue alone saves weeks, and LCEL gives streaming and batching without code. It does not win for a Go service (there is no LangChain for Go that a platform team should depend on) or when every layer must be auditable; `05_rag_langchain.py` shows that the chunking, threshold and citation decisions are still yours, and that the framework's contribution is the markdown splitter, a swappable store, and a chain object you can stream and trace.

**Problem it solves → value added.** In the lab, swapping `InMemoryVectorStore` for Chroma or Qdrant is one constructor, and `chain.stream()` gives token streaming with no extra code; in the raw version both are work. The cost is visible too: the `RunnablePassthrough.assign` plumbing needed to keep the retrieved documents alongside the answer for citation printing is harder to read than the raw version's five lines.

**In the capstone.** `labs/python/05_rag_langchain.py`. Conceptual for the Go code.

### Llama Index

**What it is.** LlamaIndex is a Python (and TypeScript) framework built around the index: `SimpleDirectoryReader` loads documents (its `MarkdownReader` splits on headings), node parsers (`SentenceSplitter`, `MarkdownNodeParser`, `SemanticSplitterNodeParser`) turn documents into nodes with metadata, `VectorStoreIndex` embeds and stores them (in-memory `SimpleVectorStore` by default, any Module 04 database via a `StorageContext`), and query engines wrap retrieval plus synthesis: `as_query_engine()` for the default, `CitationQueryEngine` for numbered citations, router and sub-question engines for multi-index setups. Global `Settings` hold the default LLM, embedder and node parser; node postprocessors (`SimilarityPostprocessor`, re-rankers) sit between retrieval and synthesis; an evaluation module scores faithfulness and relevancy.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| LlamaIndex | RAG-first defaults; `CitationQueryEngine`; node postprocessors; evaluation built in; clean index/storage separation | Global `Settings` make multi-model setups error-prone; fewer non-RAG integrations; Python | The application is retrieval and question answering |
| LangChain | Broader; better when RAG is one step | Less opinionated about RAG | Larger chains/agents |
| Raw SDK | Control | Work | Services |
| Hosted | No code | No control | Prototypes |

**Why it wins (and when it doesn't).** LlamaIndex reaches a good RAG baseline fastest: `05_rag_llamaindex.py` gets heading-aware loading, a token-based splitter, a cosine cutoff and numbered citations in fewer lines than either other lab, because those defaults are the framework's purpose. It loses when you want `[n]` to mean something other than the framework's 512-token citation sub-chunk, or when two indexes need different embedders and the global `Settings` bite; and, like LangChain, it is not available to the Go service.

**Problem it solves → value added.** `CitationQueryEngine` is the one place in either framework where citations are a feature rather than your own string formatting: it re-splits retrieved nodes into citation units, labels them, and prompts the model to reference them. In the lab that saves the prompt engineering the raw version does by hand, at the cost of a citation granularity you did not choose. The `SimilarityPostprocessor` does exactly what the capstone's `Retriever.MinScore` does, and the lab's out-of-corpus question demonstrates both.

**In the capstone.** `labs/python/05_rag_llamaindex.py`. Conceptual for the Go code.

### RAG Alternative: OpenAI Assistant API

**What it is.** OpenAI's hosted alternative to building RAG is a vector store you upload files to (`/v1/vector_stores`, with files or file batches and a coarse chunking strategy of max tokens and overlap) plus the `file_search` tool, which the model can call to retrieve from that store with OpenAI's own embedding, hybrid search and re-ranking, returning answers with file citation annotations. The tool was introduced with the Assistants API (assistant → thread → run objects with persistent state) and is now exposed through the Responses API as a single `responses.create` call with `tools=[{"type": "file_search", "vector_store_ids": [...]}]`. OpenAI has deprecated the Assistants API in favour of Responses; new code should use Responses, and the lab runs both against the same vector store so the migration is visible. Billing is per GB-day of vector storage beyond a free allowance plus per file-search tool call, in addition to model tokens; see OpenAI's pricing page.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| OpenAI vector stores + `file_search` (Responses API; Assistants API legacy) | Zero retrieval code; hybrid search and re-ranking included; citations as annotations; file types handled | No control of chunk boundaries (no heading awareness), embedding model, threshold or k beyond `max_num_results`; documents stored by the vendor; OpenAI-only; no offline or CI mode | Prototypes and internal tools whose documents may leave; teams without capacity to run retrieval |
| Own RAG (`internal/rag`) | Full control; any model; data stays; testable offline | You build and tune it | Production; regulated or sensitive documents; non-OpenAI models |
| Other hosted RAG (Vertex AI Search/RAG Engine, Bedrock Knowledge Bases, Azure AI Search) | Same convenience inside another cloud; some allow choosing embedder and chunking | Cloud lock-in; varying control | Already committed to that cloud |
| Anthropic/Gemini with long context and citations features | Fewer moving parts for small corpora | Cost per call scales with corpus | Small, stable corpora |

**Why it wins (and when it doesn't).** The hosted path wins the prototype: `05_openai_assistants_file_search.py` uploads the six documents and answers the same questions with citations and no retrieval code, in the time it takes the upload to index. It loses on every axis the capstone cares about: the runbooks contain account IDs and incident details that a platform team's security policy typically keeps off third-party storage; the chunker is not heading-aware (compare the citations on the OOMKilled question with the raw lab's); the threshold and model are not yours; and nothing runs in CI without a key and a bill. It is also the right thing to know about when someone asks "why not just use OpenAI's RAG?", because the honest answer is "for a prototype, yes; here is the ADR explaining why not for this product".

**Problem it solves → value added.** For a team that wants a document chatbot by Friday and is allowed to upload its documents, the hosted store removes Modules 03–05 from the critical path. For the capstone, running it is the control experiment: same documents, same model family, different retrieval; when its answer to the OOMKilled question omits the rollback step because the chunk split the remediation list, you have a concrete reason for owning chunking.

**In the capstone.** Not implemented in Go; conceptual, recorded in the RAG ADR. `labs/python/05_openai_assistants_file_search.py` (`--api responses` default, `--api assistants` legacy, shared vector store, cleanup unless `--keep`).

## Lab

#### Part 1 — End to end in Go, no keys

```bash
make build
make ingest
./bin/copilot ask "what is the OOMKilled remediation for payments-api?"
```

Expected with the mock provider (illustrative; the mock LLM echoes the top passage rather than reasoning):

```
Raise the memory limit to **1.5Gi** so the pods stop dying while you fix config: [2].
-c payments-api --limits=memory=1.5Gi --requests=memory=1Gi [2].
Roll back the ConfigMap to **v41** (`LEDGER_BATCH_SIZE=500`) and restart: [2].

Sources:
  [1] runbook-payments-api-crashloop.md › Runbook: payments-api CrashLoopBackOff › Branch A: Reason is `OOMKilled` (exit code 137)  (score 0.033)
  [2] runbook-payments-api-crashloop.md › Runbook: payments-api CrashLoopBackOff › Branch A: Reason is `OOMKilled` (exit code 137)  (score 0.032)
  ...
  [5] runbook-payments-api-crashloop.md › Runbook: payments-api CrashLoopBackOff › Escalation  (score 0.029)
model=mock/mock-1 tokens=1279+58 cost=$0 (local/mock) latency=5ms
```

The mock pipeline is the real pipeline: chunking, hashing, retrieval, threshold, prompt assembly, budget trimming and citation parsing all ran. Only the embedder (hashed bag-of-words) and the LLM (echo) are fake. Now break retrieval on purpose:

```bash
./bin/copilot ask "how do I rotate the TLS certificate for the ingress controller?"
```

```
I could not find this in the indexed documents. Try `copilot search` with different keywords, or add the source document to data/knowledge and re-run `make ingest`.
```

(With the mock embedder `COPILOT_MIN_SCORE` defaults to 0, so low-scoring chunks are still passed in and the abstention comes from the extractive mock finding no matching sentence; with a real embedder set `COPILOT_MIN_SCORE=0.25` or so and the `Sources:` list itself goes empty.)

Then inspect what would have been sent:

```bash
./bin/copilot search -k 5 "payments pod OOMKilled"
./bin/copilot ask --print-prompt 'what is the OOMKilled remediation for payments-api?' | sed -n '/----- prompt -----/,/----- \/prompt -----/p' > /tmp/prompt.txt
./bin/copilot tokens /tmp/prompt.txt
```

`search` shows the ranked chunks with scores; `tokens` shows the prompt size against the model's window. Between them you can diagnose any wrong answer as retrieval, budget, or generation.

#### Part 2 — The same pipeline three ways in Python

```bash
. .venv/bin/activate
export OPENAI_API_KEY=sk-...
python labs/python/05_rag_raw_sdk.py
python labs/python/05_rag_langchain.py
python labs/python/05_rag_llamaindex.py
```

Each script answers the same questions and prints sources. Expected for the first question (raw SDK; wording varies):

```
Q: What is the OOMKilled remediation for payments-api?
A: Raise the memory limit to 1.5Gi and the request to 1Gi [1], then roll the ConfigMap back to
   v41 (LEDGER_BATCH_SIZE=500) and restart the deployment [1]. Do not exceed 2Gi without
   Platform Infra [1].
Sources:
  [1] runbook-payments-api-crashloop.md > Branch A: Reason is OOMKilled (exit code 137)  (cosine 0.612)
  [2] k8s-payments-deployment.yaml > (file)                                               (cosine 0.401)
```

And for the out-of-corpus question, all three print the no-answer sentence. Compare the sources across the three scripts: the raw and LangChain versions cite the same heading-aligned chunks; LlamaIndex's `CitationQueryEngine` cites 512-token sub-chunks, so its `[n]` count is higher for the same passages.

Run the raw version against Ollama to prove the pipeline is provider-agnostic:

```bash
ollama pull llama3.1 && ollama pull nomic-embed-text
OPENAI_BASE_URL=http://localhost:11434/v1 OPENAI_API_KEY=ollama \
RAG_CHAT_MODEL=llama3.1 RAG_EMBED_MODEL=nomic-embed-text \
python labs/python/05_rag_raw_sdk.py "after how many minutes is the secondary paged for a SEV2?"
```

#### Part 3 — The hosted alternative

```bash
python labs/python/05_openai_assistants_file_search.py
python labs/python/05_openai_assistants_file_search.py --api assistants "what is the OOMKilled remediation for payments-api?"
```

The script uploads the six documents to an OpenAI vector store, asks through the Responses API (or the legacy Assistants API), prints the cited files and the retrieved chunks with scores, and deletes the store. Compare the OOMKilled answer with Part 2: look for whether both remediation steps survived the hosted chunker.

#### With real models in Go

```bash
export COPILOT_PROVIDER=openai COPILOT_EMBED_PROVIDER=openai OPENAI_API_KEY=sk-...
make ingest
./bin/copilot ask "why did customers see duplicate holds in March 2026 and what changed afterwards?"
```

Expect an answer that cites the postmortem's root cause (ledger-worker v1.9.0, `allkeys-lru`, idempotency keys) and the action items (volatile-lru, dedicated `redis-idempotency`, readiness change), with the cost line showing the real token count and price from `internal/tokens`.

## Production notes

- **Build the question set before tuning anything.** 50–100 real questions with expected sources, including 10–20 with no answer in the corpus. Score retrieval (recall@5, hit@1), answer correctness, citation precision and no-answer accuracy on every change to chunker, threshold, model or prompt. This is the CI for RAG.
- **Chunk boundaries are the first lever, the threshold is the second, the prompt is the third.** Changing the LLM is the fourth. Teams usually try them in the opposite order.
- **Ingest is a job.** Run it on document change (webhook or CronJob), idempotently by content hash; delete chunks of removed files; record the embedder and chunker version with the index so a change forces a controlled re-index.
- **Prompt injection lives in your documents too.** A runbook that says "ignore previous instructions" will be retrieved and shown to the model. `internal/safety` screens both the question and the retrieved passages; treat the corpus as untrusted input (Module 08).
- **Access control is a retrieval filter, enforced server-side.** Users should only retrieve chunks they may read; pass the caller's permissions into `Retriever.Filter`, never into the prompt.
- **Budget every request.** Cap k, cap chunk size, trim to the window with `tokens.Budget`, set `max_tokens` for output, and record cost per answer with the end-user id so spend is attributable.
- **Observe the pipeline, not just the model.** Per question: retrieval latency, scores of hits, count below threshold, tokens in/out, model latency, cost, whether citations validated. A rising no-answer rate is a corpus gap or a retrieval regression; a falling citation-validity rate is a prompt or model regression.
- **Freshness expectations.** Tell users how fresh the index is (last ingest time in the answer footer). A correct answer from a stale runbook is a wrong answer.
- **Cache where it is safe.** Identical question text with an unchanged index can return a cached answer; anything keyed on user permissions cannot be shared.
- **Plan for the hosted option honestly.** If the documents may leave and the team cannot run retrieval, the OpenAI vector store is a legitimate choice; write the ADR either way so the decision is reviewable.

## Check your understanding

1. `copilot ask "what is the OOMKilled remediation?"` returns the 1.5Gi limit but omits the ConfigMap rollback. `copilot search` shows the Branch A chunk ranked first. Where is the bug and what do you change?
2. A teammate proposes fine-tuning `gpt-4o-mini` on all runbooks "so it just knows them" and dropping the vector store. Give the two technical reasons this fails for an on-call copilot and the one case where fine-tuning would be appropriate here.
3. You are asked to support a question that spans two documents: "is the HPA max consistent with the RDS connection ceiling described in the runbook?" Which retrieval design handles this and what does it cost?
4. The security team asks why the copilot does not use OpenAI's hosted file search, which "already does RAG". Write the two-sentence answer.
5. The no-answer rate rose from 8% to 21% this week. List three possible causes in order of likelihood and how you would distinguish them.

<details>
<summary>Answers</summary>

1. Retrieval is correct, so the bug is in chunking or budgeting. Either the Branch A section was windowed so the rollback step fell into a second chunk that ranked below the threshold or the per-source cap (check `internal/rag/chunk.go` chunk size against the section length), or `tokens.Budget.FitContext` trimmed the second chunk to fit the window (check `copilot ask --print-prompt` for which chunks survived). Fix: larger markdown chunk size or parent-section retrieval so a remediation list is never split; or a larger budget / smaller k.

2. Fine-tuning injects knowledge unevenly and without provenance, so the model cannot cite and cannot be audited; and knowledge is frozen at training time, so the next ConfigMap version or postmortem requires another training run and the model confidently reports stale facts in the meantime. Fine-tuning is appropriate for behaviour, for example training a small local model to reliably produce the copilot's strict `[n]` citation format from supplied context, which reduces instruction tokens without changing what it knows.

3. Multi-hop retrieval: either retrieve more (k′ = 20) with a per-source cap so both the manifest's HPA chunk and the runbook's pgbouncer chunk reach the prompt, or use query decomposition (the model splits the question into "HPA maxReplicas" and "RDS connection ceiling", retrieves for each, then answers), or hand it to the Module 06 agent with `search_docs` as a tool. Costs: more prompt tokens, one or more extra LLM calls, higher latency and less predictable behaviour; justified only if the question set shows multi-hop questions matter.

4. Hosted file search requires uploading runbooks and postmortems containing account identifiers and incident details to third-party storage, which our data policy does not permit, and it removes control over chunking, the embedding model and the similarity threshold, which are exactly the levers that determine whether an on-call answer is complete and cited. We keep it as the documented prototype path in the ADR and run our own pipeline with the same interface for production.

5. (a) Corpus change: documents removed, renamed or re-chunked so expected chunks no longer exist; check ingest logs and chunk counts per source. (b) Embedder or store mismatch: a model or dimension change without a full re-index, or a collection name mismatch; check index metadata and scores (a uniform drop in top scores is the signature). (c) Question mix shifted: a new team started asking about a domain the corpus does not cover; sample the no-answer questions and cluster them. Distinguish by re-running last week's question set against this week's index: unchanged scores on the old set plus new unanswered topics means (c); dropped scores means (a) or (b).

</details>

## References

- Lewis et al., "Retrieval-Augmented Generation for Knowledge-Intensive NLP Tasks" (NeurIPS 2020): https://arxiv.org/abs/2005.11401
- Gao et al., "Retrieval-Augmented Generation for Large Language Models: A Survey" (2023): https://arxiv.org/abs/2312.10997
- Liu et al., "Lost in the Middle: How Language Models Use Long Contexts" (TACL 2024): https://arxiv.org/abs/2307.03172
- OpenAI, Retrieval / file search guide, vector stores API, Responses API, Assistants API deprecation notes: https://platform.openai.com/docs/guides/retrieval · https://platform.openai.com/docs/api-reference/vector-stores · https://platform.openai.com/docs/api-reference/responses · https://platform.openai.com/docs/assistants/overview
- OpenAI, API pricing (file search storage and tool calls): https://openai.com/api/pricing/
- OpenAI, Fine-tuning guide ("when to use fine-tuning vs. retrieval"): https://platform.openai.com/docs/guides/fine-tuning
- LangChain documentation: RAG tutorial, text splitters, LCEL: https://python.langchain.com/docs/tutorials/rag/ · https://python.langchain.com/docs/concepts/text_splitters/ · https://python.langchain.com/docs/concepts/lcel/
- LlamaIndex documentation: node parsers, `CitationQueryEngine`, `Settings`: https://docs.llamaindex.ai/en/stable/ · https://docs.llamaindex.ai/en/stable/examples/query_engine/citation_query_engine/
- Ollama OpenAI compatibility: https://github.com/ollama/ollama/blob/main/docs/openai.md
- Cormack, Clarke, Buettcher, "Reciprocal Rank Fusion" (SIGIR 2009): https://plg.uwaterloo.ca/~gvcormac/cormacksigir09-rrf.pdf
