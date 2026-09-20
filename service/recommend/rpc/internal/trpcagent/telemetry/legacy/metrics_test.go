// metrics_test.go — metrics.go 单测（Task 14.1）。
//
// 测试覆盖：
//   - Counter Inc/Add/Value 并发安全
//   - Gauge Set/Add/Value 并发安全
//   - Histogram Observe/Mean/Count/Sum/Buckets/Counts
//   - MetricsRegistry 注册与 Get/HasMetric/Names
//   - Format 输出 Prometheus exposition 格式
//   - RegisterDefaultMetrics 注册 20 个指标
//   - RecordRecommend 从推荐响应记录指标
//
// stub 类型用 OBS 前缀避免与 callbacks_test.go 冲突。
package obs

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ----------------------------------------------------------------------------
// Counter 测试
// ----------------------------------------------------------------------------

func TestCounter_Inc_Add_Value(t *testing.T) {
	c := &Counter{name: "test_counter"}
	c.Inc()
	c.Inc()
	c.Add(5)
	if got := c.Value(); got != 7 {
		t.Fatalf("Value = %d, want 7", got)
	}
	c.Add(-2)
	if got := c.Value(); got != 5 {
		t.Fatalf("Value = %d, want 5", got)
	}
}

func TestCounter_ConcurrentSafe(t *testing.T) {
	c := &Counter{name: "test_counter"}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Inc()
		}()
	}
	wg.Wait()
	if got := c.Value(); got != 100 {
		t.Fatalf("Value = %d, want 100", got)
	}
}

func TestCounter_NilSafe(t *testing.T) {
	var c *Counter
	c.Inc()
	c.Add(5)
	if got := c.Value(); got != 0 {
		t.Fatalf("nil Counter Value = %d, want 0", got)
	}
	if c.Name() != "" {
		t.Fatalf("nil Counter Name = %q, want empty", c.Name())
	}
}

func TestCounter_Labels_ReturnsCopy(t *testing.T) {
	c := &Counter{name: "c", labels: map[string]string{"path": "fast"}}
	labels := c.Labels()
	labels["path"] = "mutated"
	if c.Labels()["path"] != "fast" {
		t.Fatal("Labels should return a copy")
	}
}

// ----------------------------------------------------------------------------
// Gauge 测试
// ----------------------------------------------------------------------------

func TestGauge_Set_Add_Value(t *testing.T) {
	g := &Gauge{name: "test_gauge"}
	g.Set(10.5)
	if got := g.Value(); got != 10.5 {
		t.Fatalf("Value = %v, want 10.5", got)
	}
	g.Add(2.5)
	if got := g.Value(); got != 13 {
		t.Fatalf("Value = %v, want 13", got)
	}
	g.Add(-3)
	if got := g.Value(); got != 10 {
		t.Fatalf("Value = %v, want 10", got)
	}
}

func TestGauge_ConcurrentSafe(t *testing.T) {
	g := &Gauge{name: "test_gauge"}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Add(1)
		}()
	}
	wg.Wait()
	if got := g.Value(); got != 100 {
		t.Fatalf("Value = %v, want 100", got)
	}
}

func TestGauge_NilSafe(t *testing.T) {
	var g *Gauge
	g.Set(1)
	g.Add(1)
	if got := g.Value(); got != 0 {
		t.Fatalf("nil Gauge Value = %v, want 0", got)
	}
}

func TestGauge_Labels_ReturnsCopy(t *testing.T) {
	g := &Gauge{name: "g", labels: map[string]string{"path": "fast"}}
	labels := g.Labels()
	labels["path"] = "mutated"
	if g.Labels()["path"] != "fast" {
		t.Fatal("Labels should return a copy")
	}
}

// ----------------------------------------------------------------------------
// Histogram 测试
// ----------------------------------------------------------------------------

func TestHistogram_Observed_InCorrectBuckets(t *testing.T) {
	h := &Histogram{
		name:    "test_hist",
		buckets: []float64{10, 50, 100},
		counts:  make([]int64, 3),
	}
	h.Observe(5)   // <= 10, 50, 100
	h.Observe(20)  // <= 50, 100
	h.Observe(80)  // <= 100
	h.Observe(150) // 不在任何桶
	counts := h.Counts()
	if counts[0] != 1 {
		t.Fatalf("bucket[0]=%d, want 1", counts[0])
	}
	if counts[1] != 2 {
		t.Fatalf("bucket[1]=%d, want 2", counts[1])
	}
	if counts[2] != 3 {
		t.Fatalf("bucket[2]=%d, want 3", counts[2])
	}
	if h.Count() != 4 {
		t.Fatalf("Count = %d, want 4", h.Count())
	}
	wantSum := 5 + 20 + 80 + 150.0
	if h.Sum() != wantSum {
		t.Fatalf("Sum = %v, want %v", h.Sum(), wantSum)
	}
}

func TestHistogram_Mean(t *testing.T) {
	h := &Histogram{
		name:    "test_hist",
		buckets: []float64{100},
		counts:  make([]int64, 1),
	}
	h.Observe(10)
	h.Observe(20)
	h.Observe(30)
	if got := h.Mean(); got != 20 {
		t.Fatalf("Mean = %v, want 20", got)
	}
}

func TestHistogram_Mean_ZeroCount(t *testing.T) {
	h := &Histogram{
		name:    "test_hist",
		buckets: []float64{100},
		counts:  make([]int64, 1),
	}
	if got := h.Mean(); got != 0 {
		t.Fatalf("Mean = %v, want 0", got)
	}
}

func TestHistogram_ConcurrentSafe(t *testing.T) {
	h := &Histogram{
		name:    "test_hist",
		buckets: []float64{100},
		counts:  make([]int64, 1),
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.Observe(1)
		}()
	}
	wg.Wait()
	if h.Count() != 100 {
		t.Fatalf("Count = %d, want 100", h.Count())
	}
	if h.Sum() != 100 {
		t.Fatalf("Sum = %v, want 100", h.Sum())
	}
}

func TestHistogram_Buckets_ReturnsCopy(t *testing.T) {
	h := &Histogram{
		name:    "test_hist",
		buckets: []float64{10, 50},
		counts:  make([]int64, 2),
	}
	buckets := h.Buckets()
	buckets[0] = 9999
	if h.Buckets()[0] != 10 {
		t.Fatal("Buckets should return a copy")
	}
}

func TestHistogram_NilSafe(t *testing.T) {
	var h *Histogram
	h.Observe(1)
	if h.Mean() != 0 {
		t.Fatalf("nil Mean = %v, want 0", h.Mean())
	}
	if h.Count() != 0 {
		t.Fatalf("nil Count = %d, want 0", h.Count())
	}
	if h.Name() != "" {
		t.Fatalf("nil Name = %q", h.Name())
	}
}

// ----------------------------------------------------------------------------
// MetricsRegistry 测试
// ----------------------------------------------------------------------------

func TestNewMetricsRegistry_Empty(t *testing.T) {
	r := NewMetricsRegistry()
	if r == nil {
		t.Fatal("registry should not be nil")
	}
	if len(r.CounterNames()) != 0 {
		t.Fatalf("counter names = %v, want empty", r.CounterNames())
	}
	if len(r.GaugeNames()) != 0 {
		t.Fatalf("gauge names = %v, want empty", r.GaugeNames())
	}
	if len(r.HistogramNames()) != 0 {
		t.Fatalf("histogram names = %v, want empty", r.HistogramNames())
	}
}

func TestMetricsRegistry_RegisterCounter(t *testing.T) {
	r := NewMetricsRegistry()
	c := r.RegisterCounter("test_counter", map[string]string{"path": "fast"})
	if c == nil {
		t.Fatal("counter should not be nil")
	}
	if c.Name() != "test_counter" {
		t.Fatalf("Name = %q", c.Name())
	}
	if got := r.GetCounter("test_counter"); got != c {
		t.Fatal("GetCounter should return registered counter")
	}
	if !r.HasMetric("test_counter") {
		t.Fatal("HasMetric should be true")
	}
	if r.HasMetric("nonexistent") {
		t.Fatal("HasMetric should be false for nonexistent")
	}
}

func TestMetricsRegistry_RegisterGauge(t *testing.T) {
	r := NewMetricsRegistry()
	g := r.RegisterGauge("test_gauge", nil)
	if g == nil {
		t.Fatal("gauge should not be nil")
	}
	if got := r.GetGauge("test_gauge"); got != g {
		t.Fatal("GetGauge should return registered gauge")
	}
}

func TestMetricsRegistry_RegisterHistogram_DefaultBuckets(t *testing.T) {
	r := NewMetricsRegistry()
	h := r.RegisterHistogram("test_hist", nil)
	if h == nil {
		t.Fatal("histogram should not be nil")
	}
	if len(h.Buckets()) != len(DefaultLatencyBuckets) {
		t.Fatalf("buckets len = %d, want %d", len(h.Buckets()), len(DefaultLatencyBuckets))
	}
}

func TestMetricsRegistry_RegisterHistogram_CustomBuckets(t *testing.T) {
	r := NewMetricsRegistry()
	custom := []float64{1, 2, 3}
	h := r.RegisterHistogram("test_hist", custom)
	if len(h.Buckets()) != 3 {
		t.Fatalf("buckets len = %d, want 3", len(h.Buckets()))
	}
}

func TestMetricsRegistry_GetNonexistent(t *testing.T) {
	r := NewMetricsRegistry()
	if r.GetCounter("nope") != nil {
		t.Fatal("GetCounter should return nil")
	}
	if r.GetGauge("nope") != nil {
		t.Fatal("GetGauge should return nil")
	}
	if r.GetHistogram("nope") != nil {
		t.Fatal("GetHistogram should return nil")
	}
}

func TestMetricsRegistry_Names_Sorted(t *testing.T) {
	r := NewMetricsRegistry()
	r.RegisterCounter("c_b", nil)
	r.RegisterCounter("c_a", nil)
	r.RegisterGauge("g_b", nil)
	r.RegisterGauge("g_a", nil)
	r.RegisterHistogram("h_b", nil)
	r.RegisterHistogram("h_a", nil)

	if got := r.CounterNames(); len(got) != 2 || got[0] != "c_a" || got[1] != "c_b" {
		t.Fatalf("CounterNames = %v", got)
	}
	if got := r.GaugeNames(); len(got) != 2 || got[0] != "g_a" || got[1] != "g_b" {
		t.Fatalf("GaugeNames = %v", got)
	}
	if got := r.HistogramNames(); len(got) != 2 || got[0] != "h_a" || got[1] != "h_b" {
		t.Fatalf("HistogramNames = %v", got)
	}
}

// ----------------------------------------------------------------------------
// Format 测试
// ----------------------------------------------------------------------------

func TestMetricsRegistry_Format_Counters(t *testing.T) {
	r := NewMetricsRegistry()
	c := r.RegisterCounter("genrec_recommend_total", nil)
	c.Inc()
	c.Inc()
	out := r.Format()
	if !strings.Contains(out, "# TYPE genrec_recommend_total counter") {
		t.Fatalf("Format missing TYPE line: %s", out)
	}
	if !strings.Contains(out, "genrec_recommend_total 2") {
		t.Fatalf("Format missing value line: %s", out)
	}
}

func TestMetricsRegistry_Format_Gauges(t *testing.T) {
	r := NewMetricsRegistry()
	g := r.RegisterGauge("genrec_recall_count", nil)
	g.Set(42)
	out := r.Format()
	if !strings.Contains(out, "# TYPE genrec_recall_count gauge") {
		t.Fatalf("Format missing TYPE line: %s", out)
	}
	if !strings.Contains(out, "genrec_recall_count 42") {
		t.Fatalf("Format missing value line: %s", out)
	}
}

func TestMetricsRegistry_Format_Histogram(t *testing.T) {
	r := NewMetricsRegistry()
	h := r.RegisterHistogram("genrec_recommend_latency_ms", []float64{100, 500})
	h.Observe(50)
	h.Observe(200)
	out := r.Format()
	if !strings.Contains(out, "# TYPE genrec_recommend_latency_ms histogram") {
		t.Fatalf("Format missing TYPE line: %s", out)
	}
	if !strings.Contains(out, `genrec_recommend_latency_ms_bucket{le="100"}`) {
		t.Fatalf("Format missing bucket line: %s", out)
	}
	if !strings.Contains(out, `genrec_recommend_latency_ms_bucket{le="+Inf"}`) {
		t.Fatalf("Format missing +Inf bucket line: %s", out)
	}
	if !strings.Contains(out, "genrec_recommend_latency_ms_count 2") {
		t.Fatalf("Format missing count line: %s", out)
	}
}

func TestMetricsRegistry_Format_WithLabels(t *testing.T) {
	r := NewMetricsRegistry()
	c := r.RegisterCounter("genrec_path_distribution", map[string]string{"path": "fast"})
	c.Inc()
	out := r.Format()
	if !strings.Contains(out, `genrec_path_distribution{path="fast"} 1`) {
		t.Fatalf("Format missing labeled line: %s", out)
	}
}

func TestMetricsRegistry_Format_Nil(t *testing.T) {
	var r *MetricsRegistry
	if out := r.Format(); out != "" {
		t.Fatalf("nil Format = %q, want empty", out)
	}
}

// ----------------------------------------------------------------------------
// RegisterDefaultMetrics 测试
// ----------------------------------------------------------------------------

func TestRegisterDefaultMetrics_Registers20(t *testing.T) {
	r := NewMetricsRegistry()
	dm := r.RegisterDefaultMetrics()
	if dm == nil {
		t.Fatal("DefaultMetrics should not be nil")
	}
	// 20 个核心指标应全部注册
	expected := []string{
		MetricRecommendTotal,
		MetricRecommendLatencyMs,
		MetricPathDistribution,
		MetricRecallCount,
		MetricRerankCount,
		MetricQualityPassRate,
		MetricCostYuanTotal,
		MetricCostTokensTotal,
		MetricCFHitRate,
		MetricRerankWinRate,
		MetricGraphQueryLatencyMs,
		MetricGraphNodeCount,
		MetricChannelDistribution,
		MetricCPUUtilization,
		MetricProfileDecayScore,
		MetricPromptCacheHitRatio,
		MetricFallbackRate,
		MetricCTR,
		MetricCVR,
		MetricAgentDecisionCount,
	}
	for _, name := range expected {
		if !r.HasMetric(name) {
			t.Fatalf("metric %q not registered", name)
		}
	}
	// 验证 counter/gauge/histogram 句柄非 nil
	if dm.RecommendTotal == nil {
		t.Fatal("RecommendTotal should not be nil")
	}
	if dm.RecommendLatencyMs == nil {
		t.Fatal("RecommendLatencyMs should not be nil")
	}
	if dm.RecallCount == nil {
		t.Fatal("RecallCount should not be nil")
	}
	if dm.AgentDecisionCount == nil {
		t.Fatal("AgentDecisionCount should not be nil")
	}
}

func TestDefaultMetrics_RecallCount_Settable(t *testing.T) {
	r := NewMetricsRegistry()
	dm := r.RegisterDefaultMetrics()
	dm.RecallCount.Set(42)
	if got := dm.RecallCount.Value(); got != 42 {
		t.Fatalf("RecallCount = %v, want 42", got)
	}
}

// ----------------------------------------------------------------------------
// RecordRecommend 测试
// ----------------------------------------------------------------------------

func TestRecordRecommend_RecordsCoreMetrics(t *testing.T) {
	r := NewMetricsRegistry()
	r.RegisterDefaultMetrics()
	resp := domain.RecommendResponse{
		Candidates: []domain.Candidate{
			{ArticleID: "a1"},
			{ArticleID: "a2"},
			{ArticleID: "a3"},
		},
		Cost: domain.CostReport{
			TokensIn:      100,
			TokensOut:     50,
			CachedTokens:  25,
			EstimatedCost: 0.5,
		},
		Channel:   "tech",
		PathTaken: "fast",
		RerankScores: map[string]float64{
			"a1": 0.9,
			"a2": 0.8,
		},
		GraphTrace: &domain.GraphTrace{
			Cypher:   "MATCH (n) RETURN n",
			Results:  10,
			Duration: 100 * time.Millisecond,
		},
	}
	r.RecordRecommend(context.Background(), resp)

	if got := r.GetCounter(MetricRecommendTotal).Value(); got != 1 {
		t.Fatalf("RecommendTotal = %d, want 1", got)
	}
	if got := r.GetCounter(MetricPathDistribution).Value(); got != 1 {
		t.Fatalf("PathDistribution = %d, want 1", got)
	}
	if got := r.GetGauge(MetricRecallCount).Value(); got != 3 {
		t.Fatalf("RecallCount = %v, want 3", got)
	}
	if got := r.GetCounter(MetricCostTokensTotal).Value(); got != 175 {
		t.Fatalf("CostTokensTotal = %d, want 175", got)
	}
	if got := r.GetCounter(MetricChannelDistribution).Value(); got != 1 {
		t.Fatalf("ChannelDistribution = %d, want 1", got)
	}
	if got := r.GetGauge(MetricGraphNodeCount).Value(); got != 10 {
		t.Fatalf("GraphNodeCount = %v, want 10", got)
	}
	h := r.GetHistogram(MetricGraphQueryLatencyMs)
	if h.Count() != 1 {
		t.Fatalf("GraphQueryLatencyMs Count = %d, want 1", h.Count())
	}
	if h.Sum() != 100 {
		t.Fatalf("GraphQueryLatencyMs Sum = %v, want 100", h.Sum())
	}
	if got := r.GetGauge(MetricRerankCount).Value(); got != 2 {
		t.Fatalf("RerankCount = %v, want 2", got)
	}
}

func TestRecordRecommend_NilRegistry_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RecordRecommend panicked: %v", r)
		}
	}()
	var r *MetricsRegistry
	r.RecordRecommend(context.Background(), domain.RecommendResponse{})
}

func TestRecordRecommend_NoGraphTrace_NoPanic(t *testing.T) {
	r := NewMetricsRegistry()
	r.RegisterDefaultMetrics()
	resp := domain.RecommendResponse{
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Channel:    "tech",
		PathTaken:  "fast",
	}
	r.RecordRecommend(context.Background(), resp)
	// 不应 panic，且 graph 指标为默认值
	if got := r.GetGauge(MetricGraphNodeCount).Value(); got != 0 {
		t.Fatalf("GraphNodeCount = %v, want 0", got)
	}
}
