// Package cpu optim.go — GOMAXPROCS 与热路径优化（Task 13.6）。
//
// 该文件实现 Optimizer：GOMAXPROCS 设置（模拟 automaxprocs，读取 cgroup quota）
// 与 sync.Pool 注册管理（热路径零分配）。
//
// 职责：
//   - SetupMaxProcs：根据 cgroup CPU quota 设置 GOMAXPROCS（不依赖 automaxprocs）
//   - RegisterPool / Get / Put：注册与使用 sync.Pool，避免热路径频繁分配
//   - EscapeAnalysis：返回逃逸分析报告（stub，提供优化建议）
//
// 二开扩展点：
//   - 通过 NewOptimizer() 创建实例
//   - 通过 RegisterPool 注册自定义 sync.Pool（如 Candidate 缓冲、RecallResult 切片）
//   - 通过 EscapeAnalysis 获取热路径优化建议
//
// 不直接 import ants / automaxprocs / trpc-agent-go，所有依赖通过 interface 注入。
package cpu

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// ----------------------------------------------------------------------------
// 逃逸分析报告
// ----------------------------------------------------------------------------

// EscapeReport 逃逸分析报告。
//
// 字段语义：
//   - HotPaths：热路径函数列表（含分配次数与是否逃逸）
//   - Recommendations：优化建议列表
type EscapeReport struct {
	// HotPaths 热路径函数列表。
	HotPaths []HotPath
	// Recommendations 优化建议列表。
	Recommendations []string
}

// HotPath 热路径函数信息。
//
// 字段语义：
//   - Function：函数名
//   - Allocations：分配次数（每调用）
//   - Escaped：是否逃逸到堆
type HotPath struct {
	// Function 函数名。
	Function string
	// Allocations 分配次数。
	Allocations int
	// Escaped 是否逃逸到堆。
	Escaped bool
}

// ----------------------------------------------------------------------------
// Optimizer
// ----------------------------------------------------------------------------

// Optimizer GOMAXPROCS 与 sync.Pool 管理器。
//
// 字段语义：
//   - maxProcs：当前 GOMAXPROCS 值
//   - pools：sync.Pool 注册表（key=pool name，value=*sync.Pool）
//   - mu：保护 pools 并发读写
//
// 二开扩展点：通过 NewOptimizer() 创建实例；通过 RegisterPool/Get/Put 管理对象池。
type Optimizer struct {
	maxProcs int
	pools    map[string]*sync.Pool
	mu       sync.Mutex
}

// NewOptimizer 构造 Optimizer。
// 返回 *Optimizer（未设置 GOMAXPROCS，需调用 SetupMaxProcs）。
func NewOptimizer() *Optimizer {
	return &Optimizer{
		maxProcs: runtime.GOMAXPROCS(0), // 读取当前值（不修改）
		pools:    make(map[string]*sync.Pool),
	}
}

// SetupMaxProcs 设置 GOMAXPROCS（模拟 automaxprocs）。
//
// 流程：
//  1. 读取 cgroup CPU quota（/sys/fs/cgroup/cpu/cpu.cfs_quota_us 和
//     cpu.cfs_period_us）
//  2. 文件不存在时用 runtime.NumCPU() 作为 GOMAXPROCS
//  3. quota 有效（>0）时按 quota/period 计算 GOMAXPROCS（向上取整，下限 1）
//  4. 用 runtime.GOMAXPROCS 设置并返回设置的值
//
// 二开扩展点：可通过 cgroup v2 路径扩展（/sys/fs/cgroup/cpu.max）。
func (o *Optimizer) SetupMaxProcs() int {
	if o == nil {
		return runtime.GOMAXPROCS(0)
	}
	procs := computeCGroupCPUCount()
	if procs <= 0 {
		procs = runtime.NumCPU()
	}
	o.maxProcs = runtime.GOMAXPROCS(procs)
	return o.maxProcs
}

// MaxProcs 返回当前 GOMAXPROCS 值（不修改）。
func (o *Optimizer) MaxProcs() int {
	if o == nil {
		return runtime.GOMAXPROCS(0)
	}
	return o.maxProcs
}

// RegisterPool 注册 sync.Pool。
//
// name pool 名称（如 "candidate"、"recall_result"）；
// factory 对象工厂函数（返回新对象）。
// 重复注册同名 pool 会覆盖。
func (o *Optimizer) RegisterPool(name string, factory func() any) {
	if o == nil || name == "" || factory == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pools[name] = &sync.Pool{
		New: factory,
	}
}

// Get 从 pool 获取对象。
//
// name pool 名称；pool 不存在时返回 nil。
func (o *Optimizer) Get(name string) any {
	if o == nil || name == "" {
		return nil
	}
	o.mu.Lock()
	p, ok := o.pools[name]
	o.mu.Unlock()
	if !ok {
		return nil
	}
	return p.Get()
}

// Put 归还对象到 pool。
//
// name pool 名称；value 待归还对象；
// pool 不存在时空操作（避免 panic）。
func (o *Optimizer) Put(name string, value any) {
	if o == nil || name == "" || value == nil {
		return
	}
	o.mu.Lock()
	p, ok := o.pools[name]
	o.mu.Unlock()
	if !ok {
		return
	}
	p.Put(value)
}

// EscapeAnalysis 返回逃逸分析报告（stub）。
//
// 该方法返回示例报告，列出推荐系统的热路径函数与优化建议：
//   - HybridRecaller.Recall：候选切片逃逸
//   - ParallelRecaller.RecallParallel：results 数组逃逸
//   - L3PromptCache.Get：cache key 字符串逃逸
//
// 真实实现可通过 `go build -gcflags="-m" ` 解析输出。
func (o *Optimizer) EscapeAnalysis() EscapeReport {
	return EscapeReport{
		HotPaths: []HotPath{
			{
				Function:    "HybridRecaller.Recall",
				Allocations: 3,
				Escaped:     true,
			},
			{
				Function:    "ParallelRecaller.RecallParallel",
				Allocations: 2,
				Escaped:     true,
			},
			{
				Function:    "L3PromptCache.Get",
				Allocations: 1,
				Escaped:     false,
			},
			{
				Function:    "BatchProcessor.BatchRecall",
				Allocations: 2,
				Escaped:     true,
			},
		},
		Recommendations: []string{
			"使用 sync.Pool 复用 Candidate 切片，避免热路径逃逸",
			"预分配 results 数组（make([]RecallResult, len(sources))）避免扩容",
			"用 strings.Builder 替代 + 拼接 cache key",
			"将 RecallResult.Items 改为预分配切片",
			"对短生命周期对象使用栈分配（避免 *Candidate 逃逸）",
		},
	}
}

// ----------------------------------------------------------------------------
// cgroup CPU quota 解析
// ----------------------------------------------------------------------------

// cgroupV1CPUFiles cgroup v1 CPU quota/period 文件路径。
var cgroupV1CPUFiles = struct {
	quota, period string
}{
	quota:  "/sys/fs/cgroup/cpu/cpu.cfs_quota_us",
	period: "/sys/fs/cgroup/cpu/cpu.cfs_period_us",
}

// cgroupV2CPUFile cgroup v2 CPU max 文件路径。
const cgroupV2CPUFile = "/sys/fs/cgroup/cpu.max"

// computeCGroupCPUCount 解析 cgroup CPU 配额，返回可用 CPU 数。
//
// 优先尝试 cgroup v2（/sys/fs/cgroup/cpu.max）；
// 失败时尝试 cgroup v1（cpu.cfs_quota_us / cpu.cfs_period_us）；
// 全部失败时返回 -1（由调用方回退到 runtime.NumCPU()）。
func computeCGroupCPUCount() int {
	// 尝试 cgroup v2。
	if n, ok := readCgroupV2CPU(); ok {
		return n
	}
	// 尝试 cgroup v1。
	if n, ok := readCgroupV1CPU(); ok {
		return n
	}
	return -1
}

// readCgroupV1CPU 读取 cgroup v1 CPU 配额。
//
// 文件不存在或解析失败时返回 (0, false)。
// quota = -1 表示无限制，返回 (0, false)。
func readCgroupV1CPU() (int, bool) {
	quotaBytes, err := os.ReadFile(cgroupV1CPUFiles.quota)
	if err != nil {
		return 0, false
	}
	periodBytes, err := os.ReadFile(cgroupV1CPUFiles.period)
	if err != nil {
		return 0, false
	}
	quota, err := strconv.ParseInt(strings.TrimSpace(string(quotaBytes)), 10, 64)
	if err != nil || quota <= 0 {
		return 0, false
	}
	period, err := strconv.ParseInt(strings.TrimSpace(string(periodBytes)), 10, 64)
	if err != nil || period <= 0 {
		return 0, false
	}
	// CPU 数 = ceil(quota / period)。
	n := int((quota + period - 1) / period)
	if n < 1 {
		n = 1
	}
	return n, true
}

// readCgroupV2CPU 读取 cgroup v2 CPU 配额。
//
// 文件格式："quota period"（如 "100000 100000"）或 "max 100000"（无限制）。
// 文件不存在或解析失败时返回 (0, false)。
func readCgroupV2CPU() (int, bool) {
	data, err := os.ReadFile(cgroupV2CPUFile)
	if err != nil {
		return 0, false
	}
	parts := strings.Fields(strings.TrimSpace(string(data)))
	if len(parts) != 2 {
		return 0, false
	}
	if parts[0] == "max" {
		return 0, false
	}
	quota, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || quota <= 0 {
		return 0, false
	}
	period, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || period <= 0 {
		return 0, false
	}
	n := int((quota + period - 1) / period)
	if n < 1 {
		n = 1
	}
	return n, true
}

// 编译期断言：Optimizer 实现 interface（暂无 interface，仅自检）。
var _ = NewOptimizer
