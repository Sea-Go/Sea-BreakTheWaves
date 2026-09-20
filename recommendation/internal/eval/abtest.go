// internal/eval/abtest.go — 在线 A/B 测试（Task 15.6）。
//
// 职责：
//   - 实现 ABTestService 管理在线 A/B 实验
//   - CreateExperiment 创建实验（含多个 Variant 与权重）
//   - AssignVariant 按 FNV-1a hash（experimentID + userID）分桶，加权采样
//   - RecordMetric 记录曝光/点击/转化/延迟/成本事件
//   - GetResults 返回实验对比报告（CTR/CVR/延迟/成本）
//   - CompareVariants 用 Z-test 检验显著性，输出 winner 与 confidence
//
// 二开扩展点：
//   - 替换 hash 函数（如改用 murmur3）
//   - 调整 confidenceThreshold 改变显著性门槛
//   - 在 GetResults 中追加自定义业务指标
package eval

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"sync"
	"time"
)

// ----------------------------------------------------------------------------
// 类型定义
// ----------------------------------------------------------------------------

// ExperimentStatus 实验状态。
type ExperimentStatus string

const (
	// ExperimentStatusRunning 实验运行中。
	ExperimentStatusRunning ExperimentStatus = "running"
	// ExperimentStatusStopped 实验已停止。
	ExperimentStatusStopped ExperimentStatus = "stopped"
)

// MetricEventType 指标事件类型。
type MetricEventType string

const (
	// MetricEventImpression 曝光事件。
	MetricEventImpression MetricEventType = "impression"
	// MetricEventClick 点击事件。
	MetricEventClick MetricEventType = "click"
	// MetricEventConversion 转化事件。
	MetricEventConversion MetricEventType = "conversion"
)

// Experiment A/B 实验定义。
//
// 字段语义：
//   - ID：实验 ID
//   - Name：实验名称
//   - Variants：变体列表（含 Weight）
//   - Metrics：变体 ID → 指标聚合（运行时维护）
//   - Status：状态（running/stopped）
//   - StartedAt/EndedAt：起止时间（Unix 秒）
type Experiment struct {
	// ID 实验 ID。
	ID string `json:"id"`
	// Name 实验名称。
	Name string `json:"name"`
	// Variants 变体列表。
	Variants []Variant `json:"variants"`
	// Metrics 变体 ID → 指标聚合（运行时维护，不序列化）。
	Metrics map[string]*VariantMetrics `json:"-"`
	// Status 实验状态。
	Status string `json:"status"`
	// StartedAt 开始时间。
	StartedAt int64 `json:"started_at"`
	// EndedAt 结束时间。
	EndedAt int64 `json:"ended_at"`
}

// Variant 实验变体。
//
// 字段语义：
//   - ID：变体 ID
//   - Name：变体名称（如 "control"/"treatment"）
//   - Weight：分桶权重（>0）
//   - Config：变体配置（如不同的 RecommendConfig）
type Variant struct {
	// ID 变体 ID。
	ID string `json:"id"`
	// Name 变体名称。
	Name string `json:"name"`
	// Weight 分桶权重。
	Weight int `json:"weight"`
	// Config 变体配置。
	Config map[string]any `json:"config"`
}

// VariantMetrics 单变体的指标聚合。
//
// 字段语义：
//   - Impressions/Clicks/Conversions：原始计数
//   - LatencyMs：累计延迟（用于计算平均延迟）
//   - CostYuan：累计成本（元）
//   - CTR/CVR：派生指标（点击率/转化率）
type VariantMetrics struct {
	// Impressions 曝光数。
	Impressions int64 `json:"impressions"`
	// Clicks 点击数。
	Clicks int64 `json:"clicks"`
	// Conversions 转化数。
	Conversions int64 `json:"conversions"`
	// LatencyMs 累计延迟（毫秒）。
	LatencyMs int64 `json:"latency_ms"`
	// CostYuan 累计成本（元）。
	CostYuan float64 `json:"cost_yuan"`
	// CTR 点击率（Clicks/Impressions）。
	CTR float64 `json:"ctr"`
	// CVR 转化率（Conversions/Clicks）。
	CVR float64 `json:"cvr"`
}

// MetricEvent 指标事件。
type MetricEvent struct {
	// Type 事件类型（impression/click/conversion）。
	Type string `json:"type"`
	// VariantID 变体 ID。
	VariantID string `json:"variant_id"`
	// UserID 用户 ID。
	UserID string `json:"user_id"`
	// LatencyMs 延迟（毫秒，仅 impression 事件携带）。
	LatencyMs int64 `json:"latency_ms"`
	// CostYuan 成本（元，仅 impression 事件携带）。
	CostYuan float64 `json:"cost_yuan"`
}

// ABTestReport A/B 测试对比报告。
type ABTestReport struct {
	// ExperimentID 实验 ID。
	ExperimentID string `json:"experiment_id"`
	// Variants 各变体结果。
	Variants []VariantResult `json:"variants"`
	// Winner 胜出变体 ID（无显著差异时为空）。
	Winner string `json:"winner"`
	// Confidence 置信度（0-1，1 表示 100% 显著）。
	Confidence float64 `json:"confidence"`
}

// VariantResult 单变体测试结果。
type VariantResult struct {
	// VariantID 变体 ID。
	VariantID string `json:"variant_id"`
	// Metrics 指标聚合。
	Metrics VariantMetrics `json:"metrics"`
	// WinRate 胜率（0-1）。
	WinRate float64 `json:"win_rate"`
	// Sample 样本数（即 Impressions）。
	Sample int `json:"sample"`
}

// ----------------------------------------------------------------------------
// ABTestService
// ----------------------------------------------------------------------------

// confidenceThreshold 显著性置信度阈值（Z-score > 1.96 → 95% 显著）。
const confidenceThreshold = 1.96

// ABTestService A/B 测试服务。
//
// 二开扩展点：替换 hash 函数；调整显著性阈值；扩展指标计算。
type ABTestService struct {
	mu        sync.RWMutex
	buckets   map[string]*Experiment
}

// NewABTestService 构造 ABTestService。
func NewABTestService() *ABTestService {
	return &ABTestService{buckets: make(map[string]*Experiment)}
}

// CreateExperiment 创建实验。
// 校验：ID 非空、至少 2 个 Variant、Weight 之和 > 0。
// 已存在同 ID 实验返回错误。
func (s *ABTestService) CreateExperiment(_ context.Context, exp Experiment) error {
	if exp.ID == "" {
		return fmt.Errorf("experiment id 不能为空")
	}
	if len(exp.Variants) < 2 {
		return fmt.Errorf("至少需要 2 个 variant, 实际 %d", len(exp.Variants))
	}
	totalWeight := 0
	for _, v := range exp.Variants {
		if v.Weight <= 0 {
			return fmt.Errorf("variant %s weight 必须 > 0, 实际 %d", v.ID, v.Weight)
		}
		totalWeight += v.Weight
	}
	if totalWeight == 0 {
		return fmt.Errorf("weight 之和必须 > 0")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.buckets[exp.ID]; exists {
		return fmt.Errorf("实验 %q 已存在", exp.ID)
	}

	// 初始化 metrics。
	metrics := make(map[string]*VariantMetrics, len(exp.Variants))
	for _, v := range exp.Variants {
		metrics[v.ID] = &VariantMetrics{}
	}
	exp.Metrics = metrics
	if exp.Status == "" {
		exp.Status = string(ExperimentStatusRunning)
	}
	if exp.StartedAt == 0 {
		exp.StartedAt = time.Now().Unix()
	}
	s.buckets[exp.ID] = &exp
	return nil
}

// AssignVariant 按 FNV-1a hash（experimentID + ":" + userID）分桶，加权采样。
// 返回命中的 Variant。实验不存在或已停止时返回错误。
func (s *ABTestService) AssignVariant(_ context.Context, experimentID, userID string) (Variant, error) {
	s.mu.RLock()
	exp, ok := s.buckets[experimentID]
	s.mu.RUnlock()
	if !ok {
		return Variant{}, fmt.Errorf("实验 %q 不存在", experimentID)
	}
	if exp.Status == string(ExperimentStatusStopped) {
		return Variant{}, fmt.Errorf("实验 %q 已停止", experimentID)
	}

	// FNV-1a hash。
	h := fnv.New32a()
	_, _ = h.Write([]byte(experimentID + ":" + userID))
	hashVal := int(h.Sum32())

	// 加权采样。
	totalWeight := 0
	for _, v := range exp.Variants {
		totalWeight += v.Weight
	}
	remainder := hashVal % totalWeight
	if remainder < 0 {
		remainder = -remainder
	}
	for _, v := range exp.Variants {
		remainder -= v.Weight
		if remainder < 0 {
			return v, nil
		}
	}
	// 兜底：返回最后一个 variant。
	return exp.Variants[len(exp.Variants)-1], nil
}

// RecordMetric 记录指标事件。
// 事件类型支持 impression/click/conversion。
func (s *ABTestService) RecordMetric(_ context.Context, experimentID, variantID string, event MetricEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.buckets[experimentID]
	if !ok {
		return fmt.Errorf("实验 %q 不存在", experimentID)
	}
	if exp.Status == string(ExperimentStatusStopped) {
		return fmt.Errorf("实验 %q 已停止", experimentID)
	}
	m, ok := exp.Metrics[variantID]
	if !ok {
		return fmt.Errorf("variant %q 不存在", variantID)
	}

	switch MetricEventType(event.Type) {
	case MetricEventImpression:
		m.Impressions++
		m.LatencyMs += event.LatencyMs
		m.CostYuan += event.CostYuan
	case MetricEventClick:
		m.Clicks++
	case MetricEventConversion:
		m.Conversions++
	default:
		return fmt.Errorf("未知事件类型 %q", event.Type)
	}
	// 派生指标。
	m.CTR = computeCTR(m.Impressions, m.Clicks)
	m.CVR = computeCVR(m.Clicks, m.Conversions)
	return nil
}

// GetResults 返回实验对比报告。
// 包含各变体指标、胜出者与置信度。
func (s *ABTestService) GetResults(_ context.Context, experimentID string) (ABTestReport, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	exp, ok := s.buckets[experimentID]
	if !ok {
		return ABTestReport{}, fmt.Errorf("实验 %q 不存在", experimentID)
	}

	report := ABTestReport{ExperimentID: experimentID}
	for _, v := range exp.Variants {
		m := exp.Metrics[v.ID]
		if m == nil {
			continue
		}
		// 拷贝 metrics 避免外部修改。
		metricsCopy := *m
		metricsCopy.CTR = computeCTR(m.Impressions, m.Clicks)
		metricsCopy.CVR = computeCVR(m.Clicks, m.Conversions)
		report.Variants = append(report.Variants, VariantResult{
			VariantID: v.ID,
			Metrics:   metricsCopy,
			Sample:    int(m.Impressions),
		})
	}

	// 计算 winner 与 confidence。
	winner, conf := CompareVariants(report)
	report.Winner = winner
	report.Confidence = conf
	return report, nil
}

// Stop 停止实验。
func (s *ABTestService) Stop(_ context.Context, experimentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.buckets[experimentID]
	if !ok {
		return fmt.Errorf("实验 %q 不存在", experimentID)
	}
	exp.Status = string(ExperimentStatusStopped)
	exp.EndedAt = time.Now().Unix()
	return nil
}

// ----------------------------------------------------------------------------
// 显著性检验
// ----------------------------------------------------------------------------

// CompareVariants 对比变体，返回 winner 与 confidence。
// 使用 Z-test 检验 CTR 差异显著性：
//   - Z = (p1 - p2) / sqrt(p_pool * (1 - p_pool) * (1/n1 + 1/n2))
//   - |Z| > 1.96 → 95% 显著
// 多于 2 个变体时取 CTR 最高的两个对比。
// 无显著差异或样本不足时返回空 winner 与 0 confidence。
func CompareVariants(report ABTestReport) (winner string, confidence float64) {
	if len(report.Variants) < 2 {
		return "", 0
	}

	// 取 CTR 最高的两个变体。
	first, second := topTwoByCTR(report.Variants)
	if first.Sample == 0 || second.Sample == 0 {
		return "", 0
	}

	p1 := first.Metrics.CTR
	p2 := second.Metrics.CTR
	n1 := float64(first.Sample)
	n2 := float64(second.Sample)

	// pooled 比例。
	x1 := float64(first.Metrics.Clicks)
	x2 := float64(second.Metrics.Clicks)
	pPool := (x1 + x2) / (n1 + n2)
	if pPool == 0 || pPool == 1 {
		// 无变化或全命中，无法判定显著性。
		if p1 > p2 {
			return first.VariantID, 0
		}
		if p2 > p1 {
			return second.VariantID, 0
		}
		return "", 0
	}

	// Z-score。
	se := math.Sqrt(pPool * (1 - pPool) * (1.0/n1 + 1.0/n2))
	if se == 0 {
		return "", 0
	}
	z := (p1 - p2) / se
	absZ := math.Abs(z)

	if absZ > confidenceThreshold {
		// 显著：胜出者为 CTR 较高者。
		if p1 >= p2 {
			return first.VariantID, normalCDF(absZ)
		}
		return second.VariantID, normalCDF(absZ)
	}
	// 不显著。
	return "", normalCDF(absZ)
}

// topTwoByCTR 返回 CTR 最高的两个 VariantResult（first ≥ second）。
func topTwoByCTR(variants []VariantResult) (VariantResult, VariantResult) {
	first := variants[0]
	second := variants[1]
	if second.Metrics.CTR > first.Metrics.CTR {
		first, second = second, first
	}
	for i := 2; i < len(variants); i++ {
		v := variants[i]
		if v.Metrics.CTR > first.Metrics.CTR {
			second = first
			first = v
		} else if v.Metrics.CTR > second.Metrics.CTR {
			second = v
		}
	}
	return first, second
}

// computeCTR 计算 CTR = clicks / impressions（impressions<=0 时返回 0）。
func computeCTR(impressions, clicks int64) float64 {
	if impressions <= 0 {
		return 0
	}
	return float64(clicks) / float64(impressions)
}

// computeCVR 计算 CVR = conversions / clicks（clicks<=0 时返回 0）。
func computeCVR(clicks, conversions int64) float64 {
	if clicks <= 0 {
		return 0
	}
	return float64(conversions) / float64(clicks)
}

// normalCDF 标准正态分布累积分布函数（近似）。
// 用于将 Z-score 转换为置信度（0-1）。
func normalCDF(z float64) float64 {
	// Abramowitz & Stegun 近似公式 7.1.26。
	absZ := math.Abs(z)
	if absZ > 6 {
		return 1.0
	}
	const a1 = 0.254829592
	const a2 = -0.284496736
	const a3 = 1.421413741
	const a4 = -1.453152027
	const a5 = 1.061405429
	const p = 0.3275911

	t := 1.0 / (1.0 + p*absZ)
	y := 1.0 - (((((a5*t+a4)*t)+a3)*t+a2)*t+a1)*t*math.Exp(-absZ*absZ)
	// 返回单侧置信度：y 范围 (0.5, 1.0)。
	// 转换为 0-1 的显著性置信度：(y - 0.5) * 2。
	confidence := (y - 0.5) * 2
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 1 {
		confidence = 1
	}
	return confidence
}
