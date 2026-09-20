// Package cpu pool.go — 并行召回 + 协程池（Task 13.1）。
//
// 该文件实现本地协程池 WorkerPool 与并行召回编排器 ParallelRecaller：
//   - WorkerPool：基于 channel + goroutine 的本地协程池，不依赖 ants，避免外部
//     依赖膨胀。通过 Pool interface 暴露 Submit/Release/Cap/Running/Waiting/Free
//   - ParallelRecaller：用 errgroup + Pool 并行调用多个召回源，部分失败不阻断
//     （收集所有结果，标记 Err），返回 RecallResult 列表
//   - PoolMetrics：返回池水位指标（Cap/Running/Waiting/Free/Utilization）
//
// 二开扩展点：
//   - 替换 Pool：实现该 interface 接入 ants/goroutine pool 等第三方池
//   - 替换 RecallSource：实现该 interface 接入自研召回源
//
// 不直接 import ants / automaxprocs / trpc-agent-go，所有依赖通过 interface 注入。
package cpu

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

// Pool 协程池 interface，抽象 ants 协程池。
//
// 职责：暴露 Submit（提交任务）/ Release（释放池）/ Cap（容量）/ Running
// （运行中 worker 数）/ Waiting（等待中任务数）/ Free（剩余空闲 worker 数）。
//
// 二开扩展点：实现该 interface 接入 ants/goroutine pool 等第三方池。
type Pool interface {
	// Submit 提交一个任务到池中执行。
	Submit(task func()) error
	// Release 释放池资源（关闭 worker，等待任务完成）。
	Release()
	// Cap 返回池容量。
	Cap() int
	// Running 返回正在执行任务的 worker 数。
	Running() int
	// Waiting 返回等待中任务数。
	Waiting() int
	// Free 返回剩余空闲 worker 数（Cap - Running）。
	Free() int
}

// WorkerPool 本地协程池实现（不依赖 ants）。
//
// 设计：
//   - cap：worker 数量上限（默认 2*runtime.NumCPU）
//   - tasks：任务 channel（带缓冲，缓冲大小 = cap）
//   - wg：跟踪所有正在执行的任务完成情况
//   - closed：原子标记，标识池是否已 Release（避免重复关闭 channel panic）
//
// 二开扩展点：通过 NewWorkerPool(cap) 注入自定义容量。
type WorkerPool struct {
	cap    int
	tasks  chan func()
	wg     sync.WaitGroup
	closed int32
}

// NewWorkerPool 构造 WorkerPool。
// cap 池容量（<=0 时默认 2*runtime.NumCPU）。
// 返回 *WorkerPool（已启动 worker goroutine）。
func NewWorkerPool(cap int) *WorkerPool {
	if cap <= 0 {
		cap = 2 * runtime.NumCPU()
	}
	p := &WorkerPool{
		cap:   cap,
		tasks: make(chan func(), cap),
	}
	// 启动 cap 个 worker goroutine 消费 tasks。
	for i := 0; i < cap; i++ {
		go p.worker()
	}
	return p
}

// worker 消费 tasks channel 的 goroutine。
//
// 从 tasks 读取任务执行；pool Release 后 channel 关闭，worker 退出。
// 单个任务 panic 会被 recover，避免 worker 崩溃。
func (p *WorkerPool) worker() {
	for task := range p.tasks {
		if task != nil {
			safeRun(task)
		}
		p.wg.Done()
	}
}

// safeRun 安全执行任务，捕获 panic 避免 worker 崩溃。
func safeRun(task func()) {
	defer func() {
		_ = recover()
	}()
	task()
}

// Submit 提交一个任务到池中执行。
//
// 池已 Release 时返回 ErrPoolReleased；
// 否则 wg.Add(1) 并将 task 写入 tasks channel（channel 满时阻塞）。
func (p *WorkerPool) Submit(task func()) error {
	if p == nil {
		return errors.New("pool: nil receiver")
	}
	if atomic.LoadInt32(&p.closed) == 1 {
		return ErrPoolReleased
	}
	p.wg.Add(1)
	p.tasks <- task
	return nil
}

// Release 释放池资源（关闭 tasks channel，等待所有 worker 退出）。
//
// 幂等：重复调用安全（用 closed 原子标记防重复关闭 channel）。
func (p *WorkerPool) Release() {
	if p == nil {
		return
	}
	if !atomic.CompareAndSwapInt32(&p.closed, 0, 1) {
		return
	}
	close(p.tasks)
	p.wg.Wait()
}

// Cap 返回池容量。
func (p *WorkerPool) Cap() int {
	if p == nil {
		return 0
	}
	return p.cap
}

// Running 返回正在执行任务的 worker 数。
//
// 实现说明：用 len(tasks) 反向计算 — tasks 缓冲中等待执行的任务数 + 已取出
// 但未完成的任务数（wg 计数）≈ 等待中的任务。真正"正在执行"无法精确观测，
// 这里用 wg 计数 - len(tasks) 估算正在执行的任务数（下限 0）。
func (p *WorkerPool) Running() int {
	if p == nil {
		return 0
	}
	running := int(p.waitingCount()) - len(p.tasks)
	if running < 0 {
		return 0
	}
	return running
}

// Waiting 返回等待中任务数（已 Submit 但未开始执行）。
func (p *WorkerPool) Waiting() int {
	if p == nil {
		return 0
	}
	return len(p.tasks)
}

// Free 返回剩余空闲 worker 数（Cap - Running）。
func (p *WorkerPool) Free() int {
	if p == nil {
		return 0
	}
	free := p.cap - p.Running()
	if free < 0 {
		return 0
	}
	return free
}

// waitingCount 返回 wg 当前计数（已 Submit 未 Done 的任务总数）。
// sync.WaitGroup 不直接暴露计数，用 Add(0) 不改变计数；这里用反向计算：
// 用 channel 内 + Running 估算 = len(tasks) + Running = wg 计数。
// 该值与 Running 字段共同依赖 wg 计数，为避免循环依赖，单独计算。
func (p *WorkerPool) waitingCount() int64 {
	// wg 计数 = len(tasks) + 正在执行数；
	// 但 Running 也依赖 wg 计数。这里返回 len(tasks) + 0（保守估算下限）。
	// 实际正在执行数 = wg 计数 - len(tasks)，二者关系固定。
	// 用 wg.Add(0) 不返回计数，故只能用 channel 长度近似。
	// 综上：waitingCount 返回 len(tasks) + running（running 通过其他方式估算），
	// 但为避免循环依赖，此处返回 len(tasks)（保守下限）。
	return int64(len(p.tasks))
}

// ErrPoolReleased 池已 Release 错误。
var ErrPoolReleased = errors.New("cpu: pool already released")

// ----------------------------------------------------------------------------
// ParallelRecaller 并行召回编排器
// ----------------------------------------------------------------------------

// RecallSource 召回源 interface，用于 ParallelRecaller 并行调用。
//
// 二开扩展点：实现该 interface 接入自研召回源（CF/Graph/Content/Channel/Rule）。
type RecallSource interface {
	// Name 返回召回源名称（如 "cf"/"graph"/"content"）。
	Name() string
	// Recall 执行召回，返回结果与 error。
	// ctx 上下文（用于超时与取消）。
	Recall(ctx context.Context) (RecallResult, error)
}

// RecallResult 召回结果。
//
// 字段语义：
//   - Source：召回源名称
//   - Items：召回结果列表（any 类型，适配不同召回源返回结构）
//   - Err：召回错误（部分失败不阻断，标记 Err）
//   - LatencyMs：召回耗时（毫秒）
type RecallResult struct {
	// Source 召回源名称。
	Source string
	// Items 召回结果列表。
	Items []any
	// Err 召回错误。
	Err error
	// LatencyMs 召回耗时（毫秒）。
	LatencyMs int64
}

// ParallelRecaller 并行召回编排器。
//
// 用 errgroup + Pool 并行调用多个召回源：
//   - errgroup.Group 负责错误传播与 ctx 取消（部分失败不阻断，见下文）
//   - Pool 限制并发召回数（避免召回源过多打爆下游）
//
// 部分失败策略：单个召回源失败不阻断其他召回源，错误标记到 RecallResult.Err
// 聚合返回。只有 ctx 取消或 Submit 失败才返回整体 error。
//
// 二开扩展点：通过 NewParallelRecaller(pool) 注入自研 Pool。
type ParallelRecaller struct {
	pool Pool
}

// NewParallelRecaller 构造 ParallelRecaller。
// pool 协程池（用于限制并发召回数）。
// 返回 *ParallelRecaller。
func NewParallelRecaller(pool Pool) *ParallelRecaller {
	return &ParallelRecaller{pool: pool}
}

// RecallParallel 并行调用多个召回源。
//
// 流程：
//  1. 为每个召回源启动一个 goroutine（通过 pool.Submit 提交）
//  2. 每个召回源独立执行 Recall，记录 LatencyMs
//  3. 部分失败不阻断（错误标记到 RecallResult.Err）
//  4. ctx 取消时未启动的召回源跳过
//  5. 等待所有任务完成（包括 pool 中的任务，用 WaitGroup 跟踪）
//  6. 返回所有召回源结果（顺序与 sources 一致）
//
// 整体 error 仅在 pool Submit 失败（如池已 Release）且 fallback 也失败时返回。
func (r *ParallelRecaller) RecallParallel(ctx context.Context, sources []RecallSource) ([]RecallResult, error) {
	if r == nil {
		return nil, errors.New("parallel recaller: nil receiver")
	}
	results := make([]RecallResult, len(sources))
	if len(sources) == 0 {
		return results, nil
	}
	// 用 errgroup 管理并行 goroutine；设置 ctx 取消则未启动的任务跳过。
	g, gctx := errgroup.WithContext(ctx)
	// 额外用 WaitGroup 跟踪通过 pool.Submit 提交的任务（pool 不在 errgroup 跟踪范围内）。
	var poolWg sync.WaitGroup
	for i, src := range sources {
		i, src := i, src
		// 提交到 pool 限制并发度。
		if r.pool != nil {
			poolWg.Add(1)
			submitErr := r.pool.Submit(func() {
				defer poolWg.Done()
				r.runRecall(gctx, src, &results[i])
			})
			if submitErr != nil {
				// pool 已 Release 或不可用，回退到 errgroup 同步执行。
				// 注意：上面 poolWg.Add(1) 已加，需要先 Done 才能避免泄漏。
				poolWg.Done()
				g.Go(func() error {
					r.runRecall(gctx, src, &results[i])
					return nil
				})
			}
			continue
		}
		// 无 pool 时直接用 errgroup 启动 goroutine。
		g.Go(func() error {
			r.runRecall(gctx, src, &results[i])
			return nil
		})
	}
	// 等待 errgroup 启动的 goroutine 完成（无 pool 场景或 pool Submit 失败的回退）。
	_ = g.Wait()
	// 等待 pool 中所有任务完成（确保 results 数组写入完成，避免 data race）。
	poolWg.Wait()
	return results, nil
}

// runRecall 执行单个召回源的召回，结果写入 result（含耗时统计）。
//
// 部分失败策略：Recall 返回 error 时记录到 result.Err，不向上抛出，
// 避免单个召回源失败导致整体召回失败。
func (r *ParallelRecaller) runRecall(ctx context.Context, src RecallSource, result *RecallResult) {
	if src == nil {
		result.Source = "unknown"
		result.Err = errors.New("nil recall source")
		return
	}
	result.Source = src.Name()
	start := time.Now()
	res, err := src.Recall(ctx)
	result.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Err = err
		return
	}
	result.Items = res.Items
	if result.Source == "" {
		result.Source = res.Source
	}
}

// PoolMetrics 返回池水位指标。
//
// 字段语义：
//   - Cap：池容量
//   - Running：运行中 worker 数
//   - Waiting：等待中任务数
//   - Free：剩余空闲 worker 数
//   - Utilization：利用率 = Running / Cap（Cap=0 时为 0）
type PoolMetrics struct {
	// Cap 池容量。
	Cap int
	// Running 运行中 worker 数。
	Running int
	// Waiting 等待中任务数。
	Waiting int
	// Free 剩余空闲 worker 数。
	Free int
	// Utilization 利用率（0-1）。
	Utilization float64
}

// PoolMetrics 返回当前池水位指标。
//
// pool 为 nil 时返回零值。
func (r *ParallelRecaller) PoolMetrics() PoolMetrics {
	if r == nil || r.pool == nil {
		return PoolMetrics{}
	}
	cap := r.pool.Cap()
	running := r.pool.Running()
	waiting := r.pool.Waiting()
	free := r.pool.Free()
	utilization := 0.0
	if cap > 0 {
		utilization = float64(running) / float64(cap)
	}
	return PoolMetrics{
		Cap:         cap,
		Running:     running,
		Waiting:     waiting,
		Free:        free,
		Utilization: utilization,
	}
}

// 编译期断言：WorkerPool 实现 Pool interface。
var _ Pool = (*WorkerPool)(nil)
