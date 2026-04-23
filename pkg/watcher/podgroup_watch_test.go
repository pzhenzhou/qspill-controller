package watcher

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/config"
)

func newTestPGWatch(policies []*api.SpillPolicy) (*PodGroupWatch, *enqueueRecorder) {
	store := config.NewRegistryStore()
	store.Set(config.NewTestRegistry(config.Defaults{}, policies))
	rec := &enqueueRecorder{}
	return &PodGroupWatch{
		policies: store.Get,
		enqueue:  rec.enqueue,
	}, rec
}

func TestPodGroupWatch_OnAdd_EnqueuesMatchingPolicy(t *testing.T) {
	w, rec := newTestPGWatch([]*api.SpillPolicy{{Name: "p1", QueueName: "q1"}})
	w.onAdd(&schedulingv1beta1.PodGroup{Spec: schedulingv1beta1.PodGroupSpec{Queue: "q1"}}, false)
	rec.assertExactly(t, "p1")
}

func TestPodGroupWatch_OnAdd_IgnoresUnknownQueue(t *testing.T) {
	w, rec := newTestPGWatch([]*api.SpillPolicy{{Name: "p1", QueueName: "q1"}})
	w.onAdd(&schedulingv1beta1.PodGroup{Spec: schedulingv1beta1.PodGroupSpec{Queue: "q-unknown"}}, false)
	rec.assertExactly(t)
}

func TestPodGroupWatch_OnUpdate_PhaseChange(t *testing.T) {
	w, rec := newTestPGWatch([]*api.SpillPolicy{{Name: "p1", QueueName: "q1"}})
	old := &schedulingv1beta1.PodGroup{Spec: schedulingv1beta1.PodGroupSpec{Queue: "q1"}, Status: schedulingv1beta1.PodGroupStatus{Phase: schedulingv1beta1.PodGroupPending}}
	new := &schedulingv1beta1.PodGroup{Spec: schedulingv1beta1.PodGroupSpec{Queue: "q1"}, Status: schedulingv1beta1.PodGroupStatus{Phase: schedulingv1beta1.PodGroupRunning}}
	w.onUpdate(old, new)
	rec.assertExactly(t, "p1")
}

func TestPodGroupWatch_OnUpdate_QueueMutation_EnqueuesBoth(t *testing.T) {
	w, rec := newTestPGWatch([]*api.SpillPolicy{
		{Name: "p1", QueueName: "q1"},
		{Name: "p2", QueueName: "q2"},
	})
	old := &schedulingv1beta1.PodGroup{Spec: schedulingv1beta1.PodGroupSpec{Queue: "q1"}}
	new := &schedulingv1beta1.PodGroup{Spec: schedulingv1beta1.PodGroupSpec{Queue: "q2"}}
	w.onUpdate(old, new)
	rec.assertContains(t, "p1")
	rec.assertContains(t, "p2")
}

func TestPodGroupWatch_OnUpdate_NoChange(t *testing.T) {
	w, rec := newTestPGWatch([]*api.SpillPolicy{{Name: "p1", QueueName: "q1"}})
	pg := &schedulingv1beta1.PodGroup{Spec: schedulingv1beta1.PodGroupSpec{Queue: "q1"}, Status: schedulingv1beta1.PodGroupStatus{Phase: schedulingv1beta1.PodGroupRunning}}
	w.onUpdate(pg, pg)
	rec.assertExactly(t)
}

func TestPodGroupWatch_OnDelete_Tombstone(t *testing.T) {
	w, rec := newTestPGWatch([]*api.SpillPolicy{{Name: "p1", QueueName: "q1"}})
	pg := &schedulingv1beta1.PodGroup{Spec: schedulingv1beta1.PodGroupSpec{Queue: "q1"}}
	w.onDelete(pg)
	rec.assertExactly(t, "p1")
}

func TestPodGroupChanged_MinResources(t *testing.T) {
	old := &schedulingv1beta1.PodGroup{
		Spec: schedulingv1beta1.PodGroupSpec{
			MinResources: &corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
		},
	}
	new := &schedulingv1beta1.PodGroup{
		Spec: schedulingv1beta1.PodGroupSpec{
			MinResources: &corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
		},
	}
	if !podGroupChanged(old, new) {
		t.Error("MinResources change should be detected")
	}
}
