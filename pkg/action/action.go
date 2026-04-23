// Package action holds the side-effecting half of the controller. The
// Action interface is the seam between the pure decision pipeline (snapshot
// -> evaluator -> Decision) and the cluster: every concrete implementation
// translates a Decision into an externally-visible side effect (write to
// stdout, server-side apply against the Volcano Queue, future webhook
// emission, ...). Keeping the interface tiny — one method, no callbacks —
// is what lets the reconciler hash-gate Apply uniformly across modes.
package action

import (
	"context"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
)

// Action realises a Decision. Every implementation must:
//
//   - be safe for concurrent use; the reconciler may share one Action
//     across worker goroutines;
//   - be idempotent: the reconciler hash-gates Apply, but a missed gate
//     (e.g. annotation drift, leader handoff) must not corrupt state;
//   - return an error only when the side effect failed; a no-op outcome
//     (decision matches existing state) is the reconciler's responsibility
//     to detect upstream, not the Action's.
type Action interface {
	// Name returns the implementation's stable identifier. Used in
	// metrics labels (queue_spill_action_apply_seconds{action}) and event
	// emission so operators can disambiguate dry-run vs production.
	Name() string

	// Apply executes the side effect described by d. The returned error
	// is propagated to the workqueue's rate limiter; success returns nil.
	Apply(ctx context.Context, d api.Decision) error
}
