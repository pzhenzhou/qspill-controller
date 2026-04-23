// Package evaluator implements the controller's pure decision pipeline: a
// Snapshot in, a Decision out, no I/O and no clock dependency. Holding all
// state-machine logic in one pure function is what lets the unit tests cover
// every edge in DESIGN.md §7 without spinning up a cluster, and what lets
// the reconciler treat each policy reconcile as a deterministic input/output
// transformation that can be replayed at will.
package evaluator

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/common"
	"github.com/pzhenzhou/qspill-controller/pkg/snapshot"
)

// logger is the per-module logr.Logger for the evaluator. Decision pipeline
// lines are tagged module=evaluator so operators can replay the §7 state
// machine from logs alone — Snapshot inputs in, transition out.
var logger = common.NewLogger("evaluator")

// Evaluator turns a Snapshot into a Decision. The interface lives here so
// the reconciler can swap in test doubles; the production implementation
// (defaultEvaluator) is the only shipped Evaluator and is goroutine-safe
// because it carries no state.
type Evaluator interface {
	Evaluate(s *snapshot.Snapshot) api.Decision
}

// New returns the production Evaluator. Construction is parameterless
// because Evaluate is pure: no clients, no logger, no clock.
func New() Evaluator { return &defaultEvaluator{} }

// defaultEvaluator is the only shipped Evaluator. The empty struct is
// intentional — every input the function consults is on the Snapshot, so
// instances are interchangeable and free to share across goroutines.
type defaultEvaluator struct{}

// Evaluate applies the §7 state machine. The function is total: every
// (Snapshot.CurrentState, Snapshot) pair maps to exactly one Decision.
//
// The execution order matches DESIGN.md §8: nextState first (because the
// hash and the materialized Queue both depend on it), materializeQueue
// next (because the hash includes the spec), then hash, then the trigger
// label (which only fires when the state actually transitioned), then the
// human reason. The DecisionInputs are the raw numbers the snapshot
// already computed; we mirror them here so the action layer can present
// them without re-reading the snapshot.
func (e *defaultEvaluator) Evaluate(s *snapshot.Snapshot) api.Decision {
	logger.Info("evaluating snapshot",
		"policy", s.Policy.Name,
		"queue", s.Policy.QueueName,
		"currentState", string(s.CurrentState),
		"overflowPodsOfPolicy", s.OverflowPodsOfPolicy,
		"stalePendingPods", s.StalePendingPods,
		"stalestPendingFor", s.StalestPendingFor.String(),
		"autoscalerExhausted", s.AutoscalerExhausted,
		"timePendingMax", s.Policy.Thresholds.TimePendingMax.String(),
	)

	t := nextState(s)
	desiredQueue := materializeQueue(s, t.nextState, t.conditionSince)
	hash := hashDecision(t.nextState, &desiredQueue.Spec, t.conditionSince)
	desiredQueue.Annotations[api.AnnotationDecisionHash] = hash

	trigger := triggerFor(s, t)
	logger.Info("decision computed",
		"policy", s.Policy.Name,
		"from", string(s.CurrentState),
		"to", string(t.nextState),
		"fired", t.fired,
		"trigger", string(trigger),
		"hashChanged", hash != s.DecisionHash,
		"hash", hash,
	)
	return api.Decision{
		Policy:       s.Policy,
		From:         s.CurrentState,
		To:           t.nextState,
		Trigger:      trigger,
		Reason:       reasonFor(s, t, trigger),
		DesiredQueue: desiredQueue,
		Hash:         hash,
		ObservedAt:   s.ObservedAt,
		Inputs:       inputsFor(s),
	}
}

// inputsFor projects the snapshot down to the public DecisionInputs view
// the action layer and the events use. Two derived values live here rather
// than on the snapshot: TimeInState (a function of ConditionSince and
// ObservedAt) and the per-resource demand/capacity quantities (peeled out
// of the snapshot's ResourceList shape).
func inputsFor(s *snapshot.Snapshot) api.DecisionInputs {
	return api.DecisionInputs{
		DemandCPU:            s.DemandResources[corev1.ResourceCPU],
		DemandMemory:         s.DemandResources[corev1.ResourceMemory],
		DemandEstimatedPGs:   s.DemandEstimatedPGs,
		MaxCPU:               s.MaxDedicatedCapacity[corev1.ResourceCPU],
		MaxMemory:            s.MaxDedicatedCapacity[corev1.ResourceMemory],
		OverflowPodsOfPolicy: s.OverflowPodsOfPolicy,
		AutoscalerExhausted:  s.AutoscalerExhausted,
		StalestPendingFor:    s.StalestPendingFor,
		StalePendingPods:     s.StalePendingPods,
		TimeInState:          timeInState(s),
	}
}
