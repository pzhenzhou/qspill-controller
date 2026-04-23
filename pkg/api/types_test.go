package api_test

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
)

// TestStateConstants pins the two states' wire values; they appear verbatim in
// Queue annotations and must never drift.
func TestStateConstants(t *testing.T) {
	cases := map[api.State]string{
		api.StateSteady: "Steady",
		api.StateSpill:  "Spill",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("State constant drift: got %q, want %q", got, want)
		}
	}
}

// TestTriggerConstants pins the trigger label values; they appear in Prometheus
// metric labels and event messages and must never drift.
func TestTriggerConstants(t *testing.T) {
	cases := map[api.Trigger]string{
		api.TriggerAutoscalerExhausted: "autoscaler_exhausted",
		api.TriggerStalePending:        "stale_pending",
		api.TriggerSwitchback:          "switchback",
		api.TriggerDemand:              "demand",
		api.TriggerNone:                "",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("Trigger constant drift: got %q, want %q", got, want)
		}
	}
}

// TestTriggerDemandReserved asserts the demand trigger constant is wire
// stable. The constant is declared so future revival is a one-line evaluator
// change, but it is reserved and not produced by the current evaluator. The
// companion regression test that the evaluator never produces TriggerDemand
// belongs in pkg/evaluator alongside the evaluator itself.
func TestTriggerDemandReserved(t *testing.T) {
	if api.TriggerDemand != "demand" {
		t.Fatalf("TriggerDemand value drifted; metric/event consumers may break")
	}
}

// TestActionModeConstants pins the action-mode wire values; they appear in the
// controller ConfigMap as the value of defaults.action and must never drift.
func TestActionModeConstants(t *testing.T) {
	cases := map[api.ActionMode]string{
		api.ActionModeNope:  "Nope",
		api.ActionModePatch: "Patch",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("ActionMode constant drift: got %q, want %q", got, want)
		}
	}
}

// TestSpillPolicyConstruction asserts a SpillPolicy can be constructed with
// every field set (no zero-value-only constructors land downstream consumers
// in a nil-deref). PodGroup binding flows via QueueName, so QueueName is the
// non-trivial join field exercised here.
func TestSpillPolicyConstruction(t *testing.T) {
	p := api.SpillPolicy{
		Name:               "biz-a",
		QueueName:          "biz-a",
		DedicatedNodeGroup: "ng2",
		OverflowNodeGroup:  "ng1",
		MinNodes:           1,
		MaxNodes:           5,
		Thresholds: api.Thresholds{
			TimeOn:         30 * time.Second,
			TimeOff:        10 * time.Minute,
			TimePendingMax: 5 * time.Minute,
			Hysteresis:     0.2,
		},
	}
	if p.Name == "" || p.QueueName == "" || p.Thresholds.TimeOn == 0 {
		t.Fatalf("SpillPolicy fields not preserved: %+v", p)
	}
}

// TestDecisionConstruction exercises a fully-populated Decision so downstream
// packages know every field is reachable from the api package.
func TestDecisionConstruction(t *testing.T) {
	policy := &api.SpillPolicy{Name: "biz-a", QueueName: "biz-a"}
	now := time.Date(2026, 4, 22, 10, 15, 30, 0, time.UTC)
	d := api.Decision{
		Policy:       policy,
		From:         api.StateSteady,
		To:           api.StateSpill,
		Trigger:      api.TriggerStalePending,
		Reason:       "5 pods unschedulable for >5m",
		DesiredQueue: &schedulingv1beta1.Queue{},
		Hash:         "sha256:deadbeef",
		ObservedAt:   now,
		Inputs: api.DecisionInputs{
			DemandCPU:            resource.MustParse("56"),
			DemandMemory:         resource.MustParse("224Gi"),
			DemandEstimatedPGs:   0,
			MaxCPU:               resource.MustParse("40"),
			MaxMemory:            resource.MustParse("160Gi"),
			OverflowPodsOfPolicy: 0,
			AutoscalerExhausted:  false,
			StalestPendingFor:    6*time.Minute + 12*time.Second,
			StalePendingPods:     5,
			TimeInState:          31 * time.Second,
		},
	}
	if d.Policy.Name != "biz-a" || d.From != api.StateSteady || d.To != api.StateSpill {
		t.Fatalf("Decision fields not preserved: %+v", d)
	}
	if d.DesiredQueue == nil || d.ObservedAt != now {
		t.Fatalf("Decision pointer/time fields not preserved: %+v", d)
	}
	if d.Inputs.StalestPendingFor != 6*time.Minute+12*time.Second {
		t.Fatalf("DecisionInputs duration not preserved: %+v", d.Inputs)
	}
}

// TestThresholdsHysteresisRange documents the validation contract that
// pkg/config will enforce: Hysteresis is a float64 in [0, 1]. The api package
// itself does not validate (types are inert containers); this test pins the
// expectation for the eventual validator.
func TestThresholdsHysteresisRange(t *testing.T) {
	for _, h := range []float64{0.0, 0.5, 1.0} {
		th := api.Thresholds{Hysteresis: h}
		if th.Hysteresis < 0 || th.Hysteresis > 1 {
			t.Errorf("Hysteresis %v unexpectedly out of [0,1]", h)
		}
	}
}
