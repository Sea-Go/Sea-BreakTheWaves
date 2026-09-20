// Package cpu monitor.go — CPU 监控与指标采集（Task 13.2）。
//
// 该文件实现 Monitor：周期性采集 runtime 与协程池快照，暴露 Prometheus
// 风格指标（gauge/counter）。
//
// 职责：
//   - 采集 MonitorSnapshot（goroutine 数 / pool stats / memstats / GC 次数）
//   - 暴露 Prometheus 风格 Metric 列表（genrec_cpu_*）
//   - 维护 pprof 采样计数（IncrementSamples）
//
// 二开扩展点：
//   - 实现 PoolStatsProvider interface 接入自研协程池
//   - 通过 Metrics() 拉取指标接入自研 metrics pipeline（不直接依赖 prometheus client）
package cpu

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// PoolStatsProvider 协程池统计 interface，抽象 ants.Pool。
//
// 职责：暴露协程池的活跃/容量/等待统计，供 Monitor 采集。
//
// 二开扩展点：实现该 interface 接入自研协程池（如 ants/goroutine pool）。
type PoolStatsProvider interface {
	// Active 返回当前正在执行任务的 worker 数。
	Active() int
	// Size 返回协程池容量。
	Size() int
	// Waiting 返回等待中的任务数。
	Waiting() int
}

// MonitorSnapshot 监控快照，单次采集的瞬时值。
//
// 字段语义：
//   - Timestamp：采集时间戳（UnixMilli）
//   - Goroutines：runtime.NumGoroutine() 当前 goroutine 数
//   - PoolSize/PoolActive/PoolWaiting：协程池统计（无池时为 0）
//   - MemAllocBytes：runtime.ReadMemStats HeapAlloc（当前分配字节数）
//   - MemSysBytes：runtime.ReadMemStats Sys（从 OS 获取字节数）
//   - NumGC：runtime.ReadMemStats NumGC（启动至今 GC 次数）
type MonitorSnapshot struct {
	// Timestamp 采集时间戳（UnixMilli）。
	Timestamp int64
	// Goroutines 当前 goroutine 数。
	Goroutines int
	// PoolSize 协程池容量。
	PoolSize int
	// PoolActive 协程池活跃 worker 数。
	PoolActive int
	// PoolWaiting 协程池等待任务数。
	PoolWaiting int
	// MemAllocBytes 堆已分配字节数。
	MemAllocBytes uint64
	// MemSysBytes 从 OS 获取的总字节数。
	MemSysBytes uint64
	// NumGC 启动至今 GC 次数。
	NumGC uint32
}

// Metric Prometheus 风格指标。
//
// 字段语义：
//   - Name：指标名（如 genrec_cpu_goroutines）
//   - Type：指标类型（gauge/counter）
//   - Value：指标值
//   - Labels：标签键值对（可为 nil）
type Metric struct {
	// Name 指标名。
	Name string
	// Type 指标类型（gauge/counter）。
	Type string
	// Value 指标值。
	Value float64
	// Labels 标签键值对。
	Labels map[string]string
}

// 指标类型常量。
const (
	// MetricTypeGauge gauge 类型（瞬时值）。
	MetricTypeGauge = "gauge"
	// MetricTypeCounter counter 类型（累加值）。
	MetricTypeCounter = "counter"
)

// 指标名常量。
const (
	// MetricGoroutines 当前 goroutine 数。
	MetricGoroutines = "genrec_cpu_goroutines"
	// MetricPoolSize 协程池容量。
	MetricPoolSize = "genrec_cpu_pool_size"
	// MetricPoolActive 协程池活跃 worker 数。
	MetricPoolActive = "genrec_cpu_pool_active"
	// MetricPProfSamples pprof 采样累计次数。
	MetricPProfSamples = "genrec_cpu_pprof_samples"
)

// Monitor CPU 监控器，采集 runtime 与协程池快照并暴露指标。
//
// 职责：
//   - Collect 采集一次 MonitorSnapshot
//   - Metrics 返回 Prometheus 风格指标列表
//   - IncrementSamples 原子递增 pprof 采样计数
//
// 字段语义：
//   - poolStats：协程池统计 provider（可为 nil，无池时统计为 0）
//   - samples：pprof 采样累计计数（atomic 操作）
//   - mu：保护内部状态（预留扩展，如历史快照缓冲）
//
// 二开扩展点：通过 NewMonitor(poolStats) 注入自研协程池 provider。
type Monitor struct {
	// poolStats 协程池统计 provider（可为 nil）。
	poolStats PoolStatsProvider
	// samples pprof 采样累计计数（atomic 操作）。
	samples int64
	// mu 保护内部状态（预留扩展，如历史快照缓冲）。
	mu sync.Mutex
}

// NewMonitor 构造 Monitor。
// poolStats 协程池统计 provider（可为 nil，无池时 PoolSize/PoolActive/PoolWaiting 为 0）。
// 返回 *Monitor。
func NewMonitor(poolStats PoolStatsProvider) *Monitor {
	return &Monitor{
		poolStats: poolStats,
	}
}

// Collect 采集一次监控快照。
//
// 采集内容：runtime.NumGoroutine + runtime.ReadMemStats + pool stats。
// ctx 当前未使用（预留周期性采集取消信号）。
// 返回 MonitorSnapshot。
func (m *Monitor) Collect(_ context.Context) MonitorSnapshot {
	if m == nil {
		return MonitorSnapshot{}
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	snap := MonitorSnapshot{
		Timestamp:     time.Now().UnixMilli(),
		Goroutines:    runtime.NumGoroutine(),
		MemAllocBytes: ms.HeapAlloc,
		MemSysBytes:   ms.Sys,
		NumGC:         ms.NumGC,
	}
	if m.poolStats != nil {
		snap.PoolSize = m.poolStats.Size()
		snap.PoolActive = m.poolStats.Active()
		snap.PoolWaiting = m.poolStats.Waiting()
	}
	return snap
}

// Metrics 返回 Prometheus 风格指标列表。
//
// 包含 4 个指标：
//   - genrec_cpu_goroutines（gauge）：当前 goroutine 数
//   - genrec_cpu_pool_size（gauge）：协程池容量
//   - genrec_cpu_pool_active（gauge）：协程池活跃 worker 数
//   - genrec_cpu_pprof_samples（counter）：pprof 采样累计次数
//
// 注意：Metrics() 不调用 runtime.ReadMemStats（避免 STW），
// 仅采集 NumGoroutine + pool stats + samples 计数。
func (m *Monitor) Metrics() []Metric {
	if m == nil {
		return nil
	}
	goroutines := runtime.NumGoroutine()
	poolSize, poolActive := 0, 0
	if m.poolStats != nil {
		poolSize = m.poolStats.Size()
		poolActive = m.poolStats.Active()
	}
	samples := atomic.LoadInt64(&m.samples)
	return []Metric{
		{Name: MetricGoroutines, Type: MetricTypeGauge, Value: float64(goroutines)},
		{Name: MetricPoolSize, Type: MetricTypeGauge, Value: float64(poolSize)},
		{Name: MetricPoolActive, Type: MetricTypeGauge, Value: float64(poolActive)},
		{Name: MetricPProfSamples, Type: MetricTypeCounter, Value: float64(samples)},
	}
}

// IncrementSamples 原子递增 pprof 采样计数。
// 用于在每次 pprof 采样完成后累加 genrec_cpu_pprof_samples 指标。
func (m *Monitor) IncrementSamples() {
	if m == nil {
		return
	}
	atomic.AddInt64(&m.samples, 1)
}
