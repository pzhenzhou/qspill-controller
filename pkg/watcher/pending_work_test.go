package watcher

import (
	"testing"
	"time"
)

func TestPendingWorkMap_ShouldEnqueueAfter_FirstCall(t *testing.T) {
	m := NewPendingWorkMap()
	deadline := time.Now().Add(5 * time.Minute)
	if !m.ShouldEnqueueAfter("p1", deadline) {
		t.Error("first call should return true")
	}
}

func TestPendingWorkMap_ShouldEnqueueAfter_LaterDeadlineDropped(t *testing.T) {
	m := NewPendingWorkMap()
	early := time.Now().Add(2 * time.Minute)
	late := time.Now().Add(10 * time.Minute)

	m.ShouldEnqueueAfter("p1", early)
	if m.ShouldEnqueueAfter("p1", late) {
		t.Error("later deadline should be dropped when earlier one exists")
	}
}

func TestPendingWorkMap_ShouldEnqueueAfter_EarlierDeadlineAccepted(t *testing.T) {
	m := NewPendingWorkMap()
	late := time.Now().Add(10 * time.Minute)
	early := time.Now().Add(2 * time.Minute)

	m.ShouldEnqueueAfter("p1", late)
	if !m.ShouldEnqueueAfter("p1", early) {
		t.Error("earlier deadline should be accepted")
	}
}

func TestPendingWorkMap_Clear(t *testing.T) {
	m := NewPendingWorkMap()
	deadline := time.Now().Add(5 * time.Minute)
	m.ShouldEnqueueAfter("p1", deadline)
	m.Clear("p1")

	if m.Size() != 0 {
		t.Errorf("Size() = %d, want 0 after Clear", m.Size())
	}

	if !m.ShouldEnqueueAfter("p1", deadline) {
		t.Error("after Clear, same deadline should be accepted again")
	}
}

func TestPendingWorkMap_IndependentPolicies(t *testing.T) {
	m := NewPendingWorkMap()
	d1 := time.Now().Add(5 * time.Minute)
	d2 := time.Now().Add(10 * time.Minute)

	if !m.ShouldEnqueueAfter("p1", d1) {
		t.Error("p1 first call should return true")
	}
	if !m.ShouldEnqueueAfter("p2", d2) {
		t.Error("p2 first call should return true (independent of p1)")
	}
	if m.Size() != 2 {
		t.Errorf("Size() = %d, want 2", m.Size())
	}
}
