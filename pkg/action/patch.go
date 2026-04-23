package action

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	applycfg "volcano.sh/apis/pkg/client/applyconfiguration/scheduling/v1beta1"
	clientset "volcano.sh/apis/pkg/client/clientset/versioned"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
)

// FieldManager is the SSA field-manager name the controller uses for Queue
// applies. It matches the deployment identity in DESIGN.md §10.3.
const FieldManager = "qspill-controller"

// ErrOwnershipConflict is returned (wrapped) when server-side apply fails with
// a conflict another field manager would prevent resolving, or the API layer
// reports HTTP 409. Use [IsOwnershipConflict] in the reconciler workqueue path.
var ErrOwnershipConflict = errors.New("qspill-controller: ownership conflict on queue apply")

// QueueApplier is the Apply surface [PatchAction] needs. Satisfied by
// [clientset.Interface] via SchedulingV1beta1().Queues().
type QueueApplier interface {
	Apply(ctx context.Context, queue *applycfg.QueueApplyConfiguration, opts metav1.ApplyOptions) (result *schedulingv1beta1.Queue, err error)
}

// PatchAction performs server-side apply for a Queue, owning only
// spec.affinity.nodeGroupAffinity and spill.example.com annotations.
type PatchAction struct {
	queues QueueApplier
}

// NewPatch constructs a PatchAction backed by the Volcano clientset.
func NewPatch(cs clientset.Interface) *PatchAction {
	return &PatchAction{queues: cs.SchedulingV1beta1().Queues()}
}

// NewPatchWithApplier wires a custom applier (used in unit tests).
func NewPatchWithApplier(q QueueApplier) *PatchAction {
	return &PatchAction{queues: q}
}

func (p *PatchAction) Name() string { return string(api.ActionModePatch) }

// Apply issues a forced SSA apply so the controller re-takes fields it owns
// when external actors have touched unrelated parts of the Queue.
func (p *PatchAction) Apply(ctx context.Context, d api.Decision) error {
	policyName := ""
	queueName := ""
	if d.Policy != nil {
		policyName = d.Policy.Name
		queueName = d.Policy.QueueName
	}
	logger.Info("applying patch action (server-side apply)",
		"action", string(api.ActionModePatch),
		"policy", policyName,
		"queue", queueName,
		"fieldManager", FieldManager,
		"from", string(d.From),
		"to", string(d.To),
		"trigger", string(d.Trigger),
		"hash", d.Hash,
	)

	if d.DesiredQueue == nil || d.Policy == nil {
		err := fmt.Errorf("PatchAction: missing DesiredQueue or Policy")
		logger.Error(err, "patch action precondition failed",
			"hasDesiredQueue", d.DesiredQueue != nil,
			"hasPolicy", d.Policy != nil)
		return err
	}
	cfg := queueApplyConfiguration(&d)
	_, err := p.queues.Apply(ctx, cfg, metav1.ApplyOptions{
		FieldManager: FieldManager,
		Force:        true,
	})
	mapped := MapApplyError(err)
	if mapped != nil {
		logger.Error(mapped, "queue server-side apply failed",
			"policy", policyName,
			"queue", queueName,
			"ownershipConflict", IsOwnershipConflict(mapped))
		return mapped
	}
	return nil
}

func queueApplyConfiguration(d *api.Decision) *applycfg.QueueApplyConfiguration {
	q := d.DesiredQueue
	meta := q.ObjectMeta
	name := meta.Name
	if name == "" {
		name = d.Policy.QueueName
	}

	b := applycfg.Queue(name).
		WithAnnotations(spillAnnotations(meta.Annotations))

	if ns := meta.Namespace; ns != "" {
		b.WithNamespace(ns)
	}

	spec := applycfg.QueueSpec().WithAffinity(affinityApplyConfig(q.Spec.Affinity))
	return b.WithSpec(spec)
}

func spillAnnotations(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	out := make(map[string]string, 3)
	for _, k := range []string{
		api.AnnotationState,
		api.AnnotationConditionSince,
		api.AnnotationDecisionHash,
	} {
		if v, ok := src[k]; ok {
			out[k] = v
		}
	}
	return out
}

func affinityApplyConfig(a *schedulingv1beta1.Affinity) *applycfg.AffinityApplyConfiguration {
	if a == nil {
		return applycfg.Affinity()
	}
	return applycfg.Affinity().WithNodeGroupAffinity(nodeGroupAffinityApply(a.NodeGroupAffinity))
}

func nodeGroupAffinityApply(ng *schedulingv1beta1.NodeGroupAffinity) *applycfg.NodeGroupAffinityApplyConfiguration {
	if ng == nil {
		return applycfg.NodeGroupAffinity()
	}
	b := applycfg.NodeGroupAffinity()
	if len(ng.RequiredDuringSchedulingIgnoredDuringExecution) > 0 {
		b.WithRequiredDuringSchedulingIgnoredDuringExecution(ng.RequiredDuringSchedulingIgnoredDuringExecution...)
	}
	if len(ng.PreferredDuringSchedulingIgnoredDuringExecution) > 0 {
		b.WithPreferredDuringSchedulingIgnoredDuringExecution(ng.PreferredDuringSchedulingIgnoredDuringExecution...)
	}
	return b
}

// MapApplyError wraps API conflicts for [IsOwnershipConflict]. Pass through
// all other errors unchanged.
func MapApplyError(err error) error {
	if err == nil {
		return nil
	}
	if apierrors.IsConflict(err) {
		return fmt.Errorf("%w: %w", ErrOwnershipConflict, err)
	}
	return err
}

// IsOwnershipConflict reports whether err wraps [ErrOwnershipConflict].
func IsOwnershipConflict(err error) bool {
	return errors.Is(err, ErrOwnershipConflict)
}
