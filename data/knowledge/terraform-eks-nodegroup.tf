# EKS managed node groups for prod-us-east-1
#
# Owner: Platform Infra. Applied via Atlantis from the platform-infra repo; plans are posted to
# #platform-oncall and need one approval from a Platform Infra engineer.
#
# Two node groups:
#   payments-general  — stateless payments workloads (payments-api, checkout-web, ledger-worker)
#   platform-system   — cluster add-ons (CoreDNS, ingress-nginx, redis-payments, redis-idempotency,
#                       External Secrets Operator, cluster-autoscaler, monitoring)
#
# Related: runbook-node-not-ready.md (DiskPressure, ENI exhaustion, PLEG), k8s-payments-deployment.yaml

locals {
  cluster_name = "prod-us-east-1"
  k8s_version  = "1.30"
  common_tags = {
    Environment = "prod"
    Cluster     = local.cluster_name
    ManagedBy   = "terraform"
    Team        = "platform-infra"
  }
}

# Latest EKS-optimised AL2023 AMI for the cluster's Kubernetes version. Pinned through the launch
# template version so node replacements are deliberate, not surprise upgrades.
data "aws_ssm_parameter" "eks_ami" {
  name = "/aws/service/eks/optimized-ami/${local.k8s_version}/amazon-linux-2023/x86_64/standard/recommended/image_id"
}

# ---------------------------------------------------------------------------------------------
# payments-general: m6i.2xlarge (8 vCPU, 32 GiB). Sized for 6-8 payments-api pods per node at a
# 1Gi limit plus checkout-web and ledger-worker. ENI math: 4 ENIs x 15 IPs = 58 usable pod IPs,
# so a node runs out around 55 pods. PLAT-1790 (prefix delegation) is approved and lifts that.
# ---------------------------------------------------------------------------------------------
resource "aws_launch_template" "payments_general" {
  name_prefix   = "${local.cluster_name}-payments-general-"
  image_id      = data.aws_ssm_parameter.eks_ami.value
  instance_type = "m6i.2xlarge"

  block_device_mappings {
    device_name = "/dev/xvda"
    ebs {
      # 100 GiB today. DiskPressure incidents (runbook Branch B) come from image churn and
      # ledger-worker debug logs. PLAT-1842 raises this to 200 GiB.
      volume_size           = 100
      volume_type           = "gp3"
      encrypted             = true
      delete_on_termination = true
    }
  }

  metadata_options {
    http_tokens                 = "required" # IMDSv2 only
    http_put_response_hop_limit = 1          # pods cannot reach the node's instance role
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_eks_node_group" "payments_general" {
  cluster_name    = local.cluster_name
  node_group_name = "payments-general"
  node_role_arn   = var.node_role_arn
  subnet_ids      = var.private_subnet_ids # three AZs: us-east-1a/b/c
  version         = local.k8s_version

  scaling_config {
    min_size     = 3  # one per AZ; the payments-api PDB (minAvailable 4) needs at least 2 nodes up
    desired_size = 6  # cluster-autoscaler owns this at runtime; ignored below
    max_size     = 12
  }

  update_config {
    max_unavailable = 1 # roll one node at a time; payments-api rollouts use maxUnavailable 0
  }

  launch_template {
    id      = aws_launch_template.payments_general.id
    version = aws_launch_template.payments_general.latest_version
  }

  labels = {
    nodegroup = "payments-general" # payments-api nodeSelector matches this
    workload  = "stateless"
  }

  # No taints: general-purpose pool. Add-ons are steered to platform-system by nodeSelector.

  tags = merge(local.common_tags, {
    "k8s.io/cluster-autoscaler/enabled"               = "true"
    "k8s.io/cluster-autoscaler/${local.cluster_name}" = "owned"
  })

  lifecycle {
    ignore_changes = [scaling_config[0].desired_size] # autoscaler changes it
  }
}

# ---------------------------------------------------------------------------------------------
# platform-system: m6i.xlarge (4 vCPU, 16 GiB), fixed at 3 nodes, tainted. Losing one is SEV2
# (runbook-node-not-ready.md) because CoreDNS and redis-idempotency run here. Capacity is fixed on
# purpose: the autoscaler must not churn these nodes. Uses the default EKS launch template with an
# 80 GiB gp3 root volume.
# ---------------------------------------------------------------------------------------------
resource "aws_eks_node_group" "platform_system" {
  cluster_name    = local.cluster_name
  node_group_name = "platform-system"
  node_role_arn   = var.node_role_arn
  subnet_ids      = var.private_subnet_ids
  version         = local.k8s_version
  instance_types  = ["m6i.xlarge"]
  disk_size       = 80

  scaling_config {
    min_size     = 3
    desired_size = 3
    max_size     = 3
  }

  labels = {
    nodegroup = "platform-system"
    workload  = "system"
  }

  taint {
    key    = "dedicated"
    value  = "platform-system"
    effect = "NO_SCHEDULE" # add-ons carry a matching toleration
  }

  tags = local.common_tags
}

variable "node_role_arn" {
  description = "IAM role for worker nodes. Must appear in the aws-auth ConfigMap as system:nodes or kubelets fail TLS bootstrap (runbook Branch A)."
  type        = string
}

variable "private_subnet_ids" {
  description = "Private subnets in us-east-1a, us-east-1b, us-east-1c."
  type        = list(string)
}

output "payments_general_asg_name" {
  value       = aws_eks_node_group.payments_general.resources[0].autoscaling_groups[0].name
  description = "Use with `aws autoscaling terminate-instance-in-auto-scaling-group` when replacing a bad node."
}
