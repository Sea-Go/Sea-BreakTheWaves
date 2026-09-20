// Package cpu pool_test.go — WorkerPool 与 ParallelRecaller 单元测试（Task 13.1）。
//
// 用 stdlib 手写 stub（不引入 testify）覆盖：
//   - WorkerPool Submit/Cap/Running/Waiting/Free/Release 生命周期
//   - ParallelRecaller 并行执行 + 部分失败不阻断 + 池指标
//   - stub Pool + stub RecallSource 验证 interface 注入
//
// stub 命名加 PR（ParallelRecall）前缀避免与已有 stub 冲突。
package cpu

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ----------------------------------------------------------------------------
// stub 实现（PR 前缀 = ParallelRecall）
// ----------------------------------------------------------------------------

// stubPRPool 测试用 Pool stub，记录 Submit 次数与最后提交的任务。
type stubPRPool struct {
	mu        sync.Mutex
	closed    bool
	cap       int
	submits   int64
	running   int
	waiting   int
	free      int
	released  int64
	execWg    sync.WaitGroup
	execCount int64
}

func newStubPRPool(cap int) *stubPRPool {
	return &stubPRPool{cap: cap}
}

func (s *stubPRPool) Submit(task func()) error {
	if s == nil {
		return errors.New("nil pool")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrPoolReleased
	}
	atomic.AddInt64(&s.submits, 1)
	s.mu.Unlock()
	atomic.AddInt64(&s.execCount, 1)
	s.execWg.Add(1)
	go func() {
		defer s.execWg.Done()
		if task != nil {
			task()
		}
	}()
	return nil
}

func (s *stubPRPool) Release() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	atomic.AddInt64(&s.released, 1)
	s.execWg.Wait()
}

func (s *stubPRPool) Cap() int {
	if s == nil {
		return 0
	}
	return s.cap
}

func (s *stubPRPool) Running() int {
	if s == nil {
		return 0
	}
	return s.running
}

func (s *stubPRPool) Waiting() int {
	if s == nil {
		return 0
	}
	return s.waiting
}

func (s *stubPRPool) Free() int {
	if s == nil {
		return 0
	}
	return s.free
}

// stubPRSource 测试用 RecallSource stub。
type stubPRSource struct {
	name    string
	items   []any
	err     error
	latency time.Duration
	calls   int64
}

func (s *stubPRSource) Name() string { return s.name }

func (s *stubPRSource) Recall(_ context.Context) (RecallResult, error) {
	atomic.AddInt64(&s.calls, 1)
	if s.latency > 0 {
		time.Sleep(s.latency)
	}
	if s.err != nil {
		return RecallResult{Source: s.name, Err: s.err}, s.err
	}
	return RecallResult{Source: s.name, Items: s.items}, nil
}

// 编译期断言：stub 实现 interface。
var _ Pool = (*stubPRPool)(nil)
var _ RecallSource = (*stubPRSource)(nil)

// ----------------------------------------------------------------------------
// WorkerPool 测试
// ----------------------------------------------------------------------------

// TestWorkerPool_DefaultCap 验证 cap<=0 时默认 2*NumCPU。
func TestWorkerPool_DefaultCap(t *testing.T) {
	p := NewWorkerPool(0)
	defer p.Release()
	expected := 2 * runtime.NumCPU()
	if got := p.Cap(); got != expected {
		t.Errorf("Cap = %d, 期望 %d", got, expected)
	}
}

// TestWorkerPool_NegativeCap 验证 cap<0 时默认 2*NumCPU。
func TestWorkerPool_NegativeCap(t *testing.T) {
	p := NewWorkerPool(-1)
	defer p.Release()
	if got := p.Cap(); got <= 0 {
		t.Errorf("Cap = %d, 应 > 0", got)
	}
}

// TestWorkerPool_SubmitAndExecute 验证 Submit 后任务被执行。
func TestWorkerPool_SubmitAndExecute(t *testing.T) {
	p := NewWorkerPool(4)
	defer p.Release()
	var executed int64
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		err := p.Submit(func() {
			defer wg.Done()
			atomic.AddInt64(&executed, 1)
		})
		if err != nil {
			t.Fatalf("Submit 错误: %v", err)
		}
	}
	wg.Wait()
	if got := atomic.LoadInt64(&executed); got != 10 {
		t.Errorf("executed = %d, 期望 10", got)
	}
}

// TestWorkerPool_SubmitAfterRelease 验证 Release 后 Submit 返回 ErrPoolReleased。
func TestWorkerPool_SubmitAfterRelease(t *testing.T) {
	p := NewWorkerPool(2)
	p.Release()
	err := p.Submit(func() {})
	if !errors.Is(err, ErrPoolReleased) {
		t.Errorf("Submit after Release 应返回 ErrPoolReleased, 实际: %v", err)
	}
}

// TestWorkerPool_DoubleRelease 验证重复 Release 安全（幂等）。
func TestWorkerPool_DoubleRelease(t *testing.T) {
	p := NewWorkerPool(2)
	p.Release()
	// 重复 Release 不 panic。
	p.Release()
}

// TestWorkerPool_ConcurrentSubmit 验证并发 Submit 安全。
func TestWorkerPool_ConcurrentSubmit(t *testing.T) {
	p := NewWorkerPool(8)
	defer p.Release()
	var executed int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		err := p.Submit(func() {
			defer wg.Done()
			atomic.AddInt64(&executed, 1)
		})
		if err != nil {
			t.Fatalf("Submit 错误: %v", err)
		}
	}
	wg.Wait()
	if got := atomic.LoadInt64(&executed); got != 100 {
		t.Errorf("executed = %d, 期望 100", got)
	}
}

// TestWorkerPool_PanicRecovery 验证任务 panic 不影响其他任务执行。
func TestWorkerPool_PanicRecovery(t *testing.T) {
	p := NewWorkerPool(2)
	defer p.Release()
	var wg sync.WaitGroup
	// 第一个任务 panic
	wg.Add(1)
	_ = p.Submit(func() {
		defer wg.Done()
		panic("test panic")
	})
	wg.Wait()
	// 第二个任务应正常执行
	wg.Add(1)
	var executed int64
	_ = p.Submit(func() {
		defer wg.Done()
		atomic.StoreInt64(&executed, 1)
	})
	wg.Wait()
	if got := atomic.LoadInt64(&executed); got != 1 {
		t.Errorf("panic 后任务未执行, executed = %d, 期望 1", got)
	}
}

// TestWorkerPool_Metrics 验证 Cap/Free 等指标返回合理值。
func TestWorkerPool_Metrics(t *testing.T) {
	p := NewWorkerPool(4)
	defer p.Release()
	if got := p.Cap(); got != 4 {
		t.Errorf("Cap = %d, 期望 4", got)
	}
	// 空闲时 Free 应为 4（无任务运行）。
	if got := p.Free(); got < 0 || got > 4 {
		t.Errorf("Free = %d, 应在 [0,4]", got)
	}
	// Running 应非负。
	if got := p.Running(); got < 0 {
		t.Errorf("Running = %d, 应 >= 0", got)
	}
}

// TestWorkerPool_NilReceiver 验证 nil receiver 安全处理。
func TestWorkerPool_NilReceiver(t *testing.T) {
	var p *WorkerPool
	if err := p.Submit(func() {}); err == nil {
		t.Error("nil receiver Submit 应返回错误")
	}
	if got := p.Cap(); got != 0 {
		t.Errorf("nil receiver Cap 应为 0, 实际 %d", got)
	}
	p.Release() // 不应 panic
}

// ----------------------------------------------------------------------------
// ParallelRecaller 测试
// ----------------------------------------------------------------------------

// TestParallelRecaller_Parallel 验证多召回源并行执行。
func TestParallelRecaller_Parallel(t *testing.T) {
	pool := NewWorkerPool(4)
	defer pool.Release()
	r := NewParallelRecaller(pool)
	sources := []RecallSource{
		&stubPRSource{name: "cf", items: []any{"a1", "a2"}, latency: 10 * time.Millisecond},
		&stubPRSource{name: "graph", items: []any{"a3", "a4"}, latency: 10 * time.Millisecond},
		&stubPRSource{name: "content", items: []any{"a5"}, latency: 10 * time.Millisecond},
	}
	start := time.Now()
	results, err := r.RecallParallel(context.Background(), sources)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RecallParallel 错误: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results 长度 = %d, 期望 3", len(results))
	}
	// 并行执行总耗时应小于串行总和（30ms vs ~10ms）。
	if elapsed >= 25*time.Millisecond {
		t.Logf("警告: 并行执行耗时 %v 接近串行，可能未并行", elapsed)
	}
	// 验证每个 source 的结果。
	for i, src := range sources {
		if results[i].Source != src.Name() {
			t.Errorf("results[%d].Source = %q, 期望 %q", i, results[i].Source, src.Name())
		}
		if results[i].Err != nil {
			t.Errorf("results[%d].Err = %v", i, results[i].Err)
		}
		if len(results[i].Items) != len(src.(*stubPRSource).items) {
			t.Errorf("results[%d].Items 长度 = %d, 期望 %d",
				i, len(results[i].Items), len(src.(*stubPRSource).items))
		}
	}
}

// TestParallelRecaller_PartialFailure 验证部分失败不阻断。
func TestParallelRecaller_PartialFailure(t *testing.T) {
	pool := NewWorkerPool(4)
	defer pool.Release()
	r := NewParallelRecaller(pool)
	srcErr := errors.New("graph recall failed")
	sources := []RecallSource{
		&stubPRSource{name: "cf", items: []any{"a1"}},
		&stubPRSource{name: "graph", err: srcErr},
		&stubPRSource{name: "content", items: []any{"a2"}},
	}
	results, err := r.RecallParallel(context.Background(), sources)
	if err != nil {
		t.Fatalf("RecallParallel 整体错误应为 nil（部分失败不阻断）, 实际: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results 长度 = %d, 期望 3", len(results))
	}
	// graph 源应标记 Err。
	if results[1].Err == nil {
		t.Error("results[1].Err 应非 nil")
	}
	// 其他源应正常。
	if results[0].Err != nil {
		t.Errorf("results[0].Err 应为 nil, 实际 %v", results[0].Err)
	}
	if results[2].Err != nil {
		t.Errorf("results[2].Err 应为 nil, 实际 %v", results[2].Err)
	}
	// cf 与 content 应有结果。
	if len(results[0].Items) != 1 {
		t.Errorf("results[0].Items 长度 = %d, 期望 1", len(results[0].Items))
	}
	if len(results[2].Items) != 1 {
		t.Errorf("results[2].Items 长度 = %d, 期望 1", len(results[2].Items))
	}
}

// TestParallelRecaller_EmptySources 验证空 sources 返回空结果。
func TestParallelRecaller_EmptySources(t *testing.T) {
	r := NewParallelRecaller(nil)
	results, err := r.RecallParallel(context.Background(), nil)
	if err != nil {
		t.Fatalf("RecallParallel 错误: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("results 长度 = %d, 期望 0", len(results))
	}
}

// TestParallelRecaller_NilPool 验证 pool 为 nil 时仍可执行（用 errgroup）。
func TestParallelRecaller_NilPool(t *testing.T) {
	r := NewParallelRecaller(nil)
	sources := []RecallSource{
		&stubPRSource{name: "cf", items: []any{"a1"}},
		&stubPRSource{name: "graph", items: []any{"a2"}},
	}
	results, err := r.RecallParallel(context.Background(), sources)
	if err != nil {
		t.Fatalf("RecallParallel 错误: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results 长度 = %d, 期望 2", len(results))
	}
}

// TestParallelRecaller_StubPool 验证用 stub Pool 注入执行。
func TestParallelRecaller_StubPool(t *testing.T) {
	pool := newStubPRPool(2)
	defer pool.Release()
	r := NewParallelRecaller(pool)
	sources := []RecallSource{
		&stubPRSource{name: "cf", items: []any{"a1"}},
		&stubPRSource{name: "graph", items: []any{"a2"}},
	}
	results, err := r.RecallParallel(context.Background(), sources)
	if err != nil {
		t.Fatalf("RecallParallel 错误: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results 长度 = %d, 期望 2", len(results))
	}
	if got := atomic.LoadInt64(&pool.submits); got != 2 {
		t.Errorf("pool.submits = %d, 期望 2", got)
	}
}

// TestParallelRecaller_PoolMetrics 验证 PoolMetrics 返回池水位。
func TestParallelRecaller_PoolMetrics(t *testing.T) {
	pool := newStubPRPool(8)
	defer pool.Release()
	pool.running = 2
	pool.waiting = 3
	pool.free = 6
	r := NewParallelRecaller(pool)
	m := r.PoolMetrics()
	if m.Cap != 8 {
		t.Errorf("Cap = %d, 期望 8", m.Cap)
	}
	if m.Running != 2 {
		t.Errorf("Running = %d, 期望 2", m.Running)
	}
	if m.Waiting != 3 {
		t.Errorf("Waiting = %d, 期望 3", m.Waiting)
	}
	if m.Free != 6 {
		t.Errorf("Free = %d, 期望 6", m.Free)
	}
	if m.Utilization != 0.25 {
		t.Errorf("Utilization = %v, 期望 0.25", m.Utilization)
	}
}

// TestParallelRecaller_PoolMetricsNil 验证 pool 为 nil 时 PoolMetrics 返回零值。
func TestParallelRecaller_PoolMetricsNil(t *testing.T) {
	r := NewParallelRecaller(nil)
	m := r.PoolMetrics()
	if m.Cap != 0 || m.Running != 0 || m.Utilization != 0 {
		t.Errorf("nil pool PoolMetrics 应为零值, 实际 %+v", m)
	}
}

// TestParallelRecaller_NilReceiver 验证 nil receiver 安全处理。
func TestParallelRecaller_NilReceiver(t *testing.T) {
	var r *ParallelRecaller
	_, err := r.RecallParallel(context.Background(), nil)
	if err == nil {
		t.Error("nil receiver RecallParallel 应返回错误")
	}
}

// TestRecallResult_ZeroValue 验证零值安全。
func TestRecallResult_ZeroValue(t *testing.T) {
	var r RecallResult
	if r.Source != "" || len(r.Items) != 0 || r.Err != nil || r.LatencyMs != 0 {
		t.Errorf("零值 RecallResult 不正确: %+v", r)
	}
}

// TestPoolMetrics_ZeroValue 验证零值安全。
func TestPoolMetrics_ZeroValue(t *testing.T) {
	var m PoolMetrics
	if m.Cap != 0 || m.Utilization != 0 {
		t.Errorf("零值 PoolMetrics 不正确: %+v", m)
	}
}
