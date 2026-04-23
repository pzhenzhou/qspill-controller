package snapshot

import (
	"time"

	corev1 "k8s.io/api/core/v1"
)

// scanStalePending computes the two stale-pending signals from the policy's
// pods: the longest PodScheduled=False duration observed and the count of
// pods whose duration already exceeds the threshold. Pods without the
// PodScheduled condition or with PodScheduled=True contribute nothing.
//
// The scan reads condition.LastTransitionTime, not the wall clock at scan
// time, so the returned durations are stable across goroutine scheduling.
// now is taken from Snapshot.ObservedAt; passing the wall clock from
// Builder keeps the snapshot the only place a clock is read.
func scanStalePending(pods []*corev1.Pod, now time.Time, threshold time.Duration) (stalest time.Duration, breaching int) {
	for _, p := range pods {
		if p == nil {
			continue
		}
		dur, ok := unschedulableFor(p, now)
		if !ok {
			continue
		}
		if dur > stalest {
			stalest = dur
		}
		if dur > threshold {
			breaching++
		}
	}
	return stalest, breaching
}

// unschedulableFor returns how long the pod has carried PodScheduled=False
// against the supplied clock, or (0, false) when the condition is absent or
// the pod is currently schedulable. A pod that has just become unschedulable
// returns 0 with ok=true so the snapshot still records the condition.
func unschedulableFor(p *corev1.Pod, now time.Time) (time.Duration, bool) {
	for _, c := range p.Status.Conditions {
		if c.Type != corev1.PodScheduled || c.Status != corev1.ConditionFalse {
			continue
		}
		if c.LastTransitionTime.IsZero() {
			return 0, true
		}
		d := now.Sub(c.LastTransitionTime.Time)
		if d < 0 {
			return 0, true
		}
		return d, true
	}
	return 0, false
}
