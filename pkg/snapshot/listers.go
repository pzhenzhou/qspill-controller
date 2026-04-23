package snapshot

import (
	corev1 "k8s.io/api/core/v1"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

// Listers is the read-side dependency surface of Builder. Each lister covers
// exactly the operations the snapshot needs and nothing more, so test fakes
// stay small and the Builder cannot accidentally reach beyond the snapshot
// contract. Production wiring instantiates these from informer-backed
// implementations (see pkg/watcher in P5); tests drop in in-memory maps.
type Listers struct {
	PodGroups PodGroupLister
	Nodes     NodeLister
	Pods      PodLister
	Events    EventLister
	Queues    QueueLister
}

// PodGroupLister enumerates non-terminal PodGroups across the cluster. The
// Builder filters by Spec.Queue itself, so the lister contract is the
// minimal "give me everything observable" surface; queue-aware indexes are
// an implementation detail of the informer wiring.
type PodGroupLister interface {
	// List returns every PodGroup currently observed by the informer cache.
	// Implementations must return a fresh slice header so callers may filter
	// without disturbing the underlying cache.
	List() ([]*schedulingv1beta1.PodGroup, error)
}

// NodeLister enumerates Nodes for the snapshot's nodegroup membership scan.
// The Builder filters by the configured nodeGroupLabelKey; the lister
// surface stays unfiltered to match informer cache semantics.
type NodeLister interface {
	List() ([]*corev1.Node, error)
}

// PodLister enumerates Pods so the snapshot can attribute them to a policy
// via the volcano group-name annotation.
type PodLister interface {
	List() ([]*corev1.Pod, error)
}

// EventLister enumerates the autoscaler-emitted Events the snapshot scans
// to populate AutoscalerExhausted. Implementations are expected to filter
// the underlying informer to relevant reasons (NotTriggerScaleUp,
// FailedScaling) — the snapshot still re-checks reason strings so the
// contract degrades gracefully when no filtering is in place.
type EventLister interface {
	List() ([]*corev1.Event, error)
}

// QueueLister fetches a single Volcano Queue by name. Returning (nil, nil)
// for a missing Queue is the documented contract: a missing Queue is a
// recoverable state (fresh policy, never reconciled), not an error.
type QueueLister interface {
	Get(name string) (*schedulingv1beta1.Queue, error)
}
