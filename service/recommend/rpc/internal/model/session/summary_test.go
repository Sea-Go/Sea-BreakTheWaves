package session

import (
	"context"
	"testing"
	"time"
)

// ============================================================================
// 该文件测试 SummaryService 的 MaybeSummary 触发与 LoadSummary 恢复。
// 覆盖：
//   - 同步模式 msgCount >= Boundary 触发 Summary 写入
//   - msgCount < Boundary 不触发
//   - 异步模式触发后（轮询等待 goroutine）写入成功
//   - LoadSummary 恢复已写入的摘要
//   - SearchSummary stub 返回空
// ============================================================================

// TestSummaryService_MaybeSummary_SyncTrigger 验证同步模式 msgCount >= Boundary 触发写入。
func TestSummaryService_MaybeSummary_SyncTrigger(t *testing.T) {
	store := newMockMemoryStore()
	svc := NewSummaryService(SummaryConfig{Async: false, Boundary: 5}, store)

	if err := svc.MaybeSummary(context.Background(), "s1", 5); err != nil {
		t.Fatalf("MaybeSummary 返回错误: %v", err)
	}
	data := store.snapshot()
	val, ok := data["{session:s1:summary}"]
	if !ok {
		t.Fatalf("期望已写入 {session:s1:summary}，实际 data=%v", data)
	}
	if val == "" {
		t.Errorf("summary 为空串，期望非空")
	}
}

// TestSummaryService_MaybeSummary_NoTrigger 验证 msgCount < Boundary 时不触发。
func TestSummaryService_MaybeSummary_NoTrigger(t *testing.T) {
	store := newMockMemoryStore()
	svc := NewSummaryService(SummaryConfig{Async: false, Boundary: 5}, store)

	if err := svc.MaybeSummary(context.Background(), "s1", 4); err != nil {
		t.Fatalf("MaybeSummary 返回错误: %v", err)
	}
	data := store.snapshot()
	if _, ok := data["{session:s1:summary}"]; ok {
		t.Errorf("msgCount=4 < Boundary=5 不应触发 Summary，但 key 已写入")
	}
}

// TestSummaryService_MaybeSummary_AsyncTrigger 验证异步模式触发后 goroutine 写入成功。
func TestSummaryService_MaybeSummary_AsyncTrigger(t *testing.T) {
	store := newMockMemoryStore()
	svc := NewSummaryService(SummaryConfig{Async: true, Boundary: 1}, store)

	// 异步模式应立即返回 nil
	if err := svc.MaybeSummary(context.Background(), "s2", 1); err != nil {
		t.Fatalf("MaybeSummary 返回错误: %v", err)
	}
	// 轮询等待 goroutine 写入（最多 1s）
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if val, _ := store.Get(context.Background(), "{session:s2:summary}"); val != "" {
			return // 写入成功
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("异步 goroutine 未在 1s 内写入 {session:s2:summary}")
}

// TestSummaryService_LoadSummary 验证 LoadSummary 恢复已写入的摘要。
func TestSummaryService_LoadSummary(t *testing.T) {
	store := newMockMemoryStore()
	svc := NewSummaryService(SummaryConfig{Async: false, Boundary: 3}, store)

	// 触发写入
	if err := svc.MaybeSummary(context.Background(), "s3", 3); err != nil {
		t.Fatalf("MaybeSummary 返回错误: %v", err)
	}
	// LoadSummary 恢复
	got, err := svc.LoadSummary(context.Background(), "s3")
	if err != nil {
		t.Fatalf("LoadSummary 返回错误: %v", err)
	}
	if got == "" {
		t.Errorf("LoadSummary 返回空串，期望非空")
	}
}

// TestSummaryService_LoadSummary_NotFound 验证未写入时 LoadSummary 返回空串无错误。
func TestSummaryService_LoadSummary_NotFound(t *testing.T) {
	store := newMockMemoryStore()
	svc := NewSummaryService(SummaryConfig{Async: false, Boundary: 3}, store)

	got, err := svc.LoadSummary(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("LoadSummary 返回错误: %v", err)
	}
	if got != "" {
		t.Errorf("LoadSummary = %q, 期望空串", got)
	}
}

// TestSummaryService_SearchSummary 验证 stub 返回空列表无错误。
func TestSummaryService_SearchSummary(t *testing.T) {
	store := newMockMemoryStore()
	svc := NewSummaryService(SummaryConfig{Async: false, Boundary: 3}, store)

	got, err := svc.SearchSummary(context.Background(), "query")
	if err != nil {
		t.Fatalf("SearchSummary 返回错误: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("SearchSummary 返回 %d 个结果, 期望 0（stub）", len(got))
	}
}
