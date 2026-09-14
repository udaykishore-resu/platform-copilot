# Runbook: Kubernetes node NotReady (EKS, prod-us-east-1)

| Field | Value |
|---|---|
| Cluster | `prod-us-east-1` (EKS 1.30, AWS account `prod-platform`) |
| Node groups | `payments-general` (m6i.2xlarge, 3–12 nodes), `platform-system` (m6i.xlarge, 3 nodes) |
| Owning team | Platform Infra (`@platform-infra-oncall`) |
| Alert names | `KubeNodeNotReady`, `KubeNodeUnreachable`, `NodeDiskPressure`, `NodeENIExhausted` |
| Default severity | SEV3 for a single node; SEV2 if two or more nodes in the same node group, or if `payments-api` ready endpoints drop below 4 |
| Dashboards | Grafana `Platform / Node health`, `Platform / EKS capacity` |
| Last reviewed | 2026-08-11 by Platform Infra |

## What "NotReady" means

The kubelet on the node has not posted a healthy `NodeStatus` to the API server within `node-monitor-grace-period` (40 seconds by default). After `pod-eviction-timeout` (5 minutes), the controller manager taints the node `node.kubernetes.io/unreachable:NoExecute` and pods with the default toleration (300 seconds) are evicted and rescheduled. For `payments-api` that means a node loss can take 5–10 minutes to fully recover unless you intervene.

## Triage (first 5 minutes)

```bash
kubectl --context prod-us-east-1 get nodes -o wide
kubectl --context prod-us-east-1 describe node <node> | sed -n '/Conditions:/,/Addresses:/p'
kubectl --context prod-us-east-1 get pods -A --field-selector spec.nodeName=<node> -o wide
aws ec2 describe-instance-status --instance-ids <i-...> --region us-east-1
```

Look at the `Conditions` block. The condition that is `True`/`Unknown` tells you the branch.

## Branch A: `Ready=Unknown`, kubelet stopped posting status

Usually the instance is gone or frozen.

1. Check `aws ec2 describe-instance-status`. If the instance is `impaired` or in `stopping`, the ASG will replace it; wait for the new node and move on to **Speed up recovery** below.
2. If the instance is `running` and healthy, SSM into it: `aws ssm start-session --target <i-...>` then `sudo journalctl -u kubelet --since "15 min ago" | tail -100`.
3. Common kubelet log lines:
   - `PLEG is not healthy: pleg was last seen active 3m0s ago` — containerd is wedged, usually by a pod with thousands of exited containers. `sudo systemctl restart containerd kubelet`. If it recurs, cordon and replace the node.
   - `failed to renew lease` / TLS errors — the node's IAM role lost the `eks:node` mapping in the `aws-auth` ConfigMap. Check `kubectl -n kube-system get cm aws-auth -o yaml` against Terraform (`terraform-eks-nodegroup.tf`).
   - No log lines at all since a fixed time — kernel hang. Terminate the instance; let the ASG replace it.

## Branch B: `DiskPressure=True`

The root volume for `payments-general` nodes is 100 GiB gp3. Disk pressure above 85% triggers image garbage collection; above 90% the kubelet evicts pods.

```bash
# on the node via SSM
df -h /var/lib/containerd /var/log
sudo crictl images | wc -l
sudo du -sh /var/log/pods/* | sort -h | tail
```

- Most common cause: `ledger-worker` writing debug logs at 200 MB/min after `LOG_LEVEL=debug` was left on. Fix the ConfigMap, then `sudo crictl rmi --prune`.
- Second most common: image churn from many canary deployments. `sudo crictl rmi --prune` reclaims 20–40 GiB.
- Long-term fix tracked in PLAT-1842: raise root volume to 200 GiB in `terraform-eks-nodegroup.tf`.

## Branch C: pods stuck `ContainerCreating`, `NodeENIExhausted` firing

The VPC CNI ran out of IP addresses. An `m6i.2xlarge` supports 4 ENIs × 15 IPs = 58 usable pod IPs. With `payments-api`, `checkout-web`, `ledger-worker` and daemonsets, a node runs out at roughly 55 pods. The CNI has `WARM_IP_TARGET=5` and `MINIMUM_IP_TARGET=20` in `prod-us-east-1`.

- Check: `kubectl -n kube-system logs -l k8s-app=aws-node --tail=50 | grep -i "no available IP"`.
- Short term: cordon the node so the scheduler stops placing pods on it.
- Medium term: enable prefix delegation (`ENABLE_PREFIX_DELEGATION=true`) — this is change request PLAT-1790, approved but not yet applied.

## Branch D: `MemoryPressure=True`

Node-level memory pressure almost always traces back to `payments-api` pods running without a limit that matches usage. See `runbook-payments-api-crashloop.md` Branch A (OOMKilled, ConfigMap v42). Check `kubectl top pods -n payments --sort-by=memory`.

## Speed up recovery

Do not wait for the 5-minute eviction timeout when `payments-api` is affected:

```bash
kubectl --context prod-us-east-1 cordon <node>
kubectl --context prod-us-east-1 drain <node> --ignore-daemonsets --delete-emptydir-data --grace-period=30 --timeout=120s
```

The `payments-api` PodDisruptionBudget (`minAvailable: 4`) will block the drain if fewer than 5 replicas are ready elsewhere. In that case scale the deployment up first: `kubectl -n payments scale deployment/payments-api --replicas=8`. The HPA will settle it back down afterwards.

Verify replacement capacity: `kubectl get nodes -l nodegroup=payments-general` should show at least the ASG `min_size` (3) nodes `Ready`, and `cluster-autoscaler` logs in `platform` namespace should show a scale-up event within 2 minutes if pods are `Pending`.

## When to replace instead of fix

Replace the instance (terminate it; the ASG recreates it) when:

- The same node flips NotReady twice in 24 hours.
- `PLEG is not healthy` recurs after a containerd restart.
- The instance is older than the current launch template version (`aws autoscaling describe-auto-scaling-instances` shows `LaunchTemplate.Version` behind `$Latest`).

```bash
aws autoscaling terminate-instance-in-auto-scaling-group \
  --instance-id <i-...> --no-should-decrement-desired-capacity --region us-east-1
```

## Escalation

Single node NotReady is SEV3 and does not page. Two or more nodes in `payments-general`, or any node in `platform-system` (which hosts CoreDNS, ingress-nginx, `redis-payments` and External Secrets Operator), is SEV2: page Platform Infra primary and notify `#inc-payments` per `oncall-escalation-policy.md`.

## Related

- `terraform-eks-nodegroup.tf` — instance type, sizes, labels and disk for both node groups
- `runbook-payments-api-crashloop.md` — symptoms that look like a node problem but are a pod problem
