package watcher

import (
	"time"

	"github.com/puzpuzpuz/xsync/v4"
)

// PendingWorkMap tracks the earliest deferred-enqueue deadline per policy so
// the stale-pending watcher can drop redundant EnqueueAfter calls when one is
// already pending for a closer (or equal) deadline. The reconciler worker
// calls Clear after processing a key so the next stale-pending event can
// re-register.
type PendingWorkMap struct {
	m *xsync.Map[string, time.Time]
}

// NewPendingWorkMap returns an empty map.
func NewPendingWorkMap() *PendingWorkMap {
	return &PendingWorkMap{m: xsync.NewMap[string, time.Time]()}
}

// ShouldEnqueueAfter returns true when no earlier-or-equal deadline is already
// pending for the given policy. When it returns true it also records the new
// deadline so subsequent calls with a later deadline are dropped.
func (p *PendingWorkMap) ShouldEnqueueAfter(policy string, deadline time.Time) bool {
	var accepted bool
	p.m.Compute(policy, func(existing time.Time, loaded bool) (time.Time, xsync.ComputeOp) {
		if loaded && !existing.After(deadline) {
			accepted = false
			return existing, xsync.CancelOp
		}
		accepted = true
		return deadline, xsync.UpdateOp
	})
	return accepted
}

// Clear removes the recorded deadline for a policy. Called by the worker
// goroutine after it has processed the key so the next stale-pending event
// can register a fresh deadline.
func (p *PendingWorkMap) Clear(policy string) {
	p.m.Delete(policy)
}

// Size returns the number of policies with pending deadlines.
func (p *PendingWorkMap) Size() int {
	return p.m.Size()
}
