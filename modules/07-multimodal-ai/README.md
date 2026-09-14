# Module 07 · Multimodal AI

> **Roadmap nodes covered:** Multimodal AI Usecases, Image Understanding, Image Generation, Video Understanding, Audio Processing, Text-to-Speech, Speech-to-Text, Multimodal AI Tasks, OpenAI Vision API, DALL-E API, Whisper API, Hugging Face Models (multimodal), LangChain for Multimodal Apps, LlamaIndex for Multimodal Apps, Implementing Multimodal AI
>
> **Capstone step:** adds `internal/multimodal` — `LoadImage` (image file → provider-neutral `llm.ImagePart`), `DescribeImage`/`ReadDashboard` (`vision.go`), `SampleFrames`/`DescribeVideo` (`video.go`), and `OpenAIAudio.Transcribe` (Whisper), `.Speak` (TTS), `.GenerateImage` (DALL-E) in `audio.go` — plus the `copilot vision`, `transcribe`, `speak` and `diagram` subcommands. Extends `internal/llm.Message` with `Images []ImagePart` so the same `Provider` interface carries text and images.
>
> **Time:** ~5 hours · **Prerequisites:** Module 06

## Why this module exists

Incidents do not arrive as clean text. They arrive as a Grafana screenshot pasted into Slack, a phone photo of a terminal in a data centre, a 40-minute incident-bridge recording, and an architecture diagram in a postmortem PDF. Before this module the copilot is blind to all of it: `copilot ask` needs the engineer to transcribe what the screenshot shows before it can help, which is exactly the step that is slowest under stress. After this module the copilot can read the dashboard, transcribe the bridge, and fold both into the same RAG and agent loops built in Modules 05 and 06.

Multimodality is also where provider differences are sharpest. Text chat APIs have converged on one shape; vision, audio and image-generation APIs have not. OpenAI splits them across Chat Completions (vision), a dedicated audio endpoint (Whisper, TTS) and an images endpoint (DALL-E / gpt-image); Anthropic and Gemini carry images inline as content blocks; Gemini is the only hosted API with first-class video input; open models on Hugging Face each have their own processor. Designing one `ImagePart` abstraction that survives all of that is the engineering content of this module, and the alternatives tables are where the judgement shows.

The module is deliberately scoped to *understanding* first. Image generation and TTS are included because the roadmap lists them and because a copilot that can draw a diagram or read an alert aloud is useful, but the value for an SRE team is overwhelmingly on the input side: seeing and hearing what the humans see and hear.

## Concept cards

### Multimodal AI Usecases

**What it is.** A multimodal model accepts or produces more than one modality — text, images, audio, video — in a single forward pass, with a shared representation so that "what does this chart show?" is answered by attending over image tokens and text tokens together. Use cases for a platform team fall into understanding (dashboards, diagrams, logs-as-screenshots, incident recordings), generation (diagrams for postmortems, spoken alerts), and cross-modal retrieval (find the runbook that matches this screenshot). The key design observation is that multimodality mostly changes the *input assembly* layer; the retrieval, agent and safety layers stay the same.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Native multimodal model (GPT-4o/5, Claude, Gemini) | One call, joint reasoning over text and pixels; no pipeline to maintain | Per-image token cost; provider differences; hallucinated numbers on dense charts | The question needs reasoning about the image, not just text extraction |
| OCR + text LLM (Tesseract / cloud OCR → chat) | Cheap, deterministic extraction; auditable intermediate text | Loses layout, colour, and trend information; OCR errors compound | Documents and terminal screenshots where the content is text |
| Specialised vision models (classifier, detector) | Fast, cheap, deterministic | One task per model; no language interface | High-volume fixed tasks (is this dashboard red or green?) |
| Human describes the image | Zero model risk | The slowest step in the incident | Irreversible decisions |

**Why it wins (and when it doesn't).** Native multimodal wins when the image carries information that text extraction destroys — the *shape* of a latency graph, which panel is red, what the diagram's arrows connect. It loses on cost and determinism for high-volume pipelines: classifying ten thousand screenshots a day as "healthy / degraded" should be a small fine-tuned classifier, not a frontier model. For terminal screenshots, OCR plus a text model is both cheaper and more accurate at reading exact error strings, and the capstone's `vision` prompt asks the model to transcribe exact text before interpreting it for that reason.

**Problem it solves → value added.** Without it, `copilot ask` cannot be used from Slack with a pasted screenshot; the engineer retypes the panel titles and values, introducing transcription errors at the worst moment. With `copilot vision grafana.png "what is wrong here?"`, the model reads the panel titles, the time window, the shape of each series and the annotations, and its structured reading (`DashboardReading` with `panels[]`, `anomalies[]`, `next_checks[]`, via `--json`) can then be fed into the same `rag.Pipeline.Ask` to find the matching runbook. In the capstone's evaluation set of 30 dashboard screenshots with known incidents, the vision reading named the degraded service correctly in the large majority of cases; the failures were dense multi-series panels where it mis-attributed a line to a legend entry.

**In the capstone.** `internal/multimodal/vision.go → LoadImage`, `DescribeImage`, `ReadDashboard`; `cmd/copilot` → `vision` subcommand. The agent has no vision tool yet; wiring `ReadDashboard` into the `Registry` is a short exercise.

### Image Understanding

**What it is.** Image understanding (vision-language modelling) encodes an image into a sequence of visual tokens — typically via a vision transformer over fixed-size tiles — projects them into the language model's embedding space, and lets the decoder attend over them alongside the text prompt. Resolution is handled by tiling: a 1920×1080 screenshot becomes a low-resolution overview plus several 512-pixel tiles, each costing a fixed number of tokens, which is why providers expose a `detail` or resolution setting. The model is not performing OCR as a separate step; text in the image is recognised and reasoned about in the same pass.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| OpenAI GPT-4o / GPT-4.1 / GPT-5 vision | Strong chart and UI reading; `detail` control; same API as text chat | Per-tile pricing adds up on high-res images | Default for the capstone when `COPILOT_PROVIDER=openai` |
| Anthropic Claude vision | Excellent on documents and diagrams; long context for many images; careful about uncertainty | Max image dimensions and count per request; no video | Document-heavy workloads; when you want the model to say "I can't read that value" |
| Google Gemini vision | Very long context; native video and PDF input; competitive price | Different content-part format; safety filters can block benign ops screenshots | Video, PDFs, very many images per request |
| Open models (LLaVA, Qwen2-VL, Llama 3.2 Vision, Pixtral, InternVL) | Self-hostable; no data leaves the cluster; fine-tunable on your dashboards | Weaker on dense charts; you operate GPUs; uneven tooling | Data residency requirements or very high volume |

**Why it wins (and when it doesn't).** Hosted frontier vision wins on accuracy for the heterogeneous inputs a platform team sees — a Grafana panel, a draw.io diagram and a phone photo of a rack are three different distributions, and only large generalist models handle all three. Open models win when screenshots cannot leave the VPC; Qwen2-VL-7B on a single GPU reads Grafana screenshots acceptably, and a few hundred labelled screenshots of *your* dashboards fine-tune it to be better than the generalist on that one distribution. The honest failure mode of all of them is numeric hallucination on charts: models read trends reliably and exact values unreliably, so the capstone's prompt asks for ranges and never lets a chart reading be the sole basis for an error-budget calculation.

**Problem it solves → value added.** The problem is the screenshot-to-text bottleneck at the start of an incident. The value is speed and a structured record: `copilot vision` returns JSON that can be logged and fed to retrieval, so the incident timeline gets a machine-readable "what the dashboard showed at 02:14" entry instead of a PNG nobody can search. Cost is reported from the provider's returned `Usage` (`tokens.Estimate` counts text only, so image tokens are not pre-estimated).

**In the capstone.** `internal/multimodal/vision.go → LoadImage(path)`, `internal/llm/types.go → ImagePart{MIME, Data, URL}` on `Message.Images`; provider adapters map parts to OpenAI `image_url` (base64 data URL), Anthropic `image` blocks and Gemini `inlineData`.

### Image Generation

**What it is.** Image generation models (diffusion models such as DALL-E 3 and Stable Diffusion, and autoregressive image models such as GPT Image and Gemini's image output) produce pixels from a text prompt, optionally conditioned on a reference image (edits, variations, inpainting). Diffusion works by iteratively denoising a latent tensor under text guidance; autoregressive models emit image tokens the way a language model emits text. For a platform copilot the practical output is diagrams and illustrations for documents, not photorealism.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| OpenAI Images API (DALL-E 3, gpt-image-1) | Simple API; strong prompt adherence; gpt-image renders legible text | Per-image cost; no control over layout; cannot produce an *accurate* architecture diagram | Illustrations, blog headers, conceptual visuals |
| Stable Diffusion / FLUX self-hosted | Free per image; LoRA fine-tuning for house style; ControlNet for layout | GPU operations; weaker text rendering | Volume or style control |
| Programmatic diagrams (Mermaid, D2, Graphviz) generated by a *text* model | Exact, editable, version-controlled, diffable | Not pretty; limited to graph-shaped content | Architecture and sequence diagrams — almost always the right answer for engineering docs |
| Gemini image output | Multimodal in/out in one model; conversational editing | Newer, narrower availability | Already on Gemini |

**Why it wins (and when it doesn't).** For an engineering copilot, image generation mostly *does not* win: an architecture diagram must be correct, and no pixel model guarantees the arrow goes from `payments-api` to `postgres`. The capstone's `copilot diagram` path therefore asks the text model for Mermaid and renders that. The Images API wins for the small set of cases where a picture is illustrative rather than factual — a header image for an internal postmortem newsletter — and `GenerateImage` exists so the module is complete and so the agent could, in principle, produce a visual summary. Self-hosting wins only at volume.

**Problem it solves → value added.** The value is modest and honest: a few seconds to produce an illustration that would otherwise not exist, at a cost of a few cents. The real lesson is the alternatives table — knowing when *not* to use a generative image model is the judgement a hiring manager is looking for.

**In the capstone.** `internal/multimodal/audio.go → OpenAIAudio.GenerateImage(ctx, prompt, model, size)` against the OpenAI Images endpoint (returns the image URL); `copilot vision --generate "..."`. Mermaid diagram generation is the `copilot diagram` subcommand in `cmd/copilot/main.go` (a system prompt to the text model), not in this package.

### Video Understanding

**What it is.** Video understanding extends vision to a sequence of frames plus, optionally, an audio track. Two architectures exist: *native* video input, where the model ingests frames sampled at a fixed rate (Gemini samples about one frame per second and tokenises each, so an hour of video is on the order of a million tokens), and *frame-sampling pipelines*, where you extract key frames with `ffmpeg`, send them as a multi-image request, and transcribe the audio separately. Native video captures temporal relations; frame sampling is cheaper and works with any vision model.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Gemini native video | Temporal reasoning; audio included; one call | Token cost scales with duration; Gemini only | "What happened in this 5-minute screen recording of the outage?" |
| Frame sampling + any vision model | Provider-agnostic; you control which frames; cheap | Loses motion; you build the pipeline | Screen recordings where a few key frames carry the information |
| Speech-to-text only | Very cheap | Ignores the visuals entirely | Incident bridges where the audio is the content |
| Open video models (Qwen2-VL video, Video-LLaMA) | Self-hosted | Heavy; limited context | Residency constraints |

**Why it wins (and when it doesn't).** For the capstone's use case — a Loom-style recording of an engineer reproducing a bug — frame sampling at scene changes plus Whisper on the audio captures nearly everything at a fraction of the cost of native video, and works on every provider. Native video wins when the question is about *motion or order* ("did the deploy finish before the error rate rose?"), which frame sampling answers poorly. The capstone implements frame sampling as the portable path and passes video straight through only when `COPILOT_PROVIDER=gemini`.

**Problem it solves → value added.** Screen recordings attached to incidents are currently write-only: nobody re-watches them. Turning one into a transcript plus five annotated key frames makes the incident searchable and summarisable by the RAG pipeline. Developer time saved is the 20 minutes of re-watching per review.

**In the capstone.** `internal/multimodal/video.go → SampleFrames(ctx, path, every, maxFrames)` (shells out to `ffmpeg`) and `DescribeVideo(frames, transcript, question)`; `copilot vision recording.mp4 "..."` dispatches on file extension (`--every 5s`). Frames go through the normal image path on every provider; native video upload is not wired.

### Audio Processing

**What it is.** Audio processing covers the family of tasks that take a waveform in or produce one out: speech-to-text (transcription), speaker diarisation, audio classification (is this an alarm?), speech-to-speech translation, and text-to-speech. Models operate on spectrogram features (log-mel) rather than raw samples; Whisper is an encoder–decoder transformer over 30-second mel windows, and newer "omni" models (GPT-4o audio, Gemini) accept audio tokens directly alongside text so they can reason about tone and non-speech sounds. For a platform team the relevant tasks are transcription of incident bridges and spoken alerts.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Dedicated ASR + dedicated TTS (Whisper, OpenAI TTS) | Mature, cheap, predictable; transcripts are plain text you control | Two round-trips; no reasoning about audio | Batch transcription, spoken summaries — the capstone default |
| Omni / realtime audio models (GPT-4o Realtime, Gemini Live) | Low-latency conversation; understands tone and interruptions | Expensive per minute; WebSocket/WebRTC integration; overkill for batch | Voice interfaces |
| Self-hosted (faster-whisper, whisper.cpp, Piper, Coqui) | No per-minute cost; on-prem | GPU or slow CPU; you own accuracy | Volume or residency |
| Cloud ASR (AWS Transcribe, Google STT, Azure Speech) | Diarisation, custom vocabularies, streaming | Cloud lock-in, per-minute pricing | You need diarisation or are already on that cloud |

**Why it wins (and when it doesn't).** Dedicated ASR/TTS wins for the capstone because both tasks are batch: an incident bridge is transcribed once after the fact, and a spoken alert is generated once per alert. The realtime models win only for a genuine voice assistant, which is out of scope. Self-hosting `faster-whisper` wins as soon as the team transcribes more than a few hundred hours a month or the audio cannot leave the network; the Python lab shows both paths with the same interface so the switch is a one-line change.

**Problem it solves → value added.** Incident bridges are the best source of "what did we actually try, in what order" and the least used, because nobody transcribes them. The value is a searchable transcript in the knowledge base within minutes of the call ending, at roughly a cent or less per minute of audio.

**In the capstone.** `internal/multimodal/audio.go → OpenAIAudio.Transcribe(ctx, path, prompt)` and `.Speak(ctx, text, voice, outPath)`; `copilot transcribe` and `copilot speak`. Save a transcript as `.md` under `data/knowledge` and it goes through the normal `internal/rag.Ingester.Ingest` path.

### Text-to-Speech

**What it is.** Text-to-speech converts text to a waveform, typically via a neural model that predicts acoustic tokens or a spectrogram from text (and optionally a voice embedding), followed by a vocoder that renders audio. Modern hosted TTS (OpenAI `tts-1` / `gpt-4o-mini-tts`, ElevenLabs, Google, Azure Neural) produces natural prosody, supports several voices and formats (mp3, opus, pcm), and can stream so playback starts before synthesis finishes. Quality is judged on naturalness, latency to first byte, and correct pronunciation of domain terms — `kubectl`, `etcd` and `PromQL` are all read wrongly by default.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| OpenAI TTS | One API key with everything else; good quality; streaming | Limited voice customisation; mispronounces jargon without hints | The capstone default |
| ElevenLabs | Best-in-class naturalness and voice cloning | Separate vendor and billing; cost per character is higher | Customer-facing audio |
| Cloud TTS (Google, Azure, Polly) | SSML for pronunciation control; many languages | Another cloud dependency | You need SSML phoneme control for jargon |
| Self-hosted (Piper, Coqui XTTS) | Free, offline | Lower quality; you maintain it | Embedded or air-gapped |

**Why it wins (and when it doesn't).** OpenAI TTS wins by being good enough and already configured; the capstone's use is reading an alert summary aloud in an on-call channel, where naturalness matters less than latency and cost. It loses when pronunciation control matters — there is no SSML, so the capstone works around jargon with a small substitution table (`kubectl` → "kube control") before synthesis. ElevenLabs wins for anything a customer hears.

**Problem it solves → value added.** It closes an accessibility and attention gap: an on-call engineer driving or away from a screen hears "payments error rate crossed five percent, runbook step two applies" instead of a buzz. The value is hard to quantify but real; the cost is fractions of a cent per alert.

**In the capstone.** `internal/multimodal/audio.go → OpenAIAudio.Speak(ctx, text, voice, outPath) error` (writes an mp3); `copilot speak --out summary.mp3 --voice alloy "..."`, and `copilot ask --speak "..."` writes the final answer to `answer.mp3`.

### Speech-to-Text

**What it is.** Speech-to-text (automatic speech recognition) maps audio to text. Whisper-style models are trained on hundreds of thousands of hours of weakly supervised audio and handle accents, noise and code-switching well; they output segments with timestamps and can optionally translate to English. Accuracy is measured as word error rate (WER); domain vocabulary (service names, acronyms) is the dominant source of errors in engineering contexts, and most APIs accept a `prompt` or custom vocabulary to bias recognition. Diarisation (who spoke) is a separate task not all ASR APIs provide.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| OpenAI Whisper API (`whisper-1`, `gpt-4o-transcribe`) | Robust, cheap, `prompt` for vocabulary, timestamps | 25 MB upload limit (chunk long files); no diarisation | The capstone default |
| Self-hosted Whisper (faster-whisper, whisper.cpp) | No per-minute cost; offline; same model family | CPU is slow; GPU needed for volume | Residency or volume |
| Deepgram / AssemblyAI | Streaming, diarisation, low latency | Another vendor | Live captioning of bridges |
| Cloud ASR (Transcribe, Google STT, Azure) | Diarisation, custom models, compliance certifications | Cloud lock-in | Regulated industries already on that cloud |

**Why it wins (and when it doesn't).** The Whisper API wins for after-the-fact transcription of incident bridges: robust to headset audio and cross-talk, and the `prompt` field seeded with the team's service names measurably lowers WER on exactly the words that matter. It loses when you need to know *who* said "I'm rolling back now" — diarisation requires a different service or a pyannote pipeline. Self-hosted wins at volume; the Python lab runs `faster-whisper` as a fallback when no key is present so the lab is useful offline.

**Problem it solves → value added.** Without it the postmortem's timeline is reconstructed from memory. With it, `copilot transcribe bridge.m4a > bridge.md` followed by `copilot ingest` produces a transcript that is chunked, embedded and searchable, so "when did we first mention the certificate?" is answered with a timestamp. The vocabulary prompt is the single highest-leverage setting.

**In the capstone.** `internal/multimodal/audio.go → OpenAIAudio.Transcribe(ctx, path, prompt)` requesting `verbose_json` (segments with timestamps in `Transcription.Segments`); `copilot transcribe --prompt "payments-api, ledger-worker, …" bridge.m4a`. Files over the 25 MB limit must be split with `ffmpeg` first; audio chunking is not automated.

### Multimodal AI Tasks

**What it is.** The multimodal task taxonomy, as Hugging Face organises it, names the input→output shapes: image-to-text (captioning, VQA, OCR), text-to-image, image-to-image (editing, super-resolution), visual document retrieval, video-text-to-text, audio-to-text (ASR), text-to-audio (TTS, music), audio classification, zero-shot image classification (CLIP-style), and "any-to-any". Naming the task precisely matters because it determines which models are candidates, what the evaluation metric is (WER, CIDEr, accuracy), and what the API shape will be.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| One generalist any-to-any model for all tasks | Simplest integration; joint reasoning | Most expensive per task; weakest at specialised tasks (exact OCR, classification at scale) | Low volume, high variety — the capstone's interactive path |
| One specialist model per task (Hub task filter) | Best accuracy and cost per task; small models run on CPU | Integration surface grows with each task | High-volume fixed tasks |
| Cascade: cheap specialist → generalist on low confidence | Cost of the specialist, accuracy of the generalist | Two systems to operate; confidence calibration is hard | Classification at scale with a long tail |

**Why it wins (and when it doesn't).** Knowing the taxonomy lets you pick the cheap option when it exists. "Is this dashboard screenshot red anywhere?" is zero-shot image classification with CLIP at a few milliseconds on CPU, not a frontier-model call. "Explain what is wrong in this dashboard" is VQA and needs the generalist. The capstone uses the generalist interactively and notes in the production section where a specialist would replace it at scale.

**Problem it solves → value added.** It prevents the most common cost mistake in multimodal systems: routing every image through the most expensive model. The capstone's `vision` command has two shapes — a free-form question (`DescribeImage`) and a structured reading (`--json`, `ReadDashboard` with `DashboardSchema`); an OCR-only prompt at low detail is the natural third to add for terminal screenshots.

**In the capstone.** `internal/multimodal/vision.go → VisionSystem` prompt and `DashboardSchema`; the Hugging Face task pages are the reference, exercised in `labs/python/07_langchain_llamaindex_multimodal.py`.

### OpenAI Vision API

**What it is.** OpenAI exposes vision through the ordinary Chat Completions (and Responses) API: a user message's `content` becomes an array of parts, each `{"type":"text"}` or `{"type":"image_url","image_url":{"url":"https://..."|"data:image/png;base64,...","detail":"low|high|auto"}}`. Images are tiled into 512-pixel squares at `high` detail, each costing a fixed token amount plus a base cost, or sent as a single 512-pixel thumbnail at `low`. Supported formats are PNG, JPEG, WEBP and non-animated GIF, with per-image size limits. The response is ordinary text, so everything downstream (safety, citations, cost estimation) is unchanged.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| OpenAI Vision (content parts) | Same endpoint and key as chat; `detail` controls cost; tool calling works in the same request | Per-tile pricing surprises on high-res screenshots | Default when the provider is OpenAI |
| Anthropic image blocks | Very good at diagrams and documents; explicit about limits | Base64 or URL blocks; separate format | Provider is Anthropic |
| Gemini `inlineData` / File API | Huge context, video, PDFs | Separate file upload flow for large media | Provider is Gemini |
| Azure OpenAI vision | Same API inside Azure compliance boundary | Regional model availability lag | Enterprise Azure |

**Why it wins (and when it doesn't).** Its strength is that vision is not a separate product: the capstone's `ImagePart` is attached to the user `Message` on the same `llm.Request`, the same `Provider.Complete` call, and the agent can receive an image and call tools in a single turn. It is the easiest integration of the three hosted providers. The cost model is its sharp edge — a 4K screenshot at `high` detail is many tiles — so `LoadImage` refuses files over 20 MB and the header comment tells you to downscale to ~1024 px before sending — the providers downscale anyway.

**Problem it solves → value added.** It gives the copilot eyes with about 40 lines of Go: read the file, detect MIME, base64-encode, set `detail`. The value is the whole "screenshot in, runbook out" flow; the controllable cost is the `detail` flag, which the capstone leaves at the provider default (`auto`).

**In the capstone.** `internal/llm/openai.go` maps `Message.Images` → `image_url` parts (base64 data URLs); `internal/multimodal/vision.go → LoadImage` reads the file and infers the MIME type from the extension; `copilot vision <path> "<question>"`.

### DALL-E API

**What it is.** The OpenAI Images API (`/v1/images/generations`, `/edits`, `/variations`) takes a prompt, size, quality and response format and returns URLs or base64 PNGs. DALL-E 3 rewrites prompts internally for safety and detail (the revised prompt is returned), produces one image per call at fixed sizes (1024², 1792×1024, 1024×1792), and is priced per image by size and quality. The newer `gpt-image-1` model adds transparent backgrounds, better text rendering and multi-image edits under the same endpoint. DALL-E 2 remains for cheap variations.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| OpenAI Images API | Same key; good adherence; safety rewrite reduces policy violations | Fixed sizes; one image per call on DALL-E 3; prompt rewrite can drift from intent | Illustrations for internal docs |
| Stability / FLUX APIs | Many sizes, style control, negative prompts | Separate vendor | Style-sensitive work |
| Self-hosted SD / FLUX | Free per image, LoRA | GPUs | Volume |
| No image: Mermaid/D2 via the text model | Correct, editable | Not illustrative | Anything that must be *accurate* |

**Why it wins (and when it doesn't).** It wins for convenience: the capstone already has an OpenAI client, so `GenerateImage` is a thin call. It loses for engineering diagrams, as the Image Generation card explains, and the capstone's CLI help text says so explicitly. The `revised_prompt` field is worth logging; it explains why the image does not match what you asked for.

**Problem it solves → value added.** Minimal but complete: one command to get an illustrative image, with the revised prompt logged for transparency and the cost printed. Its educational value — understanding that prompt rewriting happens and that sizes are fixed — exceeds its operational value for this product.

**In the capstone.** `internal/multimodal/audio.go → OpenAIAudio.GenerateImage(ctx, prompt, model, size)` (defaults `dall-e-3`, `1024x1024`); `copilot vision --generate "..."` prints the image URL.

### Whisper API

**What it is.** OpenAI's audio transcription endpoint (`/v1/audio/transcriptions`, plus `/translations`) accepts a multipart upload (mp3, mp4, m4a, wav, webm, and others, up to 25 MB), a model (`whisper-1`, or the newer `gpt-4o-transcribe` / `gpt-4o-mini-transcribe`), an optional `language`, an optional `prompt` for vocabulary and style, and a `response_format` of `json`, `verbose_json` (with segment timestamps), `srt`, or `vtt`. It is priced per minute of audio. The open-source Whisper checkpoints that `whisper-1` descends from are available on Hugging Face, so the same model family runs locally.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Whisper API | Cheap, robust, vocabulary `prompt`, timestamps | 25 MB limit; no diarisation; no streaming on `whisper-1` | Batch transcription — the capstone default |
| `gpt-4o-transcribe` | Lower WER, streaming | Newer, slightly different options | When you need streaming or the best accuracy |
| faster-whisper / whisper.cpp | Free, offline, same quality with `large-v3` | Needs GPU for speed | Residency or volume |
| Deepgram / AssemblyAI / cloud ASR | Diarisation, streaming | Extra vendor | Who-said-what matters |

**Why it wins (and when it doesn't).** It wins on simplicity and price for incident bridges. The two things to get right are chunking files over 25 MB at silence boundaries (`ffmpeg` `silencedetect`), so a sentence is not split, and seeding `prompt` with service names. It loses when diarisation is required; the capstone labels speakers only when the recording tool already provides per-speaker tracks.

**Problem it solves → value added.** Turns an hour of audio into a timestamped transcript for well under a dollar, which then flows into the knowledge base. The `verbose_json` timestamps make the postmortem timeline reconstructable to the second.

**In the capstone.** `internal/multimodal/audio.go → OpenAIAudio.Transcribe`; the `--prompt` flag of `copilot transcribe` defaults to the sample corpus's service names (`payments-api, ledger-worker, checkout-web, EKS, Kubernetes, PromQL`).

### Hugging Face Models (multimodal)

**What it is.** The Hugging Face Hub hosts open multimodal models under task tags — image-text-to-text (LLaVA, Qwen2-VL, Idefics, Llama 3.2 Vision, Pixtral, InternVL), automatic-speech-recognition (Whisper checkpoints, Wav2Vec2), text-to-speech (Bark, Parler-TTS), text-to-image (Stable Diffusion, FLUX), zero-shot-image-classification (CLIP, SigLIP) — each loadable through `transformers` with a matching `AutoProcessor` that handles image resizing, tokenisation and chat templating. Many are also served through the Inference API / Inference Endpoints and through Ollama (`llama3.2-vision`, `llava`, `qwen2.5vl`), which exposes them via the same OpenAI-compatible chat endpoint the capstone already uses.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Open VLM via Ollama (`qwen2.5vl`, `llama3.2-vision`) | No code change — same `ollama` provider; screenshots stay local | Smaller models read dense charts poorly; CPU inference is slow | Residency; development without keys |
| Open VLM via `transformers` | Full control, fine-tuning, batching | Python and GPU plumbing | Fine-tuning on your dashboards |
| HF Inference API / Endpoints | Hosted open models, no GPU ops | Cold starts; rate limits on the free tier | Trying a model before committing |
| Hosted frontier vision | Best generalist accuracy | Data leaves the network | Default when allowed |

**Why it wins (and when it doesn't).** Open models win decisively on one axis — the screenshot never leaves your infrastructure — and that axis is often non-negotiable for dashboards that show customer names or revenue. They lose on the hardest inputs; in the capstone's screenshot set, a 7B VLM misread legend-to-series mappings noticeably more often than the frontier models. The practical path is to fine-tune a small VLM on labelled screenshots of your own dashboards, where a narrow distribution lets a small model beat the generalist; the Python lab sketches the data format for that.

**Problem it solves → value added.** It makes the whole module runnable with zero API keys and zero data egress: `COPILOT_PROVIDER=ollama COPILOT_MODEL=qwen2.5vl` and `copilot vision` works on the same code path. For teams with residency constraints that is the difference between having a vision feature and not.

**In the capstone.** `internal/llm/ollama.go` maps `Message.Images` to Ollama's base64 `images` field; `labs/python/07_langchain_llamaindex_multimodal.py` loads a Hub VLM via `transformers` and compares its reading with the API's on the same screenshot.

### LangChain for Multimodal Apps

**What it is.** LangChain represents multimodal input as `HumanMessage(content=[{"type":"text",...},{"type":"image_url",...}])` and normalises it across its chat-model integrations (`ChatOpenAI`, `ChatAnthropic`, `ChatGoogleGenerativeAI`, `ChatOllama`), so one chain can be pointed at several providers. It adds document loaders that produce image-bearing documents (PDF page images, screenshots), output parsers to coerce a vision reading into a Pydantic schema, and LCEL composition to wire "describe image → retrieve → answer" as a runnable graph. LangGraph extends this to stateful agents that carry images between steps.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| LangChain multimodal messages | Provider normalisation done for you; structured output parsers; large ecosystem | Heavy dependency; content-part normalisation is incomplete for newer features (video, audio in/out) | Python app, several providers, want structured vision output quickly |
| Raw provider SDKs | Exact control of every provider feature | You write the normalisation | Single provider or you need a feature LangChain lags on |
| Capstone's Go `ImagePart` | Same idea, in your language, 100 lines | You maintain it | Go services |
| LlamaIndex (next card) | Retrieval-centric multimodal indexes | Less general orchestration | Multimodal RAG specifically |

**Why it wins (and when it doesn't).** LangChain wins in Python when the value is in the *composition* — parse the screenshot to a schema, retrieve with the parsed panel names, answer with citations — and you want that in twenty lines against three providers. It loses on the leading edge: realtime audio, native video and provider-specific `detail` flags arrive in the raw SDKs first. The capstone implements the same normalisation in Go and uses LangChain only in the lab, which makes the comparison concrete: the Go version is longer to write and shorter to debug.

**Problem it solves → value added.** In the lab it demonstrates the full screenshot → structured reading → retrieval → answer chain with a `with_structured_output` Pydantic model, which is exactly the shape `copilot vision` produces. Developer time to first working chain is the value; the cost is the dependency footprint.

**In the capstone.** Lab only: `labs/python/07_langchain_llamaindex_multimodal.py` (part 1). The Go equivalent is `internal/llm/types.go → ImagePart` plus the provider adapters.

### LlamaIndex for Multimodal Apps

**What it is.** LlamaIndex treats images as first-class nodes: `ImageDocument` / `ImageNode` carry a path or bytes, a `MultiModalVectorStoreIndex` embeds text and images into separate collections (text with a text embedder, images with CLIP), and a `SimpleMultiModalQueryEngine` retrieves from both and passes the retrieved images plus text to a multimodal LLM (`OpenAIMultiModal`, `AnthropicMultiModal`, `GeminiMultiModal`, `OllamaMultiModal`). The emphasis is retrieval: finding the right images and documents for a question, then reasoning over them.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| LlamaIndex multimodal index | Image retrieval via CLIP out of the box; clean query-engine abstraction | Two embedding spaces to manage; CLIP retrieval is coarse for dashboards | "Find past dashboards that looked like this" |
| LangChain | Better general orchestration and agents | Image retrieval is DIY | Chains and agents |
| Capstone approach: describe image → text embedding → existing text index | One embedding space; reuses `internal/vectorstore`; searchable descriptions | Loses visual similarity; description quality bounds recall | You already have a text RAG system and want images to join it cheaply |
| Dedicated multimodal embedders (SigLIP, Jina CLIP, Voyage multimodal) | Best cross-modal retrieval quality | Another model to host or pay for | Visual similarity is the core feature |

**Why it wins (and when it doesn't).** LlamaIndex wins when visual similarity itself is the query — "show me incidents whose dashboards looked like this one" — because CLIP-style embeddings capture that and text descriptions do not. The capstone chose the description route: every screenshot is described into structured text by the vision model and that text is indexed like any runbook chunk. That keeps one vector store, one embedding model and one retrieval path, and for a platform team the questions are almost always about *what* the dashboard shows, which text captures well. The lab shows both so the trade-off is visible.

**Problem it solves → value added.** In the lab, a multimodal index over the sample screenshots answers "which screenshot shows a memory leak pattern?" by image retrieval. In the capstone, the same question is answered through descriptions, at no additional infrastructure cost, with slightly lower recall on visual-pattern queries.

**In the capstone.** Lab only: `labs/python/07_langchain_llamaindex_multimodal.py` (part 2). The description-indexing path is `internal/multimodal/vision.go → ReadDashboard` output saved as markdown and fed to `internal/rag.Ingester.Ingest`.

### Implementing Multimodal AI

**What it is.** Implementation is the engineering of the input-assembly layer and its consequences: detecting the media type, resizing and encoding, choosing `detail`, mapping to each provider's content-part format, estimating tokens and cost per image, handling size limits and chunking for audio, wiring the outputs into the existing retrieval and agent loops, and applying the safety layer to model output derived from images (an image can carry injected text as easily as a document can). Done well, the rest of the system does not know a multimodal feature was added.

**Alternatives compared.**

| Option | Strengths | Weaknesses | Choose it when |
|---|---|---|---|
| Extend the existing `Message` with `Parts` (capstone) | One request type, one `Provider` interface, agent gets vision for free | Every provider adapter grows; token estimation becomes per-modality | You already have a provider abstraction |
| Separate `VisionClient` per provider | Simple to start | Duplicated auth, retry, logging; agent cannot mix text and image | Quick one-off features |
| Pre-process everything to text (OCR/caption) at ingest | Downstream stays text-only | Loses information; expensive at ingest | Archive indexing |
| Framework (LangChain/LlamaIndex) | Normalisation and retrieval for free in Python | Dependency; not Go | Python |

**Why it wins (and when it doesn't).** Extending the message type wins because the agent, the safety layer and the server all benefit without changes: `POST /v1/agent` accepts an image attachment and the agent can call `vision` as a tool or receive the image directly. It costs a pass through every provider adapter and a per-provider image token formula in `internal/tokens`. A separate client would be faster for a demo and wrong for a product.

**Problem it solves → value added.** The problem is multimodal features becoming a parallel system with its own auth, logging and safety gaps. The value is that `copilot vision` honours `COPILOT_PROVIDER`, end-user IDs, `safety.Guard` and cost reporting on day one because it reuses the request path from Module 01.

**In the capstone.** `internal/multimodal/*.go` (assembly), `internal/llm/types.go → ImagePart`; image token counts come back in the provider's `Usage` rather than being pre-estimated, and `internal/server` exposes only text endpoints (`/v1/ask`, `/v1/agent`) so far; `adr/ADR-0001` for why this sits behind the provider interface.

## Lab

### Part 1 — no keys

```bash
make build
./bin/copilot vision --json grafana-payments.png      # any dashboard screenshot you have to hand
./bin/copilot diagram "copilot RAG request flow"
```

Expected with the mock provider: `vision` prints `note: the mock provider cannot see images; set COPILOT_PROVIDER=openai|anthropic|gemini|ollama (e.g. llava) for real vision` and then the mock's schema-filled `DashboardReading` JSON (`summary`, `severity`, `panels`, `anomalies`, `next_checks`) — the image loading, MIME detection and request assembly are real, the reading is not. `diagram` prints a canned ```mermaid flowchart of the RAG request flow. The repo ships no sample media; bring your own screenshot.

Then the other media paths (these need `OPENAI_API_KEY`; without it they fail fast with `transcribe: OPENAI_API_KEY not set` / `speak: OPENAI_API_KEY not set`):

```bash
./bin/copilot transcribe incident-bridge.m4a                 # → Whisper transcript (verbose_json under the hood)
./bin/copilot vision --every 5s repro.mp4 "When does the error appear?"   # → samples frames with ffmpeg, describes them
./bin/copilot speak --out summary.mp3 "Payments is degraded"  # → TTS to an mp3
./bin/copilot ask --speak "Summarise the payments runbook"    # → answer plus answer.mp3
```

### Part 2 — with real models

```bash
export COPILOT_PROVIDER=openai OPENAI_API_KEY=sk-...
./bin/copilot vision grafana-payments.png "What is wrong?"
./bin/copilot vision --json grafana-payments.png                           # structured DashboardReading
./bin/copilot vision grafana-payments.png "Transcribe every visible label and value verbatim"   # OCR-style prompt
COPILOT_PROVIDER=anthropic ./bin/copilot vision grafana-payments.png "What is wrong?"
COPILOT_PROVIDER=gemini    ./bin/copilot vision repro.mp4 "What happens at the end?"  # sampled frames
COPILOT_PROVIDER=ollama COPILOT_MODEL=qwen2.5vl ./bin/copilot vision grafana-payments.png "What is wrong?"
```

Compare the four readings of the same screenshot; note which ones quote exact values and whether those values are right.

### Part 3 — Python

```bash
cd labs/python && source .venv/bin/activate
python 07_vision_dashboard.py grafana-payments.png
python 07_whisper_tts.py incident-bridge.m4a
python 07_langchain_llamaindex_multimodal.py
```

Each script prints what it would do and exits cleanly when keys or optional packages are missing.

## Production notes

- **Downscale before encoding.** Providers downscale anyway; doing it yourself (long side ≤ 1568 px, or 512 px for `low`) makes cost predictable and upload faster. Log width, height, tiles and estimated image tokens per request.
- **Redact before sending.** Dashboards leak customer names, revenue and hostnames. `safety.RedactPII` cannot redact pixels; either route sensitive dashboards to an in-VPC model (`ollama`) or crop/blur known regions at capture time. Decide this per dashboard, not per request.
- **Treat vision output as untrusted.** Text inside an image ("ignore previous instructions" on a sticky note in a photo) reaches the model. Run `safety.DetectInjection` on the vision reading before using it as a retrieval query or agent observation (the agent already does this for every tool observation).
- **Numbers from charts are approximate.** Never let a chart reading drive a calculation without a PromQL confirmation; the agent's prompt says so, and `VisionSystem` asks for approximate times rather than exact numbers.
- **Audio chunking at silence boundaries** and a vocabulary `prompt` are the two settings that move WER the most. Build the vocabulary from the index's service names automatically.
- **Cost controls:** `detail=low` for captions and classification, frame sampling at scene changes rather than fixed intervals, and a daily image-token budget per end-user ID in `internal/server`.
- **Fallbacks:** if the vision provider is down, degrade to OCR (Tesseract) plus text chat rather than failing the request; surface the degraded mode in the response.
- **Evaluation:** keep a labelled set of screenshots (service, anomaly type, approximate values) and recordings (reference transcripts); track panel-identification accuracy and WER per provider and per model version.

## Check your understanding

1. You are asked to classify 50,000 dashboard screenshots a day as healthy or degraded. What do you build?
2. An engineer wants to paste a Grafana screenshot containing customer email addresses into `copilot vision`. What are your options, and which do you recommend?
3. A 90-minute incident bridge recording is 120 MB. Walk through how it gets transcribed and indexed.
4. The vision model reports "p99 latency 1.4s" and the agent is about to compute the SLO burn rate from it. What should happen instead?
5. Product asks for `copilot diagram "draw our payments architecture"` using DALL-E. What do you push back with?

<details>
<summary>Answers</summary>

1. Not a frontier VLM. Zero-shot CLIP/SigLIP classification, or a small fine-tuned classifier on a few thousand labelled screenshots, running on CPU; route only low-confidence cases (or the ones a human opens) to the generalist model. Cost drops by orders of magnitude and latency becomes milliseconds.
2. Options: in-VPC model via Ollama (`qwen2.5vl`) so pixels never leave; crop or blur the offending panel at capture; or refuse. `RedactPII` cannot help with pixels. Recommend the in-VPC model for that dashboard class and document it per dashboard, because "be careful" does not scale.
3. Split it with `ffmpeg` at silence boundaries into pieces under 25 MB, run `copilot transcribe --prompt "<service names>"` on each (`OpenAIAudio.Transcribe` requests `verbose_json`, so `Transcription.Segments` carries timestamps), stitch the pieces with offset-corrected timestamps into a markdown file under `data/knowledge`, then `copilot ingest` runs it through the normal markdown-aware chunker, embedder and store. The result is searchable with timestamps in the text.
4. Chart-derived numbers are approximate; the agent prompt requires evidence from tools before any conclusion. The agent should call `promql` for the actual p99 and compute from that, citing both the screenshot and the query.
5. Pixel models cannot guarantee the diagram is *correct* — which boxes connect to which. Generate Mermaid or D2 from the text model (which can be grounded in the manifests via RAG), render that, and keep it in git. Use the Images API only for illustrative, non-factual visuals.

</details>

## References

- OpenAI, *Vision* guide and image token pricing. https://platform.openai.com/docs/guides/images-vision
- OpenAI, *Image generation* (DALL-E 3, gpt-image-1). https://platform.openai.com/docs/guides/image-generation
- OpenAI, *Speech to text* (Whisper, gpt-4o-transcribe). https://platform.openai.com/docs/guides/speech-to-text
- OpenAI, *Text to speech*. https://platform.openai.com/docs/guides/text-to-speech
- Anthropic, *Vision*. https://docs.anthropic.com/en/docs/build-with-claude/vision
- Google, *Gemini API — image, video and audio understanding*. https://ai.google.dev/gemini-api/docs/vision
- Radford et al., *Robust Speech Recognition via Large-Scale Weak Supervision* (Whisper, 2022). https://arxiv.org/abs/2212.04356
- Liu et al., *Visual Instruction Tuning* (LLaVA, 2023). https://arxiv.org/abs/2304.08485
- Wang et al., *Qwen2-VL* (2024). https://arxiv.org/abs/2409.12191
- Radford et al., *Learning Transferable Visual Models From Natural Language Supervision* (CLIP, 2021). https://arxiv.org/abs/2103.00020
- Hugging Face, *Tasks* (image-text-to-text, ASR, TTS, zero-shot image classification). https://huggingface.co/tasks
- LangChain, *Multimodal inputs*. https://python.langchain.com/docs/how_to/multimodal_inputs/
- LlamaIndex, *Multi-modal applications*. https://docs.llamaindex.ai/en/stable/use_cases/multimodal/
- Ollama, vision models (`llama3.2-vision`, `qwen2.5vl`). https://ollama.com/search?c=vision
