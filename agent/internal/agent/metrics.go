package agent

import "github.com/prometheus/client_golang/prometheus"

var (
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "agent_graph_requests_total",
			Help: "Total requests handled by the agent graph, by entry node.",
		},
		[]string{"entry"},
	)

	nodeErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "agent_graph_node_errors_total",
			Help: "Total node execution errors, by node ID and kind.",
		},
		[]string{"node", "kind"},
	)

	requestDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "agent_graph_request_duration_seconds",
			Help:    "End-to-end duration of requests through the agent graph.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"entry"},
	)

	toolCallsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "agent_graph_tool_calls_total",
			Help: "Total tool invocations, by tool name.",
		},
		[]string{"tool"},
	)
)

func init() {
	prometheus.MustRegister(
		requestsTotal,
		nodeErrorsTotal,
		requestDurationSeconds,
		toolCallsTotal,
	)
}
