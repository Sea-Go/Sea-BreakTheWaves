package metrics

import (
	"os"
	"sea/config"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	// token 用量（总量累加）
	GenRecAgentTotalTokensMetric = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "total_tokens",
		Help:      "GenRec Agent 的 token 总消耗。",
	})

	// E2E 延迟（秒）。建议按 status 维度看分布。
	GenRecAgentE2ELatencySecondsMetric = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "e2e_latency_seconds",
		Help:      "GenRec Agent 端到端延迟（秒）。",
		Buckets:   []float64{0.02, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20},
	}, []string{"agent", "surface", "status"}) // ok|error|degraded|fallback

	// 请求总数（按 agent/surface/status 分桶）
	GenRecAgentRequestsTotalMetric = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "requests_total",
		Help:      "GenRec Agent 请求总数（按 agent/surface/status 分桶）。",
	}, []string{"agent", "surface", "status"})

	// 路由选择次数（按 agent/surface/route）
	GenRecAgentRouteDecisionsTotalMetric = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "route_decisions_total",
		Help:      "GenRec Agent 路由决策次数。",
	}, []string{"agent", "surface", "route"})

	// 检索次数（按 agent/surface）
	GenRecAgentRetrievalRequestsTotalMetric = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "retrieval_requests_total",
		Help:      "GenRec Agent 触发检索次数。",
	}, []string{"agent", "surface"})

	// 检索返回文档数分布（按 agent/surface）
	GenRecAgentRetrievalReturnedDocsMetric = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "retrieval_returned_docs",
		Help:      "每次检索返回文档数量分布。",
		Buckets:   []float64{0, 1, 2, 3, 5, 8, 13, 21, 34, 55, 89},
	}, []string{"agent", "surface"})

	// 验证结果计数（比如 pass|fail|skip）
	GenRecAgentValidationTotalMetric = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "validation_total",
		Help:      "验证结果计数。",
	}, []string{"result"})

	// 可观测性自身健康（自检）
	GenRecAgentTracesTotalMetric = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "traces_total",
		Help:      "服务输出的 trace/span 总数（观测自检）。",
	})

	// tool error/timeout
	GenRecAgentToolCallsTotalMetric = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "genrec",
		Subsystem: "agent",
		Name:      "tool_calls_total",
		Help:      "工具调用次数（按工具名、状态分桶）。",
	}, []string{"tool", "status"})

	// guardrail block
	GuardrailDecisionsTotalMetric = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "guardrail",
		Subsystem: "",
		Name:      "decisions_total",
		Help:      "护栏（guardrail）决策次数。",
	}, []string{"decision"})
)

// InitMetrics 在 main 中调用，用于显式注册项目内的业务指标。
// 参数 signal/cfg 预留给未来“优雅退出/动态配置”场景；当前实现不强依赖。
func InitMetrics(_ chan os.Signal, _ *config.Config) {
	// 多次调用可能导致重复注册，这里使用 MustRegister 并假设只初始化一次。
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
	)
}
