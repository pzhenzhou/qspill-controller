package controller

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	qspillv1alpha1 "github.com/pzhenzhou/qspill-controller/pkg/api/v1alpha1"
	volcanov1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

func newScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = qspillv1alpha1.AddToScheme(scheme)
	_ = volcanov1beta1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	return scheme
}

func TestReconcile_ActivePhase(t *testing.T) {
	scheme := newScheme()

	queue := &volcanov1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tenant-queue",
		},
		Spec: volcanov1beta1.QueueSpec{
			Capability: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("100"),
			},
		},
		Status: volcanov1beta1.QueueStatus{
			Allocated: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("50"),
			},
		},
	}

	policy := &qspillv1alpha1.QSpillPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-policy",
			Namespace:  "default",
			Finalizers: []string{finalizerName},
		},
		Spec: qspillv1alpha1.QSpillPolicySpec{
			SourceQueue: "tenant-queue",
			SpillTrigger: qspillv1alpha1.SpillTrigger{
				UtilizationThreshold: "0.8",
				EvaluationPeriod:     metav1.Duration{Duration: 10 * time.Second},
			},
			SpillTargets: []qspillv1alpha1.SpillTarget{
				{
					QueueName: "shared-queue",
					Priority:  50,
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(queue, policy).
		WithStatusSubresource(policy).
		Build()

	reconciler := &QSpillPolicyReconciler{
		Client: fakeClient,
		Log:    testr.New(t),
		Scheme: scheme,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-policy",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("Expected non-zero RequeueAfter")
	}

	updatedPolicy := &qspillv1alpha1.QSpillPolicy{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updatedPolicy); err != nil {
		t.Fatalf("Failed to get updated policy: %v", err)
	}

	if updatedPolicy.Status.Phase != qspillv1alpha1.QSpillPolicyPhaseActive {
		t.Errorf("Expected Active phase, got %s", updatedPolicy.Status.Phase)
	}
}

func TestReconcile_SpillingPhase(t *testing.T) {
	scheme := newScheme()

	queue := &volcanov1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tenant-queue",
		},
		Spec: volcanov1beta1.QueueSpec{
			Capability: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("100"),
			},
		},
		Status: volcanov1beta1.QueueStatus{
			Allocated: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("90"),
			},
		},
	}

	targetQueue := &volcanov1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{
			Name: "shared-queue",
		},
		Spec: volcanov1beta1.QueueSpec{
			Capability: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("200"),
			},
		},
	}

	policy := &qspillv1alpha1.QSpillPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-policy",
			Namespace:  "default",
			Finalizers: []string{finalizerName},
		},
		Spec: qspillv1alpha1.QSpillPolicySpec{
			SourceQueue: "tenant-queue",
			SpillTrigger: qspillv1alpha1.SpillTrigger{
				UtilizationThreshold: "0.8",
				EvaluationPeriod:     metav1.Duration{Duration: 10 * time.Second},
			},
			SpillTargets: []qspillv1alpha1.SpillTarget{
				{
					QueueName: "shared-queue",
					Priority:  50,
					MaxSpillCapacity: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("10"),
					},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(queue, targetQueue, policy).
		WithStatusSubresource(policy).
		Build()

	reconciler := &QSpillPolicyReconciler{
		Client: fakeClient,
		Log:    testr.New(t),
		Scheme: scheme,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-policy",
			Namespace: "default",
		},
	}

	_, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	updatedPolicy := &qspillv1alpha1.QSpillPolicy{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updatedPolicy); err != nil {
		t.Fatalf("Failed to get updated policy: %v", err)
	}

	if updatedPolicy.Status.Phase != qspillv1alpha1.QSpillPolicyPhaseSpilling {
		t.Errorf("Expected Spilling phase, got %s", updatedPolicy.Status.Phase)
	}

	if len(updatedPolicy.Status.CurrentSpillTargets) == 0 {
		t.Error("Expected at least one active spill target")
	}

	updatedTarget := &volcanov1beta1.Queue{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "shared-queue"}, updatedTarget); err != nil {
		t.Fatalf("Failed to get updated target queue: %v", err)
	}
	if updatedTarget.Spec.Deserved == nil {
		t.Error("Expected target queue deserved to be updated")
	}
}

func TestReconcile_ReclaimLogic(t *testing.T) {
	scheme := newScheme()

	queue := &volcanov1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tenant-queue",
		},
		Spec: volcanov1beta1.QueueSpec{
			Capability: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("100"),
			},
		},
		Status: volcanov1beta1.QueueStatus{
			Allocated: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("30"),
			},
		},
	}

	targetQueue := &volcanov1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{
			Name: "shared-queue",
		},
		Spec: volcanov1beta1.QueueSpec{
			Deserved: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("10"),
			},
		},
	}

	spillStartTime := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	policy := &qspillv1alpha1.QSpillPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-policy",
			Namespace:  "default",
			Finalizers: []string{finalizerName},
		},
		Spec: qspillv1alpha1.QSpillPolicySpec{
			SourceQueue: "tenant-queue",
			SpillTrigger: qspillv1alpha1.SpillTrigger{
				UtilizationThreshold: "0.8",
				EvaluationPeriod:     metav1.Duration{Duration: 10 * time.Second},
			},
			SpillTargets: []qspillv1alpha1.SpillTarget{
				{
					QueueName: "shared-queue",
					Priority:  50,
				},
			},
			ReclaimPolicy: &qspillv1alpha1.ReclaimPolicy{
				Strategy:    qspillv1alpha1.ReclaimStrategyImmediate,
				GracePeriod: metav1.Duration{Duration: 5 * time.Minute},
			},
		},
		Status: qspillv1alpha1.QSpillPolicyStatus{
			Phase: qspillv1alpha1.QSpillPolicyPhaseSpilling,
			CurrentSpillTargets: []qspillv1alpha1.ActiveSpillTarget{
				{
					QueueName: "shared-queue",
					Namespace: "default",
					SpilledCapacity: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("10"),
					},
					SpillStartTime: spillStartTime,
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(queue, targetQueue, policy).
		WithStatusSubresource(policy).
		Build()

	reconciler := &QSpillPolicyReconciler{
		Client: fakeClient,
		Log:    testr.New(t),
		Scheme: scheme,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-policy",
			Namespace: "default",
		},
	}

	_, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	updatedPolicy := &qspillv1alpha1.QSpillPolicy{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updatedPolicy); err != nil {
		t.Fatalf("Failed to get updated policy: %v", err)
	}

	if updatedPolicy.Status.Phase != qspillv1alpha1.QSpillPolicyPhaseActive {
		t.Errorf("Expected Active phase after reclaim, got %s", updatedPolicy.Status.Phase)
	}
}
