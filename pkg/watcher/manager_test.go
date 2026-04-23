package watcher

import (
	"context"
	"errors"
	"testing"
	"time"

	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/pzhenzhou/qspill-controller/pkg/config"
	"github.com/pzhenzhou/qspill-controller/pkg/snapshot"
)

func TestNewManager_NilKubeClient(t *testing.T) {
	_, err := NewManager(nil, nil, nil, nil, 0, 0, "", "", "")
	if err == nil {
		t.Fatal("expected error for nil kube client")
	}
}

func TestNewManager_NilVolcanoClient(t *testing.T) {
	store := config.NewRegistryStore()
	_, err := NewManager(kubefake.NewSimpleClientset(), nil, store, fakeReconcilerFactory(nil), 30*time.Second, 30*time.Second, "k", "ns", "cm")
	if err == nil {
		t.Fatal("expected error for nil volcano client")
	}
}

func TestNewManager_ZeroResync(t *testing.T) {
	store := config.NewRegistryStore()
	_, err := NewManager(kubefake.NewSimpleClientset(), &volcanoStub{}, store, fakeReconcilerFactory(nil), 0, 30*time.Second, "k", "ns", "cm")
	if err == nil {
		t.Fatal("expected error for zero resync period")
	}
}

func TestNewManager_ZeroReconcileResync(t *testing.T) {
	store := config.NewRegistryStore()
	_, err := NewManager(kubefake.NewSimpleClientset(), &volcanoStub{}, store, fakeReconcilerFactory(nil), 30*time.Second, 0, "k", "ns", "cm")
	if err == nil {
		t.Fatal("expected error for zero reconcile resync period")
	}
}

func TestNewManager_NilReconcilerFactory(t *testing.T) {
	store := config.NewRegistryStore()
	_, err := NewManager(kubefake.NewSimpleClientset(), &volcanoStub{}, store, nil, 30*time.Second, 30*time.Second, "k", "ns", "cm")
	if err == nil {
		t.Fatal("expected error for nil reconciler factory")
	}
}

func TestNewManager_EmptyNodeGroupKey(t *testing.T) {
	store := config.NewRegistryStore()
	_, err := NewManager(kubefake.NewSimpleClientset(), &volcanoStub{}, store, fakeReconcilerFactory(nil), 30*time.Second, 30*time.Second, "", "ns", "cm")
	if err == nil {
		t.Fatal("expected error for empty node group key")
	}
}

func TestNewManager_Valid(t *testing.T) {
	store := config.NewRegistryStore()
	mgr, err := NewManager(kubefake.NewSimpleClientset(), &volcanoStub{}, store, fakeReconcilerFactory(nil), 30*time.Second, 30*time.Second, "k", "ns", "cm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr == nil {
		t.Fatal("manager should not be nil")
	}
}

func TestManager_Enqueue(t *testing.T) {
	store := config.NewRegistryStore()
	mgr, err := NewManager(kubefake.NewSimpleClientset(), &volcanoStub{}, store, fakeReconcilerFactory(nil), 30*time.Second, 30*time.Second, "k", "ns", "cm")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	mgr.Enqueue("policy-a")
	if mgr.QueueLen() != 1 {
		t.Errorf("QueueLen() = %d, want 1", mgr.QueueLen())
	}
}

func TestManager_EnqueueAfter_Coalesces(t *testing.T) {
	store := config.NewRegistryStore()
	mgr, err := NewManager(kubefake.NewSimpleClientset(), &volcanoStub{}, store, fakeReconcilerFactory(nil), 30*time.Second, 30*time.Second, "k", "ns", "cm")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	mgr.EnqueueAfter("policy-a", 1*time.Hour)
	mgr.EnqueueAfter("policy-a", 2*time.Hour)
	if mgr.pendingWork.Size() != 1 {
		t.Errorf("pendingWork.Size() = %d, want 1", mgr.pendingWork.Size())
	}
}

func TestManager_ProcessNextItem_DropsUnknownPolicy(t *testing.T) {
	store := config.NewRegistryStore()
	mgr, err := NewManager(kubefake.NewSimpleClientset(), &volcanoStub{}, store, fakeReconcilerFactory(snapshot.ErrUnknownPolicy), 30*time.Second, 30*time.Second, "k", "ns", "cm")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	mgr.reconciler = fakeReconciler{err: snapshot.ErrUnknownPolicy}

	mgr.Enqueue("deleted-policy")
	if !mgr.processNextItem(context.Background()) {
		t.Fatal("processNextItem returned false, want true")
	}
	if got := mgr.workqueue.NumRequeues("deleted-policy"); got != 0 {
		t.Fatalf("NumRequeues(deleted-policy) = %d, want 0", got)
	}
	if got := mgr.QueueLen(); got != 0 {
		t.Fatalf("QueueLen() = %d, want 0", got)
	}
}

func TestManager_ProcessNextItem_RequeuesRetriableError(t *testing.T) {
	store := config.NewRegistryStore()
	reconcileErr := errors.New("boom")
	mgr, err := NewManager(kubefake.NewSimpleClientset(), &volcanoStub{}, store, fakeReconcilerFactory(reconcileErr), 30*time.Second, 30*time.Second, "k", "ns", "cm")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	mgr.reconciler = fakeReconciler{err: reconcileErr}

	mgr.Enqueue("policy-a")
	if !mgr.processNextItem(context.Background()) {
		t.Fatal("processNextItem returned false, want true")
	}
	if got := mgr.workqueue.NumRequeues("policy-a"); got != 1 {
		t.Fatalf("NumRequeues(policy-a) = %d, want 1", got)
	}
}
