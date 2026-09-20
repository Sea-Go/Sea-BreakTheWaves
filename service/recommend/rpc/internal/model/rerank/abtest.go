package rerank

import (
	"context"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"sea/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件实现 A/B 测试与外部 rerank 对比（Task 8.7）。
//
// 核心能力：
//   - ABTest：A/B 分桶对比，基于 userID hash 分配 self/external 桶
//   - 按桶调用自研 Reranker（domain.Reranker）或外部 ExternalReranker
//   - 记录曝光/点击/延迟/成本，实时计算 CTR 与 self 胜率
//   - 监控指标上报（genrec_rerank_self_win_rate 等）
//
// 二开扩展点：
//   - 分桶策略：替换 AssignBucket（如一致性哈希/灰度名单/用户标签分桶）
//   - 胜率计算：替换 ComputeWinRate（如统计显著性检验 t-test/卡方）
//   - 监控后端：实现 MetricsRecorder interface 注入 Prometheus/自定义监控
// ============================================================================

// 监控指标名常量。
const (
	// metricSelfWinRate self 胜率 Gauge。
	metricSelfWinRate = "genrec_rerank_self_win_rate"
	// metricSelfCostTotal self 重排累计成本 Counter。
	metricSelfCostTotal = "genrec_rerank_self_cost_total"
	// metricExternalCostTotal external 重排累计成本 Counter。
	metricExternalCostTotal = "genrec_rerank_external_cost_total"
)

// 桶名常量。
const (
	bucketSelf     = "self"
	bucketExternal = "external"
)

// MetricsRecorder 监控指标记录器抽象。
//
// 二开扩展点：实现该 interface 注入 Prometheus CounterVec/GaugeVec 或
// 自定义监控后端（如 OpenTSDB/Datadog）。nil 时跳过监控上报。
type MetricsRecorder interface {
	// RecordCounter 记录计数器（累加值，如成本）。
	RecordCounter(name string, value float64, tags map[string]string)
	// RecordGauge 记录仪表盘（当前值，如胜率）。
	RecordGauge(name string, value float64, tags map[string]string)
}

// ExternalReranker 外部 rerank 抽象（如 DashScope/Cohere/Jina rerank API）。
//
// 与 domain.Reranker 不同，ExternalReranker 接收 query + candidates + topK，
// 返回排序后的候选列表，供 ABTest 对比自研与外部效果。
// server.go 的 Server 与本文件 ABTest 均依赖该 interface（统一定义于此，避免重复声明）。
//
// 二开扩展点：实现该 interface 接入外部 rerank 服务（如 DashScope/Cohere/Jina）。
type ExternalReranker interface {
	// Rerank 调用外部 rerank 服务。
	Rerank(ctx context.Context, query string, candidates []domain.Candidate, topK int) ([]domain.Candidate, error)
}

// ABMetrics A/B 测试对比指标快照。
//
// 字段说明：
//   - SelfCTR/ExternalCTR：按文章 ID 的 CTR（clicks/impressions）
//   - SelfLatency/ExternalLatency：按文章 ID 的平均重排延迟（秒）
//   - SelfCost/ExternalCost：累计重排成本（元）
//   - SelfWinRate：self 胜率（self CTR > external CTR 的文章占比）
type ABMetrics struct {
	// SelfCTR self 桶按文章 ID 的 CTR。
	SelfCTR map[string]float64
	// ExternalCTR external 桶按文章 ID 的 CTR。
	ExternalCTR map[string]float64
	// SelfLatency self 桶按文章 ID 的平均重排延迟（秒）。
	SelfLatency map[string]float64
	// ExternalLatency external 桶按文章 ID 的平均重排延迟（秒）。
	ExternalLatency map[string]float64
	// SelfCost self 桶累计重排成本（元）。
	SelfCost float64
	// ExternalCost external 桶累计重排成本（元）。
	ExternalCost float64
	// SelfWinRate self 胜率（0-1）。
	SelfWinRate float64
}

// ABTest A/B 分桶对比，基于 userID hash 分配 self/external 桶。
//
// 工作流程：
//  1. AssignBucket(userID) → "self" / "external"
//  2. Rerank 按桶调用 selfReranker 或 externalReranker
//  3. RecordOutcome 记录曝光/点击/延迟/成本
//  4. ComputeWinRate 计算 self 胜率
//
// 二开扩展点：
//   - 分桶策略：替换 AssignBucket 实现一致性哈希/灰度名单
//   - 胜率计算：替换 ComputeWinRate 实现统计显著性检验
type ABTest struct {
	// selfReranker 自研 reranker（domain.Reranker 实现，如 SelfReranker）。
	selfReranker domain.Reranker
	// externalReranker 外部 reranker（如 DashScope）。
	externalReranker ExternalReranker
	// bucketRatio self 桶比例（0-1），默认 0.5。
	bucketRatio float64
	// metrics 当前 A/B 指标快照。
	metrics ABMetrics

	// counters 内部累积计数（不导出，用于 CTR/延迟均值/胜率计算）。
	counters abCounters

	mu       sync.RWMutex
	recorder MetricsRecorder
}

// abCounters 内部累积计数（不导出）。
type abCounters struct {
	selfClicks           map[string]int
	selfImpressions      map[string]int
	externalClicks       map[string]int
	externalImpressions  map[string]int
	selfLatencySum       map[string]time.Duration
	externalLatencySum   map[string]time.Duration
	selfLatencyCount     map[string]int
	externalLatencyCount map[string]int
}

// NewABTest 创建 A/B 测试。
//
// self 自研 reranker（domain.Reranker 实现）；external 外部 reranker；
// bucketRatio self 桶比例（0-1），超出 (0,1) 范围默认 0.5（50% self / 50% external）。
func NewABTest(self domain.Reranker, external ExternalReranker, bucketRatio float64) *ABTest {
	if bucketRatio <= 0 || bucketRatio >= 1 {
		bucketRatio = 0.5
	}
	return &ABTest{
		selfReranker:     self,
		externalReranker: external,
		bucketRatio:      bucketRatio,
		metrics: ABMetrics{
			SelfCTR:         make(map[string]float64),
			ExternalCTR:     make(map[string]float64),
			SelfLatency:     make(map[string]float64),
			ExternalLatency: make(map[string]float64),
		},
		counters: abCounters{
			selfClicks:           make(map[string]int),
			selfImpressions:      make(map[string]int),
			externalClicks:       make(map[string]int),
			externalImpressions:  make(map[string]int),
			selfLatencySum:       make(map[string]time.Duration),
			externalLatencySum:   make(map[string]time.Duration),
			selfLatencyCount:     make(map[string]int),
			externalLatencyCount: make(map[string]int),
		},
	}
}

// SetMetricsRecorder 注入监控指标记录器（可选，nil 时跳过监控上报）。
func (ab *ABTest) SetMetricsRecorder(r MetricsRecorder) {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	ab.recorder = r
}

// SetBucketRatio 调整 self 桶比例（0-1），超出范围忽略。
// 二开：用于动态调整流量分配（如初期 5% self 灰度，逐步放量到 50%）。
func (ab *ABTest) SetBucketRatio(ratio float64) {
	if ratio <= 0 || ratio >= 1 {
		return
	}
	ab.mu.Lock()
	defer ab.mu.Unlock()
	ab.bucketRatio = ratio
}

// AssignBucket 基于 userID hash 分桶。
//
// 使用 FNV-1a hash 将 userID 映射到 [0,1)，< bucketRatio 分配到 "self"，
// 否则分配到 "external"。同一 userID 桶分配稳定（hash 决定性）。
//
// 二开扩展点：替换为一致性哈希/灰度名单/用户标签分桶等策略。
func (ab *ABTest) AssignBucket(userID string) string {
	ab.mu.RLock()
	ratio := ab.bucketRatio
	ab.mu.RUnlock()

	h := fnv.New32a()
	_, _ = h.Write([]byte(userID))
	// 归一化到 [0, 1)
	v := float64(h.Sum32()) / float64(^uint32(0))
	if v < ratio {
		return bucketSelf
	}
	return bucketExternal
}

// Rerank 根据用户分桶调用 self/external reranker。
//
// 流程：
//  1. AssignBucket(userID) 确定桶
//  2. self 桶 → selfReranker.Rerank
//  3. external 桶 → externalReranker.Rerank（转 RerankResult）
func (ab *ABTest) Rerank(ctx context.Context, req domain.RerankRequest) (domain.RerankResult, error) {
	bucket := ab.AssignBucket(req.UserKey.UserID)

	ab.mu.RLock()
	self := ab.selfReranker
	external := ab.externalReranker
	ab.mu.RUnlock()

	if bucket == bucketSelf {
		if self == nil {
			return domain.RerankResult{}, fmt.Errorf("ab test: self reranker not configured")
		}
		return self.Rerank(ctx, req)
	}

	// external 桶
	if external == nil {
		return domain.RerankResult{}, fmt.Errorf("ab test: external reranker not configured")
	}
	cands, err := external.Rerank(ctx, req.Query, req.Candidates, req.TopK)
	if err != nil {
		return domain.RerankResult{}, err
	}
	return domain.RerankResult{Candidates: cands, ModelUsed: bucketExternal}, nil
}

// ExternalRerank 显式调用 external reranker（不做分桶），供 SelfReranker 的 "external"
// 模型路径与 RerankTools.ExternalRerank 工具调用。
func (ab *ABTest) ExternalRerank(ctx context.Context, query string, candidates []domain.Candidate, topK int) ([]domain.Candidate, error) {
	ab.mu.RLock()
	external := ab.externalReranker
	ab.mu.RUnlock()
	if external == nil {
		return nil, fmt.Errorf("ab test: external reranker not configured")
	}
	return external.Rerank(ctx, query, candidates, topK)
}

// RecordOutcome 记录单次曝光/点击结果。
//
// bucket "self"/"external"；articleID 文章 ID；clicked 是否点击；
// latency 重排耗时；cost 单次重排成本（元）。
//
// 调用后实时更新 CTR/延迟/胜率并上报监控指标。
func (ab *ABTest) RecordOutcome(bucket, articleID string, clicked bool, latency time.Duration, cost float64) {
	ab.mu.Lock()
	defer ab.mu.Unlock()

	switch bucket {
	case bucketSelf:
		ab.counters.selfImpressions[articleID]++
		if clicked {
			ab.counters.selfClicks[articleID]++
		}
		ab.counters.selfLatencySum[articleID] += latency
		ab.counters.selfLatencyCount[articleID]++
		ab.metrics.SelfCost += cost
	case bucketExternal:
		ab.counters.externalImpressions[articleID]++
		if clicked {
			ab.counters.externalClicks[articleID]++
		}
		ab.counters.externalLatencySum[articleID] += latency
		ab.counters.externalLatencyCount[articleID]++
		ab.metrics.ExternalCost += cost
	default:
		return
	}

	ab.recomputeLocked()
	ab.recordMetricsLocked()
}

// recomputeLocked 重算 CTR/延迟/胜率，调用方持锁。
func (ab *ABTest) recomputeLocked() {
	c := &ab.counters
	m := &ab.metrics

	for id, imp := range c.selfImpressions {
		if imp > 0 {
			m.SelfCTR[id] = float64(c.selfClicks[id]) / float64(imp)
		}
		cnt := c.selfLatencyCount[id]
		if cnt > 0 {
			m.SelfLatency[id] = float64(c.selfLatencySum[id].Seconds()) / float64(cnt)
		}
	}
	for id, imp := range c.externalImpressions {
		if imp > 0 {
			m.ExternalCTR[id] = float64(c.externalClicks[id]) / float64(imp)
		}
		cnt := c.externalLatencyCount[id]
		if cnt > 0 {
			m.ExternalLatency[id] = float64(c.externalLatencySum[id].Seconds()) / float64(cnt)
		}
	}
	m.SelfWinRate = ab.computeWinRateLocked()
}

// computeWinRateLocked 计算 self 胜率（CTR 对比），调用方持锁。
//
// 对每个有 self/external 双侧曝光的文章，比较 CTR：
//   - selfCTR > externalCTR → self 胜 1 场
//   - selfCTR <= externalCTR → self 不胜
//
// SelfWinRate = selfWins / totalCompared。
//
// 二开扩展点：替换为统计显著性检验（如 t-test/卡方），避免小样本误判。
func (ab *ABTest) computeWinRateLocked() float64 {
	var selfWins, total int
	for id, selfCTR := range ab.metrics.SelfCTR {
		extCTR, ok := ab.metrics.ExternalCTR[id]
		if !ok {
			continue
		}
		total++
		if selfCTR > extCTR {
			selfWins++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(selfWins) / float64(total)
}

// ComputeWinRate 计算 self 胜率（线程安全）。
func (ab *ABTest) ComputeWinRate() float64 {
	ab.mu.RLock()
	defer ab.mu.RUnlock()
	return ab.computeWinRateLocked()
}

// GetMetrics 获取当前 A/B 指标快照（深拷贝，避免外部修改内部状态）。
func (ab *ABTest) GetMetrics() ABMetrics {
	ab.mu.RLock()
	defer ab.mu.RUnlock()
	return ab.cloneMetricsLocked()
}

// cloneMetricsLocked 克隆指标快照（调用方持锁）。
func (ab *ABTest) cloneMetricsLocked() ABMetrics {
	src := ab.metrics
	return ABMetrics{
		SelfCTR:         cloneFloatMap(src.SelfCTR),
		ExternalCTR:     cloneFloatMap(src.ExternalCTR),
		SelfLatency:     cloneFloatMap(src.SelfLatency),
		ExternalLatency: cloneFloatMap(src.ExternalLatency),
		SelfCost:        src.SelfCost,
		ExternalCost:    src.ExternalCost,
		SelfWinRate:     src.SelfWinRate,
	}
}

// cloneFloatMap 深拷贝 map[string]float64。
func cloneFloatMap(m map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// recordMetricsLocked 上报监控指标（调用方持锁）。
func (ab *ABTest) recordMetricsLocked() {
	if ab.recorder == nil {
		return
	}
	ab.recorder.RecordGauge(metricSelfWinRate, ab.metrics.SelfWinRate, nil)
	ab.recorder.RecordCounter(metricSelfCostTotal, ab.metrics.SelfCost, nil)
	ab.recorder.RecordCounter(metricExternalCostTotal, ab.metrics.ExternalCost, nil)
}
