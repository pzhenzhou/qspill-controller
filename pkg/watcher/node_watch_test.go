package watcher

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/config"
)

const testNodeGroupKey = "volcano.sh/nodegroup-name"

func newTestNodeWatch(policies []*api.SpillPolicy) (*NodeWatch, *enqueueRecorder) {
	store := config.NewRegistryStore()
	store.Set(config.NewTestRegistry(config.Defaults{NodeGroupLabelKey: testNodeGroupKey}, policies))
	rec := &enqueueRecorder{}
	return &NodeWatch{
		policies:     store.Get,
		enqueue:      rec.enqueue,
		nodeGroupKey: testNodeGroupKey,
	}, rec
}

func TestNodeWatch_OnAdd(t *testing.T) {
	w, rec := newTestNodeWatch([]*api.SpillPolicy{
		{Name: "p1", DedicatedNodeGroup: "ng2"},
	})
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Labels: map[string]string{testNodeGroupKey: "ng2"},
	}}
	w.onAdd(node, false)
	rec.assertExactly(t, "p1")
}

func TestNodeWatch_OnAdd_UnknownValue(t *testing.T) {
	w, rec := newTestNodeWatch([]*api.SpillPolicy{
		{Name: "p1", DedicatedNodeGroup: "ng2"},
	})
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Labels: map[string]string{testNodeGroupKey: "ng-unknown"},
	}}
	w.onAdd(node, false)
	rec.assertEmpty(t)
}

func TestNodeWatch_OnUpdate_Relabel(t *testing.T) {
	w, rec := newTestNodeWatch([]*api.SpillPolicy{
		{Name: "p1", DedicatedNodeGroup: "ng2"},
		{Name: "p2", DedicatedNodeGroup: "ng3"},
	})
	old := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{testNodeGroupKey: "ng2"}}}
	new := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{testNodeGroupKey: "ng3"}}}
	w.onUpdate(old, new)
	rec.assertContains(t, "p1")
	rec.assertContains(t, "p2")
}

func TestNodeWatch_OnUpdate_SupplyChange(t *testing.T) {
	w, rec := newTestNodeWatch([]*api.SpillPolicy{
		{Name: "p1", DedicatedNodeGroup: "ng2"},
	})
	old := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{testNodeGroupKey: "ng2"}},
		Status:     corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")}},
	}
	new := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{testNodeGroupKey: "ng2"}},
		Status:     corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("16")}},
	}
	w.onUpdate(old, new)
	rec.assertExactly(t, "p1")
}

func TestNodeWatch_OnUpdate_NoChange(t *testing.T) {
	w, rec := newTestNodeWatch([]*api.SpillPolicy{
		{Name: "p1", DedicatedNodeGroup: "ng2"},
	})
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{testNodeGroupKey: "ng2"}}}
	w.onUpdate(node, node)
	rec.assertEmpty(t)
}

func TestNodeWatch_OnDelete_Tombstone(t *testing.T) {
	w, rec := newTestNodeWatch([]*api.SpillPolicy{
		{Name: "p1", DedicatedNodeGroup: "ng2"},
	})
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   "node-1",
		Labels: map[string]string{testNodeGroupKey: "ng2"},
	}}
	tombstone := cache.DeletedFinalStateUnknown{Key: "node-1", Obj: node}
	w.onDelete(tombstone)
	rec.assertExactly(t, "p1")
}

func TestNodeWatch_OnUpdate_UnschedulableChange(t *testing.T) {
	w, rec := newTestNodeWatch([]*api.SpillPolicy{
		{Name: "p1", DedicatedNodeGroup: "ng2"},
	})
	old := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{testNodeGroupKey: "ng2"}},
		Spec:       corev1.NodeSpec{Unschedulable: false},
	}
	new := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{testNodeGroupKey: "ng2"}},
		Spec:       corev1.NodeSpec{Unschedulable: true},
	}
	w.onUpdate(old, new)
	rec.assertExactly(t, "p1")
}

func TestNodeWatch_OnUpdate_ReadyConditionChange(t *testing.T) {
	w, rec := newTestNodeWatch([]*api.SpillPolicy{
		{Name: "p1", DedicatedNodeGroup: "ng2"},
	})
	old := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{testNodeGroupKey: "ng2"}},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	new := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{testNodeGroupKey: "ng2"}},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}},
		},
	}
	w.onUpdate(old, new)
	rec.assertExactly(t, "p1")
}
