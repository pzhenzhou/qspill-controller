# Queue spill controller — design

This document explains *why* the controller is shaped the way it is, by walking through the small set of design decisions that the current code rests on. It is written to match the code in this repository; where the longer narrative in [`DESIGN.md`](../DESIGN.md) at the repo root disagrees with the implementation, **the code is the source of truth** (see [Where this document differs from `DESIGN.md`](#where-this-document-differs-from-designmd)).

The intended reader already knows Volcano, has used `PodGroup` / `Queue`, and is comfortable with the idea of an elastic node pool. The Volcano-specific primer in [§1](#1-volcano-nodegroup-affinity-the-thing-this-controller-bends) only fixes the vocabulary used in the rest of the document.

---

## 1. Volcano `nodegroup` affinity: the thing this controller bends

Volcano’s [`nodegroup` plugin](../../volcano/pkg/scheduler/plugins/nodegroup/nodegroup.go) reads `Queue.spec.affinity.nodeGroupAffinity` and constrains scheduling decisions for every `PodGroup` whose `spec.queue` points at that `Queue`. The relevant pieces are:

- **A node label key**, e.g. `volcano.sh/nodegroup-name`, partitions cluster nodes into named pools (“nodegroups”). The same key is configurable on this controller (`defaults.nodeGroupLabelKey`).
- **`required`** values are a hard predicate: a pod from this queue can only land on a node whose label value is in the list.
- **`preferred`** values add a *score bias* (`+0.5 × BaseScore`) on top of the `required` bias (`+BaseScore`). Other node-order plugins still vote, so `preferred` is **a bias, not a strict ordering** — see [§9.1](#91-spill-is-a-bias-not-a-strict-ordering).
- The plugin has **no notion of pool size, autoscaler limits, or stock**. If `required` is a single dedicated nodegroup at its hard cap, pods queue forever no matter what the autoscaler does.

That last property is the entire reason this controller exists.

The setup the controller is designed for: a `Queue` whose pods *should* run on a dedicated nodegroup (with autoscaler `min=1, max=N`), but *must not* be blocked indefinitely if that pool genuinely cannot grow. The controller flips one knob — `Queue.spec.affinity.nodeGroupAffinity` — between two shapes:

- **Steady**: `required: [dedicated]`. No mention of any other pool.
- **Spill**: `required: [dedicated, overflow]`, `preferred: [dedicated]`. Both pools admissible; dedicated still wins on score when it has capacity.

Everything else in this document is the machinery that decides *when* to flip and how to do it without disrupting users, fighting other writers of the `Queue`, or losing its place across restarts.

---

## 2. The decision the controller makes, restated as a model

A `SpillPolicy` (in-memory; loaded from a `ConfigMap`, no CRD) ties together the things the controller treats as a single decision unit:

```text
SpillPolicy {
  Name                        // workqueue key, log/metric label
  QueueName                   // Volcano Queue name + PodGroup join key
  DedicatedNodeGroup          // value of node label
  OverflowNodeGroup           // value of node label
  Thresholds {
    TimeOn                    // Steady -> Spill cooldown
    TimeOff                   // base Spill -> Steady cooldown
    TimePendingMax            // any pod unschedulable longer than this -> spill
    Hysteresis  ∈ [0, 1]      // Spill -> Steady cooldown is TimeOff*(1+Hysteresis)
  }
}
```

The membership rules are intentionally narrow so the join works on **objects already shipped by Volcano**, with no controller-specific labels:

- **`PodGroup` belongs to a policy** iff `pg.Spec.Queue == policy.QueueName`. PodGroup labels are not consulted.
- **`Pod` belongs to a policy** by following its Volcano group annotation (`scheduling.volcano.sh/pod-group` or the kube-batch alias) to a PodGroup, then through `pg.Spec.Queue` to a policy.
- **`Node` belongs to dedicated/overflow** iff its label under `defaults.nodeGroupLabelKey` equals the policy’s value. Nodes without the key are dropped.

For a single policy, the controller answers exactly one question per reconcile: *given the current state and observable cluster facts, what should `Queue.spec.affinity.nodeGroupAffinity` look like, and is now the right time to write it?*

---

## 3. The two-state cooldown ledger that survives restarts

The state machine has two states (`Steady`, `Spill`) and two transitions, each gated by an **instantaneous condition** plus a **cooldown** that starts the moment the condition first becomes true and resets the moment it stops being true. The implementation is in [`pkg/evaluator/state_machine.go`](../pkg/evaluator/state_machine.go) and the conditions are derived in [`pkg/snapshot`](../pkg/snapshot/).

### 3.1 Conditions

**Steady → Spill** (instantaneous condition):
```
AutoscalerExhausted
  OR  StalestPendingFor > Thresholds.TimePendingMax
```
The first signal is fast and explicit (cluster-autoscaler emitted `NotTriggerScaleUp` or `FailedScaling` for a pod owned by the policy). The second is the *liveness fallback*: even when the autoscaler stays silent, any pod stuck `PodScheduled=False` long enough flips the policy. Together they make correctness **independent of the autoscaler’s cooperation**.

**Spill → Steady** (instantaneous condition):
```
OverflowPodsOfPolicy == 0
```
Switch-back fires only when there is no policy pod currently bound to a node in the overflow pool. This is the rule that prevents the *stuck state* — flipping back early would leave any future pod queued on the dedicated pool while the overflow pool still hosts policy work that contradicts the “Steady” story.

### 3.2 Cooldowns are asymmetric on purpose

Cooldowns are computed by `cooldownFor`:
```
Steady -> Spill : Thresholds.TimeOn                          // short, e.g. 30s
Spill  -> Steady: Thresholds.TimeOff * (1 + Hysteresis)      // long, e.g. 10m * 1.2
```
Switch-on is short because users are blocked on capacity. Switch-back is long because giving back a resource we may need again is the cheapest mistake to recover from. `Hysteresis` is the single anti-flap knob; `0` collapses to plain `TimeOff`, the loader clamps out-of-range values.

### 3.3 The ledger lives on the `Queue`, not in process memory

Three annotations under the `spill.example.com/` namespace make the controller restart-safe (constants in [`pkg/api/types.go`](../pkg/api/types.go)):

| Annotation | Meaning |
|------------|---------|
| `spill.example.com/state` | The current state (`Steady` / `Spill`) the controller believes the Queue is in. |
| `spill.example.com/condition-since` | RFC3339 timestamp of when the current instantaneous condition first became true; cleared the moment the condition turns false. |
| `spill.example.com/decision-hash` | sha256 fingerprint of the last fully-applied decision; the [§5](#5-deciding-when-to-actually-write-the-hash-gate) idempotency gate. |

`nextState` (in [`pkg/evaluator/state_machine.go`](../pkg/evaluator/state_machine.go)) is an exact transcription of:

```text
if instantCond {
  if conditionSince is set:
    if now - conditionSince >= cooldown: flip; conditionSince = now; fired = true
    else                               : keep state, keep timer
  else: keep state, conditionSince = now      // start the timer (must persist!)
} else {
  keep state, clear conditionSince            // never transition
}
```

Two consequences worth calling out:

1. The “start the timer” branch *changes the desired Queue annotations even though the spec is unchanged*. The decision-hash includes `conditionSince` ([§5](#5-deciding-when-to-actually-write-the-hash-gate)) so this case still produces a new hash and the action layer is allowed through the gate. Otherwise a restart would lose the cooldown clock.
2. Trigger attribution is computed from the same condition evaluator (`evaluateInstantCondition`); when both signals fire in `Steady` the **autoscaler-explicit** trigger is reported ahead of stale-pending. This labelling matters for events, logs, and (future) metrics.

### 3.4 Materializing the desired Queue

`materializeQueue` returns a `Queue` populated **only** with the fields the controller is willing to own:

- `spec.affinity.nodeGroupAffinity`, shaped per state (`Steady`: `required: [dedicated]`; `Spill`: `required: [dedicated, overflow]`, `preferred: [dedicated]`).
- The three `spill.example.com/*` annotations.

Everything else — `spec.capability`, `spec.weight`, `spec.guarantee`, unrelated annotations, `nodeGroupAntiAffinity` — is **deliberately absent**, because the apply layer ([§6](#6-applying-the-decision-without-fighting-other-writers)) only claims what `materializeQueue` produces.

---

## 4. Turning cluster events into reconciles

Decisions are useful only if they happen at the right moment. The trigger pipeline lives in [`pkg/watcher`](../pkg/watcher) and is structured so that **only the snapshot reads cluster state**, **only the workqueue holds backpressure**, and **only the reconciler decides**.

### 4.1 Pipeline shape

```text
informers ──> per-resource Watch handlers ──> Manager.Enqueue(policyName)
                                                    │
                                       workqueue (string keys, dedup, rate-limit)
                                                    │
                                              processNextItem
                                                    │
                                Reconciler.ReconcilePolicy(ctx, name)
                                                    │
                          Snapshot.Build → Evaluator.Evaluate → Action.Apply
```

The workqueue is keyed by `SpillPolicy.Name`. A burst of N events for the same policy collapses to a single reconcile because the workqueue dedupes by key while the item is queued, and the reconciler reads cluster state freshly via the snapshot — so no event content is needed alongside the key. This is also the property that makes the **forced periodic resync** a self-healing floor: every `defaults.reconcileResyncPeriod` the manager re-enqueues every known policy ([`Manager.runPeriodicResync`](../pkg/watcher/manager.go)) and any missed watch event is corrected without extra logic.

### 4.2 What each watcher contributes

| Watcher | Informer scope | Enqueues policy when … |
|---|---|---|
| **PodGroup** ([`podgroup_watch.go`](../pkg/watcher/podgroup_watch.go)) | All PodGroups, cluster-wide | `pg.Spec.Queue` resolves to a known policy on add/delete; on update only if `Spec.Queue`, `Status.Phase`, `Spec.MinMember`, or `Spec.MinResources` changed. A queue rebinding enqueues *both* the previous and the current policy. |
| **Node** ([`node_watch.go`](../pkg/watcher/node_watch.go)) | Nodes filtered server-side by `LabelSelector: <key> exists` | Old or new label value matches any policy’s dedicated/overflow value; relabels enqueue *both* affected policies. |
| **Pod / Event** ([`pod_event_watch.go`](../pkg/watcher/pod_event_watch.go)) | Pods cluster-wide; Events cluster-wide (filtered in handlers) | A pod whose owning PodGroup ties back to a policy enters/exits `PodScheduled=False`, or an Event with reason `NotTriggerScaleUp`/`FailedScaling` involves such a pod. |
| **Queue** ([`queue_watch.go`](../pkg/watcher/queue_watch.go)) | Queues, cluster-wide | A known policy’s Queue is created, deleted, or had its controller-touched fields edited (drift detection — re-applies what we own). |
| **ConfigMap** ([`configmap_watch.go`](../pkg/watcher/configmap_watch.go)) | Single ConfigMap (field-selected by name in the controller namespace) | Configuration is reloaded (atomic `RegistryStore` swap); every policy in the *union* of old and new registries is enqueued so removed policies get one final reconcile. |

A few choices deserve emphasis because they are easy to miss:

- **No controller-runtime in production code.** Informers are built from `cache.NewSharedIndexInformer` over typed `client-go` / Volcano clientsets, with `cache.ResourceEventHandlerDetailedFuncs` so handlers can distinguish initial-list from steady-state events.
- **Field-level membership lives in the snapshot, not on the wire.** The PodGroup informer is unfiltered because membership is `Spec.Queue` (non-indexed); the Node informer is server-side-filtered to nodes carrying *the key* (presence), not specific values, so the watch budget stays bounded without restarting on config edits.
- **Drift detection is part of the trigger surface.** The Queue watcher exists so a human or another controller editing the Queue causes the controller to recompute, mismatch the hash, and re-assert what it owns.

### 4.3 Stale-pending uses a deferred re-enqueue, coalesced

The stale-pending trigger needs the controller to “revisit policy P at time T+TimePendingMax” even if no other event happens before then. The pod/event watcher computes that deadline when it sees a `PodScheduled=False` condition and calls `Manager.EnqueueAfter`. To prevent thousands of pods from each scheduling their own deferred reconcile for the same policy, the [`PendingWorkMap`](../pkg/watcher/pending_work.go) tracks the *earliest* pending deadline per policy and silently drops calls for later deadlines. The worker clears the entry as soon as it dequeues the key, so the next stale-pending event re-arms cleanly.

This is the only place the trigger layer actively schedules work. Every other path is purely reactive to informer events.

### 4.4 Why one leader and one worker is enough

A single `processNextItem` worker drives reconciles serially per dequeue. Concurrency across policies is not needed because each policy’s reconcile is dominated by an in-memory pass over informer caches, and serial execution dramatically simplifies the cooldown ledger (no two reconciles for the same key can race on the annotations). [`pkg/leader`](../pkg/leader/leader.go) wraps client-go leader election so only one replica runs the watcher at a time, even during rolling updates — this is what protects the SSA write path from two replicas racing on the same Queue.

---

## 5. Deciding when to actually write: the hash gate

The reconciler is the smallest piece of code in the controller and the most important contract:

```go
func (r *reconciler) ReconcilePolicy(ctx, policyName) error {
    snap := r.Build(ctx, policyName, r.Now())
    d    := r.Evaluate(snap)
    if d.Hash == snap.DecisionHash { return nil }   // no-op
    return r.Apply(ctx, d)
}
```

`Decision.Hash` is computed by [`hashDecision`](../pkg/evaluator/hash.go) over:

- the destination state,
- the canonicalised `NodeGroupAffinity` (sorted `required` and `preferred` lists, so map/slice ordering cannot perturb the digest),
- the `conditionSince` stamp (so the “start the timer without changing the spec” case in [§3.3](#33-the-ledger-lives-on-the-queue-not-in-process-memory) still mismatches the prior hash and persists the annotation).

Operator-managed fields (`spec.capability`, `spec.weight`, …) are deliberately excluded from the hash because the controller never writes them and so they have no business influencing whether the controller should re-write what it does own.

Two properties fall out of this:

1. **Restarts are free.** After a restart, every watcher fires its initial-list events, the workqueue receives one item per policy, the reconciler builds a snapshot, computes the same hash that was already on the Queue annotation, and the gate short-circuits. No API writes happen unless reality disagrees with the annotation.
2. **Drift is automatically corrected.** If a human edits an owned field, the Queue watcher fires, the snapshot reads the drifted value, the hash mismatches, and the action layer puts it back.

---

## 6. Applying the decision without fighting other writers

The side-effecting layer is intentionally small and behind an interface:

```go
type Action interface {
    Name() string
    Apply(ctx context.Context, d api.Decision) error
}
```

(See [`pkg/action/action.go`](../pkg/action/action.go).) Two implementations ship today:

- **`NopeAction`** ([`nope.go`](../pkg/action/nope.go)) — the default, dry-run mode. It marshals the decision and the desired Queue into a self-contained YAML envelope and writes it to a configurable sink (stdout by default). Critically, it derives `observedAt` from `Decision.ObservedAt` rather than `time.Now()`, so re-emitting the same decision is byte-identical — that is what lets golden-file tests work.
- **`PatchAction`** ([`patch.go`](../pkg/action/patch.go)) — production mode. It performs **server-side apply** against the Volcano `Queues` API with:
  - **Field manager** `qspill-controller`,
  - **Owned fields** populated only from `materializeQueue` (`spec.affinity.nodeGroupAffinity` plus the three `spill.example.com/*` annotations),
  - **`Force: true`**, so the controller can reclaim its owned fields if another writer touched them.
  Conflicts are mapped to `ErrOwnershipConflict` so the reconciler can rate-limit/back off without retrying forever.

The selection happens once at startup from `defaults.action` (`Nope` | `Patch`); there is no per-policy override. This means a single binary either runs entirely dry-run or entirely live — the team’s rollout playbook is therefore “run with `Nope` for N days, diff the YAML, then switch to `Patch`.”

The fact that `PatchAction` only mentions the fields it owns is the entire reason operators can keep editing `spec.capability` (sizing) without triggering an SSA conflict with this controller. The matching RBAC ([`config/rbac/cluster_role.yaml`](../config/rbac/cluster_role.yaml)) grants `patch` on Queues but not `update`, so the verb surface lines up with the field-ownership story.

---

## 7. Configuration: a ConfigMap, hot-reloaded, fail-closed

Configuration is loaded by [`pkg/config`](../pkg/config), with three layered sources at increasing precedence: **built-in defaults < file (`QSPILL_CONTROLLER_CONFIG`) or ConfigMap bytes < env-var allowlist**. Only the `defaults.*` block can be overridden by env; per-policy fields come from the file or ConfigMap because env-driven array indexing is unreviewable in production.

The hot-reload contract is simple but worth pinning down because the rest of the system depends on it:

1. The `ConfigMap` watcher reads `data.config.yaml`, calls `LoadFromBytes`, and on success **atomically swaps** the in-memory `PolicyRegistry` (`atomic.Pointer[PolicyRegistry]`).
2. On failure (any validation error), the **previous** registry stays installed; nothing else changes. This is the “fail-closed” property — invalid config never reaches the workqueue.
3. After a successful swap, every policy in the **union** of old and new registries is enqueued, so policies that disappeared get one final reconcile and policies that newly appeared get a first reconcile.

The validator ([`pkg/config/validation.go`](../pkg/config/validation.go)) accumulates *every* defect using `errors.Join` rather than failing fast, so an operator sees the full set of problems in one round trip. `Hysteresis` is **clamped**, not rejected, because a slightly out-of-range tuning knob has a sensible boundary value and rejecting it would block the entire registry.

---

## 8. Testability: the seams that make the controller verifiable without a cluster

A non-trivial fraction of the design exists specifically so the controller is testable. The seams below are the reason the unit-test layer covers the entire decision pipeline without spinning up a kube-apiserver.

### 8.1 The `Evaluator` is pure

`Evaluator.Evaluate(*Snapshot) Decision` does no I/O, holds no fields (`defaultEvaluator{}` is the empty struct), and never reads the wall clock — `ObservedAt` is supplied on the snapshot. Same snapshot in, same decision out. That property makes [`evaluator_test.go`](../pkg/evaluator/evaluator_test.go) a table-driven exhaustive enumeration of the §3 state machine: every cooldown boundary, both triggers individually and combined, the `Hysteresis = 0` collapse, the “timer-just-started” edge case, the regression test that pins `TriggerDemand` as **never** produced.

### 8.2 The `Snapshot` is the only place state is read

The reconciler depends on `snapshot.Builder.Build` and on the `Listers` struct ([`pkg/snapshot/listers.go`](../pkg/snapshot/listers.go)) — five tiny interfaces (`PodGroupLister`, `NodeLister`, `PodLister`, `EventLister`, `QueueLister`), each with one or two methods. Production wiring backs them with informer stores ([`pkg/watcher/listers.go`](../pkg/watcher/listers.go)); tests back them with in-memory slices. The Builder itself takes a `PolicyResolver`, decoupling it from the registry’s concurrency mechanics.

This is the seam that lets [`builder_test.go`](../pkg/snapshot/builder_test.go) and friends exercise the demand fallback chain ([`demand.go`](../pkg/snapshot/demand.go)), the stale-pending scan ([`stale_pending.go`](../pkg/snapshot/stale_pending.go)), and the supply/autoscaler signals ([`supply.go`](../pkg/snapshot/supply.go)) without touching a fake clientset, let alone a real one.

### 8.3 Time is injectable end-to-end

The reconciler accepts a `Now func() time.Time` ([`reconcile.New`](../pkg/reconcile/reconciler.go)), defaulted to `time.Now`. It is the *only* clock in the pipeline; it flows into `Build`, ends up on `Snapshot.ObservedAt`, and from there reaches every cooldown comparison and every event timestamp. Tests can therefore stamp a deterministic time and assert a specific transition without sleeping.

### 8.4 The `Action` interface is the dry-run lever

Because side effects are isolated behind one method, three things become possible:

1. **`NopeAction`** is the production-grade dry-run: it generates the byte-identical YAML the operator will eventually patch, so review can happen against real decisions captured from real cluster state.
2. **`RecordingAction`** ([`fake.go`](../pkg/action/fake.go)) is the unit-test fake: it captures every successful Apply for assertion. [`reconciler_test.go`](../pkg/reconcile/reconciler_test.go) uses it to assert hash-gating, error propagation, and the no-op short-circuit.
3. **`PatchAction`** has its own focused test ([`patch_test.go`](../pkg/action/patch_test.go)) using a custom `QueueApplier` so SSA conflict mapping (`ErrOwnershipConflict`) can be exercised without a real apiserver.

### 8.5 Hash determinism and YAML golden files

The decision hash is sorted-list-canonical, and the `NopeAction` YAML envelope is field-by-field deterministic ([`newPayload`](../pkg/action/nope.go) explicitly enumerates fields rather than embedding `api.Decision`). Together they make `pkg/action/testdata/*.golden.yaml` viable: a snapshot-driven decision produces exactly the same bytes today and a year from now, so a behavioural regression surfaces as a one-line diff.

### 8.6 Watcher tests run against the informer Store

The watchers are tested ([`pkg/watcher/*_test.go`](../pkg/watcher/)) by constructing real informers backed by `client-go` fakes, populating the Store, and asserting that the workqueue receives the expected policy keys and that `PendingWorkMap` coalesces deferred enqueues correctly. No cluster, no network — but the same code path runs in production.

The cumulative effect is that **every layer is tested by fakes appropriate to its narrow contract**, and the only integration target left for envtest/E2E is the boundary between SSA and the real Volcano CRDs.

---

## 9. Constraints, dependencies, and operational expectations

These are the assumptions the controller bakes in. Listing them in one place so operators know what they sign up for.

### 9.1 Spill is a bias, not a strict ordering

In Spill, both pools satisfy the `nodegroup` predicate, and `preferred: [dedicated]` adds a +50 score bias relative to `required` alone. Other node-order plugins still vote, so a busy-but-not-full overflow node can occasionally outrank an empty dedicated node. The bound on this effect is that Spill is *temporary* (entered only when dedicated is genuinely struggling, exited as soon as the overflow pool drains), so the misplacement window is bounded. Mitigations available without modifying Volcano are scheduler-side (reduce other plugins’ weights). This is documented in [`DESIGN.md`](../DESIGN.md) §7.7 in more depth.

### 9.2 Volcano scheduler dependency

- `scheduling.volcano.sh/v1beta1` `Queue` and `PodGroup` CRDs must be installed.
- The `nodegroup` plugin must be enabled and the controller’s `defaults.nodeGroupLabelKey` must equal the label key the plugin reads (default `volcano.sh/nodegroup-name`).
- Spill is implemented **only** through `Queue.spec.affinity.nodeGroupAffinity`. Alternatives like per-workload taints/tolerations or pod-level node selectors are not used for this concern — they remain valid complementary cluster tooling, but they are not how spill works here.

### 9.3 Node and workload requirements

- Nodes participating in either pool must carry the configured label key; the node informer is server-side-filtered by **presence** of the key.
- `minNodes` / `maxNodes` per policy are **informational** in v1 (validation requires them but no decision consumes them; the autoscaler enforces actual bounds).
- Pods must carry the Volcano PodGroup annotation so the Pod → PodGroup → Queue → policy chain resolves.

### 9.4 Autoscaler dependency

- Autoscaler exhaustion is inferred from `corev1.Event` reasons `NotTriggerScaleUp` or `FailedScaling` involving a policy pod. This is the standard cluster-autoscaler behaviour; providers that emit different reasons reduce the autoscaler-explicit signal’s usefulness but do not affect correctness, because the stale-pending fallback covers any remaining gap.
- Deeper, vendor-specific signals (e.g. ACK `ack-goatscaler` inventory ConfigMaps) are out of scope for v1 but documented in [`DESIGN.md`](../DESIGN.md) §6.3.1 as a future plug-in point.

### 9.5 Kubernetes RBAC and runtime

`ClusterRole` (see [`config/rbac/cluster_role.yaml`](../config/rbac/cluster_role.yaml)):

- **Read**: `queues`, `podgroups`, `nodes`, `pods`, `configmaps`, `events`.
- **Write**: `patch` on `queues` (server-side apply); `create`/`patch` on `events` (forward-looking, see §9.7).
- **Lease**: `coordination.k8s.io/leases` in the controller namespace for leader election.

The Deployment is single-replica-active (lease-protected) by design — there is no horizontal scaling story for the controller.

### 9.6 Configuration surface

- File path via `QSPILL_CONTROLLER_CONFIG`; ConfigMap by `metadata.name == -config-name` (default `qspill-controller-config`) in the controller namespace; key `data.config.yaml`. Same schema in both places.
- Env-var overrides allowed only for `defaults.*`; per-policy fields must come from the file/ConfigMap.
- `defaults.action` selects the side-effect mode at startup (`Nope` | `Patch`) and applies uniformly across every policy.

### 9.7 Observability — current vs planned

| Area | Status today |
|---|---|
| Structured zap logs (`module=…` per package) | **Implemented** |
| `/healthz` HTTP probe | **Implemented** |
| `/metrics` (Prometheus) | **Not started** — no metrics server is currently exposed; the counters/gauges in [`DESIGN.md`](../DESIGN.md) §12 are targets, not implemented behaviour. |
| Kubernetes Events on the Queue | **Not emitted** — the reconciler is constructed with a `nil` recorder; RBAC for `events` exists in anticipation. |

Roadmap and phasing are tracked in [`IMPLEMENTATION.md`](../IMPLEMENTATION.md) (observability is the deferred Appendix A).

---

## Where this document differs from `DESIGN.md`

When the root [`DESIGN.md`](../DESIGN.md) and the code disagree, the code is the source of truth. The known, intentional deltas as of today:

| Topic | `DESIGN.md` | Repository code |
|---|---|---|
| Binary path | `cmd/qspill-controller/main.go` | `cmd/controller/main.go` |
| Deployment manifests directory | `deploy/` | `config/` (Kustomize overlays) |
| Watcher manager type name | `WatcherManager` | `Manager` (`pkg/watcher`) |
| PodGroup `onUpdate` change-detection fields | Includes `Status.MinResources` | `Spec.Queue`, `Status.Phase`, `Spec.MinMember`, `Spec.MinResources` only (`podGroupChanged`) |
| Event informer | Field-selected list/watch | Cluster-wide list/watch; reason filter applied in handlers and snapshot |
| Prometheus metrics | Specified in §12 | Flag is parsed; no metrics server registered |
| `EventRecorder` on the reconciler | Emits events on every transition | Constructed with `nil`; no events emitted yet |
| Where `time.Now()` is read | “Never outside SnapshotBuilder” | The reconciler reads it once and *passes* it into `Build`; the evaluator stays pure on `ObservedAt`. The intent (one clock for the whole pipeline) is preserved. |
| `DESIGN.md` front-matter `todos` | Many `pending` | Implementation has progressed substantially; see [`IMPLEMENTATION.md`](../IMPLEMENTATION.md). |

---

## Related documents

- [`DESIGN.md`](../DESIGN.md) — full narrative specification, failure modes, and future work.
- [`IMPLEMENTATION.md`](../IMPLEMENTATION.md) — phase status and recorded deviations.
- Example config: [`config/base/configmap.yaml`](../config/base/configmap.yaml).
