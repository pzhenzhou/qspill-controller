package config

import (
	"sync/atomic"
	"time"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
)

// Defaults captures the controller-wide configuration that is not specific to
// any one policy. It is loaded from the defaults block of the ConfigMap and is
// read by the watchers and the reconciler to drive node-label selection,
// action dispatch, and the forced resync cadence.
//
// PodGroup → policy binding is intentionally absent here: PodGroups are
// matched by spec.queue against SpillPolicy.QueueName via PolicyByQueue, so
// no label key needs to be configured.
type Defaults struct {
	// NodeGroupLabelKey is the label key on Nodes whose value identifies the
	// nodegroup the Node belongs to (e.g. volcano.sh/nodegroup-name). The
	// node watcher uses it to map Nodes to nodegroups; the reverse index in
	// PolicyRegistry uses the same key.
	NodeGroupLabelKey string

	// Action selects how the reconciler realises a Decision.
	Action api.ActionMode

	// ReconcileResyncPeriod is the forced full-reconcile cadence; the
	// reconciler re-evaluates every policy this often even in the absence
	// of watch events, providing a self-healing floor for missed signals.
	ReconcileResyncPeriod time.Duration

	// Thresholds is the controller-wide threshold baseline. Each policy's
	// effective thresholds are computed by overlaying its own per-policy
	// overrides on top of these values.
	Thresholds api.Thresholds
}

// PolicyRegistry is the immutable result of a successful Load. It owns the
// canonical ordered list of policies plus the lookup indexes the reconciler
// and watchers rely on. A registry never mutates after construction; new
// configuration produces a new registry that callers swap in atomically via
// RegistryStore.
type PolicyRegistry struct {
	defaults    Defaults
	policies    []*api.SpillPolicy
	byName      map[string]*api.SpillPolicy
	byQueue     map[string]*api.SpillPolicy
	byNodeGroup map[string][]string
}

// newPolicyRegistry assembles the lookup indexes from a flat policy slice.
// Duplicate names or queues silently overwrite here; the caller (Load) runs
// validation immediately afterwards to surface the duplicates as errors.
func newPolicyRegistry(defaults Defaults, policies []*api.SpillPolicy) *PolicyRegistry {
	r := &PolicyRegistry{
		defaults:    defaults,
		policies:    policies,
		byName:      make(map[string]*api.SpillPolicy, len(policies)),
		byQueue:     make(map[string]*api.SpillPolicy, len(policies)),
		byNodeGroup: make(map[string][]string),
	}
	for _, p := range policies {
		if p.Name != "" {
			r.byName[p.Name] = p
		}
		if p.QueueName != "" {
			r.byQueue[p.QueueName] = p
		}
		if p.DedicatedNodeGroup != "" {
			r.byNodeGroup[p.DedicatedNodeGroup] = append(r.byNodeGroup[p.DedicatedNodeGroup], p.Name)
		}
		// Skip indexing the overflow entry when it equals the dedicated
		// entry; we still want the validator to flag the duplicate but
		// the watcher should not enqueue the same policy twice for one
		// node label.
		if p.OverflowNodeGroup != "" && p.OverflowNodeGroup != p.DedicatedNodeGroup {
			r.byNodeGroup[p.OverflowNodeGroup] = append(r.byNodeGroup[p.OverflowNodeGroup], p.Name)
		}
	}
	return r
}

// Defaults returns the controller-wide defaults block. Treat as read-only.
func (r *PolicyRegistry) Defaults() Defaults {
	return r.defaults
}

// Policies returns a defensive copy of the registry's policies in YAML order.
// The slice header is fresh on every call; the *api.SpillPolicy pointers are
// shared and must not be mutated.
func (r *PolicyRegistry) Policies() []*api.SpillPolicy {
	out := make([]*api.SpillPolicy, len(r.policies))
	copy(out, r.policies)
	return out
}

// PolicyByName returns the policy with the given name, if any. The boolean
// distinguishes "not present" from a zero-value match.
func (r *PolicyRegistry) PolicyByName(name string) (*api.SpillPolicy, bool) {
	p, ok := r.byName[name]
	return p, ok
}

// PolicyByQueue returns the policy that owns the given Queue name, if any.
// Queue names are unique across policies (enforced by validation), so this
// is a single-value lookup.
func (r *PolicyRegistry) PolicyByQueue(queue string) (*api.SpillPolicy, bool) {
	p, ok := r.byQueue[queue]
	return p, ok
}

// PoliciesForNodeGroup returns the names of every policy that references the
// given nodegroup label value as either its dedicated or its overflow group.
// The node watcher calls this to determine which policies to enqueue when a
// Node's nodegroup membership changes. Returns nil for unknown values; the
// returned slice is a defensive copy.
func (r *PolicyRegistry) PoliciesForNodeGroup(value string) []string {
	src := r.byNodeGroup[value]
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// RegistryStore is the lock-free pointer-swap holder for the active
// PolicyRegistry. It is constructed empty (so Get never returns nil) and is
// safe for concurrent use by any number of readers and writers.
type RegistryStore struct {
	p atomic.Pointer[PolicyRegistry]
}

// NewRegistryStore returns a store seeded with an empty registry. Readers
// can call Get immediately; PoliciesForNodeGroup and similar lookups will
// return nil until the first successful Load is installed via Set.
func NewRegistryStore() *RegistryStore {
	s := &RegistryStore{}
	s.p.Store(newPolicyRegistry(Defaults{}, nil))
	return s
}

// Get returns the active registry. Never nil.
func (s *RegistryStore) Get() *PolicyRegistry {
	return s.p.Load()
}

// Set installs r as the active registry. Calls with a nil r are silently
// ignored so that a fail-closed reload path can call Set unconditionally
// after a failed Load without first checking the error.
func (s *RegistryStore) Set(r *PolicyRegistry) {
	if r == nil {
		logger.Info("registry swap skipped",
			"reason", "nil registry; keeping prior",
		)
		return
	}
	prior := s.p.Load()
	s.p.Store(r)
	priorPolicies := 0
	if prior != nil {
		priorPolicies = len(prior.policies)
	}
	logger.Info("registry installed",
		"policies", len(r.policies),
		"priorPolicies", priorPolicies,
		"action", string(r.defaults.Action),
	)
}
