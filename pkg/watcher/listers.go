package watcher

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/snapshot"
)

// Compile-time interface checks.
var (
	_ snapshot.PodGroupLister = (*podGroupListerAdapter)(nil)
	_ snapshot.NodeLister     = (*nodeListerAdapter)(nil)
	_ snapshot.PodLister      = (*podListerAdapter)(nil)
	_ snapshot.EventLister    = (*eventListerAdapter)(nil)
	_ snapshot.QueueLister    = (*queueListerAdapter)(nil)
)

// NewListersFromInformers constructs snapshot.Listers backed by the provided
// informer caches. Each adapter reads directly from the informer's thread-safe
// store, so no additional locking is needed.
func NewListersFromInformers(
	pgInformer cache.SharedIndexInformer,
	nodeInformer cache.SharedIndexInformer,
	podInformer cache.SharedIndexInformer,
	eventInformer cache.SharedIndexInformer,
	queueInformer cache.SharedIndexInformer,
) snapshot.Listers {
	return snapshot.Listers{
		PodGroups: &podGroupListerAdapter{store: pgInformer.GetStore()},
		Nodes:     &nodeListerAdapter{store: nodeInformer.GetStore()},
		Pods:      &podListerAdapter{store: podInformer.GetStore()},
		Events:    &eventListerAdapter{store: eventInformer.GetStore()},
		Queues:    &queueListerAdapter{store: queueInformer.GetStore()},
	}
}

// ---------- PodGroup ----------

type podGroupListerAdapter struct{ store cache.Store }

func (a *podGroupListerAdapter) List() ([]*schedulingv1beta1.PodGroup, error) {
	items := a.store.List()
	out := make([]*schedulingv1beta1.PodGroup, 0, len(items))
	for _, item := range items {
		pg, ok := item.(*schedulingv1beta1.PodGroup)
		if !ok {
			continue
		}
		out = append(out, pg)
	}
	return out, nil
}

// ---------- Node ----------

type nodeListerAdapter struct{ store cache.Store }

func (a *nodeListerAdapter) List() ([]*corev1.Node, error) {
	items := a.store.List()
	out := make([]*corev1.Node, 0, len(items))
	for _, item := range items {
		n, ok := item.(*corev1.Node)
		if !ok {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

// ---------- Pod ----------

type podListerAdapter struct{ store cache.Store }

func (a *podListerAdapter) List() ([]*corev1.Pod, error) {
	items := a.store.List()
	out := make([]*corev1.Pod, 0, len(items))
	for _, item := range items {
		p, ok := item.(*corev1.Pod)
		if !ok {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// ---------- Event ----------

type eventListerAdapter struct{ store cache.Store }

func (a *eventListerAdapter) List() ([]*corev1.Event, error) {
	items := a.store.List()
	out := make([]*corev1.Event, 0, len(items))
	for _, item := range items {
		e, ok := item.(*corev1.Event)
		if !ok {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// ---------- Queue ----------

type queueListerAdapter struct{ store cache.Store }

func (a *queueListerAdapter) Get(name string) (*schedulingv1beta1.Queue, error) {
	item, exists, err := a.store.GetByKey(name)
	if err != nil {
		wrapped := fmt.Errorf("queue lister: get %q: %w", name, err)
		logger.Error(wrapped, "queue lister GetByKey failed", "queue", name)
		return nil, wrapped
	}
	if !exists {
		return nil, nil
	}
	q, ok := item.(*schedulingv1beta1.Queue)
	if !ok {
		err := fmt.Errorf("queue lister: unexpected type %T for key %q", item, name)
		logger.Error(err, "queue lister type assertion failed", "queue", name)
		return nil, err
	}
	return q, nil
}
