package recommendationv2

import (
	"sync"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/metricx"
)

type ObservationSummary struct {
	Requests                 int64             `json:"requests"`
	EventsAccepted           int64             `json:"events_accepted"`
	EventsRejected           int64             `json:"events_rejected"`
	HookFailures             int64             `json:"hook_failures"`
	ActivityAnalyzed         int64             `json:"activity_analyzed"`
	ActivityAnalysisFailures int64             `json:"activity_analysis_failures"`
	ActivityAgentTriggers    int64             `json:"activity_agent_triggers"`
	ByPath                   map[string]int64  `json:"by_path"`
	LastTraceID              string            `json:"last_trace_id,omitempty"`
	LastRequestID            string            `json:"last_request_id,omitempty"`
	TotalCost                CostReport        `json:"total_cost"`
	AverageLatencyMS         float64           `json:"average_latency_ms"`
	Labels                   map[string]string `json:"labels,omitempty"`
}

type observationCollector struct {
	mu                       sync.Mutex
	requests                 int64
	eventsAccepted           int64
	eventsRejected           int64
	hookFailures             int64
	activityAnalyzed         int64
	activityAnalysisFailures int64
	activityAgentTriggers    int64
	byPath                   map[string]int64
	lastTraceID              string
	lastRequestID            string
	totalCost                CostReport
	totalLatencyMS           int64
	traces                   []TraceRecord
	traceLimit               int
}

func newObservationCollector() *observationCollector {
	return &observationCollector{byPath: map[string]int64{}, traceLimit: 256}
}

func (c *observationCollector) recordRecommendation(resp RecommendResponse) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests++
	c.byPath[resp.PathTaken]++
	c.lastTraceID = resp.TraceID
	c.lastRequestID = resp.RequestID
	c.totalLatencyMS += resp.Metrics.LatencyMillis
	c.totalCost.TokensIn += resp.Cost.TokensIn
	c.totalCost.TokensOut += resp.Cost.TokensOut
	c.totalCost.CachedTokens += resp.Cost.CachedTokens
	c.totalCost.LLMCalls += resp.Cost.LLMCalls
	c.totalCost.ToolCalls += resp.Cost.ToolCalls
	c.totalCost.GraphQueries += resp.Cost.GraphQueries
	c.totalCost.VectorQueries += resp.Cost.VectorQueries
	c.totalCost.RerankCost += resp.Cost.RerankCost
	c.totalCost.EstimatedAmount += resp.Cost.EstimatedAmount
	c.totalCost.CacheSavedAmount += resp.Cost.CacheSavedAmount
	c.recordTraceLocked(traceFromResponse(resp))
	emitRecommendationMetrics(resp)
}

func (c *observationCollector) recordEvents(accepted int, rejected int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.eventsAccepted += int64(accepted)
	c.eventsRejected += int64(rejected)
}

func (c *observationCollector) recordBehaviorEvent(event BehaviorEvent, accepted bool) {
	if c == nil {
		return
	}
	status := "accepted"
	if !accepted {
		status = "rejected"
	}
	metrics.RecoV2EventsTotal.WithLabelValues(
		labelValue(event.TenantID, "default"),
		labelValue(event.Channel, "unknown"),
		labelValue(event.EventType, "unknown"),
		status,
	).Inc()
}

func (c *observationCollector) recordActivityAnalysis(analyzed, triggered bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if analyzed {
		c.activityAnalyzed++
	} else {
		c.activityAnalysisFailures++
	}
	if triggered {
		c.activityAgentTriggers++
	}
}

func (c *observationCollector) recordHookFailure(event BehaviorEvent, hookName string, mode HookMode) {
	if c == nil {
		return
	}
	c.recordHookFailures(1)
	metrics.RecoV2HookFailuresTotal.WithLabelValues(
		labelValue(event.EventType, "unknown"),
		labelValue(hookName, "unknown"),
		hookModeLabel(mode),
	).Inc()
}

func (c *observationCollector) recordHookFailures(count int) {
	if c == nil || count <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hookFailures += int64(count)
}

func (c *observationCollector) summary() ObservationSummary {
	if c == nil {
		return ObservationSummary{ByPath: map[string]int64{}}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	byPath := make(map[string]int64, len(c.byPath))
	for k, v := range c.byPath {
		byPath[k] = v
	}
	avg := 0.0
	if c.requests > 0 {
		avg = float64(c.totalLatencyMS) / float64(c.requests)
	}
	return ObservationSummary{
		Requests:                 c.requests,
		EventsAccepted:           c.eventsAccepted,
		EventsRejected:           c.eventsRejected,
		HookFailures:             c.hookFailures,
		ActivityAnalyzed:         c.activityAnalyzed,
		ActivityAnalysisFailures: c.activityAnalysisFailures,
		ActivityAgentTriggers:    c.activityAgentTriggers,
		ByPath:                   byPath,
		LastTraceID:              c.lastTraceID,
		LastRequestID:            c.lastRequestID,
		TotalCost:                c.totalCost,
		AverageLatencyMS:         avg,
		Labels:                   map[string]string{"framework": "trpc-agent-go", "runtime": "RecommendationRuntime"},
	}
}

func (c *observationCollector) trace(query TraceQueryRequest) TraceQueryResponse {
	if c == nil {
		return TraceQueryResponse{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	limit := query.Limit
	if limit <= 0 || limit > c.traceLimit {
		limit = c.traceLimit
	}
	matches := make([]TraceRecord, 0, limit)
	for i := len(c.traces) - 1; i >= 0 && len(matches) < limit; i-- {
		trace := c.traces[i]
		if !traceMatches(trace, query) {
			continue
		}
		matches = append(matches, trace)
	}
	return TraceQueryResponse{Traces: matches, Total: len(matches)}
}

func (c *observationCollector) recordTraceLocked(trace TraceRecord) {
	if trace.TraceID == "" {
		return
	}
	c.traces = append(c.traces, trace)
	if c.traceLimit <= 0 {
		c.traceLimit = 256
	}
	if len(c.traces) > c.traceLimit {
		c.traces = append([]TraceRecord(nil), c.traces[len(c.traces)-c.traceLimit:]...)
	}
}

func traceFromResponse(resp RecommendResponse) TraceRecord {
	startedAt := resp.MetricsStartTime()
	finishedAt := resp.MetricsFinishTime()
	return TraceRecord{
		TraceID:     resp.TraceID,
		RequestID:   resp.RequestID,
		TenantID:    resp.Metrics.Labels["tenant_id"],
		UserID:      resp.Metrics.Labels["user_id"],
		AnonymousID: resp.Metrics.Labels["anonymous_id"],
		Channel:     resp.Metrics.Labels["channel"],
		Scenario:    resp.Metrics.Labels["scenario"],
		Path:        resp.PathTaken,
		StartedAt:   startedAt,
		FinishedAt:  finishedAt,
		LatencyMS:   resp.Metrics.LatencyMillis,
		Steps:       append([]TraceStep(nil), resp.Steps...),
		Cost:        resp.Cost,
		Fallback:    resp.Fallback != nil,
		Items:       len(resp.Items),
		Skills:      skillsFromSteps(resp.Steps),
	}
}

func (resp RecommendResponse) MetricsStartTime() time.Time {
	if len(resp.Steps) > 0 {
		return resp.Steps[0].StartedAt
	}
	return time.Time{}
}

func (resp RecommendResponse) MetricsFinishTime() time.Time {
	if len(resp.Steps) > 0 {
		return resp.Steps[len(resp.Steps)-1].FinishedAt
	}
	return time.Time{}
}

func traceMatches(trace TraceRecord, query TraceQueryRequest) bool {
	if query.TraceID != "" && trace.TraceID != query.TraceID {
		return false
	}
	if query.RequestID != "" && trace.RequestID != query.RequestID {
		return false
	}
	if query.TenantID != "" && trace.TenantID != query.TenantID {
		return false
	}
	if query.UserID != "" && trace.UserID != query.UserID {
		return false
	}
	if query.Channel != "" && trace.Channel != query.Channel {
		return false
	}
	if query.Path != "" && trace.Path != query.Path {
		return false
	}
	if query.Skill != "" && !traceHasSkill(trace, query.Skill) {
		return false
	}
	return true
}

func traceHasSkill(trace TraceRecord, skill string) bool {
	for _, got := range trace.Skills {
		if got == skill {
			return true
		}
	}
	return false
}

func skillsFromSteps(steps []TraceStep) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, step := range steps {
		skill := skillNameForNode(step.Name)
		if skill == "" {
			continue
		}
		if _, ok := seen[skill]; ok {
			continue
		}
		seen[skill] = struct{}{}
		out = append(out, skill)
	}
	return out
}

func emitRecommendationMetrics(resp RecommendResponse) {
	tenantID := labelValue(resp.Metrics.Labels["tenant_id"], "default")
	channel := labelValue(resp.Metrics.Labels["channel"], "unknown")
	scenario := labelValue(resp.Metrics.Labels["scenario"], "recommend")
	path := labelValue(resp.PathTaken, "unknown")
	status := recommendationStatus(resp)
	metrics.RecoV2RequestsTotal.WithLabelValues(tenantID, channel, scenario, path, status).Inc()
	metrics.RecoV2LatencySeconds.WithLabelValues(tenantID, channel, scenario, path, status).Observe(float64(resp.Metrics.LatencyMillis) / 1000)
	metrics.RecoV2ReturnedItems.WithLabelValues(tenantID, channel, scenario, path, status).Observe(float64(len(resp.Items)))
	metrics.RecoV2CostAmount.WithLabelValues(tenantID, channel, scenario, path, "estimated").Add(resp.Cost.EstimatedAmount)
	metrics.RecoV2CostAmount.WithLabelValues(tenantID, channel, scenario, path, "rerank").Add(resp.Cost.RerankCost)
	metrics.RecoV2CostAmount.WithLabelValues(tenantID, channel, scenario, path, "cache_saved").Add(resp.Cost.CacheSavedAmount)
	metrics.RecoV2TokensTotal.WithLabelValues(tenantID, channel, scenario, path, "input").Add(float64(resp.Cost.TokensIn))
	metrics.RecoV2TokensTotal.WithLabelValues(tenantID, channel, scenario, path, "output").Add(float64(resp.Cost.TokensOut))
	metrics.RecoV2TokensTotal.WithLabelValues(tenantID, channel, scenario, path, "cached").Add(float64(resp.Cost.CachedTokens))
	metrics.RecoV2CallsTotal.WithLabelValues(tenantID, channel, scenario, path, "llm").Add(float64(resp.Cost.LLMCalls))
	metrics.RecoV2CallsTotal.WithLabelValues(tenantID, channel, scenario, path, "tool").Add(float64(resp.Cost.ToolCalls))
	metrics.RecoV2CallsTotal.WithLabelValues(tenantID, channel, scenario, path, "graph_query").Add(float64(resp.Cost.GraphQueries))
	metrics.RecoV2CallsTotal.WithLabelValues(tenantID, channel, scenario, path, "vector_query").Add(float64(resp.Cost.VectorQueries))
	for _, step := range resp.Steps {
		metrics.RecoV2TraceStepsTotal.WithLabelValues(
			path,
			labelValue(step.Name, "unknown"),
			labelValue(step.Type, "unknown"),
			labelValue(step.Status, "unknown"),
		).Inc()
	}
}

func recommendationStatus(resp RecommendResponse) string {
	if resp.Fallback == nil {
		return "ok"
	}
	if resp.Fallback.Source == "error" {
		return "error"
	}
	return "fallback"
}

func hookModeLabel(mode HookMode) string {
	switch mode {
	case HookModeBlocking:
		return "blocking"
	case HookModeAsync:
		return "async"
	default:
		return "unknown"
	}
}

func labelValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
