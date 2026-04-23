// Package reconcile wires snapshot → evaluator → action with hash-gated apply.
// Metrics and Kubernetes Events live in Appendix A per IMPLEMENTATION.md.
package reconcile

import (
	"context"
	"time"

	"github.com/pzhenzhou/qspill-controller/pkg/action"
	"github.com/pzhenzhou/qspill-controller/pkg/common"
	"github.com/pzhenzhou/qspill-controller/pkg/evaluator"
	"github.com/pzhenzhou/qspill-controller/pkg/snapshot"
)

// logger is the per-module logr.Logger for the reconcile loop. Lines tag
// `module=reconcile` so operators can isolate the snapshot → evaluate →
// apply pipeline from the watcher's queue plumbing or the action layer's
// API traffic.
var logger = common.NewLogger("reconcile")

// Reconciler reconciles one policy name per invocation (DESIGN.md §4.8).
type Reconciler interface {
	ReconcilePolicy(ctx context.Context, policyName string) error
}

type reconciler struct {
	snapshot.Builder
	evaluator.Evaluator
	action.Action
	Now func() time.Time
}

// New constructs a Reconciler. Now defaults to time.Now when nil.
func New(b snapshot.Builder, e evaluator.Evaluator, a action.Action, now func() time.Time) Reconciler {
	if now == nil {
		now = time.Now
	}
	return &reconciler{
		Builder:   b,
		Evaluator: e,
		Action:    a,
		Now:       now,
	}
}

func (r *reconciler) ReconcilePolicy(ctx context.Context, policyName string) error {
	now := r.Now()
	logger.Info("reconciling policy",
		"policy", policyName,
		"action", r.Action.Name(),
		"observedAt", now.UTC().Format(time.RFC3339),
	)

	snap, err := r.Build(ctx, policyName, now)
	if err != nil {
		logger.Error(err, "snapshot build failed", "policy", policyName)
		return err
	}

	d := r.Evaluate(snap)
	if d.Hash == snap.DecisionHash {
		logger.Info("reconcile no-op",
			"policy", policyName,
			"reason", "decision hash matches observed annotation",
			"hash", d.Hash,
			"state", string(d.To),
		)
		return nil
	}

	logger.Info("applying decision",
		"policy", policyName,
		"action", r.Action.Name(),
		"from", string(d.From),
		"to", string(d.To),
		"trigger", string(d.Trigger),
		"hash", d.Hash,
		"observedHash", snap.DecisionHash,
	)
	if err := r.Apply(ctx, d); err != nil {
		logger.Error(err, "action apply failed",
			"policy", policyName,
			"action", r.Action.Name(),
			"from", string(d.From),
			"to", string(d.To),
		)
		return err
	}
	logger.Info("reconcile succeeded",
		"policy", policyName,
		"action", r.Action.Name(),
		"to", string(d.To),
		"hash", d.Hash,
	)
	return nil
}
