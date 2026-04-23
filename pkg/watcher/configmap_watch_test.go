package watcher

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/config"
)

func newTestCMWatch(initialPolicies []*api.SpillPolicy) (*ConfigMapWatch, *config.RegistryStore, *enqueueRecorder) {
	store := config.NewRegistryStore()
	store.Set(config.NewTestRegistry(config.Defaults{
		NodeGroupLabelKey: "volcano.sh/nodegroup-name",
		Action:            api.ActionModeNope,
	}, initialPolicies))
	rec := &enqueueRecorder{}
	return &ConfigMapWatch{
		registryStore: store,
		enqueue:       rec.enqueue,
	}, store, rec
}

func TestConfigMapWatch_ValidSwap_EnqueuesOldAndNew(t *testing.T) {
	w, store, rec := newTestCMWatch([]*api.SpillPolicy{
		{Name: "old-p", QueueName: "old-q", DedicatedNodeGroup: "ng1", OverflowNodeGroup: "ng2"},
	})

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "test-config"},
		Data: map[string]string{
			configDataKey: `
defaults:
  nodeGroupLabelKey: volcano.sh/nodegroup-name
  action: Nope
policies:
  - name: new-p
    queueName: new-q
    dedicatedNodeGroup: ng3
    overflowNodeGroup: ng4
`,
		},
	}
	w.reload(cm)

	// Registry should have been swapped.
	reg := store.Get()
	if _, ok := reg.PolicyByName("new-p"); !ok {
		t.Error("new policy should be present in registry after reload")
	}

	// Both old-p and new-p should be enqueued.
	rec.assertContains(t, "old-p")
	rec.assertContains(t, "new-p")
}

func TestConfigMapWatch_InvalidConfig_KeepsPriorRegistry(t *testing.T) {
	w, store, rec := newTestCMWatch([]*api.SpillPolicy{
		{Name: "stable", QueueName: "q1", DedicatedNodeGroup: "ng1", OverflowNodeGroup: "ng2"},
	})

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "test-config"},
		Data: map[string]string{
			configDataKey: `{{{invalid yaml`,
		},
	}
	w.reload(cm)

	// Registry should NOT have been swapped.
	reg := store.Get()
	if _, ok := reg.PolicyByName("stable"); !ok {
		t.Error("prior registry should be retained on invalid config")
	}

	rec.assertEmpty(t)
}

func TestConfigMapWatch_OnAdd(t *testing.T) {
	w, _, _ := newTestCMWatch(nil)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "test-config"},
		Data: map[string]string{
			configDataKey: `
defaults:
  nodeGroupLabelKey: volcano.sh/nodegroup-name
  action: Nope
policies:
  - name: via-add
    queueName: q-add
    dedicatedNodeGroup: ng1
    overflowNodeGroup: ng2
`,
		},
	}
	w.onAdd(cm, false)
	// No panic, reload ran.
}
