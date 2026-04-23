package action

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
)

// updateGolden lets a developer regenerate testdata/*.golden.yaml when the
// output format intentionally changes (`go test ./pkg/action/... -update`).
// In CI the flag stays unset and the test enforces byte-identity against
// the committed golden file.
var updateGolden = flag.Bool("update", false, "rewrite golden files with the test's actual output")

// TestNopeActionWritesGoldenSteadyToSpill is the headline NopeAction test:
// the canonical Steady -> Spill / stale_pending scenario from DESIGN.md
// §9.1 must produce a deterministic YAML envelope that operators can diff
// against expectations during the safe-rollout window.
func TestNopeActionWritesGoldenSteadyToSpill(t *testing.T) {
	d := steadyToSpillStalePendingDecision()

	var buf bytes.Buffer
	n := NewNope(&buf)
	if err := n.Apply(context.Background(), d); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	goldenPath := filepath.Join("testdata", "steady_to_spill_stale_pending.golden.yaml")
	got := buf.Bytes()
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v (run `go test ./pkg/action/... -update` to regenerate)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("YAML output mismatch.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestNopeActionRepeatedCallsAreDeterministic guards the contract that
// re-applying an unchanged Decision produces byte-identical output. This
// is what makes the dry-run document safe to compare across reconciles
// in the safe-rollout phase.
func TestNopeActionRepeatedCallsAreDeterministic(t *testing.T) {
	d := steadyToSpillStalePendingDecision()
	var first, second bytes.Buffer
	if err := NewNope(&first).Apply(context.Background(), d); err != nil {
		t.Fatalf("first Apply failed: %v", err)
	}
	if err := NewNope(&second).Apply(context.Background(), d); err != nil {
		t.Fatalf("second Apply failed: %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Errorf("repeated Apply diverged.\nfirst:\n%s\nsecond:\n%s", first.String(), second.String())
	}
}

// TestNopeActionDefaultsToStdout ensures the zero-value sink behaviour
// matches the design's contract: passing nil to NewNope must fall back
// to os.Stdout so callers do not need to import os themselves.
func TestNopeActionDefaultsToStdout(t *testing.T) {
	n := NewNope(nil)
	if n.sink() != os.Stdout {
		t.Errorf("sink() = %v, want os.Stdout", n.sink())
	}
}

// TestNopeActionName pins the "Nope" identifier used in metrics labels.
// Renaming the constant elsewhere without updating this test would catch
// the change in CI before it leaks into a dashboard.
func TestNopeActionName(t *testing.T) {
	if got := (&NopeAction{}).Name(); got != "Nope" {
		t.Errorf("Name() = %q, want Nope", got)
	}
}

// steadyToSpillStalePendingDecision constructs the canonical fixture used
// by the golden-file tests. Every value is fixed (no clocks, no resource
// allocation rounding) so the YAML envelope is byte-deterministic.
func steadyToSpillStalePendingDecision() api.Decision {
	observedAt := time.Date(2026, 4, 22, 10, 15, 30, 0, time.UTC)

	policy := &api.SpillPolicy{
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

	queue := &schedulingv1beta1.Queue{
		TypeMeta: metav1.TypeMeta{Kind: "Queue", APIVersion: "scheduling.volcano.sh/v1beta1"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "biz-a",
			Annotations: map[string]string{
				api.AnnotationState:          string(api.StateSpill),
				api.AnnotationConditionSince: observedAt.Format(time.RFC3339),
				api.AnnotationDecisionHash:   "sha256:fixed-for-golden-test",
			},
		},
		Spec: schedulingv1beta1.QueueSpec{
			Affinity: &schedulingv1beta1.Affinity{
				NodeGroupAffinity: &schedulingv1beta1.NodeGroupAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution:  []string{"ng2", "ng1"},
					PreferredDuringSchedulingIgnoredDuringExecution: []string{"ng2"},
				},
			},
		},
	}

	return api.Decision{
		Policy:       policy,
		From:         api.StateSteady,
		To:           api.StateSpill,
		Trigger:      api.TriggerStalePending,
		Reason:       `stale pending: 5 pods of policy "biz-a" unschedulable >5m0s (stalest 6m12s) after cooldown 30s`,
		Hash:         "sha256:fixed-for-golden-test",
		DesiredQueue: queue,
		ObservedAt:   observedAt,
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
}
