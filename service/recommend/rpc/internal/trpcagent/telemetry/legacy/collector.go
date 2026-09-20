// collector.go — 业务指标整合（Task 14.6）。
//
// 该文件实现 BusinessCollector，从推荐响应与各子系统采集业务指标并写入
// MetricsRegistry，统一对外暴露。
//
// 职责：
//   - CollectRecommend：从推荐响应采集 path/cost/quality/channel/graph/agent 指标
//   - CollectCF/CollectRerank/CollectPromptCache：子系统指标采集
//   - CollectCPU/CollectProfile：系统与画像指标采集
//   - CollectCTR/CollectFallback：转化与兜底指标采集
//   - ValidateMetrics：校验无死指标残留（DeadMetrics 列表）
//
// 二开扩展点：
//   - 新增 CollectXxx 方法采集自定义指标
//   - 扩展 DeadMetrics 列表防止旧指标复用
package obs

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// DeadMetrics 死指标列表，禁止在 registry 中残留。
// 这些是历史遗留指标，已废弃，ValidateMetrics 会检查 registry 中是否仍有注册。
var DeadMetrics = []string{
	"old_short_id_metric",
	"old_legacy_counter",
	"deprecated_reco_count",
}

// BusinessCollector 业务指标采集器，整合各子系统指标到 MetricsRegistry。
//
// 字段语义：
//   - registry：底层指标注册中心
type BusinessCollector struct {
	registry *MetricsRegistry
}

// NewBusinessCollector 构造 BusinessCollector。
// registry 指标注册中心（应已调用 RegisterDefaultMetrics）。
func NewBusinessCollector(registry *MetricsRegistry) *BusinessCollector {
	return &BusinessCollector{registry: registry}
}

// CollectRecommend 从推荐响应采集业务指标。
//
// 记录指标：
//   - genrec_path_distribution{path=<PathTaken>} +1
//   - genrec_cost_yuan_total += Cost.EstimatedCost（按分存储，乘 100 转 int64）
//   - genrec_cost_tokens_total{type=input} += TokensIn
//   - genrec_cost_tokens_total{type=output} += TokensOut
//   - genrec_cost_tokens_total{type=cached} += CachedTokens
//   - genrec_quality_pass_rate = passCount/totalCount
//   - genrec_channel_distribution{channel=<Channel>} +1（Channel 非空时）
//   - genrec_graph_query_latency_ms observe Duration（GraphTrace 非空时）
//   - genrec_graph_node_count = Results（GraphTrace 非空时）
//   - genrec_agent_decision_count{agent=orchestrator} +1
func (c *BusinessCollector) CollectRecommend(response domain.RecommendResponse) {
	if c == nil || c.registry == nil {
		return
	}
	r := c.registry

	// path_distribution
	path := response.PathTaken
	if path == "" {
		path = "unknown"
	}
	r.GetOrCreateCounter(MetricPathDistribution, map[string]string{"path": path}).Inc()

	// cost_yuan_total（按分存储避免浮点累计误差）
	if response.Cost.EstimatedCost > 0 {
		r.GetCounter(MetricCostYuanTotal).Add(int64(response.Cost.EstimatedCost * 100))
	}

	// cost_tokens_total（按 type 标签分桶）
	r.GetOrCreateCounter(MetricCostTokensTotal, map[string]string{"type": "input"}).Add(int64(response.Cost.TokensIn))
	r.GetOrCreateCounter(MetricCostTokensTotal, map[string]string{"type": "output"}).Add(int64(response.Cost.TokensOut))
	r.GetOrCreateCounter(MetricCostTokensTotal, map[string]string{"type": "cached"}).Add(int64(response.Cost.CachedTokens))

	// quality_pass_rate
	if g := r.GetGauge(MetricQualityPassRate); g != nil && len(response.QualityScores) > 0 {
		threshold := response.Config.QualityThreshold
		passCount := 0
		for _, q := range response.QualityScores {
			if q.Overall >= threshold {
				passCount++
			}
		}
		g.Set(float64(passCount) / float64(len(response.QualityScores)))
	}

	// channel_distribution
	if response.Channel != "" {
		r.GetOrCreateCounter(MetricChannelDistribution, map[string]string{"channel": response.Channel}).Inc()
	}

	// graph 指标
	if response.GraphTrace != nil {
		if h := r.GetHistogram(MetricGraphQueryLatencyMs); h != nil {
			h.Observe(float64(response.GraphTrace.Duration.Milliseconds()))
		}
		if g := r.GetGauge(MetricGraphNodeCount); g != nil {
			g.Set(float64(response.GraphTrace.Results))
		}
	}

	// agent_decision_count（推荐响应由 orchestrator 产出，记录一次决策）
	r.GetOrCreateCounter(MetricAgentDecisionCount, map[string]string{"agent": "orchestrator"}).Inc()
}

// CollectCF 采集协同过滤命中率。
func (c *BusinessCollector) CollectCF(hitRate float64) {
	if c == nil || c.registry == nil {
		return
	}
	if g := c.registry.GetGauge(MetricCFHitRate); g != nil {
		g.Set(hitRate)
	}
}

// CollectRerank 采集重排胜率。
func (c *BusinessCollector) CollectRerank(winRate float64) {
	if c == nil || c.registry == nil {
		return
	}
	if g := c.registry.GetGauge(MetricRerankWinRate); g != nil {
		g.Set(winRate)
	}
}

// CollectPromptCache 采集 Prompt Cache 命中率与命中 token 数。
// hitRatio 命中率（0-1）；cachedTokens 命中 token 数。
func (c *BusinessCollector) CollectPromptCache(hitRatio float64, cachedTokens int64) {
	if c == nil || c.registry == nil {
		return
	}
	if g := c.registry.GetGauge(MetricPromptCacheHitRatio); g != nil {
		g.Set(hitRatio)
	}
	if g := c.registry.GetGauge(MetricRecallCount); g != nil {
		// cachedTokens 作为召回量参考写入 recall_count（无独立 cached_tokens gauge）
		g.Set(float64(cachedTokens))
	}
}

// CollectCPU 采集 CPU 利用率。
func (c *BusinessCollector) CollectCPU(utilization float64) {
	if c == nil || c.registry == nil {
		return
	}
	if g := c.registry.GetGauge(MetricCPUUtilization); g != nil {
		g.Set(utilization)
	}
}

// CollectProfile 采集画像衰减分。
func (c *BusinessCollector) CollectProfile(decayScore float64) {
	if c == nil || c.registry == nil {
		return
	}
	if h := c.registry.GetHistogram(MetricProfileDecayScore); h != nil {
		h.Observe(decayScore)
	}
}

// CollectCTR 采集点击率与转化率。
func (c *BusinessCollector) CollectCTR(ctr, cvr float64) {
	if c == nil || c.registry == nil {
		return
	}
	if g := c.registry.GetGauge(MetricCTR); g != nil {
		g.Set(ctr)
	}
	if g := c.registry.GetGauge(MetricCVR); g != nil {
		g.Set(cvr)
	}
}

// CollectFallback 采集兜底事件（兜底率 +1 次累计，gauge 表示累计次数）。
func (c *BusinessCollector) CollectFallback() {
	if c == nil || c.registry == nil {
		return
	}
	if g := c.registry.GetGauge(MetricFallbackRate); g != nil {
		g.Add(1)
	}
}

// ValidateMetrics 校验 registry 中无死指标残留。
// 返回 error 包含所有命中的死指标名（多个用逗号分隔）；无死指标返回 nil。
func (c *BusinessCollector) ValidateMetrics() error {
	if c == nil || c.registry == nil {
		return nil
	}
	var hits []string
	for _, dead := range DeadMetrics {
		if c.registry.HasMetric(dead) {
			hits = append(hits, dead)
		}
	}
	if len(hits) > 0 {
		return errors.New("dead metrics detected: " + strings.Join(hits, ", "))
	}
	return nil
}

// String 返回 collector 状态摘要（调试用）。
func (c *BusinessCollector) String() string {
	if c == nil {
		return "BusinessCollector(nil)"
	}
	return fmt.Sprintf("BusinessCollector(registry=%p)", c.registry)
}
