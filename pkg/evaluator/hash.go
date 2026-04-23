package evaluator

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"sort"
	"strings"
	"time"

	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
)

// hashPrefix labels every emitted hash so consumers can tell at a glance
// what algorithm produced the digest. Matches the example in DESIGN.md
// §9.1 ("decision-hash: 'sha256:...'") so dry-run output and Patch action
// output stay byte-identical for the same decision.
const hashPrefix = "sha256:"

// hashDecision computes the canonical fingerprint of a decision's
// observable effect on the Queue. The hash covers:
//
//   - the destination state, so a transition forces a re-apply even if
//     the affinity payload happens to be byte-identical (which it is for
//     a 1-policy / 1-overflow / 1-dedicated configuration: required and
//     preferred just rotate);
//   - the controller-owned NodeGroupAffinity, sorted so map/slice
//     ordering of upstream sources cannot perturb the digest;
//   - the conditionSince stamp, so the "set since=now without changing
//     spec" reconcile in §7.2 still mismatches the prior hash and the
//     action layer persists the new annotation.
//
// Operator-managed Queue fields (capability, weight, guarantee, ...) are
// deliberately excluded — the controller never writes them, so they have
// no business influencing whether the action layer should write again.
func hashDecision(state api.State, spec *schedulingv1beta1.QueueSpec, conditionSince time.Time) string {
	h := sha256.New()
	writeLine(h, "state="+string(state))
	writeAffinity(h, spec)
	writeLine(h, "since="+formatSince(conditionSince))
	return hashPrefix + hex.EncodeToString(h.Sum(nil))
}

// writeAffinity feeds the canonical NodeGroupAffinity payload into the
// digest. nil affinity is encoded as the empty pair so a nil-vs-empty
// difference does not leak into the hash; sorted lists guarantee the
// digest is invariant under upstream reordering.
func writeAffinity(h hash.Hash, spec *schedulingv1beta1.QueueSpec) {
	var required, preferred []string
	if spec != nil && spec.Affinity != nil && spec.Affinity.NodeGroupAffinity != nil {
		nga := spec.Affinity.NodeGroupAffinity
		required = append(required, nga.RequiredDuringSchedulingIgnoredDuringExecution...)
		preferred = append(preferred, nga.PreferredDuringSchedulingIgnoredDuringExecution...)
	}
	sort.Strings(required)
	sort.Strings(preferred)
	writeLine(h, "required="+strings.Join(required, ","))
	writeLine(h, "preferred="+strings.Join(preferred, ","))
}

// formatSince renders the cooldown anchor in a stable, timezone-free form.
// Empty string for the zero value matches the materialize step's choice
// to omit the annotation entirely; this keeps the hash invariant of "no
// timer" identical regardless of which code path produced the zero.
func formatSince(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// writeLine appends a newline-terminated record to the digest. A separator
// matters because the inputs are concatenated; without it,
// (state="A", required="B") and (state="AB", required="") would collide.
func writeLine(h hash.Hash, line string) {
	h.Write([]byte(line))
	h.Write([]byte{'\n'})
}
