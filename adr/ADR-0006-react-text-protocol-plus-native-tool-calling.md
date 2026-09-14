# ADR-0006 · Two agent loops: native tool calling by default, the ReAct text protocol as the portable fallback

**Status:** Accepted · **Date:** 2025-09 · **Owner:** Udaykishore Resu · **Modules:** 06 (AI Agents)

## Context

Module 06 requires an agent that can decide to look at the cluster, search the docs, run a PromQL
query and do arithmetic, then answer with citations. There are four broad ways to build that loop
and the roadmap names three of them as nodes: ReAct prompting, OpenAI Functions/Tools, and the
OpenAI Assistants API; plan-and-execute is the fourth common pattern.

Constraints from earlier decisions shape the choice. ADR-0001 requires the agent to work against
five providers behind one interface, including Ollama-hosted open models with uneven tool-calling
support, and the mock provider in CI. ADR-0007 requires tools to be read-only with argument
allowlists enforced in code, which makes *argument fidelity* — does the model produce well-formed,
in-schema arguments — a safety property as well as a reliability one. The audience must be able to
read the trace of a run during an incident review, so the reasoning has to be loggable.

Measured on the Module 06 evaluation trajectories (a set of recorded triage questions against the
fixture cluster), the two candidate loops differ in ways that matter: native tool calling produced
malformed or out-of-allowlist `kubectl_get` arguments in well under one percent of steps, while the
free-text ReAct protocol on the same model did so roughly an order of magnitude more often, each
costing a retry round-trip. Native mode also took fewer steps on average because it could issue
parallel tool calls. Conversely, ReAct ran on every provider including small local models, where
native tool calling was either unsupported or unreliable.

The Assistants API was evaluated through `labs/python/06_assistants_api_agent.py`. It removes the
loop entirely but moves conversation state, retrieval and the loop onto one vendor's servers, with
no path to the mock provider, no control over hybrid retrieval (ADR-0005) and a harder audit story
(ADR-0010). OpenAI has also announced its supersession by the Responses API.

## Decision

`internal/agent` implements **two loops behind one `Agent.Run`**, selected by `Agent.Mode` (`--mode react|native`):

- **`native`** (`internal/agent/native.go`) — the default. Tools from the `Registry` are passed as
  `llm.Tool` JSON Schemas (`Registry.LLMTools`); the model's `ToolCall`s are dispatched by
  `Registry.Call`, executed sequentially (each tool enforces its own allowlist and timeout), and
  returned as `role: tool` messages. Provider adapters normalise each vendor's wire format
  (ADR-0001) so this file sees one shape.
- **`react`** (`internal/agent/react.go`) — the portable fallback for providers or models without
  tool support, selected with `copilot agent --mode react`. The system prompt (`prompts.go →
  ReActSystem`) defines the `Thought:` / `Action:` / `Action Input:` / `Observation:` / `Final
  Answer:` protocol; the `reAction`/`reInput` regexes and `toJSONArgs` accept either a JSON object
  or a plain string (mapped onto the tool's first schema property), and an unknown tool name is fed
  back as the observation.

Both loops share the `Registry`, the step and token budgets (`MaxSteps` default 8, `MaxTokens`
default 30k), the observation safety wrapping (`safety.DetectInjection` on every tool output),
structured trajectory logging, and the final `safety.Guard` check. Plan-and-execute is not
implemented; the Assistants API is not used in the Go code and is kept as a Python lab for
comparison.

## Alternatives considered

| Option | Why not |
|---|---|
| **ReAct only** | Maximally portable and trivially debuggable, but pays a measurable tax in malformed arguments, retries and extra tokens on providers that have native tool calling. Since argument validity is also a safety property under ADR-0007, leaving the better mechanism unused where it exists was not defensible. |
| **Native tool calling only** | Best reliability where supported, but excludes Ollama models without tool support and any provider whose tool calling is immature, and makes the agent untestable on the mock provider without teaching the mock to emit tool calls (which it now does, but ReAct was also needed for the human-readable trace). |
| **Assistants API (hosted loop)** | Fastest to a demo. Rejected for the Go capstone: single vendor, no mock path, no control over retrieval or loop, state outside our audit boundary, and announced supersession. Retained as a lab so the trade-off is measured. |
| **Plan-and-execute** (one planning call, then execute the plan's steps) | Predictable cost and good for long independent task lists, but triage questions are exactly the case where the first observation changes the plan; re-planning logic adds complexity for no gain on this workload. Documented in Module 06's comparison table; not implemented. |
| **Reflexion / self-critique loops** | Higher accuracy on hard problems at two to three times the cost; the eval set did not show enough failures of the kind reflection fixes to justify it. Could be layered on either loop later. |
| **A framework agent (LangChainGo, genkit, a Go port of LangGraph)** | Hides the loop the module exists to teach and brings its own prompt and tool models that would sit awkwardly next to `internal/llm`. The loops are ~300 lines combined. |

## Consequences

**Positive.**
- The best available mechanism is used per provider with no caller involvement; `copilot agent`
  works on all five providers.
- Argument validation happens on structured JSON before execution in native mode, strengthening
  ADR-0007's allowlist enforcement; in ReAct mode the same `Registry` validation applies after
  parsing.
- Both loops emit the same `Result{Steps}` trace (`copilot agent --trace out.json`), so
  `internal/agent/agent_test.go → TestAgentBothModesEndToEnd` and the incident-review trace are
  mode-independent.
- The ReAct loop keeps the agent fully testable against the mock provider and understandable by
  reading one file, which is the pedagogical requirement.

**Negative / costs.**
- Two code paths to maintain and test; a tool added to the `Registry` must be described both as a
  JSON Schema (native) and in the prompt's tool listing (react), generated from the same
  `Tool.Schema` by `Registry.LLMTools` and `Registry.Describe` to prevent drift.
- Provider adapters carry the complexity of tool-call normalisation (ADR-0001), including edge cases
  such as Anthropic requiring tool results as content blocks in a single user message and Gemini's
  parallel `functionCall` parts.
- ReAct mode's parser is a regex and will always have a long tail of deviations; the one-retry
  policy bounds the cost, but small models can still exhaust the step budget without answering.
- Native mode hides the model's reasoning unless the provider exposes it; the trace shows tool calls
  and results but not a `Thought:`. For incident reviews this is a real loss, partially mitigated by
  asking the model to include a one-line rationale in the final answer.
- Behaviour can differ subtly between modes on the same question (step count, which tools are
  called), which complicates "why did it do that?" comparisons across providers. The eval harness
  reports per-mode metrics separately.

**Follow-ups.**
- Teach the mock provider richer tool-call scripts so native mode is exercised as thoroughly as
  ReAct in CI.
- Evaluate a plan-and-execute mode if a long-horizon use case (bulk manifest audits) arrives; it
  would slot in as a third `Mode`.
- Revisit the Assistants decision when the Responses API stabilises: if it offers a hosted loop with
  our tools and acceptable audit hooks, it could become an optional sixth "provider" for the agent
  rather than a replacement for the loop.

