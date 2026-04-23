package snapshot

import (
	corev1 "k8s.io/api/core/v1"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/config"
)

// staticListers is the in-memory implementation of Listers used across the
// snapshot tests. Each field is wrapped in a lister so tests can plug a
// stable fixture in once and let the Builder filter as it would in
// production. Fields are exported only inside this test package.
type staticListers struct {
	pgs    []*schedulingv1beta1.PodGroup
	nodes  []*corev1.Node
	pods   []*corev1.Pod
	events []*corev1.Event
	queues map[string]*schedulingv1beta1.Queue

	// Inject errors per source so individual tests can assert the
	// snapshot's failure plumbing without rebuilding lister types.
	pgErr     error
	nodeErr   error
	podErr    error
	eventErr  error
	queueErr  error
	queueGets int
}

func (s *staticListers) toListers() Listers {
	return Listers{
		PodGroups: pgListerFunc{s},
		Nodes:     nodeListerFunc{s},
		Pods:      podListerFunc{s},
		Events:    eventListerFunc{s},
		Queues:    queueListerFunc{s},
	}
}

type pgListerFunc struct{ s *staticListers }

func (l pgListerFunc) List() ([]*schedulingv1beta1.PodGroup, error) {
	if l.s.pgErr != nil {
		return nil, l.s.pgErr
	}
	out := make([]*schedulingv1beta1.PodGroup, len(l.s.pgs))
	copy(out, l.s.pgs)
	return out, nil
}

type nodeListerFunc struct{ s *staticListers }

func (l nodeListerFunc) List() ([]*corev1.Node, error) {
	if l.s.nodeErr != nil {
		return nil, l.s.nodeErr
	}
	out := make([]*corev1.Node, len(l.s.nodes))
	copy(out, l.s.nodes)
	return out, nil
}

type podListerFunc struct{ s *staticListers }

func (l podListerFunc) List() ([]*corev1.Pod, error) {
	if l.s.podErr != nil {
		return nil, l.s.podErr
	}
	out := make([]*corev1.Pod, len(l.s.pods))
	copy(out, l.s.pods)
	return out, nil
}

type eventListerFunc struct{ s *staticListers }

func (l eventListerFunc) List() ([]*corev1.Event, error) {
	if l.s.eventErr != nil {
		return nil, l.s.eventErr
	}
	out := make([]*corev1.Event, len(l.s.events))
	copy(out, l.s.events)
	return out, nil
}

type queueListerFunc struct{ s *staticListers }

func (l queueListerFunc) Get(name string) (*schedulingv1beta1.Queue, error) {
	l.s.queueGets++
	if l.s.queueErr != nil {
		return nil, l.s.queueErr
	}
	q, ok := l.s.queues[name]
	if !ok {
		return nil, nil
	}
	return q, nil
}

// staticResolver implements PolicyResolver from a fixed map. The defaults
// block is stored in full so tests can flex the NodeGroupLabelKey.
type staticResolver struct {
	policies map[string]*api.SpillPolicy
	defaults config.Defaults
}

func (s *staticResolver) PolicyByName(name string) (*api.SpillPolicy, bool) {
	p, ok := s.policies[name]
	return p, ok
}

func (s *staticResolver) Defaults() config.Defaults {
	return s.defaults
}
