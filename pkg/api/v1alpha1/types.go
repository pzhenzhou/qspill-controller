package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// QSpillPolicySpec defines the desired state of QSpillPolicy
type QSpillPolicySpec struct {
	// SourceQueue is the Volcano queue name this policy watches
	SourceQueue string `json:"sourceQueue"`

	// SpillTrigger defines when to activate spilling
	SpillTrigger SpillTrigger `json:"spillTrigger"`

	// SpillTargets lists queues that can receive spilled resources
	SpillTargets []SpillTarget `json:"spillTargets"`

	// ReclaimPolicy defines how to reclaim spilled resources
	// +optional
	ReclaimPolicy *ReclaimPolicy `json:"reclaimPolicy,omitempty"`
}

// SpillTrigger defines the condition that triggers a spill
type SpillTrigger struct {
	// UtilizationThreshold triggers spill when source queue utilization exceeds this value (0.0-1.0)
	// +kubebuilder:validation:Pattern=`^(0(\.\d+)?|1(\.0+)?)$`
	UtilizationThreshold string `json:"utilizationThreshold"`

	// EvaluationPeriod is how often to evaluate the trigger condition
	// +optional
	// +kubebuilder:default="60s"
	EvaluationPeriod metav1.Duration `json:"evaluationPeriod,omitempty"`
}

// SpillTarget defines a target queue for spilled resources
type SpillTarget struct {
	// QueueName is the name of the Volcano queue to spill into
	QueueName string `json:"queueName"`

	// MaxSpillCapacity is the maximum capacity that can be spilled to this queue
	// +optional
	MaxSpillCapacity corev1.ResourceList `json:"maxSpillCapacity,omitempty"`

	// Priority determines spill order (higher = preferred first)
	// +optional
	// +kubebuilder:default=50
	Priority int32 `json:"priority,omitempty"`

	// Namespace is the namespace where the target queue exists.
	// Defaults to the same namespace as the QSpillPolicy.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ReclaimPolicy defines how to reclaim spilled resources
type ReclaimPolicy struct {
	// GracePeriod is how long to wait before reclaiming spilled resources
	// +optional
	// +kubebuilder:default="5m"
	GracePeriod metav1.Duration `json:"gracePeriod,omitempty"`

	// Strategy is the reclaim strategy: Immediate or Graceful
	// +optional
	// +kubebuilder:validation:Enum=Immediate;Graceful
	// +kubebuilder:default=Graceful
	Strategy ReclaimStrategy `json:"strategy,omitempty"`
}

// ReclaimStrategy defines the strategy for reclaiming spilled resources
type ReclaimStrategy string

const (
	ReclaimStrategyImmediate ReclaimStrategy = "Immediate"
	ReclaimStrategyGraceful  ReclaimStrategy = "Graceful"
)

// QSpillPolicyPhase represents the phase of a QSpillPolicy
type QSpillPolicyPhase string

const (
	QSpillPolicyPhaseActive   QSpillPolicyPhase = "Active"
	QSpillPolicyPhaseInactive QSpillPolicyPhase = "Inactive"
	QSpillPolicyPhaseSpilling QSpillPolicyPhase = "Spilling"
)

// QSpillPolicyStatus defines the observed state of QSpillPolicy
type QSpillPolicyStatus struct {
	// Phase is the current phase of the policy
	// +optional
	Phase QSpillPolicyPhase `json:"phase,omitempty"`

	// ObservedGeneration is the most recent generation observed
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// CurrentSpillTargets lists queues currently receiving spilled resources
	// +optional
	CurrentSpillTargets []ActiveSpillTarget `json:"currentSpillTargets,omitempty"`

	// SourceQueueUtilization is the last observed utilization of the source queue
	// +optional
	SourceQueueUtilization string `json:"sourceQueueUtilization,omitempty"`

	// Conditions holds the conditions for the QSpillPolicy
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastTransitionTime is when the phase last changed
	// +optional
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`
}

// ActiveSpillTarget represents a queue currently receiving spilled resources
type ActiveSpillTarget struct {
	QueueName       string              `json:"queueName"`
	Namespace       string              `json:"namespace"`
	SpilledCapacity corev1.ResourceList `json:"spilledCapacity,omitempty"`
	SpillStartTime  metav1.Time         `json:"spillStartTime"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=qsp
// +kubebuilder:printcolumn:name="Source Queue",type=string,JSONPath=`.spec.sourceQueue`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Utilization",type=string,JSONPath=`.status.sourceQueueUtilization`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// QSpillPolicy is the Schema for the qspillpolicies API
type QSpillPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   QSpillPolicySpec   `json:"spec,omitempty"`
	Status QSpillPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// QSpillPolicyList contains a list of QSpillPolicy
type QSpillPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []QSpillPolicy `json:"items"`
}
