# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

A Kubernetes controller that manages Volcano Queue `spec.affinity.nodeGroupAffinity` to spill workloads between dedicated and overflow nodegroups. It never evicts pods or modifies Volcano internals — all decisions are realized by changing what the scheduler considers for future placements.

## Build & Test

```sh
make build          # builds bin/qspill-controller-bin (runs gofmt first)
make test           # builds then runs all pkg/ tests with coverage
make vet            # go vet ./...
make fmt            # gofmt -l -w -d ./pkg ./cmd

# Run a single test
go test ./pkg/evaluator/... -run TestEvaluate -v

# Run locally (requires kubeconfig)
make run ARGS="--kubeconfig=$HOME/.kube/config"
make run-dev        # with pprof + metrics enabled

# Docker
make docker-build   # builds qspill-controller:latest
```

## Architecture

The controller follows a linear pipeline executed per-policy on each reconcile:

```
Watcher (informer events) → Workqueue → Snapshot.Build → Evaluator.Evaluate → Action.Apply
```

### Key packages (all under `pkg/`):

- **api** — Domain types: `SpillPolicy`, `State` (Steady/Spill), `Decision`, `Thresholds`, annotation constants. No CRDs; config is via ConfigMap.
- **config** — Layered config loading (koanf): struct defaults → YAML/JSON/TOML file → env vars. Produces an immutable `PolicyRegistry` swapped atomically via `RegistryStore`. Env vars are prefixed `QSPILL_CONTROLLER_*`.
- **snapshot** — `Builder.Build(ctx, policyName, now)` assembles a point-in-time `Snapshot` from informer-backed listers: PodGroups, Pods, Nodes, Queue, Events. Computes demand, supply, stale-pending, autoscaler-exhaustion signals.
- **evaluator** — Pure function: `Evaluate(Snapshot) → Decision`. Implements the DESIGN.md §7 state machine with cooldown timers. No I/O, no clock dependency — everything comes from the Snapshot.
- **action** — Side-effect boundary. `Action.Apply(ctx, Decision)` writes to the cluster. Two implementations: `patch` (server-side apply to Volcano Queue) and `nope` (dry-run / log-only).
- **reconcile** — Wires snapshot → evaluator → action with hash-gated idempotency. Short-circuits when `Decision.Hash == observed annotation hash`.
- **watcher** — `Manager` owns shared informers (PodGroup, Pod, Node, Event, Queue, ConfigMap), a rate-limited workqueue, and worker goroutines. Informer handlers enqueue policy names; `PendingWorkMap` tracks in-flight work.
- **leader** — Leader election wrapper using client-go's `leaderelection` package.
- **common** — Logger factory (`common.NewLogger("module")`) using zap via logr.

### State machine

Two states: **Steady** (dedicated nodegroup only) and **Spill** (dedicated + overflow). Transitions are gated by cooldown timers (`TimeOn`, `TimeOff` with hysteresis). All state-machine bookkeeping is persisted as annotations on the Volcano Queue object (`spill.example.com/state`, `spill.example.com/condition-since`, `spill.example.com/decision-hash`), making restarts safe.

### Concurrency

Use `github.com/puzpuzpuz/xsync/v4` for concurrent maps and counters. Do not introduce `sync.Map` or hand-rolled mutex-guarded maps.

## Design References

- `DESIGN.md` — Full design document; section §7 defines the state machine, §8 the execution order.
- `IMPLEMENTATION.md` — Implementation details.

## Config / Deployment

- Kustomize base manifests in `config/base/`, RBAC in `config/rbac/`.
- Namespace: `qspill-controller-system`.
- Config file path default: `/etc/qspill-controller/config.yaml` (overridden via `--config` flag or `QSPILL_CONTROLLER_CONFIG` env).

## Go Module

- Module: `github.com/pzhenzhou/qspill-controller`
- Go 1.25.0
- Volcano APIs pinned to v1.13.0 (v1.19.x excluded in go.mod due to non-monotonic versioning).


## Design Discussion Rules

- When the user presents a bug analysis or design direction, validate it against the code FIRST before proposing alternatives. Do not over-engineer or propose Option C when the user is asking a focused question.
- When proposing algorithm designs, walk through concrete examples with actual data before committing to an approach.
- If unsure about dependency trees, retry behavior, or downstream triggering logic, read the relevant SQL queries and Go callers before speculating.

## Answering Scope

- Match the scope of your answer to the scope of the user's question. If they ask a narrow yes/no question, answer it directly before expanding. Do not over-analyze or broaden the investigation unprompted.
- When the user corrects your analysis, fully adopt their framing rather than trying to merge it with your previous (incorrect) approach.

## Bug fix Rules

- For any bug fix, a clear error analysis must be provided first. After I review it, I will decide how to make the
  modification.
