# Postmortem: Redis key eviction caused duplicate payment authorizations

| Field | Value |
|---|---|
| Incident ID | INC-2026-0314 |
| Date | 2026-03-14 |
| Severity | SEV2 (initially paged as SEV3, upgraded at 09:41 UTC) |
| Duration | 47 minutes of customer impact (09:22–10:09 UTC) |
| Services affected | `payments-api`, `checkout-web` (both `payments` namespace, `prod-us-east-1`) |
| Root-cause component | `redis-payments` StatefulSet in the `platform` namespace |
| Incident commander | Platform Infra primary on-call |
| Author | Udaykishore Resu |
| Status | Closed — all P0/P1 action items complete as of 2026-05-30 |

## Summary

A `ledger-worker` release (v1.9.0) began caching full ledger event payloads in the shared `redis-payments` instance under the key prefix `ledger:event:` without a TTL. The instance has `maxmemory 4gb` and, at the time, `maxmemory-policy allkeys-lru`. Within 90 minutes the new keys filled the instance and Redis began evicting the least-recently-used keys — which included `payments-api` idempotency keys (prefix `idem:`, 24-hour TTL). When `checkout-web` retried timed-out requests, `payments-api` no longer recognised the idempotency key and sent a second authorization to the payment service provider (PSP). 1,212 duplicate authorization attempts were sent; 0 duplicate captures occurred because the PSP enforces its own idempotency on capture. No customer was charged twice, but 1,212 customers saw a temporary duplicate pending hold on their card statements.

## Impact

- 1,212 duplicate authorization holds, released by the PSP within 24–72 hours.
- Checkout success rate dropped from 99.7% to 98.9% for 31 minutes because `payments-api` `/readyz` flapped while Redis latency spiked, removing pods from the Service.
- 214 support tickets over the following week.
- No data loss; no duplicate captures; no ledger inconsistency (ledger is sourced from Kafka, not Redis).

## Timeline (UTC)

| Time | Event |
|---|---|
| 07:50 | `ledger-worker` v1.9.0 deployed to `prod-us-east-1`. Release notes mention "event payload cache for replay". |
| 08:05 | `redis_memory_used_bytes` for `redis-payments` begins climbing at ~40 MB/min. No alert exists on memory growth rate. |
| 09:18 | `redis-payments` reaches `maxmemory` (4 GiB). `evicted_keys` counter starts increasing at ~800/s. |
| 09:22 | First duplicate authorization (reconstructed from PSP logs). Customer impact begins. |
| 09:31 | Alert `PaymentsApiReadinessFailing` fires (SEV3). `/readyz` times out on Redis PING > 500 ms. |
| 09:36 | Payments Platform primary acknowledges; sees Redis p99 latency at 900 ms, assumes network. |
| 09:41 | PSP dashboard shows authorization volume 2.1x baseline. Incident upgraded to SEV2, Platform Infra paged, `#inc-payments` opened. |
| 09:48 | `redis-cli --bigkeys` and `INFO keyspace` show 3.1 GiB of `ledger:event:*` keys with no TTL. Correlated with the 07:50 deploy. |
| 09:52 | Decision: roll back `ledger-worker` to v1.8.2 rather than hot-patch. |
| 09:55 | `kubectl -n payments rollout undo deployment/ledger-worker` completes. Key growth stops. |
| 09:58 | `redis-cli --scan --pattern 'ledger:event:*' | xargs redis-cli UNLINK` run in batches of 1000. Memory drops to 1.2 GiB by 10:05. |
| 10:09 | Evictions stop. Duplicate authorizations stop. Customer impact ends. |
| 10:30 | `maxmemory-policy` changed from `allkeys-lru` to `volatile-lru` via the `redis-payments` ConfigMap and `CONFIG SET`, so keys without a TTL are never evicted in favour of keys with one. |
| 11:15 | Incident closed; postmortem scheduled. |

## Root cause

Two independent design decisions combined:

1. **Shared Redis for unrelated workloads.** `redis-payments` was provisioned in 2024 for `checkout-web` session cache and `payments-api` idempotency keys. `ledger-worker` was added as a third tenant in v1.9.0 without a capacity review.
2. **`allkeys-lru` on an instance holding correctness-critical keys.** Idempotency keys are not a cache; losing one changes business behaviour. Under `allkeys-lru`, a burst of new keys from any tenant can evict them.

The contributing factor was the missing TTL on `ledger:event:*` keys. Even with a TTL the instance would have filled, but `volatile-lru` would then have evicted the ledger cache rather than idempotency keys.

## What went well

- Rollback of `ledger-worker` was the right call and took under 3 minutes.
- PSP-side idempotency on capture prevented any double charge.
- The `--bigkeys` triage step was fast because the on-call had practised it in the March game day.

## What went badly

- 19 minutes between first customer impact (09:22) and the first alert (09:31), and a further 10 minutes before severity was upgraded. The paging alert was on `payments-api` readiness, a downstream symptom, not on Redis evictions.
- `payments-api` `/readyz` depends on Redis PING, so Redis latency removed healthy API pods from the Service and made the checkout impact worse than the idempotency bug alone.
- No per-tenant key-prefix memory accounting existed, so the `ledger:event:` growth was invisible until `--bigkeys` was run by hand.

## Action items

| # | Priority | Action | Owner | Status |
|---|---|---|---|---|
| 1 | P0 | Change `maxmemory-policy` to `volatile-lru` on `redis-payments` (done during incident; codified in `platform/redis-payments` Helm values) | Platform Infra | Done 2026-03-14 |
| 2 | P0 | Alert `RedisPaymentsEvictions`: page SEV2 when `rate(redis_evicted_keys_total[5m]) > 0` for 2 minutes | Platform Infra | Done 2026-03-17 |
| 3 | P1 | Move idempotency keys to a dedicated instance `redis-idempotency` in `platform` namespace, `maxmemory-policy noeviction`, 2 GiB, with alerting on `used_memory > 80%` | Payments Platform | Done 2026-04-22 |
| 4 | P1 | `ledger-worker` event cache: add 6-hour TTL and cap at 500 MiB via `MAXMEMORY`-aware client-side LRU; re-release as v1.9.1 | Payments Platform | Done 2026-04-03 |
| 5 | P1 | Change `payments-api` `/readyz` to report Redis as degraded (still ready) rather than unready; only `/healthz` fails hard | Payments Platform | Done 2026-04-10 |
| 6 | P2 | Per-prefix memory dashboard using `redis_memory_by_prefix` exporter sidecar | Platform Infra | Done 2026-05-30 |
| 7 | P2 | Add "new Redis tenant" checklist to the production readiness review template | Platform Infra | Done 2026-05-12 |

## Lessons

- Treat idempotency keys, locks and rate-limit counters as state, not cache. They belong on an instance with `noeviction` and a hard alert, not on a shared LRU cache.
- Alert on the cause (`evicted_keys`, memory growth rate) not only the symptom (readiness). The symptom alert fired 9 minutes after impact started; an eviction alert would have fired at 09:18, four minutes before the first duplicate.
- A readiness probe that fails on a soft dependency converts a partial outage into a larger one. Readiness should mean "can serve traffic", not "all dependencies are perfect".

## Related documents

- `runbook-payments-api-crashloop.md` Branch C — readiness failures when Redis is unreachable
- `oncall-escalation-policy.md` — the SEV definitions used above
- `k8s-payments-deployment.yaml` — the probe configuration changed in action item 5
