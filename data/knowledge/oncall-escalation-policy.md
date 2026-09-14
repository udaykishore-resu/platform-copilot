# On-call and escalation policy — Payments and Platform

| Field | Value |
|---|---|
| Applies to | `payments-api`, `checkout-web`, `ledger-worker` (Payments Platform); `prod-us-east-1` cluster, `platform` namespace add-ons (Platform Infra) |
| Paging tool | PagerDuty. Services: `payments-platform-prod`, `platform-infra-prod` |
| Incident channel | `#inc-payments` (Slack, created per incident by the `/incident` bot), `#platform-oncall` for infra chatter |
| Status page | `status.internal` — updated by the incident commander for SEV1/SEV2 |
| Owner of this document | Head of Platform Engineering |
| Last reviewed | 2026-08-01 |

## Rotations

| Rotation | Schedule | Handoff | Shift | Who |
|---|---|---|---|---|
| Payments Platform primary | `payments-platform-primary` | Tuesday 10:00 UTC | 1 week | 8 engineers |
| Payments Platform secondary | `payments-platform-secondary` | Tuesday 10:00 UTC | 1 week | same pool, offset by one slot |
| Platform Infra primary | `platform-infra-primary` | Wednesday 10:00 UTC | 1 week | 6 engineers |
| Platform Infra secondary | `platform-infra-secondary` | Wednesday 10:00 UTC | 1 week | same pool, offset |
| Engineering manager escalation | `eng-manager-escalation` | Monthly | 1 month | Payments EM, Platform EM |

Primary carries the pager. Secondary is paged automatically if primary has not acknowledged within the timeout for the severity (below). Secondary is also the first person primary should pull in for a second pair of eyes — that is not an escalation, it is expected.

## Severity definitions

| Severity | Definition | Examples | Acknowledge within | Page |
|---|---|---|---|---|
| SEV1 | Customers cannot pay, or money is at risk of being moved incorrectly | checkout success rate < 99.0% for 5 min; any confirmed duplicate capture; `payments-api` < 2 ready endpoints | 5 minutes | Primary, secondary, and EM escalation simultaneously; status page within 15 min |
| SEV2 | Material degradation with a workaround, or a correctness risk that has not yet reached customers | `payments-api` CrashLoopBackOff on ≥ 2 pods; `redis-idempotency` evictions or > 80% memory; ≥ 2 `payments-general` nodes NotReady; any `platform-system` node NotReady; ledger lag > 10 min | 15 minutes | Primary; secondary after 10 min unacknowledged; status page if customer-visible |
| SEV3 | Single-component fault, fully absorbed by redundancy | one node NotReady; one pod restart loop that self-heals; non-prod environments | next business hour | Primary (low-urgency notification, no phone call outside 08:00–20:00 local) |
| SEV4 | Cosmetic or tooling | dashboard broken; flaky alert | ticket | none |

Severity is set by whoever acknowledges the page and may be changed at any time. When in doubt, page higher: downgrading is free, upgrading late is what made INC-2026-0314 a 47-minute incident instead of a 15-minute one.

## Escalation path

1. **Primary on-call** acknowledges within the severity timeout and starts a thread in `#inc-payments` (or `#platform-oncall` for infra-only SEV3).
2. **Secondary on-call** is paged automatically after **10 minutes** without acknowledgement for SEV2, **5 minutes** for SEV1, or immediately when primary asks.
3. **Cross-team page.** Payments Platform pages Platform Infra (`platform-infra-prod`, "Request infra help" in PagerDuty) when the cause looks like nodes, networking, Redis, Kafka, DNS, or secrets sync. Platform Infra pages Payments Platform when a cluster problem is causing payments alerts. Do not debug the other team's service for more than 10 minutes before paging them.
4. **Engineering manager escalation** (`eng-manager-escalation`) after **30 minutes** of a SEV2 without a clear mitigation path, or immediately for any SEV1. The EM's job is decisions and communication (rollback authority, customer comms, pulling in more engineers), not debugging.
5. **Head of Platform Engineering** is informed by the EM for any SEV1 and for any SEV2 longer than 2 hours.

## Incident commander

For SEV1 and SEV2 the first responder is the incident commander (IC) until they explicitly hand off. The IC:

- Posts a status update in `#inc-payments` every 15 minutes for SEV1, every 30 minutes for SEV2, even if the update is "no change".
- Keeps a timeline in the channel (the `/incident` bot pins it). This becomes the postmortem timeline.
- Has rollback authority without further approval for any deployment in the `payments` or `platform` namespaces. Rolling back is always an acceptable first action.
- Decides when customer impact has ended and closes the incident.

## Runbook-first rule

Every paging alert links to a runbook. Follow the runbook before improvising:

| Alert | Runbook |
|---|---|
| `PaymentsApiCrashLooping`, `PaymentsApiOOMKilled`, `PaymentsApiReadinessFailing` | `runbook-payments-api-crashloop.md` |
| `KubeNodeNotReady`, `NodeDiskPressure`, `NodeENIExhausted` | `runbook-node-not-ready.md` |
| `RedisPaymentsEvictions`, `RedisIdempotencyMemoryHigh` | `postmortem-2026-03-redis-eviction.md` (remediation section) until a dedicated runbook lands (PLAT-1950) |

If a runbook is wrong or missing, fixing it is part of closing the incident.

## Change freeze

No production deploys to `payments` or `platform` namespaces while a SEV1 or SEV2 is open, except rollbacks and fixes approved by the IC. Atlantis and Argo CD both check the incident status API and refuse applies automatically.

## Handoff

Outgoing primary posts a handoff note in `#platform-oncall` before 10:00 UTC on handoff day: open incidents, silenced alerts (with expiry), risky changes in the last week, and anything that paged more than once. Silences longer than 7 days need an EM approval and a ticket.

## Postmortems

- Required for every SEV1 and SEV2; optional for SEV3 if the on-call learned something.
- Draft within 3 business days, review meeting within 10. Blameless: the document names components and decisions, not people, except in the author field.
- Action items get a priority (P0 fix-now, P1 this quarter, P2 backlog) and an owner team. P0 and P1 items are tracked to completion in the weekly platform review; the postmortem is not closed until they are done.
- Template and past postmortems live alongside this document; see `postmortem-2026-03-redis-eviction.md` for a completed example.
