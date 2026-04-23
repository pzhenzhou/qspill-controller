package snapshot

import (
	corev1 "k8s.io/api/core/v1"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

// computeDemand applies the per-PodGroup fallback chain documented in
// DESIGN.md §4.3.1 and returns the aggregated demand together with a count
// of PodGroups whose demand had to be inferred from individual pod requests.
// The pods slice must already contain *only* this policy's pods; callers
// pre-filter to keep this helper allocation-free in the common path.
//
// Fallback order, evaluated per PodGroup:
//  1. pg.Spec.MinResources, when non-nil and non-zero.
//  2. Sum of pod.Spec.Containers[*].Resources.Requests across this group's
//     non-terminal pods. Increments estimated.
//  3. Empty contribution. Increments estimated; the StalePendingPods trigger
//     remains the safety net for groups invisible to the demand model.
//
// The earlier draft also probed pg.Status.MinResources, but the Volcano
// v1beta1 PodGroupStatus on the pinned API surface (volcano.sh/apis v1.13.0)
// has no such field; Spec.MinResources is the authoritative declaration and
// the only structured demand source available without scanning Pods.
func computeDemand(pgs []*schedulingv1beta1.PodGroup, pods []*corev1.Pod) (corev1.ResourceList, int) {
	total := corev1.ResourceList{}
	estimated := 0

	byOwner := indexPodsByGroupName(pods)

	for _, pg := range pgs {
		if pg == nil {
			continue
		}
		switch contrib, used := perPodGroupDemand(pg, byOwner); used {
		case demandSourceSpec:
			addResources(total, contrib)
		case demandSourcePods:
			estimated++
			addResources(total, contrib)
		case demandSourceNone:
			estimated++
		}
	}
	return total, estimated
}

// demandSource enumerates which fallback branch produced the per-PodGroup
// contribution. Internal: the snapshot caller only needs the totals plus the
// estimated count.
type demandSource int

const (
	demandSourceSpec demandSource = iota
	demandSourcePods
	demandSourceNone
)

func perPodGroupDemand(pg *schedulingv1beta1.PodGroup, podsByGroup map[string][]*corev1.Pod) (corev1.ResourceList, demandSource) {
	if rl := pg.Spec.MinResources; rl != nil && !isZeroResources(*rl) {
		return cloneResources(*rl), demandSourceSpec
	}
	pods := podsByGroup[pg.Name]
	if len(pods) == 0 {
		return nil, demandSourceNone
	}
	sum := corev1.ResourceList{}
	any := false
	for _, p := range pods {
		if p == nil || isPodTerminal(p) {
			continue
		}
		for _, c := range p.Spec.Containers {
			if len(c.Resources.Requests) == 0 {
				continue
			}
			any = true
			addResources(sum, c.Resources.Requests)
		}
	}
	if !any {
		return nil, demandSourceNone
	}
	return sum, demandSourcePods
}

// indexPodsByGroupName groups pods by the PodGroup name discovered on each
// pod's annotations. Pods missing the annotation contribute to no group and
// are silently skipped — the same pod still counts toward StalePendingPods
// independently because that scan walks the policy-pods slice directly.
func indexPodsByGroupName(pods []*corev1.Pod) map[string][]*corev1.Pod {
	out := make(map[string][]*corev1.Pod, len(pods))
	for _, p := range pods {
		if p == nil {
			continue
		}
		if name := podGroupNameOf(p); name != "" {
			out[name] = append(out[name], p)
		}
	}
	return out
}

// podGroupNameOf reads the PodGroup-name annotation that volcano writes onto
// scheduled pods. The newer volcano.sh/group-name key takes precedence over
// the legacy scheduling.k8s.io/group-name to match Volcano's own resolution
// order; both keys are accepted so older operators stay functional.
func podGroupNameOf(p *corev1.Pod) string {
	if name := p.Annotations[schedulingv1beta1.VolcanoGroupNameAnnotationKey]; name != "" {
		return name
	}
	return p.Annotations[schedulingv1beta1.KubeGroupNameAnnotationKey]
}

// isZeroResources reports whether every quantity in rl is zero. Treats a nil
// or empty list as zero by the same logic.
func isZeroResources(rl corev1.ResourceList) bool {
	for _, q := range rl {
		if !q.IsZero() {
			return false
		}
	}
	return true
}

// addResources accumulates src into dst in place. Quantities use canonical
// addition so units stay normalised (e.g. 500m + 500m = 1).
func addResources(dst, src corev1.ResourceList) {
	for name, q := range src {
		cur := dst[name]
		cur.Add(q)
		dst[name] = cur
	}
}

// cloneResources returns a deep copy of rl so the snapshot never aliases
// quantities owned by the informer cache.
func cloneResources(rl corev1.ResourceList) corev1.ResourceList {
	out := make(corev1.ResourceList, len(rl))
	for name, q := range rl {
		out[name] = q.DeepCopy()
	}
	return out
}

// isPodTerminal mirrors the standard k8s notion of a pod that is no longer
// consuming scheduler attention. Used when summing pod requests so completed
// or failed pods do not inflate the demand estimate.
func isPodTerminal(p *corev1.Pod) bool {
	switch p.Status.Phase {
	case corev1.PodSucceeded, corev1.PodFailed:
		return true
	}
	return false
}
