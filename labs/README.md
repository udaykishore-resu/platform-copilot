# Labs

The Go capstone covers every roadmap node that can be built without the Python ecosystem. These labs cover the rest — the parts of the roadmap that *are* Python or JavaScript libraries — and, for RAG, show the same pipeline four ways so you can see what the frameworks abstract.

| Lab | Roadmap nodes | Needs |
|---|---|---|
| `python/01_chat_completions_compare.py` | Chat Completions API, Popular AI Models, Pricing | any of `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, Ollama |
| `python/01_token_counting.py` | Token Counting, Maximum Tokens, Managing Tokens | nothing (tiktoken is local) |
| `python/01_finetune_job.py` | Fine-tuning | `OPENAI_API_KEY` for `--submit`; dry-run otherwise |
| `python/02_hf_inference.py` | Hugging Face Hub, Tasks, Inference SDK | `HF_TOKEN` |
| `python/02_transformers_local.py` | Using Open Source Models | nothing (downloads small models) |
| `python/02_ollama_sdk.py` | Ollama, Ollama Models, Ollama SDK | `ollama serve` |
| `js/transformers-js/index.html` | Transformers.js | a browser |
| `python/03_sentence_transformers.py` | Open-Source Embeddings, Semantic Search, Classification, Anomaly Detection | nothing |
| `python/03_openai_embeddings.py` | OpenAI Embedding Models & API, Pricing | `OPENAI_API_KEY` (cost estimate runs without) |
| `python/04_vector_db_compare.py` | Chroma, FAISS, Qdrant, Indexing, Similarity Search | nothing (Qdrant optional) |
| `python/05_rag_raw_sdk.py` · `05_rag_langchain.py` · `05_rag_llamaindex.py` | Using SDKs Directly, LangChain, LlamaIndex | `OPENAI_API_KEY` or Ollama via `OPENAI_BASE_URL` |
| `python/05_openai_assistants_file_search.py` | RAG Alternative: OpenAI Assistant API | `OPENAI_API_KEY` |
| `python/06_openai_functions_agent.py` · `06_assistants_api_agent.py` | OpenAI Functions/Tools, Assistant API | `OPENAI_API_KEY` |
| `python/07_vision_dashboard.py` · `07_whisper_tts.py` · `07_langchain_llamaindex_multimodal.py` | Vision, Whisper, TTS, HF multimodal, LangChain/LlamaIndex multimodal | `OPENAI_API_KEY` |
| `python/08_moderation_and_injection_tests.py` | Moderation API, Adversarial testing | `OPENAI_API_KEY` (local fallback otherwise) |

```bash
make labs-setup            # python venv with pinned deps
. .venv/bin/activate
cd labs/python && python 03_sentence_transformers.py
```

Every lab prints `skipped: <reason>` and exits 0 when a key or server is missing, so `python -m py_compile labs/python/*.py` and the labs themselves are safe to run in CI.
