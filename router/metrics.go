package main

import "github.com/prometheus/client_golang/prometheus"

var (
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "thinking_router_requests_total",
			Help: "Total chat completion requests routed, by classification.",
		},
		[]string{"classification"},
	)

	classifyErrorsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "thinking_router_classify_errors_total",
			Help: "Total classification failures that triggered fail-open (thinking=true).",
		},
	)

	classifyDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "thinking_router_classify_duration_seconds",
			Help:    "Duration of the vLLM classification call.",
			Buckets: prometheus.DefBuckets,
		},
	)

	requestDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "thinking_router_request_duration_seconds",
			Help:    "End-to-end duration of proxied chat completion requests, by classification.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"classification"},
	)
)

func init() {
	prometheus.MustRegister(
		requestsTotal,
		classifyErrorsTotal,
		classifyDurationSeconds,
		requestDurationSeconds,
	)
}
