package action

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	applycfg "volcano.sh/apis/pkg/client/applyconfiguration/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/evaluator"
	"github.com/pzhenzhou/qspill-controller/pkg/snapshot"
)

type stubQueueApplier func(ctx context.Context, cfg *applycfg.QueueApplyConfiguration, opts metav1.ApplyOptions) (*schedulingv1beta1.Queue, error)

func (f stubQueueApplier) Apply(ctx context.Context, cfg *applycfg.QueueApplyConfiguration, opts metav1.ApplyOptions) (*schedulingv1beta1.Queue, error) {
	return f(ctx, cfg, opts)
}

func TestPatchApplyPayloadOmitsCapability(t *testing.T) {
	var captured []byte
	applier := stubQueueApplier(func(ctx context.Context, cfg *applycfg.QueueApplyConfiguration, opts metav1.ApplyOptions) (*schedulingv1beta1.Queue, error) {
		if opts.FieldManager != FieldManager || !opts.Force {
			t.Fatalf("unexpected ApplyOptions: %+v", opts)
		}
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		captured = data
		return &schedulingv1beta1.Queue{}, nil
	})

	d := decisionFixtureSteadyToSpillStalePending(t)
	p := NewPatchWithApplier(applier)
	if err := p.Apply(context.Background(), d); err != nil {
		t.Fatal(err)
	}

	var cfg applycfg.QueueApplyConfiguration
	if err := json.Unmarshal(captured, &cfg); err != nil {
		t.Fatalf("unmarshal apply payload: %v", err)
	}
	if cfg.Spec == nil {
		t.Fatal("expected spec")
	}
	raw, err := json.Marshal(cfg.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"capability"`) {
		t.Fatalf("apply spec must not mention capability; got JSON %s", raw)
	}
	if !strings.Contains(string(raw), `"affinity"`) {
		t.Fatalf("apply spec must include affinity; got JSON %s", raw)
	}
}

func TestPatchApplyOwnershipConflictTyped(t *testing.T) {
	applier := stubQueueApplier(func(ctx context.Context, cfg *applycfg.QueueApplyConfiguration, opts metav1.ApplyOptions) (*schedulingv1beta1.Queue, error) {
		return nil, apierrors.NewConflict(
			schema.GroupResource{Group: schedulingv1beta1.GroupName, Resource: "queues"},
			"biz-a",
			errors.New("simulated"),
		)
	})

	d := decisionFixtureSteadyToSpillStalePending(t)
	err := NewPatchWithApplier(applier).Apply(context.Background(), d)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsOwnershipConflict(err) {
		t.Fatalf("expected ownership conflict wrap, got %v", err)
	}
}

var patchTestClock = time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)

func snapFixture(state api.State, since time.Time) *snapshot.Snapshot {
	p := testPolicyFixture()
	return &snapshot.Snapshot{
		Policy:                p,
		ObservedAt:            patchTestClock,
		CurrentState:          state,
		ConditionSince:        since,
		DedicatedPodsOfPolicy: 0,
	}
}

func testPolicyFixture() *api.SpillPolicy {
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

func decisionFixtureSteadyToSpillStalePending(t *testing.T) api.Decision {
	t.Helper()
	since := patchTestClock.Add(-31 * time.Second)
	s := snapFixture(api.StateSteady, since)
	s.StalestPendingFor = 6 * time.Minute
	s.StalePendingPods = 3
	return evaluator.New().Evaluate(s)
}
