package watcher

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/config"
)

// PodEventWatch carries two triggers (autoscaler-exhaustion and
// stale-pending) using a shared Pod informer and a filtered Event informer.
// See DESIGN.md §6.3.
type PodEventWatch struct {
	policies     func() *config.PolicyRegistry
	enqueue      func(policyName string)
	enqueueAfter func(policyName string, d time.Duration)

	pgInformer  cache.SharedIndexInformer
	podInformer cache.SharedIndexInformer
}

func newPodEventWatch(
	kubeClient kubernetes.Interface,
	registryStore *config.RegistryStore,
	pgInformer cache.SharedIndexInformer,
	resyncPeriod time.Duration,
	enqueue func(string),
	enqueueAfter func(string, time.Duration),
) (*PodEventWatch, cache.SharedIndexInformer, cache.SharedIndexInformer, cache.ResourceEventHandlerRegistration, cache.ResourceEventHandlerRegistration, error) {
	if pgInformer == nil {
		err := fmt.Errorf("pod/event watch: podgroup informer is required")
		logger.Error(err, "pod/event watch: validate podgroup informer failed")
		return nil, nil, nil, nil, nil, err
	}

	// Pod informer: cluster-scoped, unfiltered.
	podInformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
				return kubeClient.CoreV1().Pods("").List(ctx, options)
			},
			WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
				return kubeClient.CoreV1().Pods("").Watch(ctx, options)
			},
		},
		&corev1.Pod{},
		resyncPeriod,
		cache.Indexers{},
	)

	w := &PodEventWatch{
		policies:     registryStore.Get,
		enqueue:      enqueue,
		enqueueAfter: enqueueAfter,
		pgInformer:   pgInformer,
		podInformer:  podInformer,
	}

	podReg, err := podInformer.AddEventHandler(cache.ResourceEventHandlerDetailedFuncs{
		AddFunc:    w.onPodAdd,
		UpdateFunc: w.onPodUpdate,
		DeleteFunc: nil, // Pod deletion doesn't trigger spill evaluation.
	})
	if err != nil {
		logger.Error(err, "pod/event watch: add pod event handler failed")
		return nil, nil, nil, nil, nil, err
	}

	// Event informer: filtered to autoscaler-emitted reasons.
	eventInformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
				return kubeClient.CoreV1().Events("").List(ctx, options)
			},
			WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
				return kubeClient.CoreV1().Events("").Watch(ctx, options)
			},
		},
		&corev1.Event{},
		resyncPeriod,
		cache.Indexers{},
	)

	eventReg, err := eventInformer.AddEventHandler(cache.ResourceEventHandlerDetailedFuncs{
		AddFunc:    w.onEventAdd,
		UpdateFunc: nil,
		DeleteFunc: nil,
	})
	if err != nil {
		logger.Error(err, "pod/event watch: add event event handler failed")
		return nil, nil, nil, nil, nil, err
	}

	return w, podInformer, eventInformer, podReg, eventReg, nil
}

// ---------- Pod handlers (stale-pending path) ----------

func (w *PodEventWatch) onPodAdd(obj interface{}, _ bool) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		logger.Error(fmt.Errorf("unexpected type %T", obj),
			"pod add: type assertion failed")
		return
	}
	w.checkStalePending(pod)
}

func (w *PodEventWatch) onPodUpdate(_, newObj interface{}) {
	pod, ok := newObj.(*corev1.Pod)
	if !ok {
		logger.Error(fmt.Errorf("unexpected type %T", newObj),
			"pod update: type assertion failed")
		return
	}
	w.checkStalePending(pod)
}

// checkStalePending decides whether to enqueue a policy in response to a Pod
// event. The detailed log at entry traces the trigger evaluation: which pod
// resolved to which policy, how long it has been pending, and what threshold
// applies. Operators reading the log can replay the §6.3 stale-pending path
// without rerunning the controller.
func (w *PodEventWatch) checkStalePending(pod *corev1.Pod) {
	policyName := w.resolvePodToPolicy(pod)
	if policyName == "" {
		return
	}

	pending := podScheduledFalseTransitionTime(pod)
	if pending.IsZero() {
		return
	}

	policy, ok := w.policies().PolicyByName(policyName)
	if !ok || policy == nil {
		logger.Error(fmt.Errorf("policy %q not found", policyName),
			"stale-pending check: policy resolved but missing in registry",
			"pod", pod.Namespace+"/"+pod.Name,
		)
		return
	}

	elapsed := time.Since(pending)
	threshold := policy.Thresholds.TimePendingMax
	logger.Info("checking stale-pending trigger",
		"policy", policyName,
		"pod", pod.Namespace+"/"+pod.Name,
		"elapsed", elapsed.String(),
		"threshold", threshold.String(),
		"breaching", elapsed >= threshold,
	)

	if elapsed >= threshold {
		logger.Info("enqueue from stale-pending breach",
			"policy", policyName,
			"pod", pod.Namespace+"/"+pod.Name,
			"elapsed", elapsed.String(),
			"threshold", threshold.String(),
		)
		w.enqueue(policyName)
		return
	}

	remaining := threshold - elapsed + time.Second
	w.enqueueAfter(policyName, remaining)
}

// ---------- Event handler (autoscaler-exhaustion path) ----------

// onEventAdd reacts to autoscaler-emitted events on Pods. Detailed log at the
// trigger-decision point so operators can correlate "FailedScaling" /
// "NotTriggerScaleUp" events with the policy enqueue that follows.
func (w *PodEventWatch) onEventAdd(obj interface{}, _ bool) {
	event, ok := obj.(*corev1.Event)
	if !ok {
		logger.Error(fmt.Errorf("unexpected type %T", obj),
			"event add: type assertion failed")
		return
	}

	if !isAutoscalerExhaustedEvent(event) {
		return
	}

	if event.InvolvedObject.Kind != "Pod" {
		return
	}

	key := event.InvolvedObject.Namespace + "/" + event.InvolvedObject.Name
	logger.Info("checking autoscaler-exhausted trigger",
		"reason", event.Reason,
		"pod", key,
		"event", event.Namespace+"/"+event.Name,
	)

	item, exists, err := w.podInformer.GetStore().GetByKey(key)
	if err != nil {
		logger.Error(err, "autoscaler event: pod store lookup failed",
			"pod", key, "reason", event.Reason)
		return
	}
	if !exists {
		return
	}
	pod, ok := item.(*corev1.Pod)
	if !ok {
		logger.Error(fmt.Errorf("unexpected pod store type %T", item),
			"autoscaler event: pod store type assertion failed",
			"pod", key)
		return
	}

	policyName := w.resolvePodToPolicy(pod)
	if policyName != "" {
		logger.Info("enqueue from autoscaler-exhausted event",
			"policy", policyName,
			"pod", key,
			"reason", event.Reason,
		)
		w.enqueue(policyName)
	}
}

// ---------- helpers ----------

// resolvePodToPolicy follows the chain: Pod → PodGroup (via annotation) →
// Queue (via Spec.Queue) → policy (via PolicyByQueue).
func (w *PodEventWatch) resolvePodToPolicy(pod *corev1.Pod) string {
	pgName := podGroupAnnotation(pod)
	if pgName == "" {
		return ""
	}

	key := pod.Namespace + "/" + pgName
	item, exists, err := w.pgInformer.GetStore().GetByKey(key)
	if err != nil {
		logger.Error(err, "resolve pod→policy: podgroup store lookup failed",
			"pod", pod.Namespace+"/"+pod.Name,
			"podGroup", key)
		return ""
	}
	if !exists {
		return ""
	}
	pg, ok := item.(*schedulingv1beta1.PodGroup)
	if !ok {
		logger.Error(fmt.Errorf("unexpected podgroup store type %T", item),
			"resolve pod→policy: podgroup store type assertion failed",
			"podGroup", key)
		return ""
	}

	p, ok := w.policies().PolicyByQueue(pg.Spec.Queue)
	if !ok {
		return ""
	}
	return p.Name
}

func podGroupAnnotation(pod *corev1.Pod) string {
	if name := pod.Annotations[schedulingv1beta1.VolcanoGroupNameAnnotationKey]; name != "" {
		return name
	}
	return pod.Annotations[schedulingv1beta1.KubeGroupNameAnnotationKey]
}

func podScheduledFalseTransitionTime(pod *corev1.Pod) time.Time {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			return c.LastTransitionTime.Time
		}
	}
	return time.Time{}
}

func isAutoscalerExhaustedEvent(event *corev1.Event) bool {
	return event.Reason == "NotTriggerScaleUp" || event.Reason == "FailedScaling"
}
