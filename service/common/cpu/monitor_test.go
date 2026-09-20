// Package cpu monitor_test.go — Monitor 单元测试（Task 13.2）。
//
// 用 stdlib 手写 stub 覆盖：
//   - Collect 快照采集（goroutines / pool stats / memstats）
//   - nil pool stats 时统计为 0
//   - Metrics 输出 4 个指标（gauge/counter）
//   - IncrementSamples 原子递增
//   - 并发 IncrementSamples 线程安全
package cpu

import (
	"context"
	"sync"
	"testing"
)

// stubPoolStatsCPU 测试用 PoolStatsProvider stub。
type stubPoolStatsCPU struct {
	active  int
	size    int
	waiting int
}

func (s *stubPoolStatsCPU) Active() int  { return s.active }
func (s *stubPoolStatsCPU) Size() int    { return s.size }
func (s *stubPoolStatsCPU) Waiting() int { return s.waiting }

// TestMonitor_Collect 验证快照采集内容。
func TestMonitor_Collect(t *testing.T) {
	pool := &stubPoolStatsCPU{active: 3, size: 10, waiting: 5}
	m := NewMonitor(pool)
	snap := m.Collect(context.Background())

	if snap.Timestamp == 0 {
		t.Error("Timestamp 应非零")
	}
	if snap.Goroutines <= 0 {
		t.Errorf("Goroutines = %d, 应 > 0", snap.Goroutines)
	}
	if snap.PoolSize != 10 {
		t.Errorf("PoolSize = %d, 期望 10", snap.PoolSize)
	}
	if snap.PoolActive != 3 {
		t.Errorf("PoolActive = %d, 期望 3", snap.PoolActive)
	}
	if snap.PoolWaiting != 5 {
		t.Errorf("PoolWaiting = %d, 期望 5", snap.PoolWaiting)
	}
	// MemAllocBytes 与 MemSysBytes 应 > 0（runtime 已分配内存）。
	if snap.MemAllocBytes == 0 {
		t.Error("MemAllocBytes 应非零")
	}
	if snap.MemSysBytes == 0 {
		t.Error("MemSysBytes 应非零")
	}
}

// TestMonitor_CollectNilPool 验证 nil pool stats 时统计为 0。
func TestMonitor_CollectNilPool(t *testing.T) {
	m := NewMonitor(nil)
	snap := m.Collect(context.Background())
	if snap.PoolSize != 0 || snap.PoolActive != 0 || snap.PoolWaiting != 0 {
		t.Errorf("nil pool 时 Pool stats 应为 0, got size=%d active=%d waiting=%d",
			snap.PoolSize, snap.PoolActive, snap.PoolWaiting)
	}
	// goroutines/memstats 仍应采集。
	if snap.Goroutines <= 0 {
		t.Errorf("nil pool 时 Goroutines 仍应 > 0, got %d", snap.Goroutines)
	}
}

// TestMonitor_Metrics 验证 Metrics 返回 4 个指标。
func TestMonitor_Metrics(t *testing.T) {
	pool := &stubPoolStatsCPU{active: 2, size: 8, waiting: 1}
	m := NewMonitor(pool)
	m.IncrementSamples()
	m.IncrementSamples()
	m.IncrementSamples()

	metrics := m.Metrics()
	if len(metrics) != 4 {
		t.Fatalf("metrics 数量 = %d, 期望 4", len(metrics))
	}

	byName := make(map[string]Metric, len(metrics))
	for _, mt := range metrics {
		byName[mt.Name] = mt
	}

	if g, ok := byName[MetricGoroutines]; !ok {
		t.Errorf("缺少指标 %s", MetricGoroutines)
	} else if g.Type != MetricTypeGauge || g.Value <= 0 {
		t.Errorf("goroutines metric 异常: %+v", g)
	}

	if ps, ok := byName[MetricPoolSize]; !ok {
		t.Errorf("缺少指标 %s", MetricPoolSize)
	} else if ps.Value != 8 {
		t.Errorf("pool_size metric Value = %v, 期望 8", ps.Value)
	}

	if pa, ok := byName[MetricPoolActive]; !ok {
		t.Errorf("缺少指标 %s", MetricPoolActive)
	} else if pa.Value != 2 {
		t.Errorf("pool_active metric Value = %v, 期望 2", pa.Value)
	}

	if s, ok := byName[MetricPProfSamples]; !ok {
		t.Errorf("缺少指标 %s", MetricPProfSamples)
	} else if s.Type != MetricTypeCounter || s.Value != 3 {
		t.Errorf("pprof_samples metric 异常: %+v", s)
	}
}

// TestMonitor_MetricsNilPool 验证 nil pool 时 Metrics 中 pool 指标为 0。
func TestMonitor_MetricsNilPool(t *testing.T) {
	m := NewMonitor(nil)
	metrics := m.Metrics()
	if len(metrics) != 4 {
		t.Fatalf("metrics 数量 = %d, 期望 4", len(metrics))
	}
	byName := make(map[string]Metric, len(metrics))
	for _, mt := range metrics {
		byName[mt.Name] = mt
	}
	if ps, ok := byName[MetricPoolSize]; !ok || ps.Value != 0 {
		t.Errorf("nil pool 时 pool_size 应为 0, got %+v", ps)
	}
	if pa, ok := byName[MetricPoolActive]; !ok || pa.Value != 0 {
		t.Errorf("nil pool 时 pool_active 应为 0, got %+v", pa)
	}
}

// TestMonitor_IncrementSamples 验证采样计数递增。
func TestMonitor_IncrementSamples(t *testing.T) {
	m := NewMonitor(nil)
	for i := 0; i < 100; i++ {
		m.IncrementSamples()
	}
	for _, mt := range m.Metrics() {
		if mt.Name == MetricPProfSamples && mt.Value != 100 {
			t.Errorf("pprof_samples = %v, 期望 100", mt.Value)
		}
	}
}

// TestMonitor_IncrementSamplesConcurrent 验证并发递增线程安全。
func TestMonitor_IncrementSamplesConcurrent(t *testing.T) {
	m := NewMonitor(nil)
	var wg sync.WaitGroup
	const goroutines = 50
	const perG = 100
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				m.IncrementSamples()
			}
		}()
	}
	wg.Wait()

	var samples float64
	for _, mt := range m.Metrics() {
		if mt.Name == MetricPProfSamples {
			samples = mt.Value
			break
		}
	}
	want := float64(goroutines * perG)
	if samples != want {
		t.Errorf("并发递增后 pprof_samples = %v, 期望 %v", samples, want)
	}
}

// TestMonitor_NilReceiver 验证 nil receiver 安全处理。
func TestMonitor_NilReceiver(t *testing.T) {
	var m *Monitor
	snap := m.Collect(context.Background())
	if snap != (MonitorSnapshot{}) {
		t.Errorf("nil receiver Collect 应返回零值快照, got %+v", snap)
	}
	if metrics := m.Metrics(); metrics != nil {
		t.Errorf("nil receiver Metrics 应返回 nil, got %v", metrics)
	}
	m.IncrementSamples() // 不应 panic
}

// TestMonitorSnapshot_ZeroValue 验证零值快照可用。
func TestMonitorSnapshot_ZeroValue(t *testing.T) {
	var snap MonitorSnapshot
	if snap.Timestamp != 0 || snap.Goroutines != 0 || snap.NumGC != 0 {
		t.Errorf("零值快照字段应全为 0, got %+v", snap)
	}
}

// TestMetric_ZeroValue 验证零值 Metric 可用。
func TestMetric_ZeroValue(t *testing.T) {
	var m Metric
	if m.Name != "" || m.Type != "" || m.Value != 0 {
		t.Errorf("零值 Metric 字段应为零值, got %+v", m)
	}
	if m.Labels != nil {
		t.Errorf("零值 Metric Labels 应为 nil, got %v", m.Labels)
	}
}
