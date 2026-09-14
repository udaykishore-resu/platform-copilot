package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Kubectl exposes `kubectl get/describe/logs/top` and nothing else. The
// allowlist is enforced in code, not in the prompt: a prompt injection can
// talk the model into asking for `kubectl delete`, but the tool will refuse.
// Resources and namespaces are validated against a strict pattern so no shell
// metacharacters can ever reach exec.
type Kubectl struct {
	Binary  string // default "kubectl"
	Context string // optional kube context
	// Namespaces restricts which namespaces may be queried (empty = any).
	Namespaces []string
	Timeout    time.Duration
	// Fake, when set, replaces exec with a canned response keyed by verb —
	// used for the mock provider demo and tests so `make run` needs no cluster.
	Fake map[string]string
}

var (
	allowedVerbs     = map[string]bool{"get": true, "describe": true, "logs": true, "top": true, "explain": true, "api-resources": true}
	reK8sIdentifier  = regexp.MustCompile(`^[a-z0-9]([a-z0-9.\-/]*[a-z0-9])?$`)
	allowedResources = map[string]bool{"pods": true, "pod": true, "po": true, "deployments": true, "deploy": true, "nodes": true, "node": true, "no": true,
		"services": true, "svc": true, "events": true, "ev": true, "configmaps": true, "cm": true, "hpa": true, "horizontalpodautoscalers": true,
		"replicasets": true, "rs": true, "statefulsets": true, "sts": true, "daemonsets": true, "ds": true, "ingress": true, "ing": true,
		"namespaces": true, "ns": true, "pvc": true, "persistentvolumeclaims": true, "jobs": true, "cronjobs": true, "endpoints": true}
)

// Tools returns the kubectl tool set for the registry.
func (k *Kubectl) Tools() []Tool {
	return []Tool{
		{
			Name:        "kubectl_get",
			Description: "Read-only `kubectl get <resource> [name] -n <namespace> -o wide`. Use to see pod status, restarts, node readiness, HPA state.",
			Schema:      Schema([]string{"resource"}, map[string]string{"resource": "e.g. pods, deployments, nodes, events, hpa", "name": "optional object name", "namespace": "namespace (omit for cluster-scoped)"}),
			Run: func(ctx context.Context, a map[string]any) (string, error) {
				return k.run(ctx, "get", a)
			},
		},
		{
			Name:        "kubectl_describe",
			Description: "Read-only `kubectl describe <resource> <name> -n <namespace>`. Shows events, conditions, last termination reason (OOMKilled, etc.).",
			Schema:      Schema([]string{"resource", "name"}, map[string]string{"resource": "e.g. pod", "name": "object name", "namespace": "namespace"}),
			Run: func(ctx context.Context, a map[string]any) (string, error) {
				return k.run(ctx, "describe", a)
			},
		},
		{
			Name:        "kubectl_logs",
			Description: "Read-only `kubectl logs <pod> -n <namespace> --tail=100 [--previous]`. Use --previous for a crashed container's last run.",
			Schema:      Schema([]string{"name"}, map[string]string{"name": "pod name", "namespace": "namespace", "container": "container name (optional)", "previous": "\"true\" to read the previous container instance"}),
			Run: func(ctx context.Context, a map[string]any) (string, error) {
				return k.run(ctx, "logs", a)
			},
		},
	}
}

func (k *Kubectl) run(ctx context.Context, verb string, a map[string]any) (string, error) {
	if !allowedVerbs[verb] {
		return "", fmt.Errorf("verb %q is not allowed (read-only tool)", verb)
	}
	str := func(key string) string { s, _ := a[key].(string); return strings.TrimSpace(s) }
	resource, name, ns := strings.ToLower(str("resource")), str("name"), str("namespace")
	if verb == "logs" {
		resource = "pod"
	}
	if resource != "" && !allowedResources[resource] {
		return "", fmt.Errorf("resource %q is not in the read allowlist", resource)
	}
	for _, v := range []string{name, ns, str("container")} {
		if v != "" && !reK8sIdentifier.MatchString(v) {
			return "", fmt.Errorf("invalid identifier %q", v)
		}
	}
	if len(k.Namespaces) > 0 && ns != "" && !contains(k.Namespaces, ns) {
		return "", fmt.Errorf("namespace %q is outside this agent's allowlist %v", ns, k.Namespaces)
	}

	args := []string{verb}
	switch verb {
	case "logs":
		if name == "" {
			return "", fmt.Errorf("logs requires a pod name")
		}
		args = append(args, name, "--tail=100")
		if c := str("container"); c != "" {
			args = append(args, "-c", c)
		}
		if str("previous") == "true" {
			args = append(args, "--previous")
		}
	default:
		args = append(args, resource)
		if name != "" {
			args = append(args, name)
		}
		if verb == "get" {
			args = append(args, "-o", "wide")
		}
	}
	if ns != "" {
		args = append(args, "-n", ns)
	} else if resource != "nodes" && resource != "node" && resource != "no" && resource != "namespaces" && resource != "ns" && verb != "api-resources" {
		args = append(args, "-A")
	}
	if k.Context != "" {
		args = append(args, "--context", k.Context)
	}

	if k.Fake != nil {
		key := verb + " " + resource
		if out, ok := k.Fake[key]; ok {
			return "$ kubectl " + strings.Join(args, " ") + "\n" + out, nil
		}
		if out, ok := k.Fake[verb]; ok {
			return "$ kubectl " + strings.Join(args, " ") + "\n" + out, nil
		}
		return "$ kubectl " + strings.Join(args, " ") + "\nNo resources found.", nil
	}

	bin := k.Binary
	if bin == "" {
		bin = "kubectl"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return "", fmt.Errorf("kubectl not found on PATH (set COPILOT_FAKE_KUBECTL=1 for the demo cluster)")
	}
	to := k.Timeout
	if to == 0 {
		to = 20 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...)
	cmd.Env = append(os.Environ(), "KUBECTL_EXTERNAL_DIFF=")
	out, err := cmd.CombinedOutput()
	res := "$ kubectl " + strings.Join(args, " ") + "\n" + string(out)
	if err != nil {
		return res, fmt.Errorf("kubectl: %v", err)
	}
	return res, nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// DemoCluster is a canned cluster state consistent with data/knowledge so the
// mock demo tells a coherent story.
func DemoCluster() map[string]string {
	return map[string]string{
		"get pods": `NAME                             READY   STATUS             RESTARTS      AGE   IP            NODE
payments-api-7c9f8d6b5-2xk9p     0/1     CrashLoopBackOff   7 (42s ago)   14m   10.42.3.17    ip-10-42-3-201.ec2.internal
payments-api-7c9f8d6b5-m4tq2     0/1     CrashLoopBackOff   7 (55s ago)   14m   10.42.5.44    ip-10-42-5-118.ec2.internal
payments-api-7c9f8d6b5-vv8hn     1/1     Running            0             14m   10.42.1.9     ip-10-42-1-77.ec2.internal
ledger-worker-5d8b7c4f9-8kq2l    1/1     Running            0             3d    10.42.2.31    ip-10-42-2-140.ec2.internal
checkout-web-6f7d8c9b4-x2n7m     1/1     Running            0             6d    10.42.4.12    ip-10-42-4-55.ec2.internal`,
		"get deployments": `NAME            READY   UP-TO-DATE   AVAILABLE   AGE   CONTAINERS     IMAGES
payments-api    1/3     3            1           41d   payments-api   registry.internal/payments-api:v2.14.0
ledger-worker   2/2     2            2           41d   ledger-worker  registry.internal/ledger-worker:v1.9.1
checkout-web    4/4     4            4           90d   checkout-web   registry.internal/checkout-web:v3.2.7`,
		"get nodes": `NAME                           STATUS   ROLES    AGE   VERSION               INSTANCE-TYPE
ip-10-42-1-77.ec2.internal     Ready    <none>   12d   v1.30.4-eks-a737599   m6i.2xlarge
ip-10-42-2-140.ec2.internal    Ready    <none>   12d   v1.30.4-eks-a737599   m6i.2xlarge
ip-10-42-3-201.ec2.internal    Ready    <none>   5d    v1.30.4-eks-a737599   m6i.2xlarge
ip-10-42-4-55.ec2.internal     Ready    <none>   12d   v1.30.4-eks-a737599   m6i.2xlarge
ip-10-42-5-118.ec2.internal    NotReady <none>   5d    v1.30.4-eks-a737599   m6i.2xlarge`,
		"get hpa": `NAME           REFERENCE                 TARGETS    MINPODS   MAXPODS   REPLICAS   AGE
payments-api   Deployment/payments-api   91%/70%    3         14        3          41d`,
		"get events": `LAST SEEN   TYPE      REASON      OBJECT                              MESSAGE
2m          Warning   BackOff     pod/payments-api-7c9f8d6b5-2xk9p    Back-off restarting failed container payments-api
3m          Warning   OOMKilling  node/ip-10-42-3-201.ec2.internal    Memory cgroup out of memory: Killed process 31337 (payments-api)
14m         Normal    Pulled      pod/payments-api-7c9f8d6b5-2xk9p    Container image "registry.internal/payments-api:v2.14.0" already present`,
		"describe": `Name:         payments-api-7c9f8d6b5-2xk9p
Namespace:    payments
Containers:
  payments-api:
    Image:          registry.internal/payments-api:v2.14.0
    State:          Waiting
      Reason:       CrashLoopBackOff
    Last State:     Terminated
      Reason:       OOMKilled
      Exit Code:    137
    Limits:
      memory:  1Gi
    Requests:
      memory:  768Mi
    Environment:
      LEDGER_BATCH_SIZE:  5000   (from ConfigMap payments-api-config v42)
Events:
  Warning  BackOff  Back-off restarting failed container`,
		"logs": `{"level":"info","msg":"starting payments-api v2.14.0","config_version":"v42"}
{"level":"info","msg":"loading ledger batch","batch_size":5000}
{"level":"warn","msg":"heap 912MiB approaching limit 1024MiB"}
fatal error: runtime: out of memory`,
	}
}
