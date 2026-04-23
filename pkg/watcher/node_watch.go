package watcher

import (
	"context"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/pzhenzhou/qspill-controller/pkg/config"
)

// NodeWatch observes label-filtered Node events and enqueues every policy
// that references the affected nodegroup(s). See DESIGN.md §6.2.
type NodeWatch struct {
	policies     func() *config.PolicyRegistry
	enqueue      func(policyName string)
	nodeGroupKey string
}

func newNodeWatch(
	kubeClient kubernetes.Interface,
	registryStore *config.RegistryStore,
	resyncPeriod time.Duration,
	nodeGroupKey string,
	enqueue func(string),
) (*NodeWatch, cache.SharedIndexInformer, cache.ResourceEventHandlerRegistration, error) {
	w := &NodeWatch{
		policies:     registryStore.Get,
		enqueue:      enqueue,
		nodeGroupKey: nodeGroupKey,
	}

	req, err := labels.NewRequirement(nodeGroupKey, selection.Exists, nil)
	if err != nil {
		logger.Error(err, "node watch: build label requirement failed",
			"nodeGroupKey", nodeGroupKey)
		return nil, nil, nil, err
	}
	selector := labels.NewSelector().Add(*req)
	selectorStr := selector.String()

	informer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
				options.LabelSelector = selectorStr
				return kubeClient.CoreV1().Nodes().List(ctx, options)
			},
			WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
				options.LabelSelector = selectorStr
				return kubeClient.CoreV1().Nodes().Watch(ctx, options)
			},
		},
		&corev1.Node{},
		resyncPeriod,
		cache.Indexers{},
	)

	reg, err := informer.AddEventHandler(cache.ResourceEventHandlerDetailedFuncs{
		AddFunc:    w.onAdd,
		UpdateFunc: w.onUpdate,
		DeleteFunc: w.onDelete,
	})
	if err != nil {
		logger.Error(err, "node watch: add event handler failed")
		return nil, nil, nil, err
	}

	return w, informer, reg, nil
}

func (w *NodeWatch) onAdd(obj interface{}, _ bool) {
	node, ok := obj.(*corev1.Node)
	if !ok {
		logger.Error(fmt.Errorf("unexpected type %T", obj),
			"node add: type assertion failed")
		return
	}
	w.enqueueForValue(node.Labels[w.nodeGroupKey], "node-add", node.Name)
}

func (w *NodeWatch) onUpdate(oldObj, newObj interface{}) {
	oldNode, ok := oldObj.(*corev1.Node)
	if !ok {
		logger.Error(fmt.Errorf("unexpected old type %T", oldObj),
			"node update: old type assertion failed")
		return
	}
	newNode, ok := newObj.(*corev1.Node)
	if !ok {
		logger.Error(fmt.Errorf("unexpected new type %T", newObj),
			"node update: new type assertion failed")
		return
	}

	oldVal := oldNode.Labels[w.nodeGroupKey]
	newVal := newNode.Labels[w.nodeGroupKey]

	if oldVal != newVal {
		logger.Info("node nodegroup label changed",
			"node", newNode.Name,
			"oldGroup", oldVal,
			"newGroup", newVal,
		)
		w.enqueueForValue(oldVal, "node-relabel-old", newNode.Name)
		w.enqueueForValue(newVal, "node-relabel-new", newNode.Name)
		return
	}

	if nodeSupplyChanged(oldNode, newNode) {
		w.enqueueForValue(newVal, "node-supply-changed", newNode.Name)
	}
}

func (w *NodeWatch) onDelete(obj interface{}) {
	rObj, ok := convertObj(obj)
	if !ok {
		logger.Error(fmt.Errorf("unexpected type %T", obj),
			"node delete: convert object failed")
		return
	}
	node, ok := rObj.(*corev1.Node)
	if !ok {
		logger.Error(fmt.Errorf("unexpected type %T", rObj),
			"node delete: type assertion failed")
		return
	}
	w.enqueueForValue(node.Labels[w.nodeGroupKey], "node-delete", node.Name)
}

// enqueueForValue looks up all policies that reference the given nodegroup
// value and enqueues each. reason and node identify the trigger for log
// correlation.
func (w *NodeWatch) enqueueForValue(value, reason, nodeName string) {
	if value == "" {
		return
	}
	policies := w.policies().PoliciesForNodeGroup(value)
	if len(policies) == 0 {
		return
	}
	for _, name := range policies {
		logger.Info("enqueue from node event",
			"policy", name,
			"reason", reason,
			"node", nodeName,
			"nodeGroup", value,
		)
		w.enqueue(name)
	}
}

// nodeSupplyChanged returns true when fields that affect supply have changed:
// Unschedulable, Ready condition, Allocatable resources, or taints.
func nodeSupplyChanged(old, new *corev1.Node) bool {
	if old.Spec.Unschedulable != new.Spec.Unschedulable {
		return true
	}
	if !reflect.DeepEqual(old.Status.Allocatable, new.Status.Allocatable) {
		return true
	}
	if readyConditionStatus(old) != readyConditionStatus(new) {
		return true
	}
	if !reflect.DeepEqual(old.Spec.Taints, new.Spec.Taints) {
		return true
	}
	return false
}

func readyConditionStatus(node *corev1.Node) corev1.ConditionStatus {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status
		}
	}
	return corev1.ConditionUnknown
}
