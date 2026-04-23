package watcher

import (
	"sync"
	"testing"
)

// enqueueRecorder captures policy names passed to Enqueue for assertions.
type enqueueRecorder struct {
	mu    sync.Mutex
	names []string
}

func (r *enqueueRecorder) enqueue(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names = append(r.names, name)
}

func (r *enqueueRecorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.names))
	copy(out, r.names)
	return out
}

func (r *enqueueRecorder) assertExactly(t *testing.T, want ...string) {
	t.Helper()
	got := r.list()
	if len(got) != len(want) {
		t.Errorf("enqueued %v, want %v", got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("enqueued[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func (r *enqueueRecorder) assertContains(t *testing.T, name string) {
	t.Helper()
	for _, n := range r.list() {
		if n == name {
			return
		}
	}
	t.Errorf("enqueued %v, want to contain %q", r.list(), name)
}

func (r *enqueueRecorder) assertEmpty(t *testing.T) {
	t.Helper()
	got := r.list()
	if len(got) != 0 {
		t.Errorf("expected no enqueues, got %v", got)
	}
}
