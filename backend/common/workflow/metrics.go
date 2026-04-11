package workflow

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	workflowStarted = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "workflow_started_total",
			Help: "Total number of workflows started",
		},
		[]string{"filter_rule"},
	)
)
