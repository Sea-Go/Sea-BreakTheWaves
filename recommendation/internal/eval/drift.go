// internal/eval/drift.go — 行为漂移检测（Task 15.4）。
//
// 职责：
//   - 实现 DriftDetector 对比当前 metrics 与基线分布，检测行为漂移
//   - 基线用 Distribution（mean/std/sample）表示，由 UpdateBaseline 维护
//   - 漂移判定规则：
//     1. |current - baseline.mean| > threshold * baseline.std
//     2. 或 |current - baseline.mean| / baseline.mean > 0.1（10% 相对变化）
//     3. std=0 时退化为绝对阈值 0.1（避免除零）
//   - 提供 BaselineStore interface 抽象持久化，二开可替换为 PG/Redis
//
// 二开扩展点：
//   - 实现 BaselineStore interface 替换持久化后端
//   - 调整 threshold 改变漂移灵敏度
//   - 在 Detect 中追加自定义检测规则（如季节性修正）
package eval

import (
	"context"
	"math"
	"sync"
	"time"
)

// ----------------------------------------------------------------------------
// 类型定义
// ----------------------------------------------------------------------------

// Distribution 指标分布基线。
//
// 字段语义：
//   - Mean：均值
//   - Std：标准差
//   - Sample：样本数
//   - Updated：最近一次更新时间（Unix 秒）
type Distribution struct {
	// Mean 均值。
	Mean float64 `json:"mean"`
	// Std 标准差。
	Std float64 `json:"std"`
	// Sample 样本数。
	Sample int `json:"sample"`
	// Updated 最近更新时间（Unix 秒）。
	Updated int64 `json:"updated"`
}

// DriftReport 漂移检测报告。
//
// 字段语义：
//   - Detected：是否检测到漂移
//   - Items：各指标的漂移详情
//   - DetectedAt：检测时间（Unix 秒）
//   - Window：检测窗口标识（如 "1h"）
type DriftReport struct {
	// Detected 是否检测到漂移。
	Detected bool `json:"detected"`
	// Items 漂移项列表。
	Items []DriftItem `json:"items"`
	// DetectedAt 检测时间。
	DetectedAt int64 `json:"detected_at"`
	// Window 检测窗口。
	Window string `json:"window"`
}

// ----------------------------------------------------------------------------
// BaselineStore interface
// ----------------------------------------------------------------------------

// BaselineStore 基线持久化抽象。
//
// 二开：实现该 interface 替换为 PG/Redis 等后端。
type BaselineStore interface {
	// Load 加载基线（指标名 → 分布）。
	Load(ctx context.Context) (map[string]Distribution, error)
	// Save 保存基线。
	Save(ctx context.Context, baseline map[string]Distribution) error
}

// ----------------------------------------------------------------------------
// DriftDetector
// ----------------------------------------------------------------------------

// defaultDriftStdThreshold 默认 std 倍数阈值（mean ± threshold*std）。
const defaultDriftStdThreshold = 3.0

// defaultDriftRelativeThreshold 默认相对变化阈值（10%）。
const defaultDriftRelativeThreshold = 0.1

// defaultDriftAbsThreshold std=0 时的退化绝对阈值。
const defaultDriftAbsThreshold = 0.1

// stdEpsilon std 小于此值视为 0（避免浮点误差导致 std 倍数规则误触发）。
const stdEpsilon = 1e-12

// DriftDetector 行为漂移检测器。
//
// 工作流程：
//  1. UpdateBaseline：用历史 values 更新 metric 的 Distribution（mean/std/sample）
//  2. Detect：对比当前 metrics 与 baseline 分布，超阈值标记 drift
//  3. LoadBaseline/SaveBaseline：通过 BaselineStore 持久化
//
// 二开扩展点：替换 BaselineStore；调整 threshold；自定义检测规则。
type DriftDetector struct {
	mu         sync.RWMutex
	baseline   map[string]Distribution
	threshold  float64
	store      BaselineStore
}

// NewDriftDetector 构造 DriftDetector。
// threshold 为 std 倍数阈值（如 3.0 表示 mean ± 3σ）；<=0 时用默认值 3.0。
func NewDriftDetector(threshold float64) *DriftDetector {
	if threshold <= 0 {
		threshold = defaultDriftStdThreshold
	}
	return &DriftDetector{
		baseline:  make(map[string]Distribution),
		threshold: threshold,
	}
}

// WithStore 注入 BaselineStore（用于 LoadBaseline/SaveBaseline）。
func (d *DriftDetector) WithStore(store BaselineStore) *DriftDetector {
	d.store = store
	return d
}

// UpdateBaseline 用历史 values 更新指定 metric 的基线分布。
// 计算 mean/std/sample，使用直接计算法（适合小批量；大批量可改 Welford）。
func (d *DriftDetector) UpdateBaseline(metric string, values []float64) {
	if len(values) == 0 {
		return
	}
	// 计算 mean。
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(len(values))
	// 计算 std（总体标准差）。
	variance := 0.0
	for _, v := range values {
		diff := v - mean
		variance += diff * diff
	}
	std := math.Sqrt(variance / float64(len(values)))

	d.mu.Lock()
	defer d.mu.Unlock()
	d.baseline[metric] = Distribution{
		Mean:   mean,
		Std:    std,
		Sample: len(values),
		Updated: time.Now().Unix(),
	}
}

// Detect 检测当前 metrics 是否漂移。
//
// 判定规则（满足任一即 drift）：
//  1. |current - baseline.mean| > threshold * baseline.std（std 倍数阈值）
//  2. |current - baseline.mean| / |baseline.mean| > 0.1（10% 相对变化）
//  3. std=0 时退化为绝对阈值 0.1：|current - baseline.mean| > 0.1
func (d *DriftDetector) Detect(current map[string]float64) DriftReport {
	d.mu.RLock()
	defer d.mu.RUnlock()

	report := DriftReport{
		DetectedAt: time.Now().Unix(),
		Items:      make([]DriftItem, 0, len(d.baseline)),
	}

	for metric, dist := range d.baseline {
		curVal, ok := current[metric]
		if !ok {
			continue
		}
		diff := math.Abs(curVal - dist.Mean)
		detected := false

		// 规则 1：std 倍数阈值（std > epsilon 时启用）。
		if dist.Std > stdEpsilon {
			if diff > d.threshold*dist.Std {
				detected = true
			}
		} else {
			// 规则 3：std≈0 退化绝对阈值。
			if diff > defaultDriftAbsThreshold {
				detected = true
			}
		}

		// 规则 2：10% 相对变化。
		if dist.Mean != 0 {
			relChange := diff / math.Abs(dist.Mean)
			if relChange > defaultDriftRelativeThreshold {
				detected = true
			}
		}

		item := DriftItem{
			Metric:   metric,
			Baseline: dist.Mean,
			Current:  curVal,
			Detected: detected,
		}
		if dist.Mean != 0 {
			item.DriftPercent = (curVal - dist.Mean) / math.Abs(dist.Mean) * 100
		}
		report.Items = append(report.Items, item)
		if detected {
			report.Detected = true
		}
	}
	return report
}

// GetBaseline 返回当前基线快照（线程安全拷贝）。
func (d *DriftDetector) GetBaseline() map[string]Distribution {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make(map[string]Distribution, len(d.baseline))
	for k, v := range d.baseline {
		out[k] = v
	}
	return out
}

// LoadBaseline 从 BaselineStore 加载基线。
// 未注入 store 时返回 nil。
func (d *DriftDetector) LoadBaseline(ctx context.Context) error {
	if d.store == nil {
		return nil
	}
	loaded, err := d.store.Load(ctx)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.baseline = loaded
	if d.baseline == nil {
		d.baseline = make(map[string]Distribution)
	}
	return nil
}

// SaveBaseline 保存基线到 BaselineStore。
// 未注入 store 时返回 nil。
func (d *DriftDetector) SaveBaseline(ctx context.Context) error {
	if d.store == nil {
		return nil
	}
	d.mu.RLock()
	snapshot := make(map[string]Distribution, len(d.baseline))
	for k, v := range d.baseline {
		snapshot[k] = v
	}
	d.mu.RUnlock()
	return d.store.Save(ctx, snapshot)
}

// ----------------------------------------------------------------------------
// MemoryBaselineStore 默认实现
// ----------------------------------------------------------------------------

// MemoryBaselineStore 内存版 BaselineStore，用于测试与单机默认实现。
type MemoryBaselineStore struct {
	mu       sync.RWMutex
	state    map[string]Distribution
	loadErr  error
	saveErr  error
	loadCnt  int
	saveCnt  int
}

// NewMemoryBaselineStore 构造 MemoryBaselineStore。
func NewMemoryBaselineStore() *MemoryBaselineStore {
	return &MemoryBaselineStore{state: make(map[string]Distribution)}
}

// WithLoadErr 注入 Load 错误（测试用）。
func (m *MemoryBaselineStore) WithLoadErr(err error) *MemoryBaselineStore {
	m.loadErr = err
	return m
}

// WithSaveErr 注入 Save 错误（测试用）。
func (m *MemoryBaselineStore) WithSaveErr(err error) *MemoryBaselineStore {
	m.saveErr = err
	return m
}

// Load 加载基线。
func (m *MemoryBaselineStore) Load(_ context.Context) (map[string]Distribution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadCnt++
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	out := make(map[string]Distribution, len(m.state))
	for k, v := range m.state {
		out[k] = v
	}
	return out, nil
}

// Save 保存基线。
func (m *MemoryBaselineStore) Save(_ context.Context, baseline map[string]Distribution) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saveCnt++
	if m.saveErr != nil {
		return m.saveErr
	}
	m.state = make(map[string]Distribution, len(baseline))
	for k, v := range baseline {
		m.state[k] = v
	}
	return nil
}

// LoadCount 返回 Load 调用次数（测试用）。
func (m *MemoryBaselineStore) LoadCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.loadCnt
}

// SaveCount 返回 Save 调用次数（测试用）。
func (m *MemoryBaselineStore) SaveCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.saveCnt
}
