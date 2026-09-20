// metrics.go — Prometheus 风格指标采集（Task 14.1）。
//
// 该文件提供 Prometheus exposition 风格的指标采集，避免直接 import
// prometheus client_golang，通过自实现 Counter/Gauge/Histogram 满足
// 离线编译与薄封装需求。
//
// 职责：
//   - 实现 Counter（累加）/Gauge（瞬时）/Histogram（桶分布）三类指标
//   - 实现 MetricsRegistry 统一注册与 Format（Prometheus exposition format）
//   - 预定义 20 个推荐核心指标（RegisterDefaultMetrics）
//   - 提供 RecordRecommend 从推荐响应记录指标
//
// 二开扩展点：
//   - 通过 RegisterCounter/RegisterGauge/RegisterHistogram 注册自定义指标
//   - 替换 Format 输出对接自研 metrics pipeline（不依赖 prometheus client）
package obs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ----------------------------------------------------------------------------
// 指标名常量（20 个推荐核心指标）
// ----------------------------------------------------------------------------

// 指标名常量，与 RegisterDefaultMetrics 注册的指标一一对应。
const (
	// MetricRecommendTotal 推荐总次数（counter）。
	MetricRecommendTotal = "genrec_recommend_total"
	// MetricRecommendLatencyMs 推荐延迟（histogram，毫秒）。
	MetricRecommendLatencyMs = "genrec_recommend_latency_ms"
	// MetricPathDistribution 路径分布（counter，label: path）。
	MetricPathDistribution = "genrec_path_distribution"
	// MetricRecallCount 召回数量（gauge）。
	MetricRecallCount = "genrec_recall_count"
	// MetricRerankCount 重排数量（gauge）。
	MetricRerankCount = "genrec_rerank_count"
	// MetricQualityPassRate 质量通过率（gauge）。
	MetricQualityPassRate = "genrec_quality_pass_rate"
	// MetricCostYuanTotal 费用累计（counter，元）。
	MetricCostYuanTotal = "genrec_cost_yuan_total"
	// MetricCostTokensTotal token 累计（counter，label: type）。
	MetricCostTokensTotal = "genrec_cost_tokens_total"
	// MetricCFHitRate 协同过滤命中率（gauge）。
	MetricCFHitRate = "genrec_cf_hit_rate"
	// MetricRerankWinRate 重排胜率（gauge）。
	MetricRerankWinRate = "genrec_rerank_win_rate"
	// MetricGraphQueryLatencyMs 图谱查询延迟（histogram，毫秒）。
	MetricGraphQueryLatencyMs = "genrec_graph_query_latency_ms"
	// MetricGraphNodeCount 图谱节点数（gauge）。
	MetricGraphNodeCount = "genrec_graph_node_count"
	// MetricChannelDistribution 频道分布（counter，label: channel）。
	MetricChannelDistribution = "genrec_channel_distribution"
	// MetricCPUUtilization CPU 利用率（gauge）。
	MetricCPUUtilization = "genrec_cpu_utilization"
	// MetricProfileDecayScore 画像衰减分（histogram）。
	MetricProfileDecayScore = "genrec_profile_decay_score"
	// MetricPromptCacheHitRatio Prompt Cache 命中率（gauge）。
	MetricPromptCacheHitRatio = "genrec_prompt_cache_hit_ratio"
	// MetricFallbackRate 兜底率（gauge）。
	MetricFallbackRate = "genrec_fallback_rate"
	// MetricCTR 点击率（gauge）。
	MetricCTR = "genrec_ctr"
	// MetricCVR 转化率（gauge）。
	MetricCVR = "genrec_cvr"
	// MetricAgentDecisionCount Agent 决策次数（counter，label: agent）。
	MetricAgentDecisionCount = "genrec_agent_decision_count"
)

// DefaultLatencyBuckets 推荐延迟默认桶（毫秒）。
var DefaultLatencyBuckets = []float64{10, 50, 100, 200, 500, 1000, 2000, 5000}

// DefaultDecayBuckets 画像衰减分默认桶。
var DefaultDecayBuckets = []float64{0, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0}

// ----------------------------------------------------------------------------
// Counter
// ----------------------------------------------------------------------------

// Counter 累加型指标。
//
// 字段语义：
//   - name：指标名
//   - value：当前累计值
//   - labels：标签键值对
type Counter struct {
	name   string
	value  int64
	labels map[string]string
	mu     sync.Mutex
}

// Inc 递增 1。
func (c *Counter) Inc() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.value++
	c.mu.Unlock()
}

// Add 累加 n（可为负）。
func (c *Counter) Add(n int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.value += n
	c.mu.Unlock()
}

// Value 返回当前累计值。
func (c *Counter) Value() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

// Name 返回指标名。
func (c *Counter) Name() string {
	if c == nil {
		return ""
	}
	return c.name
}

// Labels 返回标签副本。
func (c *Counter) Labels() map[string]string {
	if c == nil {
		return nil
	}
	out := make(map[string]string, len(c.labels))
	for k, v := range c.labels {
		out[k] = v
	}
	return out
}

// ----------------------------------------------------------------------------
// Gauge
// ----------------------------------------------------------------------------

// Gauge 瞬时型指标。
//
// 字段语义：
//   - name：指标名
//   - value：当前值
//   - labels：标签键值对
type Gauge struct {
	name   string
	value  float64
	labels map[string]string
	mu     sync.Mutex
}

// Set 设置当前值。
func (g *Gauge) Set(v float64) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.value = v
	g.mu.Unlock()
}

// Add 增加增量（可为负）。
func (g *Gauge) Add(v float64) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.value += v
	g.mu.Unlock()
}

// Value 返回当前值。
func (g *Gauge) Value() float64 {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.value
}

// Name 返回指标名。
func (g *Gauge) Name() string {
	if g == nil {
		return ""
	}
	return g.name
}

// Labels 返回标签副本。
func (g *Gauge) Labels() map[string]string {
	if g == nil {
		return nil
	}
	out := make(map[string]string, len(g.labels))
	for k, v := range g.labels {
		out[k] = v
	}
	return out
}

// ----------------------------------------------------------------------------
// Histogram
// ----------------------------------------------------------------------------

// Histogram 桶分布型指标。
//
// 字段语义：
//   - name：指标名
//   - buckets：桶上界（升序）
//   - counts：各桶累计观测数（counts[i] 为 <= buckets[i] 的累计数）
//   - sum：观测值总和
//   - count：观测总次数
type Histogram struct {
	name    string
	buckets []float64
	counts  []int64
	sum     float64
	count   int64
	mu      sync.Mutex
}

// Observe 记录一次观测值 v。
func (h *Histogram) Observe(v float64) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sum += v
	h.count++
	for i, ub := range h.buckets {
		if v <= ub {
			h.counts[i]++
		}
	}
}

// Mean 返回均值（count 为 0 时返回 0）。
func (h *Histogram) Mean() float64 {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 {
		return 0
	}
	return h.sum / float64(h.count)
}

// Count 返回观测总次数。
func (h *Histogram) Count() int64 {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

// Sum 返回观测值总和。
func (h *Histogram) Sum() float64 {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sum
}

// Buckets 返回桶上界副本。
func (h *Histogram) Buckets() []float64 {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]float64, len(h.buckets))
	copy(out, h.buckets)
	return out
}

// Counts 返回各桶累计观测数副本。
func (h *Histogram) Counts() []int64 {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]int64, len(h.counts))
	copy(out, h.counts)
	return out
}

// Name 返回指标名。
func (h *Histogram) Name() string {
	if h == nil {
		return ""
	}
	return h.name
}

// ----------------------------------------------------------------------------
// MetricsRegistry
// ----------------------------------------------------------------------------

// MetricsRegistry 指标注册中心，统一管理 Counter/Gauge/Histogram。
//
// 字段语义：
//   - counters：按 name 索引的 Counter 列表（同名多标签允许多实例）
//   - gauges：按 name 索引的 Gauge 列表
//   - histograms：按 name 索引的 Histogram 列表
type MetricsRegistry struct {
	counters   map[string][]*Counter
	gauges     map[string][]*Gauge
	histograms map[string][]*Histogram
	mu         sync.RWMutex
}

// NewMetricsRegistry 构造 MetricsRegistry。
func NewMetricsRegistry() *MetricsRegistry {
	return &MetricsRegistry{
		counters:   make(map[string][]*Counter),
		gauges:     make(map[string][]*Gauge),
		histograms: make(map[string][]*Histogram),
	}
}

// RegisterCounter 注册 Counter，返回新实例。
// 同名多标签可注册多次（每次返回独立实例）。
func (r *MetricsRegistry) RegisterCounter(name string, labels map[string]string) *Counter {
	if r == nil {
		return nil
	}
	c := &Counter{name: name, labels: copyLabels(labels)}
	r.mu.Lock()
	r.counters[name] = append(r.counters[name], c)
	r.mu.Unlock()
	return c
}

// RegisterGauge 注册 Gauge，返回新实例。
func (r *MetricsRegistry) RegisterGauge(name string, labels map[string]string) *Gauge {
	if r == nil {
		return nil
	}
	g := &Gauge{name: name, labels: copyLabels(labels)}
	r.mu.Lock()
	r.gauges[name] = append(r.gauges[name], g)
	r.mu.Unlock()
	return g
}

// RegisterHistogram 注册 Histogram，返回新实例。
// buckets 为桶上界（升序），为空时使用 DefaultLatencyBuckets。
func (r *MetricsRegistry) RegisterHistogram(name string, buckets []float64) *Histogram {
	if r == nil {
		return nil
	}
	if len(buckets) == 0 {
		buckets = DefaultLatencyBuckets
	}
	// 复制 buckets 避免外部修改
	bc := make([]float64, len(buckets))
	copy(bc, buckets)
	h := &Histogram{
		name:    name,
		buckets: bc,
		counts:  make([]int64, len(bc)),
	}
	r.mu.Lock()
	r.histograms[name] = append(r.histograms[name], h)
	r.mu.Unlock()
	return h
}

// GetCounter 返回指定 name 的第一个 Counter（无则 nil）。
func (r *MetricsRegistry) GetCounter(name string) *Counter {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if cs := r.counters[name]; len(cs) > 0 {
		return cs[0]
	}
	return nil
}

// GetOrCreateCounter 查找 name 下标签完全匹配的 Counter，未命中则注册新实例。
// 用于 label 维度指标（如 path_distribution{path=fast}）的聚合采集。
func (r *MetricsRegistry) GetOrCreateCounter(name string, labels map[string]string) *Counter {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.counters[name] {
		if labelsEqual(c.labels, labels) {
			return c
		}
	}
	c := &Counter{name: name, labels: copyLabels(labels)}
	r.counters[name] = append(r.counters[name], c)
	return c
}

// labelsEqual 比较两组 labels 是否相等（均 nil 视为相等）。
func labelsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// GetGauge 返回指定 name 的第一个 Gauge（无则 nil）。
func (r *MetricsRegistry) GetGauge(name string) *Gauge {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if gs := r.gauges[name]; len(gs) > 0 {
		return gs[0]
	}
	return nil
}

// GetHistogram 返回指定 name 的第一个 Histogram（无则 nil）。
func (r *MetricsRegistry) GetHistogram(name string) *Histogram {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if hs := r.histograms[name]; len(hs) > 0 {
		return hs[0]
	}
	return nil
}

// CounterNames 返回所有已注册 counter 名（排序去重）。
func (r *MetricsRegistry) CounterNames() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return sortedMapKeys(r.counters)
}

// GaugeNames 返回所有已注册 gauge 名（排序去重）。
func (r *MetricsRegistry) GaugeNames() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.gauges))
	for k := range r.gauges {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// HistogramNames 返回所有已注册 histogram 名（排序去重）。
func (r *MetricsRegistry) HistogramNames() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.histograms))
	for k := range r.histograms {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// HasMetric 返回是否注册过指定名（任意类型）。
func (r *MetricsRegistry) HasMetric(name string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.counters[name]; ok {
		return true
	}
	if _, ok := r.gauges[name]; ok {
		return true
	}
	if _, ok := r.histograms[name]; ok {
		return true
	}
	return false
}

// Format 输出 Prometheus exposition format。
//
// 格式：
//
//	# TYPE <name> counter
//	<name>{labels} <value>
//	# TYPE <name> gauge
//	<name>{labels} <value>
//	# TYPE <name> histogram
//	<name>_bucket{le="<ub>"} <count>
//	<name>_bucket{le="+Inf"} <count>
//	<name>_sum <sum>
//	<name>_count <count>
func (r *MetricsRegistry) Format() string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var b strings.Builder
	// counters（按 name 排序保证输出稳定）
	for _, name := range sortedMapKeys(r.counters) {
		b.WriteString(fmt.Sprintf("# TYPE %s counter\n", name))
		for _, c := range r.counters[name] {
			b.WriteString(fmt.Sprintf("%s%s %d\n", name, formatLabels(c.labels), c.Value()))
		}
	}
	// gauges
	for _, name := range sortedMapKeys(r.gauges) {
		b.WriteString(fmt.Sprintf("# TYPE %s gauge\n", name))
		for _, g := range r.gauges[name] {
			b.WriteString(fmt.Sprintf("%s%s %s\n", name, formatLabels(g.labels), formatFloat(g.Value())))
		}
	}
	// histograms
	for _, name := range sortedMapKeys(r.histograms) {
		b.WriteString(fmt.Sprintf("# TYPE %s histogram\n", name))
		for _, h := range r.histograms[name] {
			h.formatExposition(&b)
		}
	}
	return b.String()
}

// formatExposition 将 histogram 写入 Prometheus exposition 格式。
func (h *Histogram) formatExposition(b *strings.Builder) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, ub := range h.buckets {
		b.WriteString(fmt.Sprintf("%s_bucket{le=%q} %d\n", h.name, formatLe(ub), h.counts[i]))
	}
	b.WriteString(fmt.Sprintf("%s_bucket{le=\"+Inf\"} %d\n", h.name, h.count))
	b.WriteString(fmt.Sprintf("%s_sum %s\n", h.name, formatFloat(h.sum)))
	b.WriteString(fmt.Sprintf("%s_count %d\n", h.name, h.count))
}

// ----------------------------------------------------------------------------
// RegisterDefaultMetrics — 预定义 20 个推荐核心指标
// ----------------------------------------------------------------------------

// RegisterDefaultMetrics 注册 20 个推荐核心指标。
//
// 返回创建的 Counter/Gauge/Histogram 句柄集合 DefaultMetrics，便于后续
// 业务代码（如 BusinessCollector）直接操作。
func (r *MetricsRegistry) RegisterDefaultMetrics() *DefaultMetrics {
	if r == nil {
		return nil
	}
	dm := &DefaultMetrics{}
	dm.RecommendTotal = r.RegisterCounter(MetricRecommendTotal, nil)
	dm.RecommendLatencyMs = r.RegisterHistogram(MetricRecommendLatencyMs, DefaultLatencyBuckets)
	dm.PathDistribution = r.RegisterCounter(MetricPathDistribution, map[string]string{"path": ""})
	dm.RecallCount = r.RegisterGauge(MetricRecallCount, nil)
	dm.RerankCount = r.RegisterGauge(MetricRerankCount, nil)
	dm.QualityPassRate = r.RegisterGauge(MetricQualityPassRate, nil)
	dm.CostYuanTotal = r.RegisterCounter(MetricCostYuanTotal, nil)
	dm.CostTokensTotal = r.RegisterCounter(MetricCostTokensTotal, map[string]string{"type": ""})
	dm.CFHitRate = r.RegisterGauge(MetricCFHitRate, nil)
	dm.RerankWinRate = r.RegisterGauge(MetricRerankWinRate, nil)
	dm.GraphQueryLatencyMs = r.RegisterHistogram(MetricGraphQueryLatencyMs, DefaultLatencyBuckets)
	dm.GraphNodeCount = r.RegisterGauge(MetricGraphNodeCount, nil)
	dm.ChannelDistribution = r.RegisterCounter(MetricChannelDistribution, map[string]string{"channel": ""})
	dm.CPUUtilization = r.RegisterGauge(MetricCPUUtilization, nil)
	dm.ProfileDecayScore = r.RegisterHistogram(MetricProfileDecayScore, DefaultDecayBuckets)
	dm.PromptCacheHitRatio = r.RegisterGauge(MetricPromptCacheHitRatio, nil)
	dm.FallbackRate = r.RegisterGauge(MetricFallbackRate, nil)
	dm.CTR = r.RegisterGauge(MetricCTR, nil)
	dm.CVR = r.RegisterGauge(MetricCVR, nil)
	dm.AgentDecisionCount = r.RegisterCounter(MetricAgentDecisionCount, map[string]string{"agent": ""})
	return dm
}

// DefaultMetrics 承载 RegisterDefaultMetrics 创建的指标句柄。
//
// 业务代码（如 BusinessCollector）通过该结构直接操作预定义指标，
// 避免每次 GetCounter/GetGauge 查找。
type DefaultMetrics struct {
	RecommendTotal      *Counter
	RecommendLatencyMs  *Histogram
	PathDistribution    *Counter
	RecallCount         *Gauge
	RerankCount         *Gauge
	QualityPassRate     *Gauge
	CostYuanTotal       *Counter
	CostTokensTotal     *Counter
	CFHitRate           *Gauge
	RerankWinRate       *Gauge
	GraphQueryLatencyMs *Histogram
	GraphNodeCount      *Gauge
	ChannelDistribution *Counter
	CPUUtilization      *Gauge
	ProfileDecayScore   *Histogram
	PromptCacheHitRatio *Gauge
	FallbackRate        *Gauge
	CTR                 *Gauge
	CVR                 *Gauge
	AgentDecisionCount  *Counter
}

// ----------------------------------------------------------------------------
// RecordRecommend — 从推荐响应记录指标
// ----------------------------------------------------------------------------

// RecordRecommend 从推荐响应记录核心指标。
//
// 记录内容：
//   - genrec_recommend_total +1
//   - genrec_path_distribution{path=<PathTaken>} +1
//   - genrec_recall_count = len(Candidates)
//   - genrec_cost_yuan_total += Cost.EstimatedCost
//   - genrec_cost_tokens_total{type=input} += TokensIn
//   - genrec_cost_tokens_total{type=output} += TokensOut
//   - genrec_cost_tokens_total{type=cached} += CachedTokens
//   - genrec_channel_distribution{channel=<Channel>} +1
//   - genrec_graph_node_count（若 GraphTrace 非空）
//
// ctx 当前未使用（预留 trace 关联）。
func (r *MetricsRegistry) RecordRecommend(_ context.Context, response domain.RecommendResponse) {
	if r == nil {
		return
	}
	if c := r.GetCounter(MetricRecommendTotal); c != nil {
		c.Inc()
	}
	if c := r.GetCounter(MetricPathDistribution); c != nil {
		c.Add(1)
		// 通过 label 标记 path（同名 counter 第一实例用于累计，path 维度由调用方
		// 通过 BusinessCollector.CollectRecommend 细化）
		_ = response.PathTaken
	}
	if g := r.GetGauge(MetricRecallCount); g != nil {
		g.Set(float64(len(response.Candidates)))
	}
	if c := r.GetCounter(MetricCostYuanTotal); c != nil {
		c.Add(int64(response.Cost.EstimatedCost * 1e6))
	}
	if c := r.GetCounter(MetricCostTokensTotal); c != nil {
		c.Add(int64(response.Cost.TokensIn + response.Cost.TokensOut + response.Cost.CachedTokens))
	}
	if response.Channel != "" {
		if c := r.GetCounter(MetricChannelDistribution); c != nil {
			c.Add(1)
		}
	}
	if response.GraphTrace != nil {
		if g := r.GetGauge(MetricGraphNodeCount); g != nil {
			g.Set(float64(response.GraphTrace.Results))
		}
		if h := r.GetHistogram(MetricGraphQueryLatencyMs); h != nil {
			h.Observe(float64(response.GraphTrace.Duration.Milliseconds()))
		}
	}
	if g := r.GetGauge(MetricRerankCount); g != nil {
		g.Set(float64(len(response.RerankScores)))
	}
}

// ----------------------------------------------------------------------------
// 辅助函数
// ----------------------------------------------------------------------------

// copyLabels 复制 labels map，避免外部修改。
func copyLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// formatLabels 格式化为 Prometheus label 字符串（{k="v",...}），无标签返回空。
func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(labels[k])
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// formatLe 格式化 histogram 桶上界（+Inf 由调用方处理）。
func formatLe(v float64) string {
	// 整数则省略小数点
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return formatFloat(v)
}

// formatFloat 格式化浮点数，整数省略小数点。
func formatFloat(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%g", v)
}

// sortedMapKeys 返回排序后的 map 键名（泛型版本，避免引入 constraints）。
func sortedMapKeys[V any](m map[string][]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
