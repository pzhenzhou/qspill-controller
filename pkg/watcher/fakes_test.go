package watcher

import (
	"context"

	"k8s.io/client-go/discovery"
	volclient "volcano.sh/apis/pkg/client/clientset/versioned"
	batchv1alpha1 "volcano.sh/apis/pkg/client/clientset/versioned/typed/batch/v1alpha1"
	busv1alpha1 "volcano.sh/apis/pkg/client/clientset/versioned/typed/bus/v1alpha1"
	flowv1alpha1 "volcano.sh/apis/pkg/client/clientset/versioned/typed/flow/v1alpha1"
	nodeinfov1alpha1 "volcano.sh/apis/pkg/client/clientset/versioned/typed/nodeinfo/v1alpha1"
	schedulingv1beta1 "volcano.sh/apis/pkg/client/clientset/versioned/typed/scheduling/v1beta1"
	topologyv1alpha1 "volcano.sh/apis/pkg/client/clientset/versioned/typed/topology/v1alpha1"

	"github.com/pzhenzhou/qspill-controller/pkg/reconcile"
	"github.com/pzhenzhou/qspill-controller/pkg/snapshot"
)

// fakeReconciler implements reconcile.Reconciler for unit tests.
type fakeReconciler struct {
	err error
}

var _ reconcile.Reconciler = fakeReconciler{}

func (f fakeReconciler) ReconcilePolicy(_ context.Context, _ string) error {
	return f.err
}

// fakeReconcilerFactory returns a ReconcilerFactory that always produces a
// fakeReconciler with the given error.
func fakeReconcilerFactory(err error) ReconcilerFactory {
	return func(_ snapshot.Listers) reconcile.Reconciler {
		return fakeReconciler{err: err}
	}
}

// volcanoStub satisfies volclient.Interface for validation-only tests.
// Methods panic if called — they should never be invoked during validation.
type volcanoStub struct{}

var _ volclient.Interface = (*volcanoStub)(nil)

func (*volcanoStub) Discovery() discovery.DiscoveryInterface                         { panic("stub") }
func (*volcanoStub) BatchV1alpha1() batchv1alpha1.BatchV1alpha1Interface             { panic("stub") }
func (*volcanoStub) BusV1alpha1() busv1alpha1.BusV1alpha1Interface                   { panic("stub") }
func (*volcanoStub) FlowV1alpha1() flowv1alpha1.FlowV1alpha1Interface                { panic("stub") }
func (*volcanoStub) NodeinfoV1alpha1() nodeinfov1alpha1.NodeinfoV1alpha1Interface    { panic("stub") }
func (*volcanoStub) SchedulingV1beta1() schedulingv1beta1.SchedulingV1beta1Interface { panic("stub") }
func (*volcanoStub) TopologyV1alpha1() topologyv1alpha1.TopologyV1alpha1Interface    { panic("stub") }
