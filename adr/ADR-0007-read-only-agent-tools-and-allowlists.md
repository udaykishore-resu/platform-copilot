# ADR-0007 · Agent tools are read-only, allowlisted in code, and budgeted

**Status:** Accepted · **Date:** 2025-09 · **Owner:** Udaykishore Resu · **Modules:** 06 (AI
Agents), 08 (AI Safety & Ethics)

## Context

The moment a model can *act*, every known weakness of LLM systems acquires a blast radius. Prompt
injection (direct from a user, or indirect through a runbook, a pod annotation, a tool result or
text inside a screenshot) can redirect an agent; hallucination can invent a justification for an
action; a loop without a budget can fan out into hundreds of calls. OWASP's LLM Top 10 names this
"Excessive Agency" and the mitigation is the same one platform engineers already apply to service
accounts: least privilege, enforced by the system rather than by policy text.

The capstone's agent (Module 06) needs enough capability to triage: list and describe Kubernetes
resources, read pod events and logs, query Prometheus, search the knowledge base, do arithmetic. It
is tempting to add remediation — restart a deployment, roll back, scale — because that is where an
agent would save the most time. The threat model (Module 08, "Know your Customers / Usecases") says
the users are authenticated platform engineers who can already perform those actions themselves with
their own credentials; the copilot adds speed and synthesis, not authority. Speed on an irreversible
action taken on the strength of a stochastic planner reading untrusted text is not what those users
asked for, and one bad action would end trust in the tool.

Detection of injection is probabilistic and will never reach 100 percent (Module 08's adversarial
suite sits around 85–90 percent detection). Containment — making the bad outcome impossible — can
reach 100 percent, but only if the limits are enforced in code paths the model cannot influence.

## Decision

1. **Read-only by construction.** The tool set is `SearchDocs`, the `Kubectl` tools
   `kubectl_get` / `kubectl_describe` / `kubectl_logs` (`allowedVerbs` in `tools_kubectl.go` is
   `get`, `describe`, `logs`, `top`, `explain`, `api-resources` — never `apply`, `delete`, `exec`,
   `scale`, `rollout` or `patch`), `PromQL` (instant *queries*) and `Calc`. There is no tool that
   mutates anything. A model that decides to "delete the
   deployment" has no way to express it.
2. **Allowlists enforced in the tools, not the prompt.** `Kubectl` accepts only namespaces in
   `COPILOT_NAMESPACES` (`Kubectl.Namespaces`; empty means any) and resource kinds in
   `allowedResources` (`pods`, `deployments`, `nodes`, `services`, `events`, `configmaps`, `hpa`,
   … — never `secrets`), and every identifier must match `reK8sIdentifier` so no shell
   metacharacter reaches `exec`. `PromQL` runs against `PROMETHEUS_URL` only. `SearchDocs` applies
   the `Retriever`'s filter (ADR-0003). `Registry.Call` parses the arguments and returns the
   tool's error text as the observation when validation fails, so the model sees the refusal.
3. **Credentials are the tool's, not the user's, and are minimal.** The kube context used by
   `Kubectl` (`COPILOT_KUBE_CONTEXT`) should be bound to a ClusterRole with only `get`/`list`/`watch` on the allowed kinds in the
   allowed namespaces, so even a bug in the allowlist cannot escalate. No tool has write credentials
   anywhere in its environment.
4. **Budgets in the loop.** `Agent.MaxSteps` (default 6, `--max-steps`) is enforced by `Agent.Run`
   (`Result.Stopped = max_steps`), each observation is truncated by `tokens.Truncate` before it
   reaches the model, and `Kubectl.Timeout` (default 20 s) bounds each exec.
5. **Observations are untrusted.** Every tool output passes through `Agent.observe`:
   `safety.RedactSecrets`, `safety.DetectInjection` (a warning on the result when it fires) and
   `safety.WrapUntrusted` delimiters before it reaches the model (ADR-0008).
6. **Adding a tool requires a new ADR**, a red-team pass on that tool, and — for anything
   non-read-only — a design for human confirmation *outside* the model loop, a separate credential,
   and an audit record. The agent may *propose* an action in its final answer; it may not take one.

## Alternatives considered

| Option | Why not |
|---|---|
| **Read-write tools with a confirmation prompt inside the loop** ("are you sure?" asked of the model or the user in-conversation) | The confirmation lives in the same channel as the injection; a model that has been redirected will confirm. A human "y/n" in the CLI is better but still conditions an irreversible action on the user trusting a summary produced from untrusted text under time pressure. If remediation is ever added it must be a separate, deliberate workflow, not a branch in the triage loop. |
| **Prompt-level restrictions only** ("you may only read; never call kubectl delete") | Cheap and worth having, but soft: the adversarial suite demonstrates that prompt rules are bypassed by a minority of injections. They reduce how often the allowlist is tested; they are not the control. |
| **Trust the model's judgement plus audit logging** | Audit tells you what happened after the outage. |
| **Per-user impersonation** (the agent acts with the requesting engineer's credentials, so it can do whatever they can) | Appealing for accountability, but turns every successful injection into an action with the engineer's full privileges, which are typically broad for platform engineers. The copilot's own minimal identity is the safer default; per-user *read* filtering is handled at the retrieval layer instead. |
| **Dry-run / plan-only write tools** (`kubectl apply --dry-run=server`) | Reasonable middle ground for proposing changes; deferred because even dry-run requires broader RBAC and the value on triage questions is small. Candidate for a future ADR. |
| **Sandboxed execution (run arbitrary `kubectl` in a restricted container)** | Sandboxing limits the *host* blast radius, not the *cluster* one; the cluster is what matters here. |

## Consequences

**Positive.**
- Containment is 100 percent by construction on the adversarial suite: no injection can cause a
  mutation because no mutation is expressible. This is the property the course can actually promise.
- The allowlist and RBAC together mean a bug in one layer is caught by the other.
- The design is familiar to the audience — it is least-privilege service accounts applied to an
  agent — which makes the safety argument short in a design review.
- Budgets bound cost and latency; the p95 of steps per run is a useful regression signal.

**Negative / costs.**
- The agent cannot fix anything. The biggest time saving an agent could offer — remediation — is
  deliberately out of scope, and the final answer says so, pointing the engineer to the runbook step
  to perform.
- The namespace allowlist must be configured per deployment; an empty `COPILOT_NAMESPACES` lets the
  agent read any namespace the kube context can, and a wide one weakens the point. Set it per
  deployment; the verb and resource allowlists are fixed in code.
- Some legitimate triage questions are refused: "what's in the secret the pod mounts?" cannot be
  answered, by design, and the refusal must be explained rather than silently returning nothing.
- Read-only is not risk-free: `logs` and `describe` can surface secrets printed by applications or
  in environment variables. `safety.RedactPII` runs on observations, and the `logs` tool caps lines
  and strips known credential shapes, but this is the residual risk the team accepts and documents.
- Adding any new tool is slow by design (ADR plus red-team); contributors will find this friction,
  which is the point.

**Follow-ups.**
- Write the "proposal" output format: the agent's final answer may include a fenced
  `proposed-actions` block with exact commands, clearly marked as not executed, so a human can copy
  and run them with their own credentials.
- Evaluate a separate, non-agentic `copilot rollback` command with explicit human confirmation and
  its own credential as the first remediation feature, under its own ADR.
- Extend the adversarial suite with tool-specific cases whenever a tool's allowlist changes.

