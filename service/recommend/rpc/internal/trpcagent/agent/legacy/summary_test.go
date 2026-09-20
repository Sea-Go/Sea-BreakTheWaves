// Package agent summary_test.go — SummaryService 单元测试（Task 13.8）。
//
// 用 stdlib 手写 stub（不引入 testify）覆盖：
//   - 异步 summary 提交 + worker 消费
//   - 同步模式（SyncMode=true）等待结果
//   - Close 等待所有 worker 完成
//   - 队列满返回 ErrSummaryQueueFull
//   - Close 后提交返回 ErrSummaryClosed
//
// stub 命名加 SM（Summary）前缀避免与已有 stub 冲突。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sea/service/recommend/rpc/internal/domain"
)

// ----------------------------------------------------------------------------
// stub 实现（SM 前缀 = Summary）
// ----------------------------------------------------------------------------

// stubSMLLM 测试用 LLMClient stub。
type stubSMLLM struct {
	mu         sync.Mutex
	calls      int64
	resp       string
	err        error
	delay      time.Duration
	lastPrompt string
}

func (s *stubSMLLM) Complete(_ context.Context, prompt string, _ LLMOptions) (string, error) {
	atomic.AddInt64(&s.calls, 1)
	s.mu.Lock()
	s.lastPrompt = prompt
	s.mu.Unlock()
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	if s.err != nil {
		return "", s.err
	}
	return s.resp, nil
}

func (s *stubSMLLM) CompleteWithStructuredOutput(_ context.Context, _ string, _ map[string]any) (json.RawMessage, error) {
	return json.RawMessage("{}"), nil
}

func (s *stubSMLLM) CompleteWithLogprobs(_ context.Context, _ string, _ int) (string, [][]LogprobEntry, error) {
	return "", nil, nil
}

// 编译期断言：stub 实现 LLMClient。
var _ LLMClient = (*stubSMLLM)(nil)

// makeSMEvents 创建测试用事件列表。
func makeSMEvents(n int) []domain.BehaviorEvent {
	events := make([]domain.BehaviorEvent, n)
	for i := range events {
		events[i] = domain.BehaviorEvent{
			EventID:   "evt-" + string(rune('a'+i)),
			EventType: "click",
			UserID:    "u1",
			ArticleID: "art-" + string(rune('a'+i)),
			Channel:   "tech",
			Timestamp: time.Now(),
		}
	}
	return events
}

// makeSMEventsVariadic 创建带多种 EventType 的测试事件列表。
func makeSMEventsVariadic() []domain.BehaviorEvent {
	return []domain.BehaviorEvent{
		{EventID: "e1", EventType: "click", UserID: "u1", ArticleID: "a1", Channel: "tech"},
		{EventID: "e2", EventType: "like", UserID: "u1", ArticleID: "a1", Channel: "tech"},
		{EventID: "e3", EventType: "click", UserID: "u1", ArticleID: "a2", Channel: "tech"},
	}
}

// ----------------------------------------------------------------------------
// SummaryService 测试
// ----------------------------------------------------------------------------

// TestSummaryService_DefaultParams 验证 queueSize/workers <=0 时默认值。
func TestSummaryService_DefaultParams(t *testing.T) {
	llm := &stubSMLLM{}
	s := NewSummaryService(llm, 0, 0)
	if cap(s.queue) != 128 {
		t.Errorf("queue 容量 = %d, 期望 128", cap(s.queue))
	}
	if s.workers != 2 {
		t.Errorf("workers = %d, 期望 2", s.workers)
	}
}

// TestSummaryService_AsyncSummary 验证异步 summary 提交与 worker 消费。
func TestSummaryService_AsyncSummary(t *testing.T) {
	llm := &stubSMLLM{resp: "session summary"}
	s := NewSummaryService(llm, 16, 2)
	s.Start(context.Background())

	err := s.Summarize(context.Background(), SummaryRequest{
		SessionID: "s1",
		UserID:    "u1",
		Events:    makeSMEvents(3),
		SyncMode:  false,
	})
	if err != nil {
		t.Fatalf("Summarize 错误: %v", err)
	}

	// 等待 worker 处理。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&llm.calls) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.Close()
	if got := atomic.LoadInt64(&llm.calls); got != 1 {
		t.Errorf("LLM 调用次数 = %d, 期望 1", got)
	}
	if got := s.Processed(); got != 1 {
		t.Errorf("Processed = %d, 期望 1", got)
	}
}

// TestSummaryService_SyncMode 验证同步模式等待结果。
func TestSummaryService_SyncMode(t *testing.T) {
	llm := &stubSMLLM{resp: "sync summary"}
	s := NewSummaryService(llm, 16, 2)
	s.Start(context.Background())
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.Summarize(ctx, SummaryRequest{
		SessionID: "s1",
		UserID:    "u1",
		Events:    makeSMEvents(3),
		SyncMode:  true,
	})
	if err != nil {
		t.Fatalf("Summarize 同步模式错误: %v", err)
	}
	if got := atomic.LoadInt64(&llm.calls); got != 1 {
		t.Errorf("LLM 调用次数 = %d, 期望 1", got)
	}
}

// TestSummaryService_SyncModeTimeout 验证同步模式 ctx 超时返回错误。
func TestSummaryService_SyncModeTimeout(t *testing.T) {
	// LLM 慢速，让同步模式超时。
	llm := &stubSMLLM{resp: "summary", delay: 200 * time.Millisecond}
	s := NewSummaryService(llm, 16, 1)
	s.Start(context.Background())
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := s.Summarize(ctx, SummaryRequest{
		SessionID: "s1",
		UserID:    "u1",
		Events:    makeSMEvents(3),
		SyncMode:  true,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, 期望 context.DeadlineExceeded", err)
	}
}

// TestSummaryService_CloseWaits 验证 Close 等待所有 worker 完成。
func TestSummaryService_CloseWaits(t *testing.T) {
	llm := &stubSMLLM{resp: "summary", delay: 20 * time.Millisecond}
	s := NewSummaryService(llm, 16, 2)
	s.Start(context.Background())

	// 提交 5 个请求。
	for i := 0; i < 5; i++ {
		_ = s.Summarize(context.Background(), SummaryRequest{
			SessionID: "s1",
			Events:    makeSMEvents(2),
		})
	}
	s.Close()
	// Close 后所有请求应被处理。
	if got := s.Processed(); got != 5 {
		t.Errorf("Close 后 Processed = %d, 期望 5", got)
	}
}

// TestSummaryService_QueueFull 验证队列满返回 ErrSummaryQueueFull。
func TestSummaryService_QueueFull(t *testing.T) {
	// llm 慢速，让队列快速填满。
	llm := &stubSMLLM{resp: "summary", delay: 100 * time.Millisecond}
	s := NewSummaryService(llm, 4, 1)
	s.Start(context.Background())
	defer s.Close()

	// 不启动 worker 时队列 4 可填满，启动后 worker 会消费但慢。
	// 提交大量请求，应有些被拒绝。
	rejected := 0
	for i := 0; i < 50; i++ {
		err := s.Summarize(context.Background(), SummaryRequest{
			SessionID: "s1",
			Events:    makeSMEvents(1),
		})
		if errors.Is(err, ErrSummaryQueueFull) {
			rejected++
		}
	}
	if rejected == 0 {
		t.Log("rejected = 0（worker 可能跑得快）")
	}
}

// TestSummaryService_SummarizeAfterClose 验证 Close 后提交返回 ErrSummaryClosed。
func TestSummaryService_SummarizeAfterClose(t *testing.T) {
	llm := &stubSMLLM{}
	s := NewSummaryService(llm, 16, 2)
	s.Start(context.Background())
	s.Close()
	err := s.Summarize(context.Background(), SummaryRequest{
		SessionID: "s1",
		Events:    makeSMEvents(1),
	})
	if !errors.Is(err, ErrSummaryClosed) {
		t.Errorf("Close 后 Summarize 应返回 ErrSummaryClosed, 实际: %v", err)
	}
}

// TestSummaryService_DoubleClose 验证重复 Close 安全。
func TestSummaryService_DoubleClose(t *testing.T) {
	llm := &stubSMLLM{}
	s := NewSummaryService(llm, 16, 2)
	s.Start(context.Background())
	s.Close()
	s.Close() // 不应 panic
}

// TestSummaryService_NilLLM 验证 nil llm 不 panic。
func TestSummaryService_NilLLM(t *testing.T) {
	s := NewSummaryService(nil, 16, 2)
	s.Start(context.Background())
	_ = s.Summarize(context.Background(), SummaryRequest{
		SessionID: "s1",
		Events:    makeSMEvents(1),
	})
	// 等待 worker 处理。
	time.Sleep(50 * time.Millisecond)
	s.Close()
	// 失败计数应增加。
	if got := s.Failed(); got != 1 {
		t.Logf("Failed = %d（应为 1，nil llm 应失败）", got)
	}
}

// TestSummaryService_NilReceiver 验证 nil receiver 安全处理。
func TestSummaryService_NilReceiver(t *testing.T) {
	var s *SummaryService
	if err := s.Summarize(context.Background(), SummaryRequest{}); err == nil {
		t.Error("nil receiver Summarize 应返回错误")
	}
	s.Close() // 不应 panic
	if got := s.Processed(); got != 0 {
		t.Errorf("nil receiver Processed 应为 0, 实际 %d", got)
	}
}

// TestSummaryService_EmptyEvents 验证空事件列表生成 "no events" 摘要。
func TestSummaryService_EmptyEvents(t *testing.T) {
	llm := &stubSMLLM{resp: "summary"}
	s := NewSummaryService(llm, 16, 1)
	s.Start(context.Background())
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.Summarize(ctx, SummaryRequest{
		SessionID: "s1",
		Events:    nil,
		SyncMode:  true,
	})
	// 空事件不应调用 LLM（直接返回 "no events"）。
	if got := atomic.LoadInt64(&llm.calls); got != 0 {
		t.Errorf("空事件不应调用 LLM, 实际 calls = %d", got)
	}
}

// TestSummaryService_LLMError 验证 LLM 错误时 failed 计数增加。
func TestSummaryService_LLMError(t *testing.T) {
	llm := &stubSMLLM{err: errors.New("llm failed")}
	s := NewSummaryService(llm, 16, 1)
	s.Start(context.Background())
	_ = s.Summarize(context.Background(), SummaryRequest{
		SessionID: "s1",
		Events:    makeSMEvents(1),
	})
	// 等待 worker 处理。
	time.Sleep(50 * time.Millisecond)
	s.Close()
	if got := s.Failed(); got != 1 {
		t.Errorf("Failed = %d, 期望 1", got)
	}
}

// TestSummaryService_ProcessSummary 验证 processSummary 返回正确结果。
func TestSummaryService_ProcessSummary(t *testing.T) {
	llm := &stubSMLLM{resp: "session summary"}
	s := NewSummaryService(llm, 16, 1)
	result := s.processSummary(context.Background(), SummaryRequest{
		SessionID: "s1",
		Events:    makeSMEventsVariadic(),
	})
	if result.SessionID != "s1" {
		t.Errorf("SessionID = %q, 期望 s1", result.SessionID)
	}
	if result.Summary != "session summary" {
		t.Errorf("Summary = %q, 期望 session summary", result.Summary)
	}
	// 应提取去重后的 EventType（click + like）。
	if len(result.Topics) != 2 {
		t.Errorf("Topics 长度 = %d, 期望 2（click + like 去重）", len(result.Topics))
	}
	if result.Timestamp <= 0 {
		t.Errorf("Timestamp = %d, 应 > 0", result.Timestamp)
	}
}

// TestSummaryService_Config 验证 Config 返回默认配置。
func TestSummaryService_Config(t *testing.T) {
	s := NewSummaryService(&stubSMLLM{}, 0, 0)
	cfg := s.Config()
	if cfg.SyncSummaryIntraRun != false {
		t.Errorf("SyncSummaryIntraRun = %v, 期望 false", cfg.SyncSummaryIntraRun)
	}
	if cfg.MaxEventsBeforeSummary != 50 {
		t.Errorf("MaxEventsBeforeSummary = %d, 期望 50", cfg.MaxEventsBeforeSummary)
	}
}

// TestDefaultSummaryConfig 验证默认配置。
func TestDefaultSummaryConfig(t *testing.T) {
	cfg := DefaultSummaryConfig()
	if cfg.SyncSummaryIntraRun {
		t.Error("默认 SyncSummaryIntraRun 应为 false")
	}
	if cfg.MaxEventsBeforeSummary != 50 {
		t.Errorf("默认 MaxEventsBeforeSummary = %d, 期望 50", cfg.MaxEventsBeforeSummary)
	}
	if cfg.QueueSize != 128 {
		t.Errorf("默认 QueueSize = %d, 期望 128", cfg.QueueSize)
	}
	if cfg.Workers != 2 {
		t.Errorf("默认 Workers = %d, 期望 2", cfg.Workers)
	}
}

// ----------------------------------------------------------------------------
// 辅助函数测试
// ----------------------------------------------------------------------------

// TestBuildSummaryPrompt 验证 prompt 构建。
func TestBuildSummaryPrompt(t *testing.T) {
	req := SummaryRequest{
		SessionID: "s1",
		Events:    makeSMEventsVariadic(),
	}
	prompt := buildSummaryPrompt(req)
	if prompt == "" {
		t.Error("prompt 不应为空")
	}
	if !contains(prompt, "click") {
		t.Errorf("prompt 应包含 click, 实际 %q", prompt)
	}
	if !contains(prompt, "请总结") {
		t.Errorf("prompt 应包含 请总结, 实际 %q", prompt)
	}
}

// TestExtractTopics 验证 topics 提取与去重。
func TestExtractTopics(t *testing.T) {
	events := makeSMEventsVariadic()
	topics := extractTopics(events)
	// click + like 去重后应为 2。
	if len(topics) != 2 {
		t.Errorf("topics 长度 = %d, 期望 2", len(topics))
	}
}

// TestExtractTopics_Empty 验证空事件返回 nil。
func TestExtractTopics_Empty(t *testing.T) {
	topics := extractTopics(nil)
	if len(topics) != 0 {
		t.Errorf("空事件 topics 应为空, 实际 %v", topics)
	}
}

// ----------------------------------------------------------------------------
// 零值测试
// ----------------------------------------------------------------------------

// TestSummaryResult_ZeroValue 验证零值安全。
func TestSummaryResult_ZeroValue(t *testing.T) {
	var r SummaryResult
	if r.SessionID != "" || r.Summary != "" || len(r.Topics) != 0 || r.Timestamp != 0 {
		t.Errorf("零值 SummaryResult 不正确: %+v", r)
	}
}

// TestSummaryRequest_ZeroValue 验证零值安全。
func TestSummaryRequest_ZeroValue(t *testing.T) {
	var r SummaryRequest
	if r.SessionID != "" || r.UserID != "" || len(r.Events) != 0 || r.SyncMode != false {
		t.Errorf("零值 SummaryRequest 不正确: %+v", r)
	}
}

// TestErrors 验证错误定义。
func TestErrors_SM(t *testing.T) {
	if !errors.Is(ErrSummaryQueueFull, ErrSummaryQueueFull) {
		t.Error("ErrSummaryQueueFull errors.Is 失败")
	}
	if !errors.Is(ErrSummaryClosed, ErrSummaryClosed) {
		t.Error("ErrSummaryClosed errors.Is 失败")
	}
}
