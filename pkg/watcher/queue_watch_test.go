package watcher

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/config"
)

func newTestQueueWatch(policies []*api.SpillPolicy) (*QueueWatch, *enqueueRecorder) {
	store := config.NewRegistryStore()
	store.Set(config.NewTestRegistry(config.Defaults{}, policies))
	rec := &enqueueRecorder{}
	return &QueueWatch{
		policies: store.Get,
		enqueue:  rec.enqueue,
	}, rec
}

func TestQueueWatch_OnAdd(t *testing.T) {
	w, rec := newTestQueueWatch([]*api.SpillPolicy{{Name: "p1", QueueName: "q1"}})
	w.onAdd(&schedulingv1beta1.Queue{ObjectMeta: metav1.ObjectMeta{Name: "q1"}}, false)
	rec.assertExactly(t, "p1")
}

func TestQueueWatch_OnAdd_UnknownQueue(t *testing.T) {
	w, rec := newTestQueueWatch([]*api.SpillPolicy{{Name: "p1", QueueName: "q1"}})
	w.onAdd(&schedulingv1beta1.Queue{ObjectMeta: metav1.ObjectMeta{Name: "q-other"}}, false)
	rec.assertExactly(t)
}

func TestQueueWatch_OnUpdate_AffinityChange(t *testing.T) {
	w, rec := newTestQueueWatch([]*api.SpillPolicy{{Name: "p1", QueueName: "q1"}})
	old := &schedulingv1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{Name: "q1"},
	}
	new := &schedulingv1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{Name: "q1"},
		Spec: schedulingv1beta1.QueueSpec{
			Affinity: &schedulingv1beta1.Affinity{
				NodeGroupAffinity: &schedulingv1beta1.NodeGroupAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: []string{"ng2"},
				},
			},
		},
	}
	w.onUpdate(old, new)
	rec.assertExactly(t, "p1")
}

func TestQueueWatch_OnUpdate_CapabilityChange(t *testing.T) {
	w, rec := newTestQueueWatch([]*api.SpillPolicy{{Name: "p1", QueueName: "q1"}})
	old := &schedulingv1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{Name: "q1"},
	}
	new := &schedulingv1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{Name: "q1"},
		Spec: schedulingv1beta1.QueueSpec{
			Capability: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100")},
		},
	}
	w.onUpdate(old, new)
	rec.assertExactly(t, "p1")
}

func TestQueueWatch_OnUpdate_SpillAnnotationChange(t *testing.T) {
	w, rec := newTestQueueWatch([]*api.SpillPolicy{{Name: "p1", QueueName: "q1"}})
	old := &schedulingv1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{Name: "q1", Annotations: map[string]string{
			"spill.example.com/state": "Steady",
		}},
	}
	new := &schedulingv1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{Name: "q1", Annotations: map[string]string{
			"spill.example.com/state": "Spill",
		}},
	}
	w.onUpdate(old, new)
	rec.assertExactly(t, "p1")
}

func TestQueueWatch_OnUpdate_NoChange(t *testing.T) {
	w, rec := newTestQueueWatch([]*api.SpillPolicy{{Name: "p1", QueueName: "q1"}})
	q := &schedulingv1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{Name: "q1", Annotations: map[string]string{
			"spill.example.com/state": "Steady",
		}},
	}
	w.onUpdate(q, q)
	rec.assertExactly(t)
}

func TestQueueWatch_OnUpdate_UnrelatedAnnotationChange(t *testing.T) {
	w, rec := newTestQueueWatch([]*api.SpillPolicy{{Name: "p1", QueueName: "q1"}})
	old := &schedulingv1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{Name: "q1"},
	}
	new := &schedulingv1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{Name: "q1", Annotations: map[string]string{
			"unrelated/key": "value",
		}},
	}
	w.onUpdate(old, new)
	rec.assertExactly(t)
}

func TestQueueWatch_OnDelete(t *testing.T) {
	w, rec := newTestQueueWatch([]*api.SpillPolicy{{Name: "p1", QueueName: "q1"}})
	w.onDelete(&schedulingv1beta1.Queue{ObjectMeta: metav1.ObjectMeta{Name: "q1"}})
	rec.assertExactly(t, "p1")
}
