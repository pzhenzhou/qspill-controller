package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	applycfg "volcano.sh/apis/pkg/client/applyconfiguration/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/action"
	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/evaluator"
	"github.com/pzhenzhou/qspill-controller/pkg/snapshot"
)

var reconcileClock = time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)

func TestReconcileHashGateNoApply(t *testing.T) {
	since := reconcileClock.Add(-31 * time.Second)
	s := snap(api.StateSteady, since)
	s.StalestPendingFor = 6 * time.Minute
	s.StalePendingPods = 3
	d := evaluator.New().Evaluate(s)
	s.DecisionHash = d.Hash

	rec := action.RecordingAction{}
	r := New(stubBuilder{snap: s}, evaluator.New(), &rec, func() time.Time { return reconcileClock })
	if err := r.ReconcilePolicy(context.Background(), "biz-a"); err != nil {
		t.Fatal(err)
	}
	if len(rec.Applies) != 0 {
		t.Fatalf("hash gate should skip Apply; got %d calls", len(rec.Applies))
	}
}

func TestReconcileSteadyToSpillStalePending(t *testing.T) {
	since := reconcileClock.Add(-31 * time.Second)
	s := snap(api.StateSteady, since)
	s.StalestPendingFor = 6 * time.Minute
	s.StalePendingPods = 3

	rec := action.RecordingAction{}
	r := New(stubBuilder{snap: s}, evaluator.New(), &rec, func() time.Time { return reconcileClock })
	if err := r.ReconcilePolicy(context.Background(), "biz-a"); err != nil {
		t.Fatal(err)
	}
	if len(rec.Applies) != 1 {
		t.Fatalf("Apply calls = %d, want 1", len(rec.Applies))
	}
	if rec.Applies[0].Trigger != api.TriggerStalePending {
		t.Errorf("Trigger = %s, want stale_pending", rec.Applies[0].Trigger)
	}
}

func TestReconcileSpillToSteadySwitchback(t *testing.T) {
	since := reconcileClock.Add(-12 * time.Minute)
	s := snap(api.StateSpill, since)
	s.OverflowPodsOfPolicy = 0

	rec := action.RecordingAction{}
	r := New(stubBuilder{snap: s}, evaluator.New(), &rec, func() time.Time { return reconcileClock })
	if err := r.ReconcilePolicy(context.Background(), "biz-a"); err != nil {
		t.Fatal(err)
	}
	if len(rec.Applies) != 1 {
		t.Fatalf("Apply calls = %d, want 1", len(rec.Applies))
	}
	if rec.Applies[0].Trigger != api.TriggerSwitchback {
		t.Errorf("Trigger = %s, want switchback", rec.Applies[0].Trigger)
	}
}

func TestReconcileApplyErrorPropagates(t *testing.T) {
	since := reconcileClock.Add(-31 * time.Second)
	s := snap(api.StateSteady, since)
	s.StalestPendingFor = 6 * time.Minute
	s.StalePendingPods = 3

	want := errors.New("apply failed")
	rec := action.RecordingAction{Err: want}
	r := New(stubBuilder{snap: s}, evaluator.New(), &rec, func() time.Time { return reconcileClock })
	err := r.ReconcilePolicy(context.Background(), "biz-a")
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestReconcilePatchOwnershipConflictPropagates(t *testing.T) {
	since := reconcileClock.Add(-31 * time.Second)
	s := snap(api.StateSteady, since)
	s.StalestPendingFor = 6 * time.Minute
	s.StalePendingPods = 3

	applier := stubQueueApplier(func(ctx context.Context, cfg *applycfg.QueueApplyConfiguration, opts metav1.ApplyOptions) (*schedulingv1beta1.Queue, error) {
		return nil, apierrors.NewConflict(
			schema.GroupResource{Group: schedulingv1beta1.GroupName, Resource: "queues"},
			"biz-a",
			errors.New("simulated"),
		)
	})

	r := New(stubBuilder{snap: s}, evaluator.New(), action.NewPatchWithApplier(applier), func() time.Time { return reconcileClock })
	err := r.ReconcilePolicy(context.Background(), "biz-a")
	if !action.IsOwnershipConflict(err) {
		t.Fatalf("expected ownership conflict, got %v", err)
	}
}

type stubQueueApplier func(ctx context.Context, cfg *applycfg.QueueApplyConfiguration, opts metav1.ApplyOptions) (*schedulingv1beta1.Queue, error)

func (f stubQueueApplier) Apply(ctx context.Context, cfg *applycfg.QueueApplyConfiguration, opts metav1.ApplyOptions) (*schedulingv1beta1.Queue, error) {
	return f(ctx, cfg, opts)
}

type stubBuilder struct {
	snap *snapshot.Snapshot
	err  error
}

func (b stubBuilder) Build(ctx context.Context, policyName string, now time.Time) (*snapshot.Snapshot, error) {
	if b.err != nil {
		return nil, b.err
	}
	return b.snap, nil
}

func snap(state api.State, since time.Time) *snapshot.Snapshot {
	return &snapshot.Snapshot{
		Policy:               testPolicy(),
		ObservedAt:           reconcileClock,
		CurrentState:         state,
		ConditionSince:       since,
		DemandResources:      corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10")},
		MaxDedicatedCapacity: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("16")},
	}
}

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
