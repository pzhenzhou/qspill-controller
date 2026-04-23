package evaluator

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/snapshot"
)

const (
	queueAPIVersion = "scheduling.volcano.sh/v1beta1"
	queueKind       = "Queue"
)

// transition is the local result type of nextState. It carries both the
// destination state and the timer anchor that materializeQueue must persist
// — keeping them together prevents the two values from drifting apart in a
// later refactor.
type transition struct {
	// nextState is the destination state (which may equal CurrentState when
	// no transition fires).
	nextState api.State

	// conditionSince is the value the controller will write into the
	// AnnotationConditionSince annotation. Zero clears the annotation
	// (instant condition is false); non-zero sets it. The semantics follow
	// DESIGN.md §7.2 verbatim.
	conditionSince time.Time

	// fired is true when the cooldown completed and the state actually
	// flipped this reconcile. The trigger labelling and event emission
	// keys off this so a cooldown-in-progress reconcile is not mis-reported
	// as a transition.
	fired bool
}

// nextState applies the §7.2 cooldown ledger. The function is a direct
// transcription of the pseudocode in the design — the imperative style
// is deliberate, so a future reader can diff this against §7.2 line by
// line. Branching is exhaustive: every (CurrentState, instantCond,
// since-zero, cooldown-elapsed) tuple produces exactly one transition
// value.
func nextState(s *snapshot.Snapshot) transition {
	now := s.ObservedAt
	instantCond, _ := evaluateInstantCondition(s)
	cooldown := cooldownFor(s)

	if instantCond {
		// Condition is currently true.
		if !s.ConditionSince.IsZero() {
			elapsed := now.Sub(s.ConditionSince)
			if elapsed >= cooldown {
				return transition{
					nextState:      flipState(s.CurrentState),
					conditionSince: now,
					fired:          true,
				}
			}
			// Cooldown still running — keep state, keep timer.
			return transition{nextState: s.CurrentState, conditionSince: s.ConditionSince}
		}
		// Condition just became true — start the timer.
		return transition{nextState: s.CurrentState, conditionSince: now}
	}

	// Condition is false — reset the timer; never transition.
	return transition{nextState: s.CurrentState}
}

// evaluateInstantCondition returns the boolean the §7 state machine reads
// and a hint about which signal made it true. The hint is consumed by
// triggerFor when the cooldown elapses: in Steady the autoscaler signal
// wins over stale-pending when both fire, matching the design's framing of
// autoscaler as the "fast, explicit failure path" and stale-pending as the
// fallback. In Spill there is only one signal so the hint is fixed.
func evaluateInstantCondition(s *snapshot.Snapshot) (bool, api.Trigger) {
	switch s.CurrentState {
	case api.StateSpill:
		// §7.4: drained iff no policy pod sits on the overflow pool.
		if s.OverflowPodsOfPolicy == 0 {
			return true, api.TriggerSwitchback
		}
		return false, api.TriggerNone

	default:
		// §7.3: autoscaler-exhausted OR stale-pending.
		if s.AutoscalerExhausted {
			return true, api.TriggerAutoscalerExhausted
		}
		if s.Policy.Thresholds.TimePendingMax > 0 && s.StalestPendingFor > s.Policy.Thresholds.TimePendingMax {
			return true, api.TriggerStalePending
		}
		return false, api.TriggerNone
	}
}

// cooldownFor returns the per-direction cooldown window. The Steady -> Spill
// path uses TimeOn directly. The Spill -> Steady path uses
// TimeOff * (1 + Hysteresis); Hysteresis is already clamped to [0, 1] by
// the config loader so the multiplication is safe to do in float and
// round-trip through Duration.
func cooldownFor(s *snapshot.Snapshot) time.Duration {
	t := s.Policy.Thresholds
	switch s.CurrentState {
	case api.StateSpill:
		base := float64(t.TimeOff)
		scaled := base * (1.0 + t.Hysteresis)
		return time.Duration(scaled)
	default:
		return t.TimeOn
	}
}

// flipState toggles the two-state machine. Defined here so the state
// machine has a single spelling for "the other state" and any future
// extension to a third state has one place to break the build.
func flipState(s api.State) api.State {
	if s == api.StateSpill {
		return api.StateSteady
	}
	return api.StateSpill
}

// materializeQueue returns the fully-formed target Queue. The controller
// owns exactly two slices of the Queue object: spec.affinity.nodeGroupAffinity
// and the spill.example.com/* annotations. Every other field is
// operator-managed and is deliberately absent here — the action layer's
// SSA patch only claims the fields that appear on this object.
//
// Spec shape per §7.1:
//   - Steady: required = [dedicated], no preferred.
//   - Spill:  required = [dedicated, overflow], preferred = [dedicated].
//
// The decision-hash annotation is populated by the caller after the spec
// is materialized — the hash depends on the spec, so the materialize step
// has to run first.
func materializeQueue(s *snapshot.Snapshot, state api.State, conditionSince time.Time) *schedulingv1beta1.Queue {
	annotations := map[string]string{
		api.AnnotationState: string(state),
	}
	if !conditionSince.IsZero() {
		annotations[api.AnnotationConditionSince] = conditionSince.UTC().Format(time.RFC3339)
	}

	q := &schedulingv1beta1.Queue{
		TypeMeta: metav1.TypeMeta{Kind: queueKind, APIVersion: queueAPIVersion},
		ObjectMeta: metav1.ObjectMeta{
			Name:        s.Policy.QueueName,
			Annotations: annotations,
		},
		Spec: schedulingv1beta1.QueueSpec{
			Affinity: &schedulingv1beta1.Affinity{
				NodeGroupAffinity: nodeGroupAffinityFor(s.Policy, state),
			},
		},
	}
	return q
}

// nodeGroupAffinityFor assembles the NodeGroupAffinity payload for the
// requested state. Returning a fresh struct every call keeps the spec
// pointer-stable from the caller's perspective and prevents any chance of
// aliasing the snapshot's Policy slice across reconciles.
func nodeGroupAffinityFor(p *api.SpillPolicy, state api.State) *schedulingv1beta1.NodeGroupAffinity {
	if state == api.StateSpill {
		return &schedulingv1beta1.NodeGroupAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution:  []string{p.DedicatedNodeGroup, p.OverflowNodeGroup},
			PreferredDuringSchedulingIgnoredDuringExecution: []string{p.DedicatedNodeGroup},
		}
	}
	return &schedulingv1beta1.NodeGroupAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []string{p.DedicatedNodeGroup},
	}
}

// triggerFor labels the trigger that produced a transition. The §12.1
// metric contract requires a trigger label *only* when a transition fires;
// in-progress cooldowns and steady-state holds report TriggerNone so the
// transitions counter never increments spuriously.
func triggerFor(s *snapshot.Snapshot, t transition) api.Trigger {
	if !t.fired {
		return api.TriggerNone
	}
	_, hint := evaluateInstantCondition(s)
	return hint
}

// reasonFor produces the human-readable Reason carried in events, dry-run
// YAML, and structured logs. Format follows the §12.2 examples: include
// the trigger name plus the inputs that drove the decision so an operator
// reading the event stream can reconstruct the cluster state without
// cross-referencing metrics.
func reasonFor(s *snapshot.Snapshot, t transition, trigger api.Trigger) string {
	if t.fired {
		switch trigger {
		case api.TriggerAutoscalerExhausted:
			return fmt.Sprintf("autoscaler exhausted: NotTriggerScaleUp/FailedScaling for policy %q after cooldown %s",
				s.Policy.Name, cooldownFor(s))
		case api.TriggerStalePending:
			return fmt.Sprintf("stale pending: %d pods of policy %q unschedulable >%s (stalest %s) after cooldown %s",
				s.StalePendingPods, s.Policy.Name, s.Policy.Thresholds.TimePendingMax, s.StalestPendingFor, cooldownFor(s))
		case api.TriggerSwitchback:
			return fmt.Sprintf("switchback: 0 policy %q pods on overflow pool %q sustained for cooldown %s",
				s.Policy.Name, s.Policy.OverflowNodeGroup, cooldownFor(s))
		}
	}

	if !t.conditionSince.IsZero() && t.nextState == api.StateSteady {
		// Steady, condition (autoscaler/stale-pending) currently true,
		// cooldown still running. Surface that fact so operators can see
		// the timer.
		return fmt.Sprintf("steady: spill condition pending (since %s, cooldown %s)",
			t.conditionSince.UTC().Format(time.RFC3339), cooldownFor(s))
	}
	if !t.conditionSince.IsZero() && t.nextState == api.StateSpill {
		return fmt.Sprintf("spill: switchback pending (overflow drained since %s, cooldown %s)",
			t.conditionSince.UTC().Format(time.RFC3339), cooldownFor(s))
	}
	if t.nextState == api.StateSpill {
		return fmt.Sprintf("spill: %d policy %q pods remain on overflow pool %q",
			s.OverflowPodsOfPolicy, s.Policy.Name, s.Policy.OverflowNodeGroup)
	}
	return fmt.Sprintf("steady: no spill condition for policy %q", s.Policy.Name)
}

// timeInState returns ObservedAt - ConditionSince clamped at zero. The
// clamp protects against clock skew between the controller node and the
// API server: if the persisted timestamp is in the future relative to the
// snapshot clock, the state machine should still treat the timer as
// untouched rather than producing a negative duration that would confuse
// downstream metrics.
func timeInState(s *snapshot.Snapshot) time.Duration {
	if s.ConditionSince.IsZero() {
		return 0
	}
	d := s.ObservedAt.Sub(s.ConditionSince)
	if d < 0 {
		return 0
	}
	return d
}
