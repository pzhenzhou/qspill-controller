package snapshot

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/common"
	"github.com/pzhenzhou/qspill-controller/pkg/config"
)

// logger is the per-module logr.Logger for the snapshot builder. Module
// scope means every Build, lister fan-out, and demand/supply tally is
// taggable as `module=snapshot` for operators correlating decision drift
// with cache state.
var logger = common.NewLogger("snapshot")

// ErrUnknownPolicy is returned by Build when policyName does not resolve in
// the active registry. The reconciler treats this as a programming error
// (the workqueue should never carry an unknown name) and surfaces it loudly
// so misconfiguration is caught at first reconcile rather than silently
// accepted.
var ErrUnknownPolicy = errors.New("snapshot: unknown policy name")

// PolicyResolver returns the live SpillPolicy for a name. The Builder
// depends only on this narrow contract so the registry's atomic-pointer
// swap mechanics stay encapsulated in pkg/config.
type PolicyResolver interface {
	PolicyByName(name string) (*api.SpillPolicy, bool)
	Defaults() config.Defaults
}

// NewBuilder wires up the production builder. The resolver carries the live
// policy registry (typically *config.PolicyRegistry behind a closure), and
// listers expose the informer caches the snapshot reads from.
//
// The returned Builder is goroutine-safe: every call to Build reads through
// the listers and constructs a fresh Snapshot; no Builder-local state is
// mutated.
func NewBuilder(resolver PolicyResolver, listers Listers) Builder {
	return &defaultBuilder{
		resolver: resolver,
		listers:  listers,
	}
}

type defaultBuilder struct {
	resolver PolicyResolver
	listers  Listers
}

// Build fully assembles the per-policy Snapshot from the listers. Each
// section (PodGroups, Pods, Nodes, Queue, Events) is read once; any later
// derived value (demand, supply, stale-pending, autoscaler-exhausted) is
// computed from the per-policy filtered slice so the work scales with the
// policy's footprint, not the cluster's total resource count.
func (b *defaultBuilder) Build(ctx context.Context, policyName string, now time.Time) (*Snapshot, error) {
	logger.Info("building snapshot",
		"policy", policyName,
		"observedAt", now.UTC().Format(time.RFC3339),
	)
	policy, ok := b.resolver.PolicyByName(policyName)
	if !ok || policy == nil {
		err := fmt.Errorf("%w: %q", ErrUnknownPolicy, policyName)
		logger.Error(err, "resolve policy failed", "policy", policyName)
		return nil, err
	}

	pgs, err := b.policyPodGroups(policy)
	if err != nil {
		logger.Error(err, "list policy podgroups failed",
			"policy", policy.Name, "queue", policy.QueueName)
		return nil, err
	}

	pods, err := b.policyPods(pgs)
	if err != nil {
		logger.Error(err, "list policy pods failed",
			"policy", policy.Name, "podGroups", len(pgs))
		return nil, err
	}

	dedicatedNodes, overflowNodes, err := b.policyNodes(policy)
	if err != nil {
		logger.Error(err, "list policy nodes failed",
			"policy", policy.Name,
			"dedicatedGroup", policy.DedicatedNodeGroup,
			"overflowGroup", policy.OverflowNodeGroup)
		return nil, err
	}

	queue, currentState, conditionSince, decisionHash, err := b.queueState(policy)
	if err != nil {
		logger.Error(err, "fetch queue state failed",
			"policy", policy.Name, "queue", policy.QueueName)
		return nil, err
	}

	events, err := b.listers.Events.List()
	if err != nil {
		wrapped := fmt.Errorf("snapshot: list events: %w", err)
		logger.Error(wrapped, "list events failed", "policy", policy.Name)
		return nil, wrapped
	}

	demandResources, demandEstimated := computeDemand(pgs, pods)
	dedicatedNames := nodeNameSet(dedicatedNodes)
	overflowNames := nodeNameSet(overflowNodes)
	dedicatedPods := countPodsOnNodes(pods, dedicatedNames)
	overflowPods := countPodsOnNodes(pods, overflowNames)
	stalest, breaching := scanStalePending(pods, now, policy.Thresholds.TimePendingMax)
	autoExhausted := autoscalerExhausted(events, podNameSet(pods))

	logger.Info("snapshot built",
		"policy", policy.Name,
		"queue", policy.QueueName,
		"podGroups", len(pgs),
		"pods", len(pods),
		"dedicatedNodes", len(dedicatedNodes),
		"overflowNodes", len(overflowNodes),
		"dedicatedPodsOfPolicy", dedicatedPods,
		"overflowPodsOfPolicy", overflowPods,
		"stalePendingPods", breaching,
		"stalestPendingFor", stalest.String(),
		"autoscalerExhausted", autoExhausted,
		"currentState", string(currentState),
		"queueObserved", queue != nil,
	)

	return &Snapshot{
		Policy:                policy,
		ObservedAt:            now,
		Queue:                 queue,
		CurrentState:          currentState,
		ConditionSince:        conditionSince,
		DecisionHash:          decisionHash,
		PodGroups:             pgs,
		DemandResources:       demandResources,
		DemandEstimatedPGs:    demandEstimated,
		DedicatedNodes:        dedicatedNodes,
		OverflowNodes:         overflowNodes,
		DedicatedPodsOfPolicy: dedicatedPods,
		OverflowPodsOfPolicy:  overflowPods,
		MaxDedicatedCapacity:  sumAllocatable(dedicatedNodes),
		StalestPendingFor:     stalest,
		StalePendingPods:      breaching,
		AutoscalerExhausted:   autoExhausted,
	}, nil
}

// policyPodGroups returns every non-terminal PodGroup whose Spec.Queue
// equals policy.QueueName. The terminal exclusion is the design's "policy
// matching, non-terminal" filter from §4.3 — Completed PodGroups carry no
// outstanding demand and would only confuse the demand fallback.
func (b *defaultBuilder) policyPodGroups(policy *api.SpillPolicy) ([]*schedulingv1beta1.PodGroup, error) {
	all, err := b.listers.PodGroups.List()
	if err != nil {
		wrapped := fmt.Errorf("snapshot: list podgroups: %w", err)
		logger.Error(wrapped, "list podgroups failed",
			"policy", policy.Name, "queue", policy.QueueName)
		return nil, wrapped
	}
	out := make([]*schedulingv1beta1.PodGroup, 0, len(all))
	for _, pg := range all {
		if pg == nil {
			continue
		}
		if pg.Spec.Queue != policy.QueueName {
			continue
		}
		if pg.Status.Phase == schedulingv1beta1.PodGroupCompleted {
			continue
		}
		out = append(out, pg)
	}
	return out, nil
}

// policyPods returns the Pods whose PodGroup-name annotation matches a
// PodGroup in the supplied slice. Pods without the annotation, or with an
// annotation pointing outside the policy's PodGroup set, are skipped — that
// is exactly the population we want to drive demand, stale-pending, and
// supply counts.
func (b *defaultBuilder) policyPods(pgs []*schedulingv1beta1.PodGroup) ([]*corev1.Pod, error) {
	all, err := b.listers.Pods.List()
	if err != nil {
		wrapped := fmt.Errorf("snapshot: list pods: %w", err)
		logger.Error(wrapped, "list pods failed", "podGroups", len(pgs))
		return nil, wrapped
	}
	if len(pgs) == 0 {
		return nil, nil
	}
	groups := make(map[string]struct{}, len(pgs))
	for _, pg := range pgs {
		groups[pg.Name] = struct{}{}
	}
	out := make([]*corev1.Pod, 0, len(all))
	for _, p := range all {
		if p == nil {
			continue
		}
		name := podGroupNameOf(p)
		if name == "" {
			continue
		}
		if _, ok := groups[name]; !ok {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// policyNodes returns the dedicated and overflow node slices using the
// configured nodeGroupLabelKey from defaults. Listing once and partitioning
// in-process keeps the supply scan O(N) per reconcile.
func (b *defaultBuilder) policyNodes(policy *api.SpillPolicy) (dedicated, overflow []*corev1.Node, err error) {
	all, err := b.listers.Nodes.List()
	if err != nil {
		wrapped := fmt.Errorf("snapshot: list nodes: %w", err)
		logger.Error(wrapped, "list nodes failed",
			"policy", policy.Name,
			"dedicatedGroup", policy.DedicatedNodeGroup,
			"overflowGroup", policy.OverflowNodeGroup)
		return nil, nil, wrapped
	}
	defaults := b.resolver.Defaults()
	dedicated, overflow = splitNodesByGroup(all, defaults.NodeGroupLabelKey, policy)
	return dedicated, overflow, nil
}

// queueState fetches the live Queue and decodes the controller-owned
// annotations. A missing Queue is not an error: it represents a freshly
// declared policy that has never been reconciled, which the evaluator
// handles as "default Steady, no condition timer". Malformed annotations
// fall back to the same defaults so a hand-edited Queue cannot deadlock the
// state machine.
func (b *defaultBuilder) queueState(policy *api.SpillPolicy) (*schedulingv1beta1.Queue, api.State, time.Time, string, error) {
	q, err := b.listers.Queues.Get(policy.QueueName)
	if err != nil {
		wrapped := fmt.Errorf("snapshot: get queue %q: %w", policy.QueueName, err)
		logger.Error(wrapped, "get queue failed",
			"policy", policy.Name, "queue", policy.QueueName)
		return nil, api.StateSteady, time.Time{}, "", wrapped
	}
	if q == nil {
		return nil, api.StateSteady, time.Time{}, "", nil
	}
	state := decodeState(q.Annotations[api.AnnotationState])
	since := decodeTime(q.Annotations[api.AnnotationConditionSince])
	hash := q.Annotations[api.AnnotationDecisionHash]
	return q, state, since, hash, nil
}

// decodeState maps the wire value of AnnotationState back to State, falling
// back to StateSteady on anything unrecognised. Robust decoding here means
// the evaluator never has to defend against typos in operator-edited
// annotations.
func decodeState(v string) api.State {
	switch api.State(v) {
	case api.StateSpill:
		return api.StateSpill
	case api.StateSteady:
		return api.StateSteady
	default:
		return api.StateSteady
	}
}

// decodeTime parses an RFC3339 timestamp, returning the zero time on any
// failure. Combined with decodeState's defaulting, this means the snapshot
// degrades cleanly when the Queue annotation set is partially missing or
// corrupted.
func decodeTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t
}
