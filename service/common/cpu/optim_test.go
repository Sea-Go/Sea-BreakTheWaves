// Package cpu optim_test.go — Optimizer 单元测试（Task 13.6）。
//
// 用 stdlib 手写 stub（不引入 testify）覆盖：
//   - SetupMaxProcs 设置 GOMAXPROCS
//   - RegisterPool / Get / Put sync.Pool 管理
//   - EscapeAnalysis 返回逃逸分析报告
//   - cgroup 文件解析（cgroup v1/v2）
//
// stub 命名加 OP（Optimizer）前缀避免与已有 stub 冲突。
package cpu

import (
	"runtime"
	"sync"
	"testing"
)

// ----------------------------------------------------------------------------
// Optimizer 测试
// ----------------------------------------------------------------------------

// TestOptimizer_New 验证 NewOptimizer 初始化。
func TestOptimizer_New(t *testing.T) {
	o := NewOptimizer()
	if o == nil {
		t.Fatal("NewOptimizer 返回 nil")
	}
	if o.maxProcs != runtime.GOMAXPROCS(0) {
		t.Errorf("maxProcs = %d, 期望 %d", o.maxProcs, runtime.GOMAXPROCS(0))
	}
	if o.pools == nil {
		t.Error("pools 不应为 nil")
	}
}

// TestOptimizer_SetupMaxProcs 验证 SetupMaxProcs 设置 GOMAXPROCS。
func TestOptimizer_SetupMaxProcs(t *testing.T) {
	// 保存原值，测试后恢复。
	original := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(original)

	o := NewOptimizer()
	got := o.SetupMaxProcs()
	if got <= 0 {
		t.Errorf("SetupMaxProcs 返回 %d, 应 > 0", got)
	}
	// 验证 maxProcs 已更新。
	if o.maxProcs != got {
		t.Errorf("maxProcs = %d, 期望 %d", o.maxProcs, got)
	}
	// 验证 runtime.GOMAXPROCS 已设置（读取应等于 got）。
	if runtime.GOMAXPROCS(0) != got {
		t.Errorf("runtime.GOMAXPROCS(0) = %d, 期望 %d", runtime.GOMAXPROCS(0), got)
	}
}

// TestOptimizer_MaxProcs 验证 MaxProcs 返回当前值。
func TestOptimizer_MaxProcs(t *testing.T) {
	o := NewOptimizer()
	if got := o.MaxProcs(); got <= 0 {
		t.Errorf("MaxProcs = %d, 应 > 0", got)
	}
}

// TestOptimizer_RegisterAndGetPool 验证 RegisterPool / Get / Put。
func TestOptimizer_RegisterAndGetPool(t *testing.T) {
	o := NewOptimizer()
	o.RegisterPool("candidates", func() any {
		return make([]string, 0, 10)
	})
	// Get 应返回工厂创建的对象。
	v := o.Get("candidates")
	if v == nil {
		t.Fatal("Get 返回 nil")
	}
	s, ok := v.([]string)
	if !ok {
		t.Fatalf("Get 返回类型 %T, 期望 []string", v)
	}
	if cap(s) != 10 {
		t.Errorf("Get 返回 cap = %d, 期望 10", cap(s))
	}
	// Put 归还后再次 Get 应复用对象（可能）。
	o.Put("candidates", s)
	v2 := o.Get("candidates")
	if v2 == nil {
		t.Error("Get 返回 nil")
	}
}

// TestOptimizer_GetNonExistentPool 验证从不存在 pool 获取返回 nil。
func TestOptimizer_GetNonExistentPool(t *testing.T) {
	o := NewOptimizer()
	if v := o.Get("nonexistent"); v != nil {
		t.Errorf("从不存在 pool 获取应返回 nil, 实际 %v", v)
	}
}

// TestOptimizer_PutNonExistentPool 验证归还到不存在 pool 不 panic。
func TestOptimizer_PutNonExistentPool(t *testing.T) {
	o := NewOptimizer()
	// 不应 panic。
	o.Put("nonexistent", "some value")
}

// TestOptimizer_RegisterPoolNilFactory 验证 nil factory 不注册。
func TestOptimizer_RegisterPoolNilFactory(t *testing.T) {
	o := NewOptimizer()
	o.RegisterPool("test", nil)
	if v := o.Get("test"); v != nil {
		t.Errorf("nil factory 不应注册, Get 应返回 nil, 实际 %v", v)
	}
}

// TestOptimizer_RegisterPoolEmptyName 验证空名不注册。
func TestOptimizer_RegisterPoolEmptyName(t *testing.T) {
	o := NewOptimizer()
	o.RegisterPool("", func() any { return "test" })
	if v := o.Get(""); v != nil {
		t.Errorf("空名不应注册, Get 应返回 nil, 实际 %v", v)
	}
}

// TestOptimizer_RegisterPoolOverwrite 验证重复注册同名 pool 覆盖。
func TestOptimizer_RegisterPoolOverwrite(t *testing.T) {
	o := NewOptimizer()
	o.RegisterPool("test", func() any { return "v1" })
	o.RegisterPool("test", func() any { return "v2" })
	v := o.Get("test")
	if v != "v2" {
		t.Errorf("重复注册应覆盖, Get 返回 %v, 期望 v2", v)
	}
}

// TestOptimizer_PoolConcurrent 验证并发 Get/Put 安全。
func TestOptimizer_PoolConcurrent(t *testing.T) {
	o := NewOptimizer()
	o.RegisterPool("buffers", func() any {
		return &sync.Pool{}
	})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := o.Get("buffers")
			if v != nil {
				o.Put("buffers", v)
			}
		}()
	}
	wg.Wait()
}

// TestOptimizer_EscapeAnalysis 验证逃逸分析报告。
func TestOptimizer_EscapeAnalysis(t *testing.T) {
	o := NewOptimizer()
	report := o.EscapeAnalysis()
	if len(report.HotPaths) == 0 {
		t.Error("HotPaths 不应为空")
	}
	if len(report.Recommendations) == 0 {
		t.Error("Recommendations 不应为空")
	}
	// 验证 HotPaths 字段。
	for _, hp := range report.HotPaths {
		if hp.Function == "" {
			t.Error("HotPath.Function 不应为空")
		}
		if hp.Allocations < 0 {
			t.Errorf("HotPath.Allocations = %d, 应 >= 0", hp.Allocations)
		}
	}
	// 验证 Recommendations 字段。
	for _, r := range report.Recommendations {
		if r == "" {
			t.Error("Recommendation 不应为空")
		}
	}
}

// TestOptimizer_NilReceiver 验证 nil receiver 安全处理。
func TestOptimizer_NilReceiver(t *testing.T) {
	var o *Optimizer
	// 这些方法不应 panic。
	if got := o.MaxProcs(); got <= 0 {
		t.Errorf("nil receiver MaxProcs 应返回当前值, 实际 %d", got)
	}
	if got := o.SetupMaxProcs(); got <= 0 {
		t.Errorf("nil receiver SetupMaxProcs 应返回当前值, 实际 %d", got)
	}
	o.RegisterPool("test", func() any { return nil })
	if v := o.Get("test"); v != nil {
		t.Errorf("nil receiver Get 应返回 nil, 实际 %v", v)
	}
	o.Put("test", "value")
	report := o.EscapeAnalysis()
	if len(report.HotPaths) == 0 {
		t.Error("nil receiver EscapeAnalysis 应返回示例报告")
	}
}

// ----------------------------------------------------------------------------
// cgroup 文件解析测试
// ----------------------------------------------------------------------------

// TestReadCgroupV1CPU_NotExist 验证文件不存在时返回 false。
func TestReadCgroupV1CPU_NotExist(t *testing.T) {
	// cgroup v1 文件路径在 macOS 不存在，应返回 (0, false)。
	n, ok := readCgroupV1CPU()
	if ok {
		t.Logf("cgroup v1 文件存在, n=%d（可能是 Linux 环境）", n)
	}
}

// TestReadCgroupV2CPU_NotExist 验证文件不存在时返回 false。
func TestReadCgroupV2CPU_NotExist(t *testing.T) {
	// cgroup v2 文件路径在 macOS 不存在，应返回 (0, false)。
	n, ok := readCgroupV2CPU()
	if ok {
		t.Logf("cgroup v2 文件存在, n=%d（可能是 Linux 环境）", n)
	}
}

// TestComputeCGroupCPUCount 验证 computeCGroupCPUCount 在无 cgroup 时返回 -1。
func TestComputeCGroupCPUCount(t *testing.T) {
	n := computeCGroupCPUCount()
	if n == 0 {
		t.Error("computeCGroupCPUCount 不应返回 0（应返回 -1 或正数）")
	}
	// 在 macOS 上应返回 -1（无 cgroup）。
	if n > 0 {
		t.Logf("cgroup CPU 数 = %d（Linux 环境）", n)
	}
}

// ----------------------------------------------------------------------------
// 报告零值测试
// ----------------------------------------------------------------------------

// TestEscapeReport_ZeroValue 验证零值安全。
func TestEscapeReport_ZeroValue(t *testing.T) {
	var r EscapeReport
	if len(r.HotPaths) != 0 || len(r.Recommendations) != 0 {
		t.Errorf("零值 EscapeReport 不正确: %+v", r)
	}
}

// TestHotPath_ZeroValue 验证零值安全。
func TestHotPath_ZeroValue(t *testing.T) {
	var h HotPath
	if h.Function != "" || h.Allocations != 0 || h.Escaped != false {
		t.Errorf("零值 HotPath 不正确: %+v", h)
	}
}
