package snapshot

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
)

const supplyTestLabelKey = "volcano.sh/nodegroup-name"

func nodeWithGroup(name, group string, allocatable corev1.ResourceList) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{supplyTestLabelKey: group},
		},
		Status: corev1.NodeStatus{Allocatable: allocatable},
	}
}

func nodeWithoutLabel(name string, allocatable corev1.ResourceList) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Allocatable: allocatable},
	}
}

func TestSplitNodesByGroupSplitsCleanly(t *testing.T) {
	policy := &api.SpillPolicy{DedicatedNodeGroup: "ng2", OverflowNodeGroup: "ng1"}
	nodes := []*corev1.Node{
		nodeWithGroup("ded-1", "ng2", nil),
		nodeWithGroup("ovr-1", "ng1", nil),
		nodeWithGroup("ovr-2", "ng1", nil),
		nodeWithGroup("other", "ng3", nil),
		nodeWithoutLabel("unlabeled", nil),
	}

	dedicated, overflow := splitNodesByGroup(nodes, supplyTestLabelKey, policy)

	if len(dedicated) != 1 || dedicated[0].Name != "ded-1" {
		t.Errorf("dedicated = %+v, want one node ded-1", dedicated)
	}
	if len(overflow) != 2 {
		t.Errorf("overflow = %+v, want two nodes", overflow)
	}
}

func TestSplitNodesByGroupReturnsNilOnEmptyInputs(t *testing.T) {
	if d, o := splitNodesByGroup(nil, "", nil); d != nil || o != nil {
		t.Errorf("expected nil/nil for empty inputs, got %v / %v", d, o)
	}
}

func TestCountPodsOnNodes(t *testing.T) {
	nodes := map[string]struct{}{"ded-1": {}, "ded-2": {}}
	pods := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "p1"}, Spec: corev1.PodSpec{NodeName: "ded-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "p2"}, Spec: corev1.PodSpec{NodeName: "ded-2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "p3"}, Spec: corev1.PodSpec{NodeName: "elsewhere"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "unbound"}},
	}
	if got := countPodsOnNodes(pods, nodes); got != 2 {
		t.Errorf("count = %d, want 2", got)
	}
}

func TestCountPodsOnNodesEmptyNodeSetReturnsZero(t *testing.T) {
	pods := []*corev1.Pod{{Spec: corev1.PodSpec{NodeName: "any"}}}
	if got := countPodsOnNodes(pods, map[string]struct{}{}); got != 0 {
		t.Errorf("count = %d, want 0 with empty node set", got)
	}
}

func TestSumAllocatableAccumulatesAcrossNodes(t *testing.T) {
	nodes := []*corev1.Node{
		nodeWithGroup("a", "ng2", resourceList("cpu", "4", "memory", "8Gi")),
		nodeWithGroup("b", "ng2", resourceList("cpu", "2", "memory", "4Gi")),
	}
	got := sumAllocatable(nodes)
	if cpu := got[corev1.ResourceCPU]; cpu.Cmp(resource.MustParse("6")) != 0 {
		t.Errorf("cpu = %s, want 6", cpu.String())
	}
	if mem := got[corev1.ResourceMemory]; mem.Cmp(resource.MustParse("12Gi")) != 0 {
		t.Errorf("memory = %s, want 12Gi", mem.String())
	}
}

func TestAutoscalerExhaustedFiresOnPolicyPod(t *testing.T) {
	policyPods := map[string]struct{}{"p-mine": {}}
	events := []*corev1.Event{
		{Reason: "NotTriggerScaleUp", InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p-mine"}},
		{Reason: "Scheduled", InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p-mine"}},
	}
	if !autoscalerExhausted(events, policyPods) {
		t.Error("expected exhaustion detected for matching pod")
	}
}

func TestAutoscalerExhaustedIgnoresOtherPods(t *testing.T) {
	policyPods := map[string]struct{}{"p-mine": {}}
	events := []*corev1.Event{
		{Reason: "NotTriggerScaleUp", InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "stranger"}},
	}
	if autoscalerExhausted(events, policyPods) {
		t.Error("expected no detection for non-policy pod")
	}
}

func TestAutoscalerExhaustedIgnoresUnrelatedReasons(t *testing.T) {
	policyPods := map[string]struct{}{"p-mine": {}}
	events := []*corev1.Event{
		{Reason: "FailedMount", InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p-mine"}},
	}
	if autoscalerExhausted(events, policyPods) {
		t.Error("expected no detection for unrelated reason")
	}
}

func TestAutoscalerExhaustedFiresOnFailedScaling(t *testing.T) {
	policyPods := map[string]struct{}{"p-mine": {}}
	events := []*corev1.Event{
		{Reason: "FailedScaling", InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p-mine"}},
	}
	if !autoscalerExhausted(events, policyPods) {
		t.Error("expected exhaustion detected for FailedScaling")
	}
}

func TestAutoscalerExhaustedNoOpOnEmptyPolicyPods(t *testing.T) {
	events := []*corev1.Event{
		{Reason: "NotTriggerScaleUp", InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "anything"}},
	}
	if autoscalerExhausted(events, map[string]struct{}{}) {
		t.Error("expected no detection when policy pod set is empty")
	}
}
