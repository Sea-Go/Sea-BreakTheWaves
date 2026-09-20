package training

import (
	"context"
	"time"
)

// ============================================================================
// 该文件实现训练流水线的定时调度器（Task 8.5 可选组件）。
//
// Scheduler 以固定间隔（默认 1 分钟）轮询 Pipeline.MaybeTrain，
// 由 MaybeTrain 内部判断是否触发全量或增量训练。
//
// 二开扩展点：
//   - 调整 ticker 间隔改变轮询频率
//   - 替换为 cron 表达式调度（接入 robfig/cron 等）
//   - 结合配置中心动态调整训练间隔
// ============================================================================

// defaultTickInterval 默认轮询间隔（1 分钟）。
const defaultTickInterval = time.Minute

// Scheduler 定时调度器，周期性触发 Pipeline.MaybeTrain。
//
// 启动后在新 goroutine 中运行轮询循环，通过 Stop 信号或 ctx 取消停止。
type Scheduler struct {
	pipeline *Pipeline
	stop     chan struct{}
	// tickInterval 轮询间隔。
	tickInterval time.Duration
}

// NewScheduler 创建调度器。
//
// p 待调度的训练流水线。
func NewScheduler(p *Pipeline) *Scheduler {
	return &Scheduler{
		pipeline:     p,
		stop:         make(chan struct{}),
		tickInterval: defaultTickInterval,
	}
}

// SetTickInterval 设置轮询间隔（二开点）。
func (s *Scheduler) SetTickInterval(d time.Duration) {
	s.tickInterval = d
}

// Start 启动定时调度，在新 goroutine 中运行轮询循环。
//
// 通过 ctx 取消或 Stop 方法停止。每 tickInterval 调用一次 Pipeline.MaybeTrain。
// 训练错误不影响下一轮调度（best-effort，错误仅被忽略）。
func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(s.tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-ticker.C:
			// best-effort：训练错误不影响下一轮调度。
			_ = s.pipeline.MaybeTrain(ctx)
		}
	}
}

// Stop 停止调度。
//
// 关闭 stop 信号，通知 Start 中的 goroutine 退出。
// 多次调用安全（close panic 由调用方保证不重复调用）。
func (s *Scheduler) Stop() {
	select {
	case <-s.stop:
		// 已关闭，不重复 close。
	default:
		close(s.stop)
	}
}
