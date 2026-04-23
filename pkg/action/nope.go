package action

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"sigs.k8s.io/yaml"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/common"
)

// logger is the per-module logr.Logger for the action layer. Both Nope and
// Patch share this logger so dry-run and production lines surface under the
// same `module=action` filter.
var logger = common.NewLogger("action")

// nopeName is the value Name() returns and the label that ends up in
// metrics and events. Defined as a constant so misspellings cannot drift
// the label between implementation and observability code.
const nopeName = "Nope"

// NopeAction is the dry-run Action: it writes a self-contained YAML
// document describing the Decision to a configurable sink and never
// touches the cluster. This is the project's safe-rollout default — operators
// run with NopeAction for N days, diff the emitted YAML against
// expectations, then promote to the production Action.
//
// Concurrency: Apply serialises writes via a mutex so concurrent reconciles
// can never interleave half-formed YAML documents on the sink.
type NopeAction struct {
	// Out receives the YAML output. Defaults to os.Stdout when nil so the
	// zero value is usable; tests inject an in-memory buffer to capture
	// the document for golden-file comparison.
	Out io.Writer

	mu sync.Mutex
}

// NewNope returns a NopeAction writing to the supplied sink. Passing nil
// selects os.Stdout so callers that just want stdout do not have to
// import the os package themselves.
func NewNope(out io.Writer) *NopeAction {
	if out == nil {
		out = os.Stdout
	}
	return &NopeAction{Out: out}
}

// Name implements Action.Name. The string is stable across releases — it
// appears in operator-facing metrics and must not change without an
// observability migration.
func (n *NopeAction) Name() string { return nopeName }

// Apply implements Action.Apply by marshalling the Decision into the
// DESIGN.md §9.1 self-contained YAML envelope and writing it to Out.
//
// The output is *fully self-contained*: an operator who copies the YAML
// can replay the decision context (inputs + desired Queue) without
// re-running the controller. Crucially, ObservedAt comes off the Decision
// (which mirrors Snapshot.ObservedAt), not time.Now() — re-emitting the
// same Decision must produce a byte-identical document so golden-file
// tests stay deterministic.
func (n *NopeAction) Apply(ctx context.Context, d api.Decision) error {
	policyName := ""
	if d.Policy != nil {
		policyName = d.Policy.Name
	}
	logger.Info("applying nope action (dry-run yaml)",
		"action", nopeName,
		"policy", policyName,
		"from", string(d.From),
		"to", string(d.To),
		"trigger", string(d.Trigger),
		"hash", d.Hash,
	)

	out := n.sink()
	payload := newPayload(d)
	body, err := yaml.Marshal(payload)
	if err != nil {
		wrapped := fmt.Errorf("nope: marshal decision: %w", err)
		logger.Error(wrapped, "marshal decision yaml failed",
			"action", nopeName, "policy", policyName)
		return wrapped
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if _, err := fmt.Fprintf(out, "---\n%s", body); err != nil {
		wrapped := fmt.Errorf("nope: write decision: %w", err)
		logger.Error(wrapped, "write decision to sink failed",
			"action", nopeName, "policy", policyName, "bytes", len(body))
		return wrapped
	}
	return nil
}

// sink resolves the writer for this Apply invocation. Reading the writer
// once at the top of Apply (rather than each fmt call) keeps the locking
// scope minimal and makes the mutex's contract clear: it serialises
// concurrent Apply calls, not concurrent reconfiguration of Out.
func (n *NopeAction) sink() io.Writer {
	if n.Out == nil {
		return os.Stdout
	}
	return n.Out
}

// nopePayload is the YAML envelope. Field names map to the design's
// example through json tags (sigs.k8s.io/yaml drives off json tags); the
// envelope deliberately does not embed api.Decision wholesale because the
// public Decision type contains pointers and unexported fields whose YAML
// shape is not part of the contract.
type nopePayload struct {
	ObservedAt time.Time                `json:"observedAt"`
	Policy     string                   `json:"policy"`
	From       api.State                `json:"from"`
	To         api.State                `json:"to"`
	Trigger    api.Trigger              `json:"trigger"`
	Reason     string                   `json:"reason"`
	Hash       string                   `json:"hash"`
	Inputs     nopeInputs               `json:"inputs"`
	Queue      *schedulingv1beta1.Queue `json:"queue"`
}

// nopeInputs mirrors api.DecisionInputs but with explicit YAML-friendly
// types: durations render as Go-style strings (e.g. "5m12s") and resource
// quantities render through their MarshalJSON so units stay canonical
// (e.g. "1500m" rather than the underlying inf.Dec representation).
type nopeInputs struct {
	DemandCPU            string `json:"demandCpu"`
	DemandMemory         string `json:"demandMemory"`
	DemandEstimatedPGs   int    `json:"demandEstimatedPGs"`
	MaxCPU               string `json:"maxCpu"`
	MaxMemory            string `json:"maxMemory"`
	OverflowPodsOfPolicy int    `json:"overflowPodsOfPolicy"`
	AutoscalerExhausted  bool   `json:"autoscalerExhausted"`
	StalestPendingFor    string `json:"stalestPendingFor"`
	StalePendingPods     int    `json:"stalePendingPods"`
	TimeInState          string `json:"timeInState"`
}

// newPayload assembles the YAML envelope from a Decision. The conversion
// is intentionally explicit: every field is named so a future change to
// either api.Decision or the YAML contract has exactly one site to update,
// and the deterministic ordering of struct fields drives the
// deterministic ordering of YAML keys.
func newPayload(d api.Decision) nopePayload {
	policyName := ""
	if d.Policy != nil {
		policyName = d.Policy.Name
	}
	return nopePayload{
		ObservedAt: d.ObservedAt.UTC(),
		Policy:     policyName,
		From:       d.From,
		To:         d.To,
		Trigger:    d.Trigger,
		Reason:     d.Reason,
		Hash:       d.Hash,
		Inputs: nopeInputs{
			DemandCPU:            d.Inputs.DemandCPU.String(),
			DemandMemory:         d.Inputs.DemandMemory.String(),
			DemandEstimatedPGs:   d.Inputs.DemandEstimatedPGs,
			MaxCPU:               d.Inputs.MaxCPU.String(),
			MaxMemory:            d.Inputs.MaxMemory.String(),
			OverflowPodsOfPolicy: d.Inputs.OverflowPodsOfPolicy,
			AutoscalerExhausted:  d.Inputs.AutoscalerExhausted,
			StalestPendingFor:    d.Inputs.StalestPendingFor.String(),
			StalePendingPods:     d.Inputs.StalePendingPods,
			TimeInState:          d.Inputs.TimeInState.String(),
		},
		Queue: d.DesiredQueue,
	}
}

// Compile-time assertion that NopeAction satisfies Action. Catches the
// case where a future refactor renames a method or changes the signature
// without touching this file.
var _ Action = (*NopeAction)(nil)
