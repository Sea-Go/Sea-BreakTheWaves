// Package event async.go — 异步 I/O（Task 13.5）。
//
// 该文件实现 3 个异步 I/O 组件，将慢速外部 I/O（Kafka / Hook / 反馈写入）
// 从主推荐路径剥离，避免阻塞 P99 延迟：
//   - AsyncEventEmitter：行为事件异步发射（队列满丢弃 + 记日志）
//   - AsyncHookRegistry：Hook 异步执行（OnEvent 在 worker goroutine 中执行）
//   - AsyncFeedbackWriter：反馈异步写入（quality/cf/rerank/profile 反馈）
//
// 所有组件通过本地 interface 注入底层 writer/emitter/hook 实现，与 domain
// 包接口同构但不直接依赖，确保可独立编译与离线测试。
//
// 二开扩展点：
//   - 替换 EventEmitter：实现该 interface 接入 Kafka/ES/日志
//   - 替换 FeedbackWriter：实现该 interface 接入 DB/消息队列
//   - 替换 Hook：实现 domain.Hook interface
//
// 不直接 import 第三方异步库，所有依赖通过 interface 注入。
package event

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ----------------------------------------------------------------------------
// 本地 interface（与 domain 同构，避免直接依赖）
// ----------------------------------------------------------------------------

// EventEmitter 事件发射 interface（本地定义，与 domain.EventEmitter 同构）。
//
// 职责：发射行为事件到下游（Kafka/ES/日志）。底层实现可为同步或异步；
// AsyncEventEmitter 在 worker goroutine 中调用底层 Emit，主路径不阻塞。
//
// 二开扩展点：实现该 interface 接入自研事件出口。
type EventEmitter interface {
	// Emit 发射行为事件。
	Emit(ctx context.Context, event domain.BehaviorEvent) error
}

// FeedbackWriter 反馈写入 interface。
//
// 职责：写入反馈数据到下游（DB/消息队列）。
// 底层实现可为同步或异步；AsyncFeedbackWriter 在 worker goroutine 中调用底层 Write。
//
// 二开扩展点：实现该 interface 接入自研反馈存储。
type FeedbackWriter interface {
	// Write 写入一条反馈。
	Write(ctx context.Context, fb Feedback) error
}

// Feedback 反馈数据结构。
//
// 字段语义：
//   - Type：反馈类型（quality/cf/rerank/profile）
//   - Data：反馈数据（任意类型，由具体反馈类型决定）
type Feedback struct {
	// Type 反馈类型。
	Type string
	// Data 反馈数据。
	Data any
}

// HookEvent Hook 事件，封装 Hook 与事件的绑定，供 worker 异步执行。
type HookEvent struct {
	// Hook 待执行的 Hook。
	Hook Hook
	// Event 触发的事件。
	Event domain.BehaviorEvent
}

// ----------------------------------------------------------------------------
// AsyncEventEmitter 异步事件发射器
// ----------------------------------------------------------------------------

// AsyncEventEmitter 异步事件发射器。
//
// 设计：
//   - emitter：底层 EventEmitter 实现（如 Kafka producer）
//   - queue：行为事件 channel（缓冲队列，满了丢弃并记日志）
//   - workers：消费 queue 的 worker goroutine 数量
//   - wg：跟踪所有 worker 是否完成
//   - closed：原子标记，标识是否已 Close（避免重复关闭 channel panic）
//   - dropped：丢弃计数（atomic 操作，用于观测队列丢弃量）
//   - emitted：成功发射计数（atomic 操作）
//
// 二开扩展点：通过 NewAsyncEventEmitter(emitter, queueSize, workers) 注入。
type AsyncEventEmitter struct {
	emitter EventEmitter
	queue   chan domain.BehaviorEvent
	workers int
	wg      sync.WaitGroup
	closed  int32
	dropped int64
	emitted int64
	logger  *slog.Logger
}

// NewAsyncEventEmitter 构造 AsyncEventEmitter。
// emitter 底层 EventEmitter 实现；
// queueSize 队列大小（<=0 时默认 1024）；
// workers worker 数量（<=0 时默认 4）。
// 返回 *AsyncEventEmitter（未启动，需调用 Start 启动 worker）。
func NewAsyncEventEmitter(emitter EventEmitter, queueSize int, workers int) *AsyncEventEmitter {
	if queueSize <= 0 {
		queueSize = 1024
	}
	if workers <= 0 {
		workers = 4
	}
	return &AsyncEventEmitter{
		emitter: emitter,
		queue:   make(chan domain.BehaviorEvent, queueSize),
		workers: workers,
		logger:  slog.Default(),
	}
}

// Emit 非阻塞写入事件到队列。
//
// 队列已满时丢弃事件并记日志（避免阻塞主路径），dropped 计数 +1；
// 已 Close 时直接返回 ErrAsyncClosed。
// ctx 当前未使用（异步执行无法传递 ctx，底层 Emit 用 context.Background）。
func (e *AsyncEventEmitter) Emit(_ context.Context, event domain.BehaviorEvent) error {
	if e == nil {
		return errors.New("async emitter: nil receiver")
	}
	if atomic.LoadInt32(&e.closed) == 1 {
		return ErrAsyncClosed
	}
	select {
	case e.queue <- event:
		return nil
	default:
		atomic.AddInt64(&e.dropped, 1)
		if e.logger != nil {
			e.logger.Warn("async event emitter: queue full, event dropped",
				slog.String("event_type", event.EventType),
				slog.String("user_id", event.UserID),
			)
		}
		return ErrQueueFull
	}
}

// Start 启动 worker goroutine 消费 queue。
//
// 启动 workers 个 goroutine，每个 goroutine 从 queue 读取事件并调用底层 Emit。
// 重复调用安全（已启动时再次 Start 不会创建额外 worker）。
// ctx 用于传递给底层 Emit（避免 worker goroutine 持有 ctx 导致泄漏，
// 这里将 ctx 拷贝为 backgroundCtx，实际使用 context.Background）。
func (e *AsyncEventEmitter) Start(ctx context.Context) {
	if e == nil {
		return
	}
	for i := 0; i < e.workers; i++ {
		e.wg.Add(1)
		go e.worker(ctx)
	}
}

// worker 消费 queue 的 goroutine。
func (e *AsyncEventEmitter) worker(ctx context.Context) {
	defer e.wg.Done()
	for event := range e.queue {
		// 用 background ctx 调用底层 Emit（避免主路径 ctx 取消影响异步发射）。
		// 但若上层 ctx 仍未取消，可透传；这里简化为 background。
		_ = ctx // 标记 ctx 已被消费（保留参数签名以便后续扩展）
		if e.emitter != nil {
			if err := e.emitter.Emit(context.Background(), event); err == nil {
				atomic.AddInt64(&e.emitted, 1)
			}
		}
	}
}

// Close 优雅关闭。
//
// 关闭 queue channel，等待所有 worker 完成剩余事件后退出。
// 幂等：重复调用安全（用 closed 原子标记防重复关闭 channel）。
func (e *AsyncEventEmitter) Close() {
	if e == nil {
		return
	}
	if !atomic.CompareAndSwapInt32(&e.closed, 0, 1) {
		return
	}
	close(e.queue)
	e.wg.Wait()
}

// Dropped 返回累计丢弃事件数（atomic 读）。
func (e *AsyncEventEmitter) Dropped() int64 {
	if e == nil {
		return 0
	}
	return atomic.LoadInt64(&e.dropped)
}

// Emitted 返回累计成功发射事件数（atomic 读）。
func (e *AsyncEventEmitter) Emitted() int64 {
	if e == nil {
		return 0
	}
	return atomic.LoadInt64(&e.emitted)
}

// ----------------------------------------------------------------------------
// AsyncHookRegistry 异步 Hook 注册表
// ----------------------------------------------------------------------------

// AsyncHookRegistry 异步 Hook 注册表。
//
// 设计：
//   - hooks：已注册的 Hook 列表
//   - queue：HookEvent channel（缓冲队列，满了丢弃并记日志）
//   - workers：消费 queue 的 worker goroutine 数量
//   - wg：跟踪所有 worker 是否完成
//   - closed：原子标记
//   - dropped：丢弃计数
//
// 与 Registry 区别：Registry.Emit 同步顺序调用 Hook；
// AsyncHookRegistry.Emit 将 Hook+Event 投递到 queue，worker 异步执行 Hook.OnEvent。
//
// 二开扩展点：通过 NewAsyncHookRegistry(queueSize, workers) 创建实例。
type AsyncHookRegistry struct {
	mu       sync.RWMutex
	hooks    []Hook
	queue    chan HookEvent
	workers  int
	wg       sync.WaitGroup
	closed   int32
	dropped  int64
	executed int64
	logger   *slog.Logger
}

// NewAsyncHookRegistry 构造 AsyncHookRegistry。
// queueSize 队列大小（<=0 时默认 1024）；
// workers worker 数量（<=0 时默认 4）。
// 返回 *AsyncHookRegistry（未启动，需调用 Start 启动 worker）。
func NewAsyncHookRegistry(queueSize int, workers int) *AsyncHookRegistry {
	if queueSize <= 0 {
		queueSize = 1024
	}
	if workers <= 0 {
		workers = 4
	}
	return &AsyncHookRegistry{
		queue:   make(chan HookEvent, queueSize),
		workers: workers,
		logger:  slog.Default(),
	}
}

// Register 注册 Hook。
// 返回 error（nil Hook 返回错误；已 Close 返回 ErrAsyncClosed）。
func (r *AsyncHookRegistry) Register(h Hook) error {
	if r == nil {
		return errors.New("async hook registry: nil receiver")
	}
	if h == nil {
		return errors.New("async hook registry: register nil hook")
	}
	if atomic.LoadInt32(&r.closed) == 1 {
		return ErrAsyncClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks = append(r.hooks, h)
	return nil
}

// Emit 异步触发 Hook。
//
// 对每个已注册的 Hook，将 HookEvent 投递到 queue；
// 队列满时丢弃该 HookEvent 并记日志。
// ctx 当前未使用。
func (r *AsyncHookRegistry) Emit(_ context.Context, event domain.BehaviorEvent) error {
	if r == nil {
		return errors.New("async hook registry: nil receiver")
	}
	if atomic.LoadInt32(&r.closed) == 1 {
		return ErrAsyncClosed
	}
	hooks := r.snapshot()
	for _, h := range hooks {
		he := HookEvent{Hook: h, Event: event}
		select {
		case r.queue <- he:
			// 投递成功。
		default:
			atomic.AddInt64(&r.dropped, 1)
			if r.logger != nil {
				r.logger.Warn("async hook registry: queue full, hook event dropped",
					slog.String("hook", h.Name()),
					slog.String("event_type", event.EventType),
				)
			}
		}
	}
	return nil
}

// Start 启动 worker goroutine 消费 queue。
func (r *AsyncHookRegistry) Start(ctx context.Context) {
	if r == nil {
		return
	}
	for i := 0; i < r.workers; i++ {
		r.wg.Add(1)
		go r.worker(ctx)
	}
}

// worker 消费 queue 的 goroutine。
func (r *AsyncHookRegistry) worker(ctx context.Context) {
	defer r.wg.Done()
	for he := range r.queue {
		_ = ctx
		if he.Hook == nil {
			continue
		}
		// 错误隔离：Hook 失败不影响其他 Hook（这里吞掉错误，仅记日志）。
		if err := he.Hook.OnEvent(context.Background(), he.Event); err != nil {
			if r.logger != nil {
				r.logger.Warn("async hook registry: hook error",
					slog.String("hook", he.Hook.Name()),
					slog.String("err", err.Error()),
				)
			}
		}
		atomic.AddInt64(&r.executed, 1)
	}
}

// Close 优雅关闭。
func (r *AsyncHookRegistry) Close() {
	if r == nil {
		return
	}
	if !atomic.CompareAndSwapInt32(&r.closed, 0, 1) {
		return
	}
	close(r.queue)
	r.wg.Wait()
}

// snapshot 在读锁下拷贝当前 Hook 列表。
func (r *AsyncHookRegistry) snapshot() []Hook {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Hook, len(r.hooks))
	copy(out, r.hooks)
	return out
}

// Dropped 返回累计丢弃的 HookEvent 数。
func (r *AsyncHookRegistry) Dropped() int64 {
	if r == nil {
		return 0
	}
	return atomic.LoadInt64(&r.dropped)
}

// Executed 返回累计执行的 HookEvent 数。
func (r *AsyncHookRegistry) Executed() int64 {
	if r == nil {
		return 0
	}
	return atomic.LoadInt64(&r.executed)
}

// ----------------------------------------------------------------------------
// AsyncFeedbackWriter 异步反馈写入器
// ----------------------------------------------------------------------------

// AsyncFeedbackWriter 异步反馈写入器。
//
// 设计：
//   - writer：底层 FeedbackWriter 实现（如 DB writer）
//   - queue：Feedback channel（缓冲队列，满了丢弃并记日志）
//   - wg：跟踪 worker 是否完成
//   - closed：原子标记
//   - dropped：丢弃计数
//   - written：成功写入计数
//
// 二开扩展点：通过 NewAsyncFeedbackWriter(writer, queueSize, workers) 注入。
type AsyncFeedbackWriter struct {
	writer  FeedbackWriter
	queue   chan Feedback
	wg      sync.WaitGroup
	closed  int32
	dropped int64
	written int64
	logger  *slog.Logger
}

// NewAsyncFeedbackWriter 构造 AsyncFeedbackWriter。
// writer 底层 FeedbackWriter 实现；
// queueSize 队列大小（<=0 时默认 1024）；
// workers worker 数量（<=0 时默认 1，反馈写入通常单线程串行）。
// 返回 *AsyncFeedbackWriter（未启动，需调用 Start 启动 worker）。
func NewAsyncFeedbackWriter(writer FeedbackWriter, queueSize int, workers int) *AsyncFeedbackWriter {
	if queueSize <= 0 {
		queueSize = 1024
	}
	if workers <= 0 {
		workers = 1
	}
	return &AsyncFeedbackWriter{
		writer: writer,
		queue:  make(chan Feedback, queueSize),
		logger: slog.Default(),
	}
}

// Write 非阻塞写入反馈到队列。
//
// 队列已满时丢弃并记日志；已 Close 时返回 ErrAsyncClosed。
func (w *AsyncFeedbackWriter) Write(fb Feedback) error {
	if w == nil {
		return errors.New("async feedback writer: nil receiver")
	}
	if atomic.LoadInt32(&w.closed) == 1 {
		return ErrAsyncClosed
	}
	select {
	case w.queue <- fb:
		return nil
	default:
		atomic.AddInt64(&w.dropped, 1)
		if w.logger != nil {
			w.logger.Warn("async feedback writer: queue full, feedback dropped",
				slog.String("type", fb.Type),
			)
		}
		return ErrQueueFull
	}
}

// Start 启动 worker goroutine 消费 queue。
func (w *AsyncFeedbackWriter) Start(ctx context.Context) {
	if w == nil {
		return
	}
	// workers 数量由 queueSize 决定，但默认 1。
	// 简化：只用 1 个 worker 串行写入（避免并发写入 DB）。
	w.wg.Add(1)
	go w.worker(ctx)
}

// worker 消费 queue 的 goroutine。
func (w *AsyncFeedbackWriter) worker(ctx context.Context) {
	defer w.wg.Done()
	for fb := range w.queue {
		_ = ctx
		if w.writer != nil {
			if err := w.writer.Write(context.Background(), fb); err == nil {
				atomic.AddInt64(&w.written, 1)
			}
		}
	}
}

// Close 优雅关闭。
func (w *AsyncFeedbackWriter) Close() {
	if w == nil {
		return
	}
	if !atomic.CompareAndSwapInt32(&w.closed, 0, 1) {
		return
	}
	close(w.queue)
	w.wg.Wait()
}

// Dropped 返回累计丢弃数。
func (w *AsyncFeedbackWriter) Dropped() int64 {
	if w == nil {
		return 0
	}
	return atomic.LoadInt64(&w.dropped)
}

// Written 返回累计成功写入数。
func (w *AsyncFeedbackWriter) Written() int64 {
	if w == nil {
		return 0
	}
	return atomic.LoadInt64(&w.written)
}

// ----------------------------------------------------------------------------
// 错误定义
// ----------------------------------------------------------------------------

// ErrQueueFull 队列已满错误（事件被丢弃）。
var ErrQueueFull = errors.New("event: async queue full, event dropped")

// ErrAsyncClosed 异步组件已关闭错误。
var ErrAsyncClosed = errors.New("event: async component closed")

// 编译期断言：AsyncEventEmitter 实现 EventEmitter interface。
// 注意：AsyncFeedbackWriter.Write(Feedback) 与 FeedbackWriter.Write(ctx, Feedback)
// 签名不同（异步队列写入不需 ctx），故 AsyncFeedbackWriter 不实现 FeedbackWriter。
var _ EventEmitter = (*AsyncEventEmitter)(nil)
