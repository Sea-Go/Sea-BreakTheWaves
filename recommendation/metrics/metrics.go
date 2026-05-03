package metrics

import (
	"os"

	"sea/config"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	GenRecAgentTotalTokensMetric = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "total_tokens",
		Help:      "Total token usage of the recommendation agent.",
	})

	GenRecAgentE2ELatencySecondsMetric = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "e2e_latency_seconds",
		Help:      "End-to-end latency of the recommendation agent.",
		Buckets:   []float64{0.02, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20},
	}, []string{"agent", "surface", "status"})

	GenRecAgentRequestsTotalMetric = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "requests_total",
		Help:      "Total requests handled by the recommendation agent.",
	}, []string{"agent", "surface", "status"})

	GenRecAgentRouteDecisionsTotalMetric = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "route_decisions_total",
		Help:      "Route decisions made by the recommendation agent.",
	}, []string{"agent", "surface", "route"})

	GenRecAgentRetrievalRequestsTotalMetric = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "retrieval_requests_total",
		Help:      "Retrieval requests issued by the recommendation agent.",
	}, []string{"agent", "surface"})

	GenRecAgentRetrievalReturnedDocsMetric = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "retrieval_returned_docs",
		Help:      "Distribution of retrieved document counts.",
		Buckets:   []float64{0, 1, 2, 3, 5, 8, 13, 21, 34, 55, 89},
	}, []string{"agent", "surface"})

	GenRecAgentValidationTotalMetric = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "validation_total",
		Help:      "Validation outcomes of the recommendation agent.",
	}, []string{"result"})

	GenRecAgentTracesTotalMetric = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "traces_total",
		Help:      "Total traces emitted by the recommendation service.",
	})

	GenRecAgentToolCallsTotalMetric = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "tool_calls_total",
		Help:      "Tool call count grouped by tool and status.",
	}, []string{"tool", "status"})

	GuardrailDecisionsTotalMetric = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "guardrail",
		Name:      "decisions_total",
		Help:      "Guardrail decisions count.",
	}, []string{"decision"})

	ArticleSyncEventsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "article_sync",
		Name:      "events_total",
		Help:      "Article sync events processed by op, status and source.",
	}, []string{"op", "status", "source"})

	ArticleSyncRetryTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "article_sync",
		Name:      "retry_total",
		Help:      "Article sync retry enqueue count by op and source.",
	}, []string{"op", "source"})

	PoolRefillEnqueueTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "pool_refill",
		Name:      "enqueue_total",
		Help:      "Pool refill enqueue results grouped by result and pool type.",
	}, []string{"result", "pool_type"})

	PoolRefillRunsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "pool_refill",
		Name:      "runs_total",
		Help:      "Pool refill run outcomes grouped by result and pool type.",
	}, []string{"result", "pool_type"})

	PoolRefillInflight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "genrec",
		Subsystem: "pool_refill",
		Name:      "inflight",
		Help:      "Current number of inflight pool refill runs.",
	}, []string{"pool_type"})

	PoolRefillStageDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "genrec",
		Subsystem: "pool_refill",
		Name:      "stage_duration_seconds",
		Help:      "Stage latency of pool refill runs.",
		Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 40},
	}, []string{"stage"})

	PoolRefillItemsInserted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "pool_refill",
		Name:      "items_inserted",
		Help:      "Total number of pool items inserted by pool type.",
	}, []string{"pool_type"})
)

func InitMetrics(_ chan os.Signal, _ *config.Config) {
	prometheus.MustRegister(
		GenRecAgentTotalTokensMetric,
		GenRecAgentE2ELatencySecondsMetric,
		GenRecAgentRequestsTotalMetric,
		GenRecAgentRouteDecisionsTotalMetric,
		GenRecAgentRetrievalRequestsTotalMetric,
		GenRecAgentRetrievalReturnedDocsMetric,
		GenRecAgentValidationTotalMetric,
		GenRecAgentTracesTotalMetric,
		GenRecAgentToolCallsTotalMetric,
		GuardrailDecisionsTotalMetric,
		ArticleSyncEventsTotal,
		ArticleSyncRetryTotal,
		PoolRefillEnqueueTotal,
		PoolRefillRunsTotal,
		PoolRefillInflight,
		PoolRefillStageDurationSeconds,
		PoolRefillItemsInserted,
	)
}
