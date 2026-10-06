package event

import "github.com/prometheus/client_golang/prometheus"

// DroppedCounter exposes the dropped-events counter for a reason to tests.
func DroppedCounter(reason string) prometheus.Counter {
	return droppedEvents.WithLabelValues(reason)
}
