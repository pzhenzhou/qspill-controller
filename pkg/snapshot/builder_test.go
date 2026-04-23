package snapshot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/config"
)

const (
	testLabelKey    = "volcano.sh/nodegroup-name"
	testQueueName   = "biz-a"
	testPolicyName  = "biz-a"
	testDedicated   = "ng2"
	testOverflow    = "ng1"
	testOtherQueue  = "biz-b"
	testOtherPolicy = "biz-b"
)

// fixtureBuilder packages the lengthy plumbing each test would otherwise
// repeat: build a policy + resolver + listers from a small set of slices,
// then return a ready-to-call Builder. Tests that need to mutate the inputs
// keep references into the staticListers value.
type fixtureBuilder struct {
	resolver *staticResolver
	listers  *staticListers
	builder  Builder
}

func newFixture(t *testing.T) *fixtureBuilder {
	t.Helper()
	policy := &api.SpillPolicy{
		Name:               testPolicyName,
		QueueName:          testQueueName,
		DedicatedNodeGroup: testDedicated,
		OverflowNodeGroup:  testOverflow,
		Thresholds: api.Thresholds{
			TimeOn:         30 * time.Second,
			TimeOff:        10 * time.Minute,
			TimePendingMax: 5 * time.Minute,
		},
	}
	resolver := &staticResolver{
		policies: map[string]*api.SpillPolicy{policy.Name: policy},
		defaults: config.Defaults{NodeGroupLabelKey: testLabelKey},
	}
	listers := &staticListers{queues: map[string]*schedulingv1beta1.Queue{}}
	return &fixtureBuilder{
		resolver: resolver,
		listers:  listers,
		builder:  NewBuilder(resolver, listers.toListers()),
	}
}

func (f *fixtureBuilder) addPolicy(p *api.SpillPolicy) {
	f.resolver.policies[p.Name] = p
}

func (f *fixtureBuilder) build(t *testing.T, policyName string) *Snapshot {
	t.Helper()
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	snap, err := f.builder.Build(context.Background(), policyName, now)
	if err != nil {
		t.Fatalf("Build(%s) failed: %v", policyName, err)
	}
	return snap
}

func pgInQueue(name, queue string, phase schedulingv1beta1.PodGroupPhase, spec *corev1.ResourceList) *schedulingv1beta1.PodGroup {
	return &schedulingv1beta1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       schedulingv1beta1.PodGroupSpec{Queue: queue, MinResources: spec},
		Status:     schedulingv1beta1.PodGroupStatus{Phase: phase},
	}
}

func TestBuildUnknownPolicyReturnsError(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	_, err := f.builder.Build(context.Background(), "ghost", now)
	if !errors.Is(err, ErrUnknownPolicy) {
		t.Errorf("err = %v, want wrapping ErrUnknownPolicy", err)
	}
}

func TestBuildMissingQueueDefaultsToSteady(t *testing.T) {
	f := newFixture(t)
	snap := f.build(t, testPolicyName)
	if snap.Queue != nil {
		t.Errorf("Queue = %+v, want nil", snap.Queue)
	}
	if snap.CurrentState != api.StateSteady {
		t.Errorf("CurrentState = %s, want Steady", snap.CurrentState)
	}
	if !snap.ConditionSince.IsZero() {
		t.Errorf("ConditionSince = %s, want zero", snap.ConditionSince)
	}
	if snap.DecisionHash != "" {
		t.Errorf("DecisionHash = %q, want empty", snap.DecisionHash)
	}
}

func TestBuildReadsQueueAnnotations(t *testing.T) {
	f := newFixture(t)
	since := time.Date(2026, 4, 23, 11, 30, 0, 0, time.UTC)
	f.listers.queues[testQueueName] = &schedulingv1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{
			Name: testQueueName,
			Annotations: map[string]string{
				api.AnnotationState:          string(api.StateSpill),
				api.AnnotationConditionSince: since.Format(time.RFC3339),
				api.AnnotationDecisionHash:   "deadbeef",
			},
		},
	}

	snap := f.build(t, testPolicyName)
	if snap.CurrentState != api.StateSpill {
		t.Errorf("CurrentState = %s, want Spill", snap.CurrentState)
	}
	if !snap.ConditionSince.Equal(since) {
		t.Errorf("ConditionSince = %s, want %s", snap.ConditionSince, since)
	}
	if snap.DecisionHash != "deadbeef" {
		t.Errorf("DecisionHash = %q, want deadbeef", snap.DecisionHash)
	}
}

func TestBuildIgnoresGarbageAnnotations(t *testing.T) {
	f := newFixture(t)
	f.listers.queues[testQueueName] = &schedulingv1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{
			Name: testQueueName,
			Annotations: map[string]string{
				api.AnnotationState:          "Frobnicated",
				api.AnnotationConditionSince: "not-a-timestamp",
			},
		},
	}
	snap := f.build(t, testPolicyName)
	if snap.CurrentState != api.StateSteady {
		t.Errorf("CurrentState = %s, want Steady (unknown value falls back)", snap.CurrentState)
	}
	if !snap.ConditionSince.IsZero() {
		t.Errorf("ConditionSince = %s, want zero on garbage input", snap.ConditionSince)
	}
}

func TestBuildFiltersPodGroupsByQueue(t *testing.T) {
	f := newFixture(t)
	mineSpec := resourceList("cpu", "1")
	mine := pgInQueue("mine", testQueueName, schedulingv1beta1.PodGroupRunning, &mineSpec)
	otherSpec := resourceList("cpu", "10")
	otherQueue := pgInQueue("other-queue", testOtherQueue, schedulingv1beta1.PodGroupRunning, &otherSpec)
	completed := pgInQueue("done", testQueueName, schedulingv1beta1.PodGroupCompleted, &mineSpec)
	f.listers.pgs = []*schedulingv1beta1.PodGroup{mine, otherQueue, completed}

	snap := f.build(t, testPolicyName)
	if len(snap.PodGroups) != 1 || snap.PodGroups[0].Name != "mine" {
		t.Errorf("PodGroups = %+v, want only the matching non-terminal entry", snap.PodGroups)
	}
}

func TestBuildAttributesPodsViaAnnotation(t *testing.T) {
	f := newFixture(t)
	rl := resourceList("cpu", "1")
	pg := pgInQueue("pg-mine", testQueueName, schedulingv1beta1.PodGroupRunning, &rl)
	f.listers.pgs = []*schedulingv1beta1.PodGroup{pg}

	pMine := podForGroup("p-mine", "pg-mine", resourceList("cpu", "1"))
	pMine.Spec.NodeName = "ded-1"
	pStranger := podForGroup("p-stranger", "pg-other", resourceList("cpu", "8"))
	pStranger.Spec.NodeName = "anywhere"
	f.listers.pods = []*corev1.Pod{pMine, pStranger}

	f.listers.nodes = []*corev1.Node{
		nodeWithGroup("ded-1", testDedicated, resourceList("cpu", "16")),
		nodeWithGroup("ovr-1", testOverflow, resourceList("cpu", "32")),
	}

	snap := f.build(t, testPolicyName)
	if snap.DedicatedPodsOfPolicy != 1 {
		t.Errorf("DedicatedPodsOfPolicy = %d, want 1", snap.DedicatedPodsOfPolicy)
	}
	if snap.OverflowPodsOfPolicy != 0 {
		t.Errorf("OverflowPodsOfPolicy = %d, want 0", snap.OverflowPodsOfPolicy)
	}
	if got := snap.MaxDedicatedCapacity[corev1.ResourceCPU]; got.String() != "16" {
		t.Errorf("MaxDedicatedCapacity cpu = %s, want 16", got.String())
	}
	if got := snap.DemandResources[corev1.ResourceCPU]; got.String() != "1" {
		t.Errorf("DemandResources cpu = %s, want 1 (spec demand wins)", got.String())
	}
}

func TestBuildIsolatesPoliciesByQueue(t *testing.T) {
	f := newFixture(t)
	other := &api.SpillPolicy{
		Name:               testOtherPolicy,
		QueueName:          testOtherQueue,
		DedicatedNodeGroup: "ng4",
		OverflowNodeGroup:  "ng3",
		Thresholds: api.Thresholds{
			TimePendingMax: time.Minute,
		},
	}
	f.addPolicy(other)

	rlA := resourceList("cpu", "1")
	rlB := resourceList("cpu", "2")
	f.listers.pgs = []*schedulingv1beta1.PodGroup{
		pgInQueue("pg-a", testQueueName, schedulingv1beta1.PodGroupRunning, &rlA),
		pgInQueue("pg-b", testOtherQueue, schedulingv1beta1.PodGroupRunning, &rlB),
	}

	snapA := f.build(t, testPolicyName)
	snapB := f.build(t, testOtherPolicy)
	if len(snapA.PodGroups) != 1 || snapA.PodGroups[0].Name != "pg-a" {
		t.Errorf("policy A saw %+v, want only pg-a", snapA.PodGroups)
	}
	if len(snapB.PodGroups) != 1 || snapB.PodGroups[0].Name != "pg-b" {
		t.Errorf("policy B saw %+v, want only pg-b", snapB.PodGroups)
	}
}

func TestBuildAutoscalerExhaustedScopedToPolicyPods(t *testing.T) {
	f := newFixture(t)
	rl := resourceList("cpu", "1")
	pg := pgInQueue("pg-mine", testQueueName, schedulingv1beta1.PodGroupRunning, &rl)
	f.listers.pgs = []*schedulingv1beta1.PodGroup{pg}

	policyPod := podForGroup("p-mine", "pg-mine", resourceList("cpu", "1"))
	f.listers.pods = []*corev1.Pod{policyPod}

	f.listers.events = []*corev1.Event{
		{Reason: "NotTriggerScaleUp", InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p-mine"}},
		{Reason: "NotTriggerScaleUp", InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "stranger"}},
	}

	snap := f.build(t, testPolicyName)
	if !snap.AutoscalerExhausted {
		t.Error("AutoscalerExhausted = false, want true (matched policy pod)")
	}
}

func TestBuildAutoscalerExhaustedFalseWithoutPolicyPodMatch(t *testing.T) {
	f := newFixture(t)
	rl := resourceList("cpu", "1")
	pg := pgInQueue("pg-mine", testQueueName, schedulingv1beta1.PodGroupRunning, &rl)
	f.listers.pgs = []*schedulingv1beta1.PodGroup{pg}
	f.listers.pods = []*corev1.Pod{podForGroup("p-mine", "pg-mine", resourceList("cpu", "1"))}
	f.listers.events = []*corev1.Event{
		{Reason: "NotTriggerScaleUp", InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "stranger"}},
	}

	snap := f.build(t, testPolicyName)
	if snap.AutoscalerExhausted {
		t.Error("AutoscalerExhausted = true, want false (event was for unrelated pod)")
	}
}

func TestBuildStalePendingPropagates(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	rl := resourceList("cpu", "1")
	pg := pgInQueue("pg-mine", testQueueName, schedulingv1beta1.PodGroupRunning, &rl)
	f.listers.pgs = []*schedulingv1beta1.PodGroup{pg}

	stuck := podForGroup("p-stuck", "pg-mine", resourceList("cpu", "1"))
	stuck.Status.Conditions = []corev1.PodCondition{{
		Type:               corev1.PodScheduled,
		Status:             corev1.ConditionFalse,
		LastTransitionTime: metav1.NewTime(now.Add(-7 * time.Minute)),
	}}
	f.listers.pods = []*corev1.Pod{stuck}

	snap, err := f.builder.Build(context.Background(), testPolicyName, now)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if snap.StalestPendingFor != 7*time.Minute {
		t.Errorf("StalestPendingFor = %s, want 7m", snap.StalestPendingFor)
	}
	if snap.StalePendingPods != 1 {
		t.Errorf("StalePendingPods = %d, want 1", snap.StalePendingPods)
	}
}

func TestBuildCountsDemandEstimatedPGs(t *testing.T) {
	f := newFixture(t)
	pgEmpty := pgInQueue("pg-empty", testQueueName, schedulingv1beta1.PodGroupRunning, nil)
	rlSpec := resourceList("cpu", "1")
	pgSpec := pgInQueue("pg-spec", testQueueName, schedulingv1beta1.PodGroupRunning, &rlSpec)
	f.listers.pgs = []*schedulingv1beta1.PodGroup{pgEmpty, pgSpec}

	f.listers.pods = []*corev1.Pod{
		podForGroup("p-empty", "pg-empty", resourceList("cpu", "750m")),
	}

	snap := f.build(t, testPolicyName)
	if snap.DemandEstimatedPGs != 1 {
		t.Errorf("DemandEstimatedPGs = %d, want 1", snap.DemandEstimatedPGs)
	}
	if got := snap.DemandResources[corev1.ResourceCPU]; got.String() != "1750m" {
		t.Errorf("DemandResources cpu = %s, want 1750m", got.String())
	}
}

func TestBuildPropagatesListerErrors(t *testing.T) {
	f := newFixture(t)
	f.listers.pgErr = errors.New("podgroup boom")
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	_, err := f.builder.Build(context.Background(), testPolicyName, now)
	if err == nil || !strings.Contains(err.Error(), "podgroup boom") {
		t.Errorf("err = %v, want wrapping podgroup boom", err)
	}
}

func TestBuildObservedAtMirrorsClock(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	snap, err := f.builder.Build(context.Background(), testPolicyName, now)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if !snap.ObservedAt.Equal(now) {
		t.Errorf("ObservedAt = %s, want %s", snap.ObservedAt, now)
	}
}
