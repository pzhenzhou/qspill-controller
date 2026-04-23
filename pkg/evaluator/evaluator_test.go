package evaluator

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/snapshot"
)

// fixtureClock is the synthetic "now" every test case is anchored against.
// Choosing a stable, far-from-epoch value keeps RFC3339-formatted hashes
// reproducible and makes time-axis subtraction sanity-check at a glance.
var fixtureClock = time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)

// testPolicy is the canonical SpillPolicy every test case uses unless it
// needs to flex a specific field (Hysteresis, TimePendingMax, etc.).
// Defined as a function rather than a var so accidental mutation in one
// test cannot bleed into the next.
func testPolicy() *api.SpillPolicy {
	return &api.SpillPolicy{
		Name:               "biz-a",
		QueueName:          "biz-a",
		DedicatedNodeGroup: "ng2",
		OverflowNodeGroup:  "ng1",
		Thresholds: api.Thresholds{
			TimeOn:         30 * time.Second,
			TimeOff:        10 * time.Minute,
			TimePendingMax: 5 * time.Minute,
			Hysteresis:     0.2,
		},
	}
}

// snap returns a Snapshot pre-stamped with fixtureClock and a defensive
// copy of testPolicy(). Callers tweak only the fields they care about.
func snap(state api.State, since time.Time) *snapshot.Snapshot {
	return &snapshot.Snapshot{
		Policy:               testPolicy(),
		ObservedAt:           fixtureClock,
		CurrentState:         state,
		ConditionSince:       since,
		DemandResources:      corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10")},
		MaxDedicatedCapacity: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("16")},
	}
}

// TestEvaluateSteadyNoCondition is the boring baseline: nothing is firing,
// the controller has never moved, and the next reconcile must produce a
// steady-state Decision with empty timer and TriggerNone.
func TestEvaluateSteadyNoCondition(t *testing.T) {
	s := snap(api.StateSteady, time.Time{})
	d := New().Evaluate(s)

	if d.From != api.StateSteady || d.To != api.StateSteady {
		t.Errorf("from/to = %s/%s, want Steady/Steady", d.From, d.To)
	}
	if d.Trigger != api.TriggerNone {
		t.Errorf("Trigger = %s, want None", d.Trigger)
	}
	if got := d.DesiredQueue.Annotations[api.AnnotationConditionSince]; got != "" {
		t.Errorf("condition-since = %q, want empty", got)
	}
	if d.DesiredQueue.Spec.Affinity.NodeGroupAffinity.PreferredDuringSchedulingIgnoredDuringExecution != nil {
		t.Errorf("steady spec must not declare preferred nodegroups; got %v",
			d.DesiredQueue.Spec.Affinity.NodeGroupAffinity.PreferredDuringSchedulingIgnoredDuringExecution)
	}
}

// TestEvaluateSteadyConditionJustBecameTrue covers the "set since=now, no
// transition yet" branch from §7.2. The Action layer still fires because
// the annotation needs to be persisted.
func TestEvaluateSteadyConditionJustBecameTrue(t *testing.T) {
	s := snap(api.StateSteady, time.Time{})
	s.AutoscalerExhausted = true

	d := New().Evaluate(s)
	if d.To != api.StateSteady {
		t.Errorf("to = %s, want Steady (cooldown not started)", d.To)
	}
	if d.Trigger != api.TriggerNone {
		t.Errorf("Trigger = %s, want None (no transition fired yet)", d.Trigger)
	}
	if got := d.DesiredQueue.Annotations[api.AnnotationConditionSince]; got == "" {
		t.Errorf("condition-since must be set when condition just became true")
	}
}

// TestEvaluateSteadyCooldownInProgress covers the case where the timer is
// already running but TimeOn has not yet elapsed.
func TestEvaluateSteadyCooldownInProgress(t *testing.T) {
	since := fixtureClock.Add(-15 * time.Second)
	s := snap(api.StateSteady, since)
	s.AutoscalerExhausted = true

	d := New().Evaluate(s)
	if d.To != api.StateSteady {
		t.Errorf("to = %s, want Steady (TimeOn not elapsed)", d.To)
	}
	if d.Trigger != api.TriggerNone {
		t.Errorf("Trigger = %s, want None", d.Trigger)
	}
	if got := d.DesiredQueue.Annotations[api.AnnotationConditionSince]; got != since.UTC().Format(time.RFC3339) {
		t.Errorf("condition-since = %q, want preserved %s", got, since.UTC().Format(time.RFC3339))
	}
}

// TestEvaluateSteadyToSpillAutoscaler covers the canonical fast-path
// transition: autoscaler fired, TimeOn elapsed, transition fires with the
// autoscaler trigger label.
func TestEvaluateSteadyToSpillAutoscaler(t *testing.T) {
	since := fixtureClock.Add(-30 * time.Second)
	s := snap(api.StateSteady, since)
	s.AutoscalerExhausted = true

	d := New().Evaluate(s)
	if d.To != api.StateSpill {
		t.Errorf("to = %s, want Spill (TimeOn elapsed)", d.To)
	}
	if d.Trigger != api.TriggerAutoscalerExhausted {
		t.Errorf("Trigger = %s, want autoscaler_exhausted", d.Trigger)
	}
	if got := d.DesiredQueue.Annotations[api.AnnotationState]; got != string(api.StateSpill) {
		t.Errorf("state annotation = %s, want Spill", got)
	}
	if got := d.DesiredQueue.Annotations[api.AnnotationConditionSince]; got != fixtureClock.UTC().Format(time.RFC3339) {
		t.Errorf("condition-since = %s, want reset to now", got)
	}
	want := []string{"ng2", "ng1"}
	if got := d.DesiredQueue.Spec.Affinity.NodeGroupAffinity.RequiredDuringSchedulingIgnoredDuringExecution; !sliceEqual(got, want) {
		t.Errorf("required = %v, want %v", got, want)
	}
	if got := d.DesiredQueue.Spec.Affinity.NodeGroupAffinity.PreferredDuringSchedulingIgnoredDuringExecution; !sliceEqual(got, []string{"ng2"}) {
		t.Errorf("preferred = %v, want [ng2]", got)
	}
}

// TestEvaluateSteadyToSpillStalePending covers the stale-pending path:
// no autoscaler signal, but the longest unschedulable duration crossed
// the threshold and the cooldown elapsed.
func TestEvaluateSteadyToSpillStalePending(t *testing.T) {
	since := fixtureClock.Add(-31 * time.Second)
	s := snap(api.StateSteady, since)
	s.StalestPendingFor = 6 * time.Minute
	s.StalePendingPods = 3

	d := New().Evaluate(s)
	if d.To != api.StateSpill {
		t.Errorf("to = %s, want Spill", d.To)
	}
	if d.Trigger != api.TriggerStalePending {
		t.Errorf("Trigger = %s, want stale_pending", d.Trigger)
	}
	if !strings.Contains(d.Reason, "stale pending") {
		t.Errorf("Reason = %q, want to mention stale pending", d.Reason)
	}
}

// TestEvaluateSteadyAutoscalerWinsWhenBothFire pins the design's framing
// of autoscaler as the precise signal: when both signals are true the
// trigger label is autoscaler so operators can attribute root cause.
func TestEvaluateSteadyAutoscalerWinsWhenBothFire(t *testing.T) {
	since := fixtureClock.Add(-time.Hour)
	s := snap(api.StateSteady, since)
	s.AutoscalerExhausted = true
	s.StalestPendingFor = 10 * time.Minute
	s.StalePendingPods = 7

	d := New().Evaluate(s)
	if d.Trigger != api.TriggerAutoscalerExhausted {
		t.Errorf("Trigger = %s, want autoscaler_exhausted (precise signal wins)", d.Trigger)
	}
}

// TestEvaluateSteadyConditionFlapsToFalseClearsTimer mirrors the §7.2
// "instantCond is false" branch — even with the timer set previously, a
// false condition this reconcile clears the annotation so the next true
// reading restarts the cooldown from zero.
func TestEvaluateSteadyConditionFlapsToFalseClearsTimer(t *testing.T) {
	since := fixtureClock.Add(-20 * time.Second)
	s := snap(api.StateSteady, since)

	d := New().Evaluate(s)
	if d.To != api.StateSteady {
		t.Errorf("to = %s, want Steady", d.To)
	}
	if got := d.DesiredQueue.Annotations[api.AnnotationConditionSince]; got != "" {
		t.Errorf("condition-since = %q, want cleared", got)
	}
}

// TestEvaluateSteadyTimePendingMaxZeroDisablesStalePending guards the
// degenerate-config case: if TimePendingMax is zero the stale-pending
// trigger is effectively disabled and only autoscaler can transition.
func TestEvaluateSteadyTimePendingMaxZeroDisablesStalePending(t *testing.T) {
	since := fixtureClock.Add(-time.Hour)
	s := snap(api.StateSteady, since)
	s.Policy.Thresholds.TimePendingMax = 0
	s.StalestPendingFor = time.Hour

	d := New().Evaluate(s)
	if d.To != api.StateSteady {
		t.Errorf("to = %s, want Steady (TimePendingMax=0 disables stale-pending)", d.To)
	}
}

// TestEvaluateSteadyToSpillBoundary verifies the §7.2 ">= cooldown" check
// fires *exactly* at the boundary, not one nanosecond later.
func TestEvaluateSteadyToSpillBoundary(t *testing.T) {
	policy := testPolicy()
	since := fixtureClock.Add(-policy.Thresholds.TimeOn)
	s := snap(api.StateSteady, since)
	s.AutoscalerExhausted = true

	d := New().Evaluate(s)
	if d.To != api.StateSpill {
		t.Errorf("to = %s, want Spill at the boundary", d.To)
	}
}

// TestEvaluateSpillSwitchbackHysteresisDefault validates the §7.4 cooldown
// is TimeOff * (1 + Hysteresis) — at fixtureClock - 12m and Hysteresis =
// 0.2 the cooldown is exactly 12m and switchback fires.
func TestEvaluateSpillSwitchbackHysteresisDefault(t *testing.T) {
	since := fixtureClock.Add(-12 * time.Minute)
	s := snap(api.StateSpill, since)
	s.OverflowPodsOfPolicy = 0

	d := New().Evaluate(s)
	if d.To != api.StateSteady {
		t.Errorf("to = %s, want Steady (TimeOff*1.2 elapsed)", d.To)
	}
	if d.Trigger != api.TriggerSwitchback {
		t.Errorf("Trigger = %s, want switchback", d.Trigger)
	}
	if d.DesiredQueue.Spec.Affinity.NodeGroupAffinity.PreferredDuringSchedulingIgnoredDuringExecution != nil {
		t.Errorf("steady spec must not declare preferred nodegroups; got %v",
			d.DesiredQueue.Spec.Affinity.NodeGroupAffinity.PreferredDuringSchedulingIgnoredDuringExecution)
	}
}

// TestEvaluateSpillSwitchbackHysteresisZeroCollapsesToTimeOff verifies
// Hysteresis=0 collapses the cooldown to plain TimeOff, so a window that
// would not be eligible at Hysteresis=0.2 (10m vs 12m) does fire.
func TestEvaluateSpillSwitchbackHysteresisZeroCollapsesToTimeOff(t *testing.T) {
	since := fixtureClock.Add(-10 * time.Minute)
	s := snap(api.StateSpill, since)
	s.OverflowPodsOfPolicy = 0
	s.Policy.Thresholds.Hysteresis = 0

	d := New().Evaluate(s)
	if d.To != api.StateSteady {
		t.Errorf("to = %s, want Steady (Hysteresis=0 makes cooldown=TimeOff)", d.To)
	}
}

// TestEvaluateSpillCooldownInProgress verifies the timer survives across
// reconciles when the cooldown has not yet elapsed.
func TestEvaluateSpillCooldownInProgress(t *testing.T) {
	since := fixtureClock.Add(-5 * time.Minute)
	s := snap(api.StateSpill, since)
	s.OverflowPodsOfPolicy = 0

	d := New().Evaluate(s)
	if d.To != api.StateSpill {
		t.Errorf("to = %s, want Spill (cooldown still running)", d.To)
	}
	if got := d.DesiredQueue.Annotations[api.AnnotationConditionSince]; got != since.UTC().Format(time.RFC3339) {
		t.Errorf("condition-since = %q, want preserved", got)
	}
}

// TestEvaluateSpillOverflowFlapsClearsTimer covers the §7.4 anti-flap
// requirement: a single non-zero overflow reading mid-cooldown clears the
// timer and the next "drained" reading restarts it from zero.
func TestEvaluateSpillOverflowFlapsClearsTimer(t *testing.T) {
	since := fixtureClock.Add(-10 * time.Minute)
	s := snap(api.StateSpill, since)
	s.OverflowPodsOfPolicy = 1

	d := New().Evaluate(s)
	if d.To != api.StateSpill {
		t.Errorf("to = %s, want Spill", d.To)
	}
	if got := d.DesiredQueue.Annotations[api.AnnotationConditionSince]; got != "" {
		t.Errorf("condition-since = %q, want cleared after flap", got)
	}
}

// TestEvaluateSpillStaysWithOverflowOccupied covers the steady-state
// "Spill is the honest description" case: as long as policy pods sit on
// the overflow pool, the controller stays in Spill with no timer.
func TestEvaluateSpillStaysWithOverflowOccupied(t *testing.T) {
	s := snap(api.StateSpill, time.Time{})
	s.OverflowPodsOfPolicy = 4

	d := New().Evaluate(s)
	if d.To != api.StateSpill {
		t.Errorf("to = %s, want Spill", d.To)
	}
	if d.Trigger != api.TriggerNone {
		t.Errorf("Trigger = %s, want None", d.Trigger)
	}
}

// TestHashStableUnderListReorder is the headline determinism test: the
// canonicalized hash must be invariant under upstream reordering of the
// affinity slices, since multiple equivalent specs would otherwise force
// repeated SSA writes after every reconcile.
func TestHashStableUnderListReorder(t *testing.T) {
	a := &schedulingv1beta1.QueueSpec{Affinity: &schedulingv1beta1.Affinity{
		NodeGroupAffinity: &schedulingv1beta1.NodeGroupAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution:  []string{"ng2", "ng1"},
			PreferredDuringSchedulingIgnoredDuringExecution: []string{"ng2"},
		},
	}}
	b := &schedulingv1beta1.QueueSpec{Affinity: &schedulingv1beta1.Affinity{
		NodeGroupAffinity: &schedulingv1beta1.NodeGroupAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution:  []string{"ng1", "ng2"},
			PreferredDuringSchedulingIgnoredDuringExecution: []string{"ng2"},
		},
	}}
	if hashDecision(api.StateSpill, a, fixtureClock) != hashDecision(api.StateSpill, b, fixtureClock) {
		t.Error("hash differs under list reorder; canonicalization is broken")
	}
}

// TestHashChangesWhenStateChanges proves the destination state is part of
// the digest. Without it, a Steady→Spill transition whose required list
// happens to coincide with the prior Spill required list would not force
// a re-apply (it can't actually happen with two states, but the property
// must hold so a future third state cannot accidentally collide).
func TestHashChangesWhenStateChanges(t *testing.T) {
	spec := &schedulingv1beta1.QueueSpec{Affinity: &schedulingv1beta1.Affinity{
		NodeGroupAffinity: &schedulingv1beta1.NodeGroupAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []string{"ng2"},
		},
	}}
	if hashDecision(api.StateSteady, spec, fixtureClock) == hashDecision(api.StateSpill, spec, fixtureClock) {
		t.Error("hash collides across states")
	}
}

// TestHashChangesWhenConditionSinceChanges covers the §7.2 "set since=now
// without changing spec" case: the timer write must not be hash-gated
// away, so a different conditionSince has to produce a different hash.
func TestHashChangesWhenConditionSinceChanges(t *testing.T) {
	spec := &schedulingv1beta1.QueueSpec{Affinity: &schedulingv1beta1.Affinity{
		NodeGroupAffinity: &schedulingv1beta1.NodeGroupAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []string{"ng2"},
		},
	}}
	a := hashDecision(api.StateSteady, spec, fixtureClock)
	b := hashDecision(api.StateSteady, spec, fixtureClock.Add(time.Second))
	if a == b {
		t.Error("hash collides under different conditionSince")
	}
}

// TestHashStableForRepeatedDecision proves the no-op reconcile path:
// re-evaluating an unchanged Steady snapshot produces an identical hash,
// which is what makes the action layer skip the write.
func TestHashStableForRepeatedDecision(t *testing.T) {
	s := snap(api.StateSteady, time.Time{})
	a := New().Evaluate(s)
	b := New().Evaluate(s)
	if a.Hash != b.Hash {
		t.Errorf("re-evaluation produced different hash: %s vs %s", a.Hash, b.Hash)
	}
}

// TestEvaluateNeverEmitsTriggerDemand is the §4.4/§12.1 regression test:
// the demand trigger is plumbed in the snapshot but the evaluator must
// never produce it. Iterating across the matrix of plausible inputs gives
// a strong signal that any future code change reviving the trigger does
// so deliberately.
func TestEvaluateNeverEmitsTriggerDemand(t *testing.T) {
	combos := []struct {
		name string
		s    *snapshot.Snapshot
	}{
		{"steady_idle", snap(api.StateSteady, time.Time{})},
		{"steady_high_demand", func() *snapshot.Snapshot {
			s := snap(api.StateSteady, time.Time{})
			s.DemandResources = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1000")}
			return s
		}()},
		{"steady_autoscaler_fires", func() *snapshot.Snapshot {
			s := snap(api.StateSteady, fixtureClock.Add(-time.Hour))
			s.AutoscalerExhausted = true
			return s
		}()},
		{"steady_stale_fires", func() *snapshot.Snapshot {
			s := snap(api.StateSteady, fixtureClock.Add(-time.Hour))
			s.StalestPendingFor = time.Hour
			s.StalePendingPods = 5
			return s
		}()},
		{"spill_drained", func() *snapshot.Snapshot {
			s := snap(api.StateSpill, fixtureClock.Add(-time.Hour))
			return s
		}()},
		{"spill_occupied_high_demand", func() *snapshot.Snapshot {
			s := snap(api.StateSpill, time.Time{})
			s.OverflowPodsOfPolicy = 8
			s.DemandResources = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1000")}
			return s
		}()},
	}

	for _, tc := range combos {
		t.Run(tc.name, func(t *testing.T) {
			d := New().Evaluate(tc.s)
			if d.Trigger == api.TriggerDemand {
				t.Errorf("evaluator emitted TriggerDemand for %s; v1 must never produce demand trigger", tc.name)
			}
		})
	}
}

// TestEvaluateInputsMirrorSnapshot ensures the DecisionInputs surface the
// numbers downstream consumers rely on (events, dry-run YAML, metrics).
func TestEvaluateInputsMirrorSnapshot(t *testing.T) {
	since := fixtureClock.Add(-90 * time.Second)
	s := snap(api.StateSpill, since)
	s.DemandResources = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("12"),
		corev1.ResourceMemory: resource.MustParse("24Gi"),
	}
	s.MaxDedicatedCapacity = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("32"),
		corev1.ResourceMemory: resource.MustParse("128Gi"),
	}
	s.OverflowPodsOfPolicy = 3
	s.AutoscalerExhausted = true
	s.StalestPendingFor = 90 * time.Second
	s.StalePendingPods = 2
	s.DemandEstimatedPGs = 1

	d := New().Evaluate(s)
	if d.Inputs.DemandCPU.Cmp(resource.MustParse("12")) != 0 {
		t.Errorf("DemandCPU = %s", d.Inputs.DemandCPU.String())
	}
	if d.Inputs.MaxMemory.Cmp(resource.MustParse("128Gi")) != 0 {
		t.Errorf("MaxMemory = %s", d.Inputs.MaxMemory.String())
	}
	if d.Inputs.OverflowPodsOfPolicy != 3 || !d.Inputs.AutoscalerExhausted ||
		d.Inputs.StalestPendingFor != 90*time.Second || d.Inputs.StalePendingPods != 2 ||
		d.Inputs.DemandEstimatedPGs != 1 {
		t.Errorf("inputs missing expected fields: %+v", d.Inputs)
	}
	if d.Inputs.TimeInState != 90*time.Second {
		t.Errorf("TimeInState = %s, want 90s", d.Inputs.TimeInState)
	}
}

// TestEvaluateClampsClockSkew verifies a future-dated ConditionSince does
// not overflow the TimeInState calculation; the controller must be robust
// to clock differences between the controller node and the API server.
func TestEvaluateClampsClockSkew(t *testing.T) {
	s := snap(api.StateSteady, fixtureClock.Add(time.Hour))

	d := New().Evaluate(s)
	if d.Inputs.TimeInState != 0 {
		t.Errorf("TimeInState = %s, want 0 (clock skew clamps)", d.Inputs.TimeInState)
	}
}

// TestEvaluateDecisionMirrorsObservedAt makes the §8 "no time.Now() calls"
// guarantee an enforceable test: the only clock the evaluator depends on
// is the one that comes in via the snapshot.
func TestEvaluateDecisionMirrorsObservedAt(t *testing.T) {
	s := snap(api.StateSteady, time.Time{})
	d := New().Evaluate(s)
	if !d.ObservedAt.Equal(fixtureClock) {
		t.Errorf("ObservedAt = %s, want %s", d.ObservedAt, fixtureClock)
	}
}

// TestEvaluateAlwaysWritesStateAndHash protects the action layer's
// expectation that every Decision carries a hash and a state annotation.
// Missing either makes idempotent re-applies impossible.
func TestEvaluateAlwaysWritesStateAndHash(t *testing.T) {
	cases := []*snapshot.Snapshot{
		snap(api.StateSteady, time.Time{}),
		snap(api.StateSpill, time.Time{}),
	}
	for _, s := range cases {
		d := New().Evaluate(s)
		if d.Hash == "" {
			t.Errorf("Decision.Hash empty for state %s", s.CurrentState)
		}
		if d.DesiredQueue.Annotations[api.AnnotationDecisionHash] != d.Hash {
			t.Errorf("annotation hash != Decision.Hash for state %s", s.CurrentState)
		}
		if d.DesiredQueue.Annotations[api.AnnotationState] == "" {
			t.Errorf("state annotation missing for state %s", s.CurrentState)
		}
	}
}

// sliceEqual is a tiny string-slice equality helper. Kept local so the
// test file does not pull in cmp/cmpopts for what is essentially a
// one-liner.
func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
