package api

import (
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

// SpillPolicy is the atomic unit of decision-making — one logical workload
// group bound to one Volcano Queue, one dedicated nodegroup, one overflow
// nodegroup, and one set of thresholds. Loaded from the controller's
// ConfigMap and held in memory; there is no CRD.
type SpillPolicy struct {
	// Name is the logical id (e.g. "biz-a"); used in metric labels, event
	// messages, and the workqueue key. Bindings to runtime objects do not
	// flow through Name — PodGroups are matched by QueueName, Nodes by
	// DedicatedNodeGroup / OverflowNodeGroup.
	Name string

	// QueueName is the Volcano Queue this policy owns and the join key for
	// PodGroup membership: a PodGroup belongs to this policy iff its
	// spec.queue equals QueueName. PodGroup labels are not consulted.
	QueueName string

	// DedicatedNodeGroup is the value of nodeGroupLabelKey for the dedicated
	// pool (e.g. "ng2").
	DedicatedNodeGroup string

	// OverflowNodeGroup is the value of nodeGroupLabelKey for the overflow
	// pool (e.g. "ng1").
	OverflowNodeGroup string

	// MinNodes / MaxNodes are informational only in v1; no decision consumes
	// them. The autoscaler enforces the actual pool bounds. They appear in
	// metric labels and event messages.
	MinNodes int
	MaxNodes int

	// Thresholds gates state-machine transitions.
	Thresholds Thresholds
}

// Thresholds carries the per-policy timing knobs for the state machine.
type Thresholds struct {
	// TimeOn is the Steady -> Spill confirmation cooldown (short; e.g. 30s).
	TimeOn time.Duration

	// TimeOff is the Spill -> Steady base confirmation cooldown (long; e.g.
	// 10m). The effective switch-back cooldown is TimeOff * (1 + Hysteresis).
	TimeOff time.Duration

	// TimePendingMax bounds how long any policy Pod may remain in
	// PodScheduled=False before the stale-pending trigger fires (e.g. 5m).
	TimePendingMax time.Duration

	// Hysteresis ∈ [0, 1] extends the switch-back cooldown to
	// TimeOff * (1 + Hysteresis); 0 collapses to plain TimeOff (no extra
	// dampening). Validation clamps to [0, 1].
	Hysteresis float64
}

// Annotation keys the controller owns on the Volcano Queue. Persisting all
// state machine bookkeeping on the Queue itself is what makes restarts safe:
// the next reconcile reconstructs the timer from the API object rather than
// from process memory.
const (
	// AnnotationState carries the current State value (Steady or Spill).
	AnnotationState = "spill.example.com/state"

	// AnnotationConditionSince is the RFC3339 timestamp of when the
	// instantaneous condition that anchors the cooldown last became true.
	// Cleared when the condition turns false.
	AnnotationConditionSince = "spill.example.com/condition-since"

	// AnnotationDecisionHash is the sha256 of the last fully-applied Decision.
	// The reconciler short-circuits when the freshly-computed Decision.Hash
	// equals this value, providing restart-safe idempotency.
	AnnotationDecisionHash = "spill.example.com/decision-hash"
)

// State is the two-state machine result. Persisted as the value of an
// annotation on the Queue (spill.example.com/state).
type State string

const (
	// StateSteady — dedicated pool only.
	StateSteady State = "Steady"

	// StateSpill — both pools, dedicated preferred.
	StateSpill State = "Spill"
)

// Trigger labels the condition that produced a state transition. Propagated
// into Decision.Reason for events and metrics.
type Trigger string

const (
	// TriggerAutoscalerExhausted — explicit autoscaler-failure path, fires on
	// NotTriggerScaleUp / FailedScaling Pod events. Active in v1.
	TriggerAutoscalerExhausted Trigger = "autoscaler_exhausted"

	// TriggerStalePending — no-progress liveness fallback, fires when any
	// policy Pod has been PodScheduled=False longer than TimePendingMax.
	// Active in v1.
	TriggerStalePending Trigger = "stale_pending"

	// TriggerSwitchback — Spill -> Steady transition driver (overflow drained,
	// cooldown elapsed). Active in v1.
	TriggerSwitchback Trigger = "switchback"

	// TriggerDemand is RESERVED. The demand-vs-max comparison is computed by
	// pkg/snapshot for observability and future revival but is intentionally
	// not produced by the current evaluator. A regression test in
	// pkg/evaluator pins this so any future revival is deliberate rather
	// than accidental.
	TriggerDemand Trigger = "demand"

	// TriggerNone is the hash-matched no-op outcome: the decision matches the
	// observed state and no transition is fired.
	TriggerNone Trigger = ""
)

// ActionMode selects which Action implementation the reconciler will use to
// realise a Decision. The mode is chosen once at configuration load time and
// is uniform across all policies in a registry.
type ActionMode string

const (
	// ActionModeNope emits a self-contained YAML document describing the
	// decision to the configured sink (stdout by default) and never mutates
	// the cluster. Default mode for safe, dry-run rollout.
	ActionModeNope ActionMode = "Nope"

	// ActionModePatch realises the decision against the Volcano Queue via
	// server-side apply, owning only spec.affinity.nodeGroupAffinity and
	// the spill.example.com/* annotations.
	ActionModePatch ActionMode = "Patch"
)

// Decision is the pure output of the Evaluator. It carries everything the
// Action needs to mutate the Queue and everything the reconciler wants to
// log, event, and meter.
type Decision struct {
	// Policy is the SpillPolicy that produced this decision.
	Policy *SpillPolicy

	// From is the state observed in the Queue annotation at evaluation time.
	From State

	// To is the desired next state.
	To State

	// Trigger is the condition that fired (TriggerAutoscalerExhausted or
	// TriggerStalePending for Steady -> Spill, TriggerSwitchback for the
	// Spill -> Steady direction, or TriggerNone for a hash-matched no-op).
	Trigger Trigger

	// Reason is human-readable, used in events and dry-run output.
	Reason string

	// DesiredQueue is the fully-formed target Queue (spec + annotations). Only
	// the controller-owned fields are populated:
	//   - spec.affinity.nodeGroupAffinity
	//   - metadata.annotations[spill.example.com/*]
	// spec.capability is intentionally never populated; operators size it
	// once for upper-bound usage and the controller leaves it alone.
	DesiredQueue *schedulingv1beta1.Queue

	// Hash is sha256 of (To, canonicalized DesiredQueue.Spec, ConditionSince).
	// The reconciler hash-gates Action.Apply on (Hash == Snapshot.DecisionHash)
	// for restart-safe idempotency.
	Hash string

	// ObservedAt mirrors Snapshot.ObservedAt; the evaluator never reads
	// time.Now() so all clock dependence is centralised in the snapshot
	// builder.
	ObservedAt time.Time

	// Inputs are the raw numbers used by the evaluator; surfaced in events,
	// metrics, and dry-run YAML.
	Inputs DecisionInputs
}

// DecisionInputs captures the concrete numbers that drove a Decision.
// Surfaced in events, metrics, and the dry-run YAML for traceability.
type DecisionInputs struct {
	// DemandCPU is the best-effort sum of policy PodGroup CPU demand. The
	// snapshot builder estimates this from PodGroup.MinResources when set,
	// falling back to the sum of pending pod requests when it is not.
	// Observability only; the demand trigger is computed but not consumed
	// by the evaluator.
	DemandCPU resource.Quantity

	// DemandMemory mirrors DemandCPU for memory.
	DemandMemory resource.Quantity

	// DemandEstimatedPGs is the count of policy PodGroups whose demand had
	// to be inferred from individual pod requests because the PodGroup
	// itself did not declare resources. Increments the
	// queue_spill_demand_estimated_total counter.
	DemandEstimatedPGs int

	// MaxCPU is the sum of node.Status.Allocatable.cpu across observed
	// dedicated nodes. Observability only.
	MaxCPU resource.Quantity

	// MaxMemory mirrors MaxCPU for memory. Observability only.
	MaxMemory resource.Quantity

	// OverflowPodsOfPolicy is the count of this policy's Pods currently bound
	// to a node in the overflow pool. Drives the Spill -> Steady switch-back
	// guard.
	OverflowPodsOfPolicy int

	// AutoscalerExhausted is true when any policy Pod has a NotTriggerScaleUp
	// or FailedScaling Event in the observation window.
	AutoscalerExhausted bool

	// StalestPendingFor is the longest PodScheduled=False duration across all
	// policy Pods (zero if none). Compared against Thresholds.TimePendingMax
	// to drive the stale-pending trigger.
	StalestPendingFor time.Duration

	// StalePendingPods is the count of policy Pods whose PodScheduled=False
	// duration already exceeds Thresholds.TimePendingMax.
	StalePendingPods int

	// TimeInState is now - ConditionSince. Used in event messages and to test
	// cooldown elapsed.
	TimeInState time.Duration
}
