# qspill-controller — Kubernetes manifests

This directory contains the Kustomize layout for deploying
`qspill-controller`.

## Structure

```
config/
├── base/                       # Workload + config
│   ├── kustomization.yaml
│   ├── namespace.yaml          # qspill-controller-system
│   ├── configmap.yaml          # SpillPolicy registry & defaults
│   ├── service.yaml            # ClusterIP exposing /healthz
│   └── deployment.yaml         # Deployment behind leader election
└── rbac/                       # Cluster-scoped RBAC (shipped once per cluster)
    ├── kustomization.yaml
    ├── service_account.yaml
    ├── cluster_role.yaml       # Volcano Queues / PodGroups, Nodes, Pods, Events
    ├── cluster_role_binding.yaml
    ├── role.yaml               # coordination.k8s.io/leases (leader election)
    └── role_binding.yaml
```

> **Workload kind:** the controller uses a `Deployment` (not a `StatefulSet`)
> because it relies on `coordination.k8s.io/Lease` for leader election
> (DESIGN.md §10.4). Pods are interchangeable; only the leaseholder reconciles
> Volcano Queues. This keeps rolling updates safe — the new pod simply waits
> for the lease before becoming the writer.

## Configuration loading

The controller loads `config.yaml` from a ConfigMap mounted at
`/etc/qspill-controller/config.yaml`. It also informer-watches the same
ConfigMap and atomically swaps its in-memory `PolicyRegistry` on every valid
change (DESIGN.md §5). Pod identity is supplied via the downward API
(`POD_NAME`, `POD_NAMESPACE`) and used for the leader-election lease identity.

| Path                                | Source            | Purpose                                |
|-------------------------------------|-------------------|----------------------------------------|
| `defaults.nodeGroupLabelKey`        | ConfigMap         | Node label whose value names a group   |
| `defaults.action`                   | ConfigMap         | `Nope` (dry-run) or `Patch` (SSA)      |
| `defaults.reconcileResyncPeriod`    | ConfigMap         | Forced re-enqueue interval             |
| `defaults.thresholds.*`             | ConfigMap         | timeOn / timeOff / timePendingMax / hysteresis |
| `policies[].queueName`              | ConfigMap         | Identifies the SpillPolicy by spec.queue |
| `policies[].dedicatedNodeGroup`     | ConfigMap         | Steady-state nodegroup                 |
| `policies[].overflowNodeGroup`      | ConfigMap         | Spill nodegroup                        |

## Deployment

### Prerequisites

- Kubernetes 1.26+
- `kubectl` configured for the target cluster
- `kustomize` 3.8+ (or use `kubectl apply -k` directly)
- The Volcano CRDs (`Queue`, `PodGroup`) must already be installed in the
  cluster.

### Deploy

```bash
# Deploy RBAC (once per cluster):
make deploy-rbac

# Deploy the controller:
make deploy
```

## RBAC contract (DESIGN.md §3, §10.3, Appendix A)

| API group                     | Resources                       | Verbs                              |
|-------------------------------|---------------------------------|------------------------------------|
| `scheduling.volcano.sh`       | `queues`                        | `get,list,watch,patch`             |
| `scheduling.volcano.sh`       | `podgroups`                     | `get,list,watch`                   |
| `""` (core)                   | `nodes,pods,configmaps`         | `get,list,watch`                   |
| `""` (core)                   | `events`                        | `get,list,watch,create,patch`      |
| `coordination.k8s.io`         | `leases` (namespace-scoped)     | `get,list,watch,create,update,patch,delete` |

> Note that `update` is intentionally **not** granted on `queues`. The
> controller writes via Server-Side Apply (`patch`) under `FieldManager:
> qspill-controller`, which only owns
> `spec.affinity.nodeGroupAffinity` and `spill.example.com/*` annotations
> (DESIGN.md §10.3). It never touches `spec.capability` or any other field
> belonging to other controllers / human operators.

## Customization

### Policies

Edit the `configmap.yaml` in `config/base/` to add SpillPolicies. Reloads are
hot-applied — no pod restart required.

### Probes

- `/healthz` on container port `8081` (Service port `probe`)

## Troubleshooting

```bash
# Effective configuration:
kubectl -n qspill-controller-system exec deploy/qspill-controller -- \
  cat /etc/qspill-controller/config.yaml

# Leader lease:
kubectl -n qspill-controller-system get lease qspill-controller -o yaml

# Recent reconcile events:
kubectl -n qspill-controller-system logs deploy/qspill-controller \
  | grep -E 'reconcile|spill|steady'
```
