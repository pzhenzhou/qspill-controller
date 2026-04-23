// Package snapshot is the read-side seam of the controller. It builds a
// Snapshot — an immutable, point-in-time view of one SpillPolicy's relevant
// cluster state — from the informer caches, and exposes it to the evaluator.
//
// The snapshot is the only place the controller reads cluster state, which
// makes it the only place we test against fakes and the only place that
// reads the wall clock. Everything downstream (evaluator, action) is pure
// with respect to a Snapshot.
package snapshot

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
)

// Snapshot is the immutable, read-once-per-reconcile view of every input the
// evaluator needs for a single SpillPolicy. Every field is computed once by
// Builder.Build and never mutated thereafter; the evaluator may share the
// Snapshot across goroutines safely.
//
// Field set is fixed by DESIGN.md §4.3. Two fields (DemandResources and
// MaxDedicatedCapacity) are observability-only in v1: the evaluator records
// them in DecisionInputs but does not consume them in transition logic. They
// are computed anyway so the demand trigger can be revived later without a
// snapshot rewrite.
type Snapshot struct {
	// Policy is the SpillPolicy this snapshot was built for. Pointer kept as
	// the same pointer the registry returned, so identity comparisons hold.
	Policy *api.SpillPolicy

	// ObservedAt is the wall clock at which this snapshot was assembled.
	// It is the only clock the evaluator may consult; downstream code that
	// needs "now" must read it from here.
	ObservedAt time.Time

	// Queue is the live Queue object as seen in the informer cache, or nil
	// if the Queue has not been observed yet. CurrentState defaults to
	// StateSteady when Queue is nil so the controller behaves like a fresh
	// install rather than crashing on first sight of a new policy.
	Queue *schedulingv1beta1.Queue

	// CurrentState is the State decoded from Queue.Annotations
	// (AnnotationState). Defaults to StateSteady when the annotation is
	// missing, malformed, or the Queue itself is missing.
	CurrentState api.State

	// ConditionSince is the time encoded in Queue.Annotations
	// (AnnotationConditionSince), or the zero value when the annotation is
	// missing or malformed. The state machine uses this anchor to compute
	// cooldown elapsed.
	ConditionSince time.Time

	// DecisionHash is the value of Queue.Annotations[AnnotationDecisionHash]
	// at observation time. The reconciler hash-gates Action.Apply against
	// Decision.Hash for idempotent re-applies.
	DecisionHash string

	// PodGroups is every non-terminal PodGroup whose Spec.Queue matches
	// Policy.QueueName. Order is informer-dependent; consumers must not
	// rely on it.
	PodGroups []*schedulingv1beta1.PodGroup

	// DemandResources is the best-effort sum of policy PodGroup demand. See
	// DESIGN.md §4.3.1 for the fallback chain. Observability-only in v1.
	DemandResources corev1.ResourceList

	// DemandEstimatedPGs counts PodGroups whose demand had to be inferred
	// from individual pod requests (fallback (3) or (4)). Powers the
	// queue_spill_demand_estimated_total counter so misconfigured workloads
	// surface in metrics before they cause incidents.
	DemandEstimatedPGs int

	// DedicatedNodes are nodes whose nodeGroupLabelKey value equals
	// Policy.DedicatedNodeGroup. Excludes nodes missing the label.
	DedicatedNodes []*corev1.Node

	// OverflowNodes are nodes whose nodeGroupLabelKey value equals
	// Policy.OverflowNodeGroup. Excludes nodes missing the label.
	OverflowNodes []*corev1.Node

	// DedicatedPodsOfPolicy is the count of this policy's pods currently
	// scheduled onto a node in the dedicated pool. Observability only.
	DedicatedPodsOfPolicy int

	// OverflowPodsOfPolicy is the count of this policy's pods currently
	// scheduled onto a node in the overflow pool. Drives the Spill -> Steady
	// switch-back guard in the evaluator.
	OverflowPodsOfPolicy int

	// MaxDedicatedCapacity is the sum of node.Status.Allocatable across
	// observed dedicated nodes. Observability only in v1.
	MaxDedicatedCapacity corev1.ResourceList

	// StalestPendingFor is the longest PodScheduled=False duration across
	// every policy pod (zero when no pod is unschedulable). The evaluator
	// compares this against Thresholds.TimePendingMax to drive the
	// stale-pending trigger.
	StalestPendingFor time.Duration

	// StalePendingPods is the count of policy pods whose PodScheduled=False
	// duration already exceeds Thresholds.TimePendingMax.
	StalePendingPods int

	// AutoscalerExhausted is true when any policy pod has a
	// NotTriggerScaleUp / FailedScaling Event in the observation window,
	// driving the autoscaler-exhausted trigger.
	AutoscalerExhausted bool
}

// Builder is the contract the reconciler depends on for constructing a
// Snapshot. The interface lives in this package (not in pkg/api) because it
// references the package-local Snapshot type and is not part of the wire
// API. Tests typically substitute a fake builder; production wiring uses
// NewBuilder with informer-backed listers.
type Builder interface {
	// Build assembles a Snapshot for the named policy. now is the wall clock
	// stamped into Snapshot.ObservedAt — the clock is injected so tests can
	// assert deterministic transitions without relying on time.Now.
	//
	// Returns an error only when the lookup itself cannot complete (lister
	// failures); a missing Queue is not an error and is reported via
	// Snapshot.Queue == nil with CurrentState == StateSteady. An unknown
	// policy name is an error: the reconciler must never enqueue work for a
	// policy that does not exist in the registry.
	Build(ctx context.Context, policyName string, now time.Time) (*Snapshot, error)
}
