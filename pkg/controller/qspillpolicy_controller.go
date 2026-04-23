package controller

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	qspillv1alpha1 "github.com/pzhenzhou/qspill-controller/pkg/api/v1alpha1"
	"github.com/pzhenzhou/qspill-controller/pkg/metrics"
	volcanov1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

const (
	conditionTypeSpilling = "Spilling"
	conditionTypeReady    = "Ready"
	finalizerName         = "scheduling.qspill.io/finalizer"
)

// QSpillPolicyReconciler reconciles a QSpillPolicy object
type QSpillPolicyReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=scheduling.qspill.io,resources=qspillpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scheduling.qspill.io,resources=qspillpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=scheduling.qspill.io,resources=qspillpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=scheduling.volcano.sh,resources=queues,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *QSpillPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("qspillpolicy", req.NamespacedName)

	policy := &qspillv1alpha1.QSpillPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !policy.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, policy)
	}

	if !containsString(policy.Finalizers, finalizerName) {
		policy.Finalizers = append(policy.Finalizers, finalizerName)
		if err := r.Update(ctx, policy); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	sourceQueue := &volcanov1beta1.Queue{}
	sourceQueueKey := types.NamespacedName{Name: policy.Spec.SourceQueue}
	if err := r.Get(ctx, sourceQueueKey, sourceQueue); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Source queue not found, requeueing", "queue", policy.Spec.SourceQueue)
			return r.setInactivePhase(ctx, policy, "SourceQueueNotFound", "Source Volcano queue not found")
		}
		return ctrl.Result{}, err
	}

	utilization, err := r.calculateUtilization(sourceQueue)
	if err != nil {
		log.Error(err, "Failed to calculate queue utilization")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	utilizationStr := fmt.Sprintf("%.4f", utilization)
	log.Info("Queue utilization calculated", "queue", policy.Spec.SourceQueue, "utilization", utilizationStr)

	metrics.QueueUtilization.WithLabelValues(policy.Spec.SourceQueue, req.Namespace).Set(utilization)

	threshold, err := strconv.ParseFloat(policy.Spec.SpillTrigger.UtilizationThreshold, 64)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("invalid utilization threshold: %w", err)
	}

	evaluationPeriod := policy.Spec.SpillTrigger.EvaluationPeriod.Duration
	if evaluationPeriod == 0 {
		evaluationPeriod = 60 * time.Second
	}

	patch := client.MergeFrom(policy.DeepCopy())
	policy.Status.SourceQueueUtilization = utilizationStr
	policy.Status.ObservedGeneration = policy.Generation

	if utilization > threshold {
		if err := r.handleSpill(ctx, log, policy, sourceQueue, utilization); err != nil {
			log.Error(err, "Failed to handle spill")
			return ctrl.Result{RequeueAfter: evaluationPeriod}, err
		}
	} else {
		if policy.Status.Phase == qspillv1alpha1.QSpillPolicyPhaseSpilling {
			if err := r.handleReclaim(ctx, log, policy); err != nil {
				log.Error(err, "Failed to handle reclaim")
				return ctrl.Result{RequeueAfter: evaluationPeriod}, err
			}
		} else {
			now := metav1.Now()
			policy.Status.Phase = qspillv1alpha1.QSpillPolicyPhaseActive
			policy.Status.LastTransitionTime = &now
			setCondition(&policy.Status.Conditions, metav1.Condition{
				Type:               conditionTypeReady,
				Status:             metav1.ConditionTrue,
				Reason:             "BelowThreshold",
				Message:            fmt.Sprintf("Queue utilization %.4f is below threshold %.4f", utilization, threshold),
				LastTransitionTime: now,
			})
		}
	}

	if err := r.Status().Patch(ctx, policy, patch); err != nil {
		log.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: evaluationPeriod}, nil
}

func (r *QSpillPolicyReconciler) handleSpill(ctx context.Context, log logr.Logger, policy *qspillv1alpha1.QSpillPolicy, sourceQueue *volcanov1beta1.Queue, utilization float64) error {
	targets := make([]qspillv1alpha1.SpillTarget, len(policy.Spec.SpillTargets))
	copy(targets, policy.Spec.SpillTargets)
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].Priority > targets[j].Priority
	})

	now := metav1.Now()
	var activeTargets []qspillv1alpha1.ActiveSpillTarget

	for _, target := range targets {
		targetNS := target.Namespace
		if targetNS == "" {
			targetNS = policy.Namespace
		}

		targetQueue := &volcanov1beta1.Queue{}
		if err := r.Get(ctx, types.NamespacedName{Name: target.QueueName}, targetQueue); err != nil {
			if errors.IsNotFound(err) {
				log.Info("Target queue not found, skipping", "queue", target.QueueName)
				continue
			}
			return fmt.Errorf("failed to get target queue %s: %w", target.QueueName, err)
		}

		spillCapacity := r.calculateSpillCapacity(sourceQueue, targetQueue, target)

		updatedQueue := targetQueue.DeepCopy()
		if updatedQueue.Spec.Deserved == nil {
			updatedQueue.Spec.Deserved = corev1.ResourceList{}
		}

		for resourceName, qty := range spillCapacity {
			existing := updatedQueue.Spec.Deserved[resourceName]
			existing.Add(qty)
			updatedQueue.Spec.Deserved[resourceName] = existing
		}

		if err := r.Update(ctx, updatedQueue); err != nil {
			return fmt.Errorf("failed to update target queue %s: %w", target.QueueName, err)
		}

		log.Info("Spilled resources to target queue", "source", policy.Spec.SourceQueue, "target", target.QueueName)
		metrics.SpillEventsTotal.WithLabelValues(policy.Spec.SourceQueue, target.QueueName).Inc()

		activeTargets = append(activeTargets, qspillv1alpha1.ActiveSpillTarget{
			QueueName:       target.QueueName,
			Namespace:       targetNS,
			SpilledCapacity: spillCapacity,
			SpillStartTime:  now,
		})
	}

	policy.Status.Phase = qspillv1alpha1.QSpillPolicyPhaseSpilling
	policy.Status.CurrentSpillTargets = activeTargets
	policy.Status.LastTransitionTime = &now
	metrics.ActiveSpills.WithLabelValues(policy.Spec.SourceQueue).Set(float64(len(activeTargets)))

	setCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               conditionTypeSpilling,
		Status:             metav1.ConditionTrue,
		Reason:             "UtilizationExceeded",
		Message:            fmt.Sprintf("Queue utilization %.4f exceeds threshold", utilization),
		LastTransitionTime: now,
	})

	return nil
}

func (r *QSpillPolicyReconciler) handleReclaim(ctx context.Context, log logr.Logger, policy *qspillv1alpha1.QSpillPolicy) error {
	reclaimPolicy := policy.Spec.ReclaimPolicy
	gracePeriod := 5 * time.Minute

	if reclaimPolicy != nil && reclaimPolicy.GracePeriod.Duration > 0 {
		gracePeriod = reclaimPolicy.GracePeriod.Duration
	}

	now := metav1.Now()
	var remainingTargets []qspillv1alpha1.ActiveSpillTarget

	for _, activeTarget := range policy.Status.CurrentSpillTargets {
		elapsed := now.Sub(activeTarget.SpillStartTime.Time)
		shouldReclaim := false

		if reclaimPolicy != nil && reclaimPolicy.Strategy == qspillv1alpha1.ReclaimStrategyImmediate {
			shouldReclaim = true
		} else if elapsed >= gracePeriod {
			shouldReclaim = true
		}

		if shouldReclaim {
			targetQueue := &volcanov1beta1.Queue{}
			if err := r.Get(ctx, types.NamespacedName{Name: activeTarget.QueueName}, targetQueue); err != nil {
				if errors.IsNotFound(err) {
					log.Info("Target queue not found during reclaim, skipping", "queue", activeTarget.QueueName)
					continue
				}
				return fmt.Errorf("failed to get target queue %s during reclaim: %w", activeTarget.QueueName, err)
			}

			updatedQueue := targetQueue.DeepCopy()
			if updatedQueue.Spec.Deserved != nil {
				for resourceName, spilledQty := range activeTarget.SpilledCapacity {
					if existing, ok := updatedQueue.Spec.Deserved[resourceName]; ok {
						existing.Sub(spilledQty)
						if existing.Cmp(resource.MustParse("0")) <= 0 {
							delete(updatedQueue.Spec.Deserved, resourceName)
						} else {
							updatedQueue.Spec.Deserved[resourceName] = existing
						}
					}
				}
			}

			if err := r.Update(ctx, updatedQueue); err != nil {
				return fmt.Errorf("failed to update target queue %s during reclaim: %w", activeTarget.QueueName, err)
			}

			log.Info("Reclaimed resources from target queue", "source", policy.Spec.SourceQueue, "target", activeTarget.QueueName)
			metrics.ReclaimEventsTotal.WithLabelValues(policy.Spec.SourceQueue, activeTarget.QueueName).Inc()
		} else {
			remainingTargets = append(remainingTargets, activeTarget)
		}
	}

	policy.Status.CurrentSpillTargets = remainingTargets
	metrics.ActiveSpills.WithLabelValues(policy.Spec.SourceQueue).Set(float64(len(remainingTargets)))

	if len(remainingTargets) == 0 {
		policy.Status.Phase = qspillv1alpha1.QSpillPolicyPhaseActive
		policy.Status.LastTransitionTime = &now
		setCondition(&policy.Status.Conditions, metav1.Condition{
			Type:               conditionTypeSpilling,
			Status:             metav1.ConditionFalse,
			Reason:             "Reclaimed",
			Message:            "All spilled resources have been reclaimed",
			LastTransitionTime: now,
		})
	}

	return nil
}

func (r *QSpillPolicyReconciler) handleDeletion(ctx context.Context, policy *qspillv1alpha1.QSpillPolicy) (ctrl.Result, error) {
	log := r.Log.WithValues("qspillpolicy", policy.Name)

	for _, activeTarget := range policy.Status.CurrentSpillTargets {
		targetQueue := &volcanov1beta1.Queue{}
		if err := r.Get(ctx, types.NamespacedName{Name: activeTarget.QueueName}, targetQueue); err != nil {
			if !errors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			continue
		}

		updatedQueue := targetQueue.DeepCopy()
		if updatedQueue.Spec.Deserved != nil {
			for resourceName, spilledQty := range activeTarget.SpilledCapacity {
				if existing, ok := updatedQueue.Spec.Deserved[resourceName]; ok {
					existing.Sub(spilledQty)
					if existing.Cmp(resource.MustParse("0")) <= 0 {
						delete(updatedQueue.Spec.Deserved, resourceName)
					} else {
						updatedQueue.Spec.Deserved[resourceName] = existing
					}
				}
			}
		}

		if err := r.Update(ctx, updatedQueue); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Reclaimed resources during deletion", "target", activeTarget.QueueName)
	}

	policy.Finalizers = removeString(policy.Finalizers, finalizerName)
	if err := r.Update(ctx, policy); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *QSpillPolicyReconciler) calculateUtilization(queue *volcanov1beta1.Queue) (float64, error) {
	capacityList := queue.Spec.Capability
	allocatedList := queue.Status.Allocated

	if capacityList == nil || len(capacityList) == 0 {
		return 0, nil
	}

	// CPU is preferred for utilization calculation as it is the most common
	// scheduling bottleneck. Fall back to memory if CPU is not configured.
	// AsApproximateFloat64() is acceptable here: small precision loss has
	// negligible impact on threshold comparisons.
	cpuCapacity, hasCPU := capacityList[corev1.ResourceCPU]
	if !hasCPU || cpuCapacity.IsZero() {
		memCapacity, hasMem := capacityList[corev1.ResourceMemory]
		if !hasMem || memCapacity.IsZero() {
			return 0, nil
		}
		memAllocated := resource.MustParse("0")
		if allocatedList != nil {
			if alloc, ok := allocatedList[corev1.ResourceMemory]; ok {
				memAllocated = alloc
			}
		}
		capVal := memCapacity.AsApproximateFloat64()
		allocVal := memAllocated.AsApproximateFloat64()
		if capVal == 0 {
			return 0, nil
		}
		return allocVal / capVal, nil
	}

	cpuAllocated := resource.MustParse("0")
	if allocatedList != nil {
		if alloc, ok := allocatedList[corev1.ResourceCPU]; ok {
			cpuAllocated = alloc
		}
	}

	capVal := cpuCapacity.AsApproximateFloat64()
	allocVal := cpuAllocated.AsApproximateFloat64()
	if capVal == 0 {
		return 0, nil
	}
	return allocVal / capVal, nil
}

func (r *QSpillPolicyReconciler) calculateSpillCapacity(sourceQueue, targetQueue *volcanov1beta1.Queue, target qspillv1alpha1.SpillTarget) corev1.ResourceList {
	result := corev1.ResourceList{}

	if target.MaxSpillCapacity != nil {
		for resourceName, qty := range target.MaxSpillCapacity {
			result[resourceName] = qty.DeepCopy()
		}
		return result
	}

	if sourceQueue.Spec.Capability != nil {
		for resourceName, capQty := range sourceQueue.Spec.Capability {
			capVal := capQty.AsApproximateFloat64()
			spillVal := capVal * 0.1
			// Use MilliValue-based scaling only for CPU (dimensionless ratio).
			// For all other resource types (memory, etc.) prefer integer quantities
			// to avoid sub-byte precision issues; fall back to 10% via NewQuantity.
			var spillQty *resource.Quantity
			if resourceName == corev1.ResourceCPU {
				spillQty = resource.NewMilliQuantity(int64(spillVal*1000), resource.DecimalSI)
			} else {
				spillQty = resource.NewQuantity(int64(spillVal), resource.BinarySI)
			}
			result[resourceName] = *spillQty
		}
	}

	return result
}

func (r *QSpillPolicyReconciler) setInactivePhase(ctx context.Context, policy *qspillv1alpha1.QSpillPolicy, reason, message string) (ctrl.Result, error) {
	patch := client.MergeFrom(policy.DeepCopy())
	now := metav1.Now()
	policy.Status.Phase = qspillv1alpha1.QSpillPolicyPhaseInactive
	policy.Status.LastTransitionTime = &now
	setCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
	if err := r.Status().Patch(ctx, policy, patch); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *QSpillPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&qspillv1alpha1.QSpillPolicy{}).
		Watches(
			&volcanov1beta1.Queue{},
			handler.EnqueueRequestsFromMapFunc(r.mapQueueToPolicy),
		).
		Complete(r)
}

func (r *QSpillPolicyReconciler) mapQueueToPolicy(ctx context.Context, obj client.Object) []reconcile.Request {
	queue, ok := obj.(*volcanov1beta1.Queue)
	if !ok {
		return nil
	}

	policyList := &qspillv1alpha1.QSpillPolicyList{}
	if err := r.List(ctx, policyList); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, policy := range policyList.Items {
		if policy.Spec.SourceQueue == queue.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      policy.Name,
					Namespace: policy.Namespace,
				},
			})
		}
		for _, target := range policy.Spec.SpillTargets {
			if target.QueueName == queue.Name {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      policy.Name,
						Namespace: policy.Namespace,
					},
				})
				break
			}
		}
	}
	return requests
}

func setCondition(conditions *[]metav1.Condition, newCondition metav1.Condition) {
	for i, c := range *conditions {
		if c.Type == newCondition.Type {
			(*conditions)[i] = newCondition
			return
		}
	}
	*conditions = append(*conditions, newCondition)
}

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) []string {
	var result []string
	for _, item := range slice {
		if item != s {
			result = append(result, item)
		}
	}
	return result
}
