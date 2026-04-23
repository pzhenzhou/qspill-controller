package watcher

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/config"
)

func TestIsAutoscalerExhaustedEvent(t *testing.T) {
	tests := []struct {
		reason string
		want   bool
	}{
		{"NotTriggerScaleUp", true},
		{"FailedScaling", true},
		{"Scheduled", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isAutoscalerExhaustedEvent(&corev1.Event{Reason: tt.reason}); got != tt.want {
			t.Errorf("isAutoscalerExhaustedEvent(%q) = %v, want %v", tt.reason, got, tt.want)
		}
	}
}

func TestPodGroupAnnotation(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        string
	}{
		{
			"volcano key",
			map[string]string{schedulingv1beta1.VolcanoGroupNameAnnotationKey: "pg-1"},
			"pg-1",
		},
		{
			"legacy key",
			map[string]string{schedulingv1beta1.KubeGroupNameAnnotationKey: "pg-2"},
			"pg-2",
		},
		{
			"both keys, volcano wins",
			map[string]string{
				schedulingv1beta1.VolcanoGroupNameAnnotationKey: "pg-volcano",
				schedulingv1beta1.KubeGroupNameAnnotationKey:    "pg-kube",
			},
			"pg-volcano",
		},
		{
			"no annotation",
			nil,
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations}}
			if got := podGroupAnnotation(pod); got != tt.want {
				t.Errorf("podGroupAnnotation() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPodScheduledFalseTransitionTime(t *testing.T) {
	now := time.Now()
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{
					Type:               corev1.PodScheduled,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(now),
				},
			},
		},
	}
	got := podScheduledFalseTransitionTime(pod)
	if !got.Equal(now) {
		t.Errorf("podScheduledFalseTransitionTime() = %v, want %v", got, now)
	}
}

func TestPodScheduledFalseTransitionTime_Scheduled(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
			},
		},
	}
	got := podScheduledFalseTransitionTime(pod)
	if !got.IsZero() {
		t.Errorf("podScheduledFalseTransitionTime() = %v, want zero", got)
	}
}

func TestPodScheduledFalseTransitionTime_NoPodScheduledCondition(t *testing.T) {
	pod := &corev1.Pod{}
	got := podScheduledFalseTransitionTime(pod)
	if !got.IsZero() {
		t.Errorf("podScheduledFalseTransitionTime() = %v, want zero", got)
	}
}

func TestResolvePodToPolicy_UsesSharedPodGroupInformer(t *testing.T) {
	pgInformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{},
		&schedulingv1beta1.PodGroup{},
		0,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)
	pg := &schedulingv1beta1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pg-a",
			Namespace: "default",
		},
		Spec: schedulingv1beta1.PodGroupSpec{
			Queue: "queue-a",
		},
	}
	if err := pgInformer.GetStore().Add(pg); err != nil {
		t.Fatalf("add podgroup to shared informer store: %v", err)
	}

	store := config.NewRegistryStore()
	store.Set(config.NewTestRegistry(config.Defaults{}, []*api.SpillPolicy{{
		Name:               "policy-a",
		QueueName:          "queue-a",
		DedicatedNodeGroup: "ng-dedicated",
		OverflowNodeGroup:  "ng-overflow",
	}}))

	w := &PodEventWatch{
		policies:   store.Get,
		pgInformer: pgInformer,
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-a",
			Namespace: "default",
			Annotations: map[string]string{
				schedulingv1beta1.VolcanoGroupNameAnnotationKey: "pg-a",
			},
		},
	}

	if got := w.resolvePodToPolicy(pod); got != "policy-a" {
		t.Fatalf("resolvePodToPolicy() = %q, want %q", got, "policy-a")
	}
}

func TestCheckStalePending_EnqueuesFromSharedPodGroupInformer(t *testing.T) {
	pgInformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{},
		&schedulingv1beta1.PodGroup{},
		0,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)
	pg := &schedulingv1beta1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pg-a",
			Namespace: "default",
		},
		Spec: schedulingv1beta1.PodGroupSpec{
			Queue: "queue-a",
		},
	}
	if err := pgInformer.GetStore().Add(pg); err != nil {
		t.Fatalf("add podgroup to shared informer store: %v", err)
	}

	store := config.NewRegistryStore()
	store.Set(config.NewTestRegistry(config.Defaults{}, []*api.SpillPolicy{{
		Name:               "policy-a",
		QueueName:          "queue-a",
		DedicatedNodeGroup: "ng-dedicated",
		OverflowNodeGroup:  "ng-overflow",
		Thresholds: api.Thresholds{
			TimePendingMax: time.Minute,
		},
	}}))

	rec := &enqueueRecorder{}
	w := &PodEventWatch{
		policies:     store.Get,
		enqueue:      rec.enqueue,
		enqueueAfter: func(string, time.Duration) {},
		pgInformer:   pgInformer,
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-a",
			Namespace: "default",
			Annotations: map[string]string{
				schedulingv1beta1.VolcanoGroupNameAnnotationKey: "pg-a",
			},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{
				Type:               corev1.PodScheduled,
				Status:             corev1.ConditionFalse,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-2 * time.Minute)),
			}},
		},
	}

	w.checkStalePending(pod)
	rec.assertExactly(t, "policy-a")
}
