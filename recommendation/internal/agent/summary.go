// Package agent summary.go — Session Summary 异步生成（Task 13.8）。
//
// 该文件实现 SummaryService：将 Session 行为事件序列异步生成摘要，
// 避免阻塞主推荐路径。支持同步模式（SyncMode=true 时同步等待结果）与异步模式。
//
// 职责：
//   - Summarize：提交摘要请求（异步或同步）
//   - Start：启动 worker goroutine 消费 queue
//   - Close：优雅关闭（关闭 queue + 等待 worker 完成）
//   - processSummary：内部方法，调用 LLM 生成摘要
//
// 二开扩展点：
//   - 通过 NewSummaryService(llm, queueSize, workers) 注入 LLMClient 与配置
//   - 通过 SummaryConfig 配置同步/异步模式与阈值
//
// 不直接 import trpc-agent-go / testify，所有依赖通过 interface 注入。
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sea/internal/domain"
)

// ----------------------------------------------------------------------------
// 请求与结果结构
// ----------------------------------------------------------------------------

// SummaryRequest 摘要请求。
//
// 字段语义：
//   - SessionID：会话 ID
//   - UserID：用户 ID
//   - Events：待摘要的行为事件列表
//   - SyncMode：true 时同步等待结果（result chan 等待）
type SummaryRequest struct {
	// SessionID 会话 ID。
	SessionID string
	// UserID 用户 ID。
	UserID string
	// Events 待摘要的行为事件列表。
	Events []domain.BehaviorEvent
	// SyncMode true 时同步等待结果。
	SyncMode bool
	// result 同步模式下的结果 channel（worker 写入后关闭）。
	result chan SummaryResult
}

// SummaryResult 摘要结果。
//
// 字段语义：
//   - SessionID：会话 ID
//   - Summary：摘要文本
//   - Topics：提取的主题列表
//   - Timestamp：生成时间戳
type SummaryResult struct {
	// SessionID 会话 ID。
	SessionID string
	// Summary 摘要文本。
	Summary string
	// Topics 提取的主题列表。
	Topics []string
	// Timestamp 生成时间戳（UnixMilli）。
	Timestamp int64
}

// ----------------------------------------------------------------------------
// 配置
// ----------------------------------------------------------------------------

// SummaryConfig 摘要服务配置。
//
// 字段语义：
//   - SyncSummaryIntraRun：是否在 Agent 运行中同步生成摘要（false=异步）
//   - MaxEventsBeforeSummary：触发摘要的事件数阈值
//   - QueueSize：queue 容量
//   - Workers：worker goroutine 数量
type SummaryConfig struct {
	// SyncSummaryIntraRun 是否同步生成摘要。
	SyncSummaryIntraRun bool
	// MaxEventsBeforeSummary 触发摘要的事件数阈值。
	MaxEventsBeforeSummary int
	// QueueSize queue 容量。
	QueueSize int
	// Workers worker goroutine 数量。
	Workers int
}

// DefaultSummaryConfig 默认摘要配置。
//
// 异步模式 + 50 事件阈值 + queue 128 + 2 workers。
func DefaultSummaryConfig() SummaryConfig {
	return SummaryConfig{
		SyncSummaryIntraRun:    false,
		MaxEventsBeforeSummary: 50,
		QueueSize:              128,
		Workers:                2,
	}
}

// ----------------------------------------------------------------------------
// SummaryService
// ----------------------------------------------------------------------------

// SummaryService 会话摘要服务。
//
// 设计：
//   - llm：底层 LLMClient 实现
//   - queue：SummaryRequest channel（缓冲队列，满了拒绝并记日志）
//   - workers：消费 queue 的 worker goroutine 数量
//   - wg：跟踪所有 worker 是否完成
//   - closed：原子标记，标识是否已 Close
//   - processed：累计处理请求数（atomic）
//   - failed：累计失败请求数（atomic）
//
// 二开扩展点：通过 NewSummaryService(llm, queueSize, workers) 注入。
type SummaryService struct {
	llm       LLMClient
	queue     chan SummaryRequest
	workers   int
	wg        sync.WaitGroup
	closed    int32
	processed int64
	failed    int64
	config    SummaryConfig
}

// NewSummaryService 构造 SummaryService。
// llm 底层 LLMClient 实现；
// queueSize queue 容量（<=0 时默认 128）；
// workers worker 数量（<=0 时默认 2）。
// 返回 *SummaryService（未启动，需调用 Start 启动 worker）。
func NewSummaryService(llm LLMClient, queueSize int, workers int) *SummaryService {
	if queueSize <= 0 {
		queueSize = 128
	}
	if workers <= 0 {
		workers = 2
	}
	return &SummaryService{
		llm:     llm,
		queue:   make(chan SummaryRequest, queueSize),
		workers: workers,
		config:  DefaultSummaryConfig(),
	}
}

// Summarize 提交摘要请求。
//
// SyncMode=true 时同步等待结果（阻塞直到 worker 处理完成或超时）；
// SyncMode=false 时异步提交（队列满时返回 ErrQueueFull）；
// 已 Close 时返回 ErrSummaryClosed。
//
// ctx 用于同步模式下的等待超时（异步模式 ctx 不阻塞）。
func (s *SummaryService) Summarize(ctx context.Context, req SummaryRequest) error {
	if s == nil {
		return errors.New("summary service: nil receiver")
	}
	if atomic.LoadInt32(&s.closed) == 1 {
		return ErrSummaryClosed
	}
	// 同步模式：初始化 result channel。
	if req.SyncMode {
		req.result = make(chan SummaryResult, 1)
	}
	select {
	case s.queue <- req:
		// 投递成功。
	default:
		return ErrSummaryQueueFull
	}
	// 同步模式：等待结果。
	if req.SyncMode {
		select {
		case <-req.result:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Start 启动 worker goroutine 消费 queue。
//
// 启动 workers 个 goroutine，每个 goroutine 从 queue 读取请求并调用 processSummary。
func (s *SummaryService) Start(ctx context.Context) {
	if s == nil {
		return
	}
	for i := 0; i < s.workers; i++ {
		s.wg.Add(1)
		go s.worker(ctx)
	}
}

// worker 消费 queue 的 goroutine。
func (s *SummaryService) worker(ctx context.Context) {
	defer s.wg.Done()
	for req := range s.queue {
		_ = ctx // 标记 ctx 已被消费
		result := s.processSummary(context.Background(), req)
		atomic.AddInt64(&s.processed, 1)
		if result.Summary == "" {
			atomic.AddInt64(&s.failed, 1)
		}
		// 同步模式：将结果写入 result channel。
		if req.result != nil {
			req.result <- result
			close(req.result)
		}
	}
}

// Close 优雅关闭。
//
// 关闭 queue channel，等待所有 worker 完成剩余请求后退出。
// 幂等：重复调用安全。
func (s *SummaryService) Close() {
	if s == nil {
		return
	}
	if !atomic.CompareAndSwapInt32(&s.closed, 0, 1) {
		return
	}
	close(s.queue)
	s.wg.Wait()
}

// Processed 返回累计处理请求数。
func (s *SummaryService) Processed() int64 {
	if s == nil {
		return 0
	}
	return atomic.LoadInt64(&s.processed)
}

// Failed 返回累计失败请求数。
func (s *SummaryService) Failed() int64 {
	if s == nil {
		return 0
	}
	return atomic.LoadInt64(&s.failed)
}

// Config 返回当前配置。
func (s *SummaryService) Config() SummaryConfig {
	if s == nil {
		return SummaryConfig{}
	}
	return s.config
}

// ----------------------------------------------------------------------------
// 内部方法
// ----------------------------------------------------------------------------

// processSummary 处理单个摘要请求，调用 LLM 生成摘要。
//
// 流程：
//  1. 将 Events 序列化为 prompt 文本
//  2. 调用 LLM.Complete 生成摘要
//  3. 解析摘要与主题（简化：topics 为空，实际应解析 LLM 输出）
//  4. 返回 SummaryResult
//
// LLM 调用失败时返回空 Summary（不向上抛错，避免影响其他请求）。
func (s *SummaryService) processSummary(ctx context.Context, req SummaryRequest) SummaryResult {
	result := SummaryResult{
		SessionID: req.SessionID,
		Timestamp: time.Now().UnixMilli(),
	}
	if s == nil || s.llm == nil {
		return result
	}
	if len(req.Events) == 0 {
		result.Summary = "no events to summarize"
		return result
	}
	// 构建 prompt：events 简要描述 + 摘要指令。
	prompt := buildSummaryPrompt(req)
	resp, err := s.llm.Complete(ctx, prompt, LLMOptions{
		Temperature: 0.3,
		MaxTokens:   512,
	})
	if err != nil {
		// LLM 失败：返回空 Summary。
		return result
	}
	result.Summary = resp
	// 简化：topics 从 events.EventType 提取去重。
	result.Topics = extractTopics(req.Events)
	return result
}

// buildSummaryPrompt 构建摘要 prompt。
//
// prompt 格式：
//
//	请总结以下用户行为事件序列：
//
//	1. [click] user=u1 article=a1 channel=tech
//	2. [like] user=u1 article=a1 channel=tech
//	...
//
//	请生成不超过 100 字的会话摘要，并提取 3-5 个关键主题。
func buildSummaryPrompt(req SummaryRequest) string {
	var b strings.Builder
	b.WriteString("请总结以下用户行为事件序列：\n\n")
	for i, e := range req.Events {
		fmt.Fprintf(&b, "%d. [%s] user=%s article=%s channel=%s\n",
			i+1, e.EventType, e.UserID, e.ArticleID, e.Channel)
	}
	b.WriteString("\n请生成不超过 100 字的会话摘要，并提取 3-5 个关键主题。")
	return b.String()
}

// extractTopics 从事件列表提取主题（简化：EventType 去重）。
func extractTopics(events []domain.BehaviorEvent) []string {
	seen := make(map[string]struct{}, len(events))
	var topics []string
	for _, e := range events {
		if e.EventType == "" {
			continue
		}
		if _, ok := seen[e.EventType]; ok {
			continue
		}
		seen[e.EventType] = struct{}{}
		topics = append(topics, e.EventType)
	}
	return topics
}

// ----------------------------------------------------------------------------
// 错误定义
// ----------------------------------------------------------------------------

// ErrSummaryQueueFull 摘要 queue 已满错误。
var ErrSummaryQueueFull = errors.New("agent: summary queue full")

// ErrSummaryClosed 摘要服务已关闭错误。
var ErrSummaryClosed = errors.New("agent: summary service closed")
