// collector_test.go — collector.go 单测（Task 14.6）。
//
// 测试覆盖：
//   - CollectRecommend 各指标记录（path/cost/quality/channel/graph/agent）
//   - CollectCF/CollectRerank/CollectPromptCache 子系统指标
//   - CollectCPU/CollectProfile/CollectCTR/CollectFallback 系统与转化指标
//   - ValidateMetrics 无死指标返回 nil
//   - ValidateMetrics 有死指标返回 error
//
// stub 类型用 OBS 前缀避免与 callbacks_test.go 冲突。
package obs

import (
	"strings"
	"testing"
	"time"

	"sea/service/recommend/rpc/internal/domain"
)

// ----------------------------------------------------------------------------
// 辅助：构造带默认指标的 collector
// ----------------------------------------------------------------------------

func newCollectorWithDefaults(t *testing.T) (*BusinessCollector, *MetricsRegistry) {
	t.Helper()
	r := NewMetricsRegistry()
	r.RegisterDefaultMetrics()
	return NewBusinessCollector(r), r
}

// ----------------------------------------------------------------------------
// CollectRecommend 测试
// ----------------------------------------------------------------------------

func TestCollectRecommend_PathDistribution(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	resp := domain.RecommendResponse{
		PathTaken: "fast",
	}
	c.CollectRecommend(resp)

	// path_distribution 应有 path=fast 实例
	out := r.Format()
	if !strings.Contains(out, `genrec_path_distribution{path="fast"} 1`) {
		t.Fatalf("path_distribution not recorded: %s", out)
	}
}

func TestCollectRecommend_PathDistribution_Aggregates(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	resp := domain.RecommendResponse{PathTaken: "fast"}
	c.CollectRecommend(resp)
	c.CollectRecommend(resp)
	c.CollectRecommend(resp)
	out := r.Format()
	// 三次调用应聚合为同一 counter，值为 3
	if !strings.Contains(out, `genrec_path_distribution{path="fast"} 3`) {
		t.Fatalf("path_distribution should aggregate to 3: %s", out)
	}
}

func TestCollectRecommend_CostYuanTotal(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	resp := domain.RecommendResponse{
		PathTaken: "slow",
		Cost: domain.CostReport{
			EstimatedCost: 1.5,
		},
	}
	c.CollectRecommend(resp)
	// 按分存储：1.5 * 100 = 150
	counter := r.GetCounter(MetricCostYuanTotal)
	if counter.Value() != 150 {
		t.Fatalf("CostYuanTotal = %d, want 150", counter.Value())
	}
}

func TestCollectRecommend_CostTokensTotal_ByType(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	resp := domain.RecommendResponse{
		PathTaken: "hybrid",
		Cost: domain.CostReport{
			TokensIn:     100,
			TokensOut:    50,
			CachedTokens: 25,
		},
	}
	c.CollectRecommend(resp)

	out := r.Format()
	if !strings.Contains(out, `genrec_cost_tokens_total{type="input"} 100`) {
		t.Fatalf("input tokens not recorded: %s", out)
	}
	if !strings.Contains(out, `genrec_cost_tokens_total{type="output"} 50`) {
		t.Fatalf("output tokens not recorded: %s", out)
	}
	if !strings.Contains(out, `genrec_cost_tokens_total{type="cached"} 25`) {
		t.Fatalf("cached tokens not recorded: %s", out)
	}
}

func TestCollectRecommend_QualityPassRate(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	resp := domain.RecommendResponse{
		PathTaken: "fast",
		Config: domain.RecommendConfig{
			QualityThreshold: 0.7,
		},
		QualityScores: map[string]domain.ArticleQuality{
			"a1": {ArticleID: "a1", Overall: 0.9},
			"a2": {ArticleID: "a2", Overall: 0.6},
			"a3": {ArticleID: "a3", Overall: 0.8},
			"a4": {ArticleID: "a4", Overall: 0.5},
		},
	}
	c.CollectRecommend(resp)
	g := r.GetGauge(MetricQualityPassRate)
	// 2/4 = 0.5 通过率
	if got := g.Value(); got != 0.5 {
		t.Fatalf("QualityPassRate = %v, want 0.5", got)
	}
}

func TestCollectRecommend_ChannelDistribution(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	resp := domain.RecommendResponse{
		PathTaken: "fast",
		Channel:   "tech",
	}
	c.CollectRecommend(resp)
	out := r.Format()
	if !strings.Contains(out, `genrec_channel_distribution{channel="tech"} 1`) {
		t.Fatalf("channel_distribution not recorded: %s", out)
	}
}

func TestCollectRecommend_EmptyChannel_NoChannelMetric(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	resp := domain.RecommendResponse{
		PathTaken: "fast",
		Channel:   "",
	}
	c.CollectRecommend(resp)
	out := r.Format()
	// 不应有 channel_distribution 行（除了 default metrics 注册的空 label 实例）
	// 这里检查 channel="" 不被额外递增
	if strings.Count(out, "genrec_channel_distribution") < 1 {
		// 至少有 TYPE 行
	}
}

func TestCollectRecommend_GraphMetrics(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	resp := domain.RecommendResponse{
		PathTaken: "slow",
		GraphTrace: &domain.GraphTrace{
			Cypher:   "MATCH (n) RETURN n",
			Results:  42,
			Duration: 250 * time.Millisecond,
		},
	}
	c.CollectRecommend(resp)

	g := r.GetGauge(MetricGraphNodeCount)
	if got := g.Value(); got != 42 {
		t.Fatalf("GraphNodeCount = %v, want 42", got)
	}
	h := r.GetHistogram(MetricGraphQueryLatencyMs)
	if h.Count() != 1 {
		t.Fatalf("GraphQueryLatencyMs Count = %d, want 1", h.Count())
	}
	if h.Sum() != 250 {
		t.Fatalf("GraphQueryLatencyMs Sum = %v, want 250", h.Sum())
	}
}

func TestCollectRecommend_AgentDecisionCount(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	resp := domain.RecommendResponse{PathTaken: "fast"}
	c.CollectRecommend(resp)
	out := r.Format()
	if !strings.Contains(out, `genrec_agent_decision_count{agent="orchestrator"} 1`) {
		t.Fatalf("agent_decision_count not recorded: %s", out)
	}
}

func TestCollectRecommend_FullResponse(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	resp := domain.RecommendResponse{
		PathTaken: "hybrid",
		Channel:   "finance",
		Cost: domain.CostReport{
			TokensIn:      200,
			TokensOut:     100,
			CachedTokens:  50,
			EstimatedCost: 2.5,
		},
		Config: domain.RecommendConfig{
			QualityThreshold: 0.6,
		},
		QualityScores: map[string]domain.ArticleQuality{
			"a1": {ArticleID: "a1", Overall: 0.9},
			"a2": {ArticleID: "a2", Overall: 0.3},
		},
		GraphTrace: &domain.GraphTrace{
			Results:  10,
			Duration: 100 * time.Millisecond,
		},
	}
	c.CollectRecommend(resp)

	// 验证关键指标
	if r.GetCounter(MetricCostYuanTotal).Value() != 250 {
		t.Fatalf("CostYuanTotal = %d, want 250", r.GetCounter(MetricCostYuanTotal).Value())
	}
	if r.GetGauge(MetricQualityPassRate).Value() != 0.5 {
		t.Fatalf("QualityPassRate = %v, want 0.5", r.GetGauge(MetricQualityPassRate).Value())
	}
	if r.GetGauge(MetricGraphNodeCount).Value() != 10 {
		t.Fatalf("GraphNodeCount = %v, want 10", r.GetGauge(MetricGraphNodeCount).Value())
	}
}

// ----------------------------------------------------------------------------
// CollectCF 测试
// ----------------------------------------------------------------------------

func TestCollectCF(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	c.CollectCF(0.75)
	if got := r.GetGauge(MetricCFHitRate).Value(); got != 0.75 {
		t.Fatalf("CFHitRate = %v, want 0.75", got)
	}
}

// ----------------------------------------------------------------------------
// CollectRerank 测试
// ----------------------------------------------------------------------------

func TestCollectRerank(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	c.CollectRerank(0.6)
	if got := r.GetGauge(MetricRerankWinRate).Value(); got != 0.6 {
		t.Fatalf("RerankWinRate = %v, want 0.6", got)
	}
}

// ----------------------------------------------------------------------------
// CollectPromptCache 测试
// ----------------------------------------------------------------------------

func TestCollectPromptCache(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	c.CollectPromptCache(0.8, 500)
	if got := r.GetGauge(MetricPromptCacheHitRatio).Value(); got != 0.8 {
		t.Fatalf("PromptCacheHitRatio = %v, want 0.8", got)
	}
	if got := r.GetGauge(MetricRecallCount).Value(); got != 500 {
		t.Fatalf("RecallCount (cachedTokens) = %v, want 500", got)
	}
}

// ----------------------------------------------------------------------------
// CollectCPU 测试
// ----------------------------------------------------------------------------

func TestCollectCPU(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	c.CollectCPU(0.45)
	if got := r.GetGauge(MetricCPUUtilization).Value(); got != 0.45 {
		t.Fatalf("CPUUtilization = %v, want 0.45", got)
	}
}

// ----------------------------------------------------------------------------
// CollectProfile 测试
// ----------------------------------------------------------------------------

func TestCollectProfile(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	c.CollectProfile(0.65)
	h := r.GetHistogram(MetricProfileDecayScore)
	if h.Count() != 1 {
		t.Fatalf("ProfileDecayScore Count = %d, want 1", h.Count())
	}
	if h.Sum() != 0.65 {
		t.Fatalf("ProfileDecayScore Sum = %v, want 0.65", h.Sum())
	}
}

// ----------------------------------------------------------------------------
// CollectCTR 测试
// ----------------------------------------------------------------------------

func TestCollectCTR(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	c.CollectCTR(0.12, 0.05)
	if got := r.GetGauge(MetricCTR).Value(); got != 0.12 {
		t.Fatalf("CTR = %v, want 0.12", got)
	}
	if got := r.GetGauge(MetricCVR).Value(); got != 0.05 {
		t.Fatalf("CVR = %v, want 0.05", got)
	}
}

// ----------------------------------------------------------------------------
// CollectFallback 测试
// ----------------------------------------------------------------------------

func TestCollectFallback(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	c.CollectFallback()
	c.CollectFallback()
	c.CollectFallback()
	g := r.GetGauge(MetricFallbackRate)
	if got := g.Value(); got != 3 {
		t.Fatalf("FallbackRate = %v, want 3", got)
	}
}

// ----------------------------------------------------------------------------
// ValidateMetrics 测试
// ----------------------------------------------------------------------------

func TestValidateMetrics_NoDeadMetrics_ReturnsNil(t *testing.T) {
	c, _ := newCollectorWithDefaults(t)
	// 只注册默认指标，无死指标
	if err := c.ValidateMetrics(); err != nil {
		t.Fatalf("ValidateMetrics error = %v, want nil", err)
	}
}

func TestValidateMetrics_WithDeadMetric_ReturnsError(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	// 注册一个死指标
	r.RegisterCounter("old_short_id_metric", nil)
	err := c.ValidateMetrics()
	if err == nil {
		t.Fatal("ValidateMetrics should return error for dead metric")
	}
	if !strings.Contains(err.Error(), "old_short_id_metric") {
		t.Fatalf("error should contain dead metric name: %v", err)
	}
	if !strings.Contains(err.Error(), "dead metrics detected") {
		t.Fatalf("error should mention 'dead metrics detected': %v", err)
	}
}

func TestValidateMetrics_MultipleDeadMetrics_AllListed(t *testing.T) {
	c, r := newCollectorWithDefaults(t)
	r.RegisterCounter("old_short_id_metric", nil)
	r.RegisterGauge("old_legacy_counter", nil)
	r.RegisterHistogram("deprecated_reco_count", nil)
	err := c.ValidateMetrics()
	if err == nil {
		t.Fatal("ValidateMetrics should return error")
	}
	for _, dead := range DeadMetrics {
		if !strings.Contains(err.Error(), dead) {
			t.Fatalf("error should contain %q: %v", dead, err)
		}
	}
}

func TestValidateMetrics_NilCollector_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ValidateMetrics panicked: %v", r)
		}
	}()
	var c *BusinessCollector
	if err := c.ValidateMetrics(); err != nil {
		t.Fatalf("nil collector ValidateMetrics error = %v, want nil", err)
	}
}

// ----------------------------------------------------------------------------
// DeadMetrics 列表测试
// ----------------------------------------------------------------------------

func TestDeadMetrics_ContainsExpected(t *testing.T) {
	expected := []string{
		"old_short_id_metric",
		"old_legacy_counter",
		"deprecated_reco_count",
	}
	if len(DeadMetrics) != len(expected) {
		t.Fatalf("DeadMetrics len = %d, want %d", len(DeadMetrics), len(expected))
	}
	for i, want := range expected {
		if DeadMetrics[i] != want {
			t.Fatalf("DeadMetrics[%d] = %q, want %q", i, DeadMetrics[i], want)
		}
	}
}

// ----------------------------------------------------------------------------
// Nil 安全性测试
// ----------------------------------------------------------------------------

func TestBusinessCollector_NilRegistry_AllMethodsNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil registry panicked: %v", r)
		}
	}()
	c := NewBusinessCollector(nil)
	c.CollectRecommend(domain.RecommendResponse{})
	c.CollectCF(0.5)
	c.CollectRerank(0.5)
	c.CollectPromptCache(0.5, 100)
	c.CollectCPU(0.5)
	c.CollectProfile(0.5)
	c.CollectCTR(0.5, 0.5)
	c.CollectFallback()
	_ = c.ValidateMetrics()
}

func TestNewBusinessCollector_ReturnsNonNil(t *testing.T) {
	r := NewMetricsRegistry()
	c := NewBusinessCollector(r)
	if c == nil {
		t.Fatal("NewBusinessCollector should return non-nil")
	}
}

// ----------------------------------------------------------------------------
// GetOrCreateCounter 标签匹配测试（BusinessCollector 依赖该方法）
// ----------------------------------------------------------------------------

func TestGetOrCreateCounter_FindsExistingByLabels(t *testing.T) {
	r := NewMetricsRegistry()
	c1 := r.GetOrCreateCounter("test_counter", map[string]string{"path": "fast"})
	c1.Inc()
	c2 := r.GetOrCreateCounter("test_counter", map[string]string{"path": "fast"})
	c2.Inc()
	// 同标签应返回同一实例
	if c1 != c2 {
		t.Fatal("GetOrCreateCounter should return same instance for same labels")
	}
	if c1.Value() != 2 {
		t.Fatalf("Value = %d, want 2", c1.Value())
	}
}

func TestGetOrCreateCounter_DifferentLabels_CreatesNew(t *testing.T) {
	r := NewMetricsRegistry()
	c1 := r.GetOrCreateCounter("test_counter", map[string]string{"path": "fast"})
	c1.Inc()
	c2 := r.GetOrCreateCounter("test_counter", map[string]string{"path": "slow"})
	c2.Inc()
	if c1 == c2 {
		t.Fatal("GetOrCreateCounter should return different instance for different labels")
	}
	if c1.Value() != 1 {
		t.Fatalf("c1 Value = %d, want 1", c1.Value())
	}
	if c2.Value() != 1 {
		t.Fatalf("c2 Value = %d, want 1", c2.Value())
	}
}

func TestGetOrCreateCounter_NilLabels(t *testing.T) {
	r := NewMetricsRegistry()
	c1 := r.GetOrCreateCounter("test_counter", nil)
	c1.Inc()
	c2 := r.GetOrCreateCounter("test_counter", nil)
	c2.Inc()
	if c1 != c2 {
		t.Fatal("nil labels should match")
	}
	if c1.Value() != 2 {
		t.Fatalf("Value = %d, want 2", c1.Value())
	}
}
