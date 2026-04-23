package snapshot

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
)

// autoscalerExhaustedReasons enumerates the upstream cluster-autoscaler
// event reasons that indicate the dedicated pool cannot grow right now.
// Documented as a fixed set here so future signal sources (cloud-vendor
// add-ons, see DESIGN.md §6.3.1) extend by composition rather than by
// expanding this allowlist.
var autoscalerExhaustedReasons = map[string]struct{}{
	"NotTriggerScaleUp": {},
	"FailedScaling":     {},
}

// splitNodesByGroup walks the cluster's nodes once and partitions them into
// the policy's dedicated and overflow pools. Nodes that do not carry
// nodeGroupLabelKey at all, or carry an unrelated value, are dropped — the
// snapshot only retains nodes the policy actually owns.
func splitNodesByGroup(nodes []*corev1.Node, labelKey string, policy *api.SpillPolicy) (dedicated, overflow []*corev1.Node) {
	if labelKey == "" || policy == nil {
		return nil, nil
	}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		v, ok := n.Labels[labelKey]
		if !ok || v == "" {
			continue
		}
		switch v {
		case policy.DedicatedNodeGroup:
			dedicated = append(dedicated, n)
		case policy.OverflowNodeGroup:
			overflow = append(overflow, n)
		}
	}
	return dedicated, overflow
}

// countPodsOnNodes returns the number of pods scheduled to a node that
// appears in the supplied nodeNames set. Pods with empty Spec.NodeName are
// not yet scheduled and contribute nothing — the count tracks realised
// placements, not bound-but-unscheduled pods.
func countPodsOnNodes(pods []*corev1.Pod, nodeNames map[string]struct{}) int {
	if len(nodeNames) == 0 {
		return 0
	}
	n := 0
	for _, p := range pods {
		if p == nil || p.Spec.NodeName == "" {
			continue
		}
		if _, ok := nodeNames[p.Spec.NodeName]; ok {
			n++
		}
	}
	return n
}

// nodeNameSet is a tiny convenience that turns a slice of nodes into the
// lookup table countPodsOnNodes expects. Lives next to its caller so the
// allocation pattern stays obvious in the Builder.
func nodeNameSet(nodes []*corev1.Node) map[string]struct{} {
	out := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		if n == nil || n.Name == "" {
			continue
		}
		out[n.Name] = struct{}{}
	}
	return out
}

// sumAllocatable returns the per-resource sum of node.Status.Allocatable
// across the supplied nodes. The result is safe to mutate further; nodes
// are not modified.
func sumAllocatable(nodes []*corev1.Node) corev1.ResourceList {
	total := corev1.ResourceList{}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		addResources(total, n.Status.Allocatable)
	}
	return total
}

// autoscalerExhausted scans the supplied events for an exhaustion-reason
// involving any pod from the policy's pod set. It returns true on the first
// match so callers see binary signal without paying for full enumeration on
// busy clusters.
func autoscalerExhausted(events []*corev1.Event, policyPodNames map[string]struct{}) bool {
	if len(policyPodNames) == 0 {
		return false
	}
	for _, e := range events {
		if e == nil {
			continue
		}
		if _, ok := autoscalerExhaustedReasons[e.Reason]; !ok {
			continue
		}
		if e.InvolvedObject.Kind != "Pod" {
			continue
		}
		if _, ok := policyPodNames[e.InvolvedObject.Name]; ok {
			return true
		}
	}
	return false
}

// podNameSet projects a pod slice onto its name set; used to gate the
// autoscaler-event scan so unrelated cluster events are dropped cheaply.
func podNameSet(pods []*corev1.Pod) map[string]struct{} {
	out := make(map[string]struct{}, len(pods))
	for _, p := range pods {
		if p == nil || p.Name == "" {
			continue
		}
		out[p.Name] = struct{}{}
	}
	return out
}
