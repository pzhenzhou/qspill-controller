package watcher

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	volclient "volcano.sh/apis/pkg/client/clientset/versioned"

	"github.com/pzhenzhou/qspill-controller/pkg/config"
)

const spillAnnotationPrefix = "spill.example.com/"

// QueueWatch observes Volcano Queue changes and enqueues the owning policy
// when controller-owned or controller-read fields drift. See DESIGN.md §6.4.
type QueueWatch struct {
	policies func() *config.PolicyRegistry
	enqueue  func(policyName string)
}

func newQueueWatch(
	volcanoClient volclient.Interface,
	registryStore *config.RegistryStore,
	resyncPeriod time.Duration,
	enqueue func(string),
) (*QueueWatch, cache.SharedIndexInformer, cache.ResourceEventHandlerRegistration, error) {
	w := &QueueWatch{
		policies: registryStore.Get,
		enqueue:  enqueue,
	}

	informer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
				return volcanoClient.SchedulingV1beta1().Queues().List(ctx, options)
			},
			WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
				return volcanoClient.SchedulingV1beta1().Queues().Watch(ctx, options)
			},
		},
		&schedulingv1beta1.Queue{},
		resyncPeriod,
		cache.Indexers{},
	)

	reg, err := informer.AddEventHandler(cache.ResourceEventHandlerDetailedFuncs{
		AddFunc:    w.onAdd,
		UpdateFunc: w.onUpdate,
		DeleteFunc: w.onDelete,
	})
	if err != nil {
		logger.Error(err, "queue watch: add event handler failed")
		return nil, nil, nil, err
	}

	return w, informer, reg, nil
}

func (w *QueueWatch) onAdd(obj interface{}, _ bool) {
	q, ok := obj.(*schedulingv1beta1.Queue)
	if !ok {
		logger.Error(fmt.Errorf("unexpected type %T", obj),
			"queue add: type assertion failed")
		return
	}
	if p, ok := w.policies().PolicyByQueue(q.Name); ok {
		logger.Info("enqueue from queue add",
			"policy", p.Name,
			"queue", q.Name,
		)
		w.enqueue(p.Name)
	}
}

func (w *QueueWatch) onUpdate(oldObj, newObj interface{}) {
	oldQ, ok := oldObj.(*schedulingv1beta1.Queue)
	if !ok {
		logger.Error(fmt.Errorf("unexpected old type %T", oldObj),
			"queue update: old type assertion failed")
		return
	}
	newQ, ok := newObj.(*schedulingv1beta1.Queue)
	if !ok {
		logger.Error(fmt.Errorf("unexpected new type %T", newObj),
			"queue update: new type assertion failed")
		return
	}

	if !queueChanged(oldQ, newQ) {
		return
	}

	if p, ok := w.policies().PolicyByQueue(newQ.Name); ok {
		logger.Info("enqueue from queue drift",
			"policy", p.Name,
			"queue", newQ.Name,
			"affinityChanged", !reflect.DeepEqual(oldQ.Spec.Affinity, newQ.Spec.Affinity),
			"capabilityChanged", !reflect.DeepEqual(oldQ.Spec.Capability, newQ.Spec.Capability),
			"spillAnnotationChanged", !spillAnnotationsEqual(oldQ.Annotations, newQ.Annotations),
		)
		w.enqueue(p.Name)
	}
}

func (w *QueueWatch) onDelete(obj interface{}) {
	rObj, ok := convertObj(obj)
	if !ok {
		logger.Error(fmt.Errorf("unexpected type %T", obj),
			"queue delete: convert object failed")
		return
	}
	q, ok := rObj.(*schedulingv1beta1.Queue)
	if !ok {
		logger.Error(fmt.Errorf("unexpected type %T", rObj),
			"queue delete: type assertion failed")
		return
	}
	if p, ok := w.policies().PolicyByQueue(q.Name); ok {
		logger.Info("enqueue from queue delete",
			"policy", p.Name,
			"queue", q.Name,
		)
		w.enqueue(p.Name)
	}
}

// queueChanged detects drift on the fields the controller owns or reads:
// spec.affinity, spec.capability, and spill.example.com/* annotations.
func queueChanged(old, new *schedulingv1beta1.Queue) bool {
	if !reflect.DeepEqual(old.Spec.Affinity, new.Spec.Affinity) {
		return true
	}
	if !reflect.DeepEqual(old.Spec.Capability, new.Spec.Capability) {
		return true
	}
	if !spillAnnotationsEqual(old.Annotations, new.Annotations) {
		return true
	}
	return false
}

func spillAnnotationsEqual(a, b map[string]string) bool {
	aSpill := filterSpillAnnotations(a)
	bSpill := filterSpillAnnotations(b)
	return reflect.DeepEqual(aSpill, bSpill)
}

func filterSpillAnnotations(m map[string]string) map[string]string {
	out := make(map[string]string)
	for k, v := range m {
		if strings.HasPrefix(k, spillAnnotationPrefix) {
			out[k] = v
		}
	}
	return out
}
