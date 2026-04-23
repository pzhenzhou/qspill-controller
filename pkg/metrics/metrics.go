package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// QueueUtilization tracks the current utilization of a Volcano queue
	QueueUtilization = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "qspill_queue_utilization",
			Help: "Current utilization of a Volcano queue (0.0-1.0)",
		},
		[]string{"queue", "namespace"},
	)

	// SpillEventsTotal counts spill events
	SpillEventsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "qspill_spill_events_total",
			Help: "Total number of spill events",
		},
		[]string{"source_queue", "target_queue"},
	)

	// ReclaimEventsTotal counts reclaim events
	ReclaimEventsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "qspill_reclaim_events_total",
			Help: "Total number of reclaim events",
		},
		[]string{"source_queue", "target_queue"},
	)

	// ActiveSpills tracks the number of active spills for a source queue
	ActiveSpills = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "qspill_active_spills",
			Help: "Number of currently active spills from a source queue",
		},
		[]string{"source_queue"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		QueueUtilization,
		SpillEventsTotal,
		ReclaimEventsTotal,
		ActiveSpills,
	)
}
