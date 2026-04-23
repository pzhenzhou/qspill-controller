package snapshot

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

// resourceList is a tiny helper that keeps the test fixtures readable —
// callers list pairs of (name, quantity) instead of building corev1.ResourceList
// literals manually.
func resourceList(pairs ...string) corev1.ResourceList {
	if len(pairs)%2 != 0 {
		panic("resourceList expects (name, quantity) pairs")
	}
	out := corev1.ResourceList{}
	for i := 0; i < len(pairs); i += 2 {
		out[corev1.ResourceName(pairs[i])] = resource.MustParse(pairs[i+1])
	}
	return out
}

// pgWithSpec builds a PodGroup with only a spec.minResources entry and a
// matching name; used as the "spec fallback" baseline.
func pgWithSpec(name string, rl corev1.ResourceList) *schedulingv1beta1.PodGroup {
	return &schedulingv1beta1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       schedulingv1beta1.PodGroupSpec{MinResources: &rl},
	}
}

// podForGroup wires a Pod to a PodGroup via the Volcano group-name annotation
// and copies the per-container request list verbatim.
func podForGroup(name, group string, req corev1.ResourceList) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{schedulingv1beta1.VolcanoGroupNameAnnotationKey: group},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Resources: corev1.ResourceRequirements{Requests: req}}},
		},
	}
}

func TestComputeDemandPrefersSpecMinResources(t *testing.T) {
	pg := pgWithSpec("pg-1", resourceList("cpu", "2", "memory", "4Gi"))
	pods := []*corev1.Pod{podForGroup("p-1", "pg-1", resourceList("cpu", "100", "memory", "100Gi"))}

	total, estimated := computeDemand([]*schedulingv1beta1.PodGroup{pg}, pods)
	if estimated != 0 {
		t.Errorf("estimated = %d, want 0 when spec fallback wins", estimated)
	}
	if got := total[corev1.ResourceCPU]; got.Cmp(resource.MustParse("2")) != 0 {
		t.Errorf("cpu total = %s, want 2 (spec.minResources, ignoring pod request)", got.String())
	}
	if got := total[corev1.ResourceMemory]; got.Cmp(resource.MustParse("4Gi")) != 0 {
		t.Errorf("memory total = %s, want 4Gi", got.String())
	}
}

func TestComputeDemandFallsBackToPodRequests(t *testing.T) {
	pg := &schedulingv1beta1.PodGroup{ObjectMeta: metav1.ObjectMeta{Name: "pg-1"}}
	pods := []*corev1.Pod{
		podForGroup("p-1", "pg-1", resourceList("cpu", "1", "memory", "1Gi")),
		podForGroup("p-2", "pg-1", resourceList("cpu", "500m", "memory", "512Mi")),
	}

	total, estimated := computeDemand([]*schedulingv1beta1.PodGroup{pg}, pods)
	if estimated != 1 {
		t.Errorf("estimated = %d, want 1 (one PG inferred from pods)", estimated)
	}
	if got := total[corev1.ResourceCPU]; got.Cmp(resource.MustParse("1500m")) != 0 {
		t.Errorf("cpu total = %s, want 1500m", got.String())
	}
	if got := total[corev1.ResourceMemory]; got.Cmp(resource.MustParse("1536Mi")) != 0 {
		t.Errorf("memory total = %s, want 1536Mi", got.String())
	}
}

func TestComputeDemandSkipsTerminalPods(t *testing.T) {
	pg := &schedulingv1beta1.PodGroup{ObjectMeta: metav1.ObjectMeta{Name: "pg-1"}}
	live := podForGroup("alive", "pg-1", resourceList("cpu", "1"))
	done := podForGroup("done", "pg-1", resourceList("cpu", "8"))
	done.Status.Phase = corev1.PodSucceeded

	total, estimated := computeDemand([]*schedulingv1beta1.PodGroup{pg}, []*corev1.Pod{live, done})
	if estimated != 1 {
		t.Errorf("estimated = %d, want 1", estimated)
	}
	if got := total[corev1.ResourceCPU]; got.Cmp(resource.MustParse("1")) != 0 {
		t.Errorf("cpu total = %s, want 1 (terminal pod ignored)", got.String())
	}
}

func TestComputeDemandNoneWhenNoSourceAvailable(t *testing.T) {
	pg := &schedulingv1beta1.PodGroup{ObjectMeta: metav1.ObjectMeta{Name: "pg-1"}}
	total, estimated := computeDemand([]*schedulingv1beta1.PodGroup{pg}, nil)
	if estimated != 1 {
		t.Errorf("estimated = %d, want 1 (PG with no demand source counts as estimated)", estimated)
	}
	if len(total) != 0 {
		t.Errorf("total = %v, want empty", total)
	}
}

func TestComputeDemandTreatsEmptySpecAsAbsent(t *testing.T) {
	rl := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0")}
	pg := &schedulingv1beta1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-1"},
		Spec:       schedulingv1beta1.PodGroupSpec{MinResources: &rl},
	}
	pods := []*corev1.Pod{podForGroup("p-1", "pg-1", resourceList("cpu", "750m"))}

	total, estimated := computeDemand([]*schedulingv1beta1.PodGroup{pg}, pods)
	if estimated != 1 {
		t.Errorf("estimated = %d, want 1 (zero spec falls through)", estimated)
	}
	if got := total[corev1.ResourceCPU]; got.Cmp(resource.MustParse("750m")) != 0 {
		t.Errorf("cpu total = %s, want 750m", got.String())
	}
}

// TestComputeDemandSumsAcrossPodGroups proves the per-PG contributions
// accumulate, including the case where two different fallback branches fire
// in the same call.
func TestComputeDemandSumsAcrossPodGroups(t *testing.T) {
	pgSpec := pgWithSpec("pg-spec", resourceList("cpu", "1", "memory", "1Gi"))
	pgInferred := &schedulingv1beta1.PodGroup{ObjectMeta: metav1.ObjectMeta{Name: "pg-inferred"}}
	pods := []*corev1.Pod{
		podForGroup("p-1", "pg-inferred", resourceList("cpu", "500m", "memory", "256Mi")),
	}

	total, estimated := computeDemand([]*schedulingv1beta1.PodGroup{pgSpec, pgInferred}, pods)
	if estimated != 1 {
		t.Errorf("estimated = %d, want 1 (only pg-inferred used pod fallback)", estimated)
	}
	if got := total[corev1.ResourceCPU]; got.Cmp(resource.MustParse("1500m")) != 0 {
		t.Errorf("cpu total = %s, want 1500m", got.String())
	}
	if got := total[corev1.ResourceMemory]; got.Cmp(resource.MustParse("1280Mi")) != 0 {
		t.Errorf("memory total = %s, want 1280Mi", got.String())
	}
}

func TestPodGroupNameOfPrefersVolcanoKey(t *testing.T) {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		schedulingv1beta1.VolcanoGroupNameAnnotationKey: "new",
		schedulingv1beta1.KubeGroupNameAnnotationKey:    "old",
	}}}
	if got := podGroupNameOf(p); got != "new" {
		t.Errorf("podGroupNameOf = %q, want new", got)
	}
}

func TestPodGroupNameOfFallsBackToLegacyKey(t *testing.T) {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		schedulingv1beta1.KubeGroupNameAnnotationKey: "legacy",
	}}}
	if got := podGroupNameOf(p); got != "legacy" {
		t.Errorf("podGroupNameOf = %q, want legacy", got)
	}
}

func TestPodGroupNameOfReturnsEmptyWhenAbsent(t *testing.T) {
	if got := podGroupNameOf(&corev1.Pod{}); got != "" {
		t.Errorf("podGroupNameOf = %q, want empty", got)
	}
}
