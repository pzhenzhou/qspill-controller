package action

import (
	"context"
	"sync"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
)

// RecordingAction captures every successful Apply invocation for assertions in
// reconciler tests. Safe for concurrent use across parallel reconciles.
type RecordingAction struct {
	mu sync.Mutex

	// Err, when non-nil, causes Apply to return it without recording.
	Err error

	Applies []api.Decision
}

func (r *RecordingAction) Name() string { return "Recording" }

func (r *RecordingAction) Apply(ctx context.Context, d api.Decision) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	r.Applies = append(r.Applies, d)
	return nil
}
