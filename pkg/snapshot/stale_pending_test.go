package snapshot

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// podPending builds a Pod with a single PodScheduled condition stamped at
// the supplied transition time and the supplied status. Tests use it to
// dial in unschedulable durations precisely against the synthetic clock.
func podPending(name string, status corev1.ConditionStatus, since time.Time) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}}
	p.Status.Conditions = []corev1.PodCondition{{
		Type:               corev1.PodScheduled,
		Status:             status,
		LastTransitionTime: metav1.NewTime(since),
	}}
	return p
}

func TestScanStalePendingHappyPath(t *testing.T) {
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	threshold := 5 * time.Minute
	pods := []*corev1.Pod{
		podPending("recent", corev1.ConditionFalse, now.Add(-1*time.Minute)),
		podPending("breaching-1", corev1.ConditionFalse, now.Add(-7*time.Minute)),
		podPending("breaching-2", corev1.ConditionFalse, now.Add(-12*time.Minute)),
		podPending("scheduled", corev1.ConditionTrue, now.Add(-30*time.Minute)),
	}

	stalest, breaching := scanStalePending(pods, now, threshold)

	if want := 12 * time.Minute; stalest != want {
		t.Errorf("stalest = %s, want %s", stalest, want)
	}
	if breaching != 2 {
		t.Errorf("breaching = %d, want 2", breaching)
	}
}

func TestScanStalePendingNoUnschedulablePods(t *testing.T) {
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	pods := []*corev1.Pod{podPending("ok", corev1.ConditionTrue, now.Add(-time.Hour))}

	stalest, breaching := scanStalePending(pods, now, time.Minute)
	if stalest != 0 {
		t.Errorf("stalest = %s, want 0", stalest)
	}
	if breaching != 0 {
		t.Errorf("breaching = %d, want 0", breaching)
	}
}

func TestScanStalePendingMissingTransitionTimeYieldsZeroDuration(t *testing.T) {
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "no-time"}}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse}}

	stalest, breaching := scanStalePending([]*corev1.Pod{p}, now, time.Second)
	if stalest != 0 {
		t.Errorf("stalest = %s, want 0", stalest)
	}
	if breaching != 0 {
		t.Errorf("breaching = %d, want 0 (zero duration cannot exceed threshold)", breaching)
	}
}

func TestScanStalePendingClockSkewClampsToZero(t *testing.T) {
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	future := podPending("future", corev1.ConditionFalse, now.Add(time.Hour))

	stalest, breaching := scanStalePending([]*corev1.Pod{future}, now, time.Second)
	if stalest != 0 {
		t.Errorf("stalest = %s, want 0 (negative durations clamp)", stalest)
	}
	if breaching != 0 {
		t.Errorf("breaching = %d, want 0", breaching)
	}
}

func TestScanStalePendingIgnoresNilPods(t *testing.T) {
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	pods := []*corev1.Pod{nil, podPending("p", corev1.ConditionFalse, now.Add(-time.Minute))}

	stalest, breaching := scanStalePending(pods, now, time.Second)
	if stalest != time.Minute {
		t.Errorf("stalest = %s, want 1m", stalest)
	}
	if breaching != 1 {
		t.Errorf("breaching = %d, want 1", breaching)
	}
}

// TestUnschedulableForReportsConditionPresence verifies the (duration, ok)
// contract: ok==false only when the PodScheduled condition is absent or
// already True; an unschedulable pod with zero duration still reports ok.
func TestUnschedulableForReportsConditionPresence(t *testing.T) {
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		pod     *corev1.Pod
		wantDur time.Duration
		wantOK  bool
	}{
		{
			name:   "no_condition",
			pod:    &corev1.Pod{},
			wantOK: false,
		},
		{
			name:   "scheduled_true",
			pod:    podPending("p", corev1.ConditionTrue, now.Add(-time.Hour)),
			wantOK: false,
		},
		{
			name:    "unschedulable_with_time",
			pod:     podPending("p", corev1.ConditionFalse, now.Add(-3*time.Minute)),
			wantDur: 3 * time.Minute,
			wantOK:  true,
		},
		{
			name: "unschedulable_no_time",
			pod: func() *corev1.Pod {
				p := &corev1.Pod{}
				p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse}}
				return p
			}(),
			wantOK: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotDur, gotOK := unschedulableFor(tc.pod, now)
			if gotOK != tc.wantOK {
				t.Errorf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			if gotDur != tc.wantDur {
				t.Errorf("dur = %s, want %s", gotDur, tc.wantDur)
			}
		})
	}
}
