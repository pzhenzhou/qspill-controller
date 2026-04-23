# qspill-controller

A Kubernetes controller for multi-tenant queue spill control with the [Volcano](https://volcano.sh) batch scheduler.

## Overview

`qspill-controller` implements a `QSpillPolicy` custom resource that monitors Volcano queue utilization and automatically spills excess workloads to designated target queues when a utilization threshold is exceeded. When utilization drops back below the threshold, spilled resources are reclaimed gracefully.

## Architecture

```
QSpillPolicy CR
    │
    ▼
QSpillPolicyReconciler
    │
    ├─► Watch Volcano Queue (source)
    │       └─► Calculate utilization (allocated / capacity)
    │
    ├─► If utilization > threshold → Spill
    │       └─► Update target Queue.Spec.Deserved
    │
    └─► If utilization ≤ threshold (was spilling) → Reclaim
            └─► Restore target Queue.Spec.Deserved
```

## CRD: QSpillPolicy

```yaml
apiVersion: scheduling.qspill.io/v1alpha1
kind: QSpillPolicy
metadata:
  name: example-policy
  namespace: default
spec:
  sourceQueue: tenant-a-queue
  spillTrigger:
    utilizationThreshold: "0.8"
    evaluationPeriod: 60s
  spillTargets:
    - queueName: shared-pool
      priority: 100
      maxSpillCapacity:
        cpu: "10"
        memory: "20Gi"
  reclaimPolicy:
    strategy: Graceful
    gracePeriod: 5m
```

### Fields

| Field | Description |
|-------|-------------|
| `spec.sourceQueue` | The Volcano queue to monitor |
| `spec.spillTrigger.utilizationThreshold` | Utilization ratio (0.0–1.0) that triggers spill |
| `spec.spillTrigger.evaluationPeriod` | How often to evaluate (default: 60s) |
| `spec.spillTargets[].queueName` | Target Volcano queue to receive spilled resources |
| `spec.spillTargets[].priority` | Priority order for target selection (higher = first) |
| `spec.spillTargets[].maxSpillCapacity` | Maximum resources to spill to this target |
| `spec.reclaimPolicy.strategy` | `Immediate` or `Graceful` (default) |
| `spec.reclaimPolicy.gracePeriod` | Wait time before reclaim (default: 5m) |

## Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `qspill_queue_utilization` | Gauge | Queue utilization ratio (0.0–1.0) |
| `qspill_spill_events_total` | Counter | Total spill events by source/target queue |
| `qspill_reclaim_events_total` | Counter | Total reclaim events by source/target queue |
| `qspill_active_spills` | Gauge | Number of active spills per source queue |

## Installation

```bash
# Install the CRD
kubectl apply -f config/crd/

# Deploy RBAC and controller
kubectl apply -f config/rbac/
kubectl apply -f config/manager/
```

## Development

```bash
# Run tests
make test

# Build binary
make build

# Build Docker image
make docker-build
```

## Prerequisites

- Kubernetes 1.27+
- Volcano 1.9+
- Go 1.21+
