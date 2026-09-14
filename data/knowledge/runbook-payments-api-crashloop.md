# Runbook: payments-api CrashLoopBackOff

| Field | Value |
|---|---|
| Service | `payments-api` |
| Cluster / namespace | `prod-us-east-1` / `payments` |
| Owning team | Payments Platform (`@payments-platform-oncall`) |
| Alert names | `PaymentsApiCrashLooping`, `PaymentsApiOOMKilled`, `PaymentsApiReadinessFailing` |
| Default severity | SEV2 (SEV1 if checkout success rate < 99.0% for 5 minutes) |
| Dashboards | Grafana `Payments / payments-api overview`, `Payments / payments-api pods` |
| Last reviewed | 2026-07-02 by Udaykishore Resu |

## Symptoms

- Alert `PaymentsApiCrashLooping` fires when `kube_pod_container_status_restarts_total{namespace="payments",container="payments-api"}` increases by more than 3 in 10 minutes on any pod.
- `checkout-web` starts returning HTTP 502 to customers because its upstream (`payments-api.payments.svc.cluster.local:8080`) has fewer than 4 ready endpoints. The `PodDisruptionBudget` for `payments-api` requires `minAvailable: 4`, so rollouts also stall.
- `ledger-worker` lag on the Kafka topic `payments.ledger.events` grows, because `payments-api` is the producer and retries with backoff.

## Triage (first 5 minutes)

```bash
kubectl --context prod-us-east-1 -n payments get pods -l app=payments-api
kubectl --context prod-us-east-1 -n payments describe pod <pod> | sed -n '/Last State/,/Ready/p'
kubectl --context prod-us-east-1 -n payments logs <pod> -c payments-api --previous --tail=200
kubectl --context prod-us-east-1 -n payments rollout history deployment/payments-api
```

Read `Last State` → `Reason`. The reason decides which branch below you follow.

## Branch A: Reason is `OOMKilled` (exit code 137)

This is the most common cause since the v2.14 release line. The container has `resources.limits.memory: 1Gi` and `requests.memory: 768Mi`. Heap growth is driven almost entirely by the ledger batch size.

**Known trigger:** ConfigMap `payments-api-config` version **v42** (rolled out 2026-06-18) raised `LEDGER_BATCH_SIZE` from `500` to `5000`. At 5000 events per batch the process peaks above 1.3Gi during the nightly reconciliation window (02:00–02:40 UTC) and is OOMKilled.

**Remediation (do both, in this order):**

1. Raise the memory limit to **1.5Gi** so the pods stop dying while you fix config:
   ```bash
   kubectl --context prod-us-east-1 -n payments set resources deployment/payments-api \
     -c payments-api --limits=memory=1.5Gi --requests=memory=1Gi
   ```
2. Roll back the ConfigMap to **v41** (`LEDGER_BATCH_SIZE=500`) and restart:
   ```bash
   kubectl --context prod-us-east-1 -n payments apply -f config/payments-api-config-v41.yaml
   kubectl --context prod-us-east-1 -n payments rollout restart deployment/payments-api
   kubectl --context prod-us-east-1 -n payments rollout status deployment/payments-api --timeout=5m
   ```

Do not raise the limit above 2Gi without talking to Platform Infra: nodes in the `payments-general` node group are `m6i.2xlarge` (32 GiB) and already run 6–8 payments pods each.

**Verify:** `container_memory_working_set_bytes{container="payments-api"}` should sit between 600Mi and 900Mi after 10 minutes; restart counter stops increasing.

## Branch B: Reason is `Error` (exit code 1) within 2 seconds of start

The binary failed to start. Almost always one of:

- **Missing secret key.** Log line `config: required key PSP_API_KEY not set`. The secret `payments-api-secrets` in the `payments` namespace is synced from AWS Secrets Manager path `prod/payments/payments-api` by External Secrets Operator. Check `kubectl -n payments get externalsecret payments-api-secrets` and the ESO logs in `platform` namespace.
- **Bad ConfigMap value.** Log line `config: LEDGER_BATCH_SIZE must be between 1 and 5000`. Compare the live ConfigMap with the last good version in Git (`config/payments-api-config-v41.yaml`).
- **Database migration lock.** Log line `migrate: database is locked by another migrator`. A previous rollout left a lock row in `schema_migrations_lock` in `payments-db`. Clear it with the `payments-db-unlock` Job (`kubectl -n payments create job --from=cronjob/payments-db-unlock unlock-$(date +%s)`), then restart the deployment.

## Branch C: Readiness probe failing, container running

Pods show `Running` but `0/1 Ready`. The readiness probe is `GET /readyz` on port 8080 every 5 seconds with `failureThreshold: 3`. `/readyz` returns 503 when:

- Redis at `redis-payments.platform.svc.cluster.local:6379` is unreachable (idempotency key store). Check `kubectl -n platform get pods -l app=redis-payments`. See postmortem `postmortem-2026-03-redis-eviction.md` for the eviction incident.
- Postgres `payments-db` connection pool is exhausted (`pgbouncer: no more connections allowed`). Default pool is 40 connections per pod; the HPA scales 6→20 replicas, which can exceed the 600-connection ceiling on the RDS instance. Scale the HPA `maxReplicas` down to 14 as a stopgap.

## Rollback

If the crash loop started within 30 minutes of a deploy, roll back first and diagnose second:

```bash
kubectl --context prod-us-east-1 -n payments rollout undo deployment/payments-api
```

The previous known-good image is `registry.internal/payments/payments-api:v2.14.2`; v2.14.3 is the current release.

## Escalation

Follow `oncall-escalation-policy.md`. If the incident is a SEV1, page Platform Infra secondary as well because node capacity in `payments-general` is usually the constraint when you scale up.

## Related

- `k8s-payments-deployment.yaml` — the live Deployment manifest with resource limits and probes
- `postmortem-2026-03-redis-eviction.md` — why `/readyz` depends on Redis
- `runbook-node-not-ready.md` — if several pods on the same node crash together
