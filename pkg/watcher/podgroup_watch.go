package watcher

import (
	"context"
	"fmt"
	"reflect"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	volclient "volcano.sh/apis/pkg/client/clientset/versioned"

	"github.com/pzhenzhou/qspill-controller/pkg/config"
)

// PodGroupWatch observes PodGroup add/update/delete events and enqueues the
// owning policy (resolved via PolicyByQueue) for reconciliation. See
// DESIGN.md §6.1 for the full specification.
type PodGroupWatch struct {
	policies func() *config.PolicyRegistry
	enqueue  func(policyName string)
}

func newPodGroupWatch(
	volcanoClient volclient.Interface,
	registryStore *config.RegistryStore,
	resyncPeriod time.Duration,
	enqueue func(string),
) (*PodGroupWatch, cache.SharedIndexInformer, cache.ResourceEventHandlerRegistration, error) {
	w := &PodGroupWatch{
		policies: registryStore.Get,
		enqueue:  enqueue,
	}

	informer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
				return volcanoClient.SchedulingV1beta1().PodGroups("").List(ctx, options)
			},
			WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
				return volcanoClient.SchedulingV1beta1().PodGroups("").Watch(ctx, options)
			},
		},
		&schedulingv1beta1.PodGroup{},
		resyncPeriod,
		cache.Indexers{},
	)

	reg, err := informer.AddEventHandler(cache.ResourceEventHandlerDetailedFuncs{
		AddFunc:    w.onAdd,
		UpdateFunc: w.onUpdate,
		DeleteFunc: w.onDelete,
	})
	if err != nil {
		logger.Error(err, "podgroup watch: add event handler failed")
		return nil, nil, nil, err
	}

	return w, informer, reg, nil
}

func (w *PodGroupWatch) onAdd(obj interface{}, _ bool) {
	pg, ok := obj.(*schedulingv1beta1.PodGroup)
	if !ok {
		logger.Error(fmt.Errorf("unexpected type %T", obj),
			"podgroup add: type assertion failed")
		return
	}
	if p, ok := w.policies().PolicyByQueue(pg.Spec.Queue); ok {
		logger.Info("enqueue from podgroup add",
			"policy", p.Name,
			"queue", pg.Spec.Queue,
			"podGroup", pg.Namespace+"/"+pg.Name,
			"phase", string(pg.Status.Phase),
		)
		w.enqueue(p.Name)
	}
}

func (w *PodGroupWatch) onUpdate(oldObj, newObj interface{}) {
	oldPG, ok := oldObj.(*schedulingv1beta1.PodGroup)
	if !ok {
		logger.Error(fmt.Errorf("unexpected old type %T", oldObj),
			"podgroup update: old type assertion failed")
		return
	}
	newPG, ok := newObj.(*schedulingv1beta1.PodGroup)
	if !ok {
		logger.Error(fmt.Errorf("unexpected new type %T", newObj),
			"podgroup update: new type assertion failed")
		return
	}

	if !podGroupChanged(oldPG, newPG) {
		return
	}

	if oldPG.Spec.Queue != newPG.Spec.Queue {
		if p, ok := w.policies().PolicyByQueue(oldPG.Spec.Queue); ok {
			logger.Info("enqueue from podgroup queue rebinding (old policy)",
				"policy", p.Name,
				"oldQueue", oldPG.Spec.Queue,
				"newQueue", newPG.Spec.Queue,
				"podGroup", newPG.Namespace+"/"+newPG.Name,
			)
			w.enqueue(p.Name)
		}
	}
	if p, ok := w.policies().PolicyByQueue(newPG.Spec.Queue); ok {
		logger.Info("enqueue from podgroup update",
			"policy", p.Name,
			"queue", newPG.Spec.Queue,
			"podGroup", newPG.Namespace+"/"+newPG.Name,
			"oldPhase", string(oldPG.Status.Phase),
			"newPhase", string(newPG.Status.Phase),
		)
		w.enqueue(p.Name)
	}
}

func (w *PodGroupWatch) onDelete(obj interface{}) {
	rObj, ok := convertObj(obj)
	if !ok {
		logger.Error(fmt.Errorf("unexpected type %T", obj),
			"podgroup delete: convert object failed")
		return
	}
	pg, ok := rObj.(*schedulingv1beta1.PodGroup)
	if !ok {
		logger.Error(fmt.Errorf("unexpected type %T", rObj),
			"podgroup delete: type assertion failed")
		return
	}
	if p, ok := w.policies().PolicyByQueue(pg.Spec.Queue); ok {
		logger.Info("enqueue from podgroup delete",
			"policy", p.Name,
			"queue", pg.Spec.Queue,
			"podGroup", pg.Namespace+"/"+pg.Name,
		)
		w.enqueue(p.Name)
	}
}

func podGroupChanged(old, new *schedulingv1beta1.PodGroup) bool {
	if old.Spec.Queue != new.Spec.Queue {
		return true
	}
	if old.Status.Phase != new.Status.Phase {
		return true
	}
	if old.Spec.MinMember != new.Spec.MinMember {
		return true
	}
	if !reflect.DeepEqual(old.Spec.MinResources, new.Spec.MinResources) {
		return true
	}
	return false
}
