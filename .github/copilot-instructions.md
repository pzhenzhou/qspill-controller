# qspill-controller — Copilot Instructions

## Build, Test, and Lint

```bash
make build          # gofmt ./pkg ./cmd, then build bin/qspill-controller-bin from ./cmd/controller
make test           # make build, then go test all packages except /cmd and /proto with coverage
make vet            # go vet ./...
make fmt            # gofmt -l -w -d ./pkg ./cmd

# Run a single package / single test
go test ./pkg/evaluator/... -run TestEvaluate -v
go test ./pkg/watcher/... -run TestNodeWatch -v

# Run locally (requires kubeconfig unless running in-cluster)
make run ARGS="--kubeconfig=$HOME/.kube/config"
make run-dev
```

Prefer the `Makefile` over `README.md` when they disagree: the active binary entrypoint is `./cmd/controller`, and the build output is `bin/qspill-controller-bin`.

## High-level Architecture

The controller is a standalone external controller for Volcano queues. It does **not** add a CRD or modify Volcano internals. Each reconcile follows this pipeline:

```text
Informer watches -> policy-keyed workqueue -> snapshot.Builder.Build -> evaluator.Evaluate -> action.Apply
```

The top-level wiring lives in `cmd/controller/main.go`:

- load config into an immutable `config.PolicyRegistry`
- build Kubernetes and Volcano clients
- choose the action implementation (`Nope` dry-run YAML or `Patch` server-side apply)
- create `watcher.Manager`
- start `/healthz`
- enter leader election before starting informers/workers

Key packages:

| Package | Responsibility |
| --- | --- |
| `pkg/api` | Core domain types: `SpillPolicy`, `Thresholds`, `State`, `Decision`, action modes, and the controller-owned Queue annotations |
| `pkg/config` | Layered config loader (defaults -> file / ConfigMap bytes -> env), validation, immutable `PolicyRegistry`, atomic `RegistryStore` swaps |
| `pkg/watcher` | Raw `client-go` informers for PodGroups, Pods, Events, Nodes, Queues, and the controller ConfigMap; owns the typed workqueue and deferred `PendingWorkMap` |
| `pkg/snapshot` | Builds a per-policy `Snapshot` from informer caches only; computes demand, supply, stale-pending, autoscaler exhaustion, and current Queue annotation state |
| `pkg/evaluator` | Pure two-state machine (`Steady` / `Spill`) that materializes the desired Queue and computes the decision hash |
| `pkg/reconcile` | Wires snapshot -> evaluator -> action and skips apply when the fresh hash matches the observed Queue annotation |
| `pkg/action` | Side-effect boundary: `Nope` emits self-contained YAML, `Patch` uses Volcano server-side apply |
| `pkg/leader` | Thin wrapper around client-go leader election |

## Key Conventions

- **Policy ownership is resolved by existing runtime objects, not custom labels.** PodGroups map to policies by `pg.Spec.Queue == policy.QueueName`. Pods map to policies through the Volcano pod-group annotation -> PodGroup -> Queue -> policy chain. Nodes map through the configured nodegroup label key.

- **The controller persists state on the Queue itself.** The owned annotations are `spill.example.com/state`, `spill.example.com/condition-since`, and `spill.example.com/decision-hash`. Restarts reconstruct cooldown state from those annotations rather than process memory.

- **`pkg/evaluator` is pure.** It should not read informer state, call Kubernetes clients, or read `time.Now()`. Snapshot building owns cache reads and clock capture; `pkg/reconcile` injects the clock and hash-gates apply.

- **Only future placement is changed.** The controller never evicts pods and never writes `spec.capability`. The only owned Queue fields are `spec.affinity.nodeGroupAffinity` plus the spill annotations.

- **`Nope` is the safe default action.** `ActionModeNope` writes a deterministic YAML envelope for inspection; `ActionModePatch` uses server-side apply and should only claim the controller-owned Queue fields.

- **Config reload is fail-closed.** `pkg/watcher/configmap_watch.go` reloads the watched ConfigMap key `config.yaml`, validates it with `config.LoadFromBytes`, and keeps the previous registry on any parse/validation error.

- **Config precedence is fixed.** Built-in defaults < config file / ConfigMap bytes < env overrides. Environment variables only override the `defaults` block; per-policy arrays remain file-backed.

- **The watcher layer uses raw `client-go`, not controller-runtime.** Informers enqueue policy names; decisions happen later in reconcile. `PendingWorkMap` uses `xsync.Map`, and concurrent maps in this repo should continue to use `github.com/puzpuzpuz/xsync/v4` instead of `sync.Map`.

- **Use the design doc for the state machine.** `DESIGN.md` is the source for the Steady/Spill transition rules, cooldown semantics, and watcher behavior. The example deployment/config manifests live under `config/base` and `config/rbac`.
