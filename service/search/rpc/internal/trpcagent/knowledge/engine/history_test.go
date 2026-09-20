package search

import (
	"context"
	"errors"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/domain"
)

// ============================================================================
// 该文件测试 internal/search/history.go 的 HistoryService 与 MemoryHistoryStore。
// 覆盖：记录/列出/点击反馈/空用户/store 错误注入/topK 截断/时间倒序。
// ============================================================================

// stubHistoryStore 用于测试的 HistoryStore stub。
type stubHistoryStore struct {
	recordErr      error
	listErr        error
	recordClickErr error
	records        []domain.SearchHistoryEntry
	clicks         []clickCall
}

type clickCall struct {
	userID    string
	query     string
	articleID string
}

func (s *stubHistoryStore) Record(_ context.Context, entry domain.SearchHistoryEntry) error {
	if s.recordErr != nil {
		return s.recordErr
	}
	s.records = append(s.records, entry)
	return nil
}

func (s *stubHistoryStore) List(_ context.Context, userID string, _ int) ([]domain.SearchHistoryEntry, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out []domain.SearchHistoryEntry
	for _, e := range s.records {
		if e.UserID == userID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *stubHistoryStore) RecordClick(_ context.Context, userID, query, articleID string) error {
	if s.recordClickErr != nil {
		return s.recordClickErr
	}
	s.clicks = append(s.clicks, clickCall{userID, query, articleID})
	return nil
}

// TestHistoryService_Record 验证记录搜索并自动填充时间戳。
func TestHistoryService_Record(t *testing.T) {
	store := &stubHistoryStore{}
	svc := NewHistoryService(store)
	err := svc.Record(context.Background(), domain.SearchHistoryEntry{
		Query:  "AI",
		UserID: "u1",
	})
	if err != nil {
		t.Fatalf("Record 返回错误: %v", err)
	}
	if len(store.records) != 1 {
		t.Fatalf("期望记录 1 条, 实际 %d", len(store.records))
	}
	if store.records[0].Query != "AI" {
		t.Errorf("Query = %q, 期望 AI", store.records[0].Query)
	}
	if store.records[0].SearchedAt == 0 {
		t.Errorf("SearchedAt 应被自动填充, 实际为 0")
	}
}

// TestHistoryService_Record_StoreError 验证 store 错误透传。
func TestHistoryService_Record_StoreError(t *testing.T) {
	store := &stubHistoryStore{recordErr: errors.New("db down")}
	svc := NewHistoryService(store)
	err := svc.Record(context.Background(), domain.SearchHistoryEntry{Query: "AI", UserID: "u1"})
	if err == nil {
		t.Fatalf("期望返回 store 错误, 实际 nil")
	}
}

// TestHistoryService_Record_PreserveSearchedAt 验证已设置的 SearchedAt 不被覆盖。
func TestHistoryService_Record_PreserveSearchedAt(t *testing.T) {
	store := &stubHistoryStore{}
	svc := NewHistoryService(store)
	err := svc.Record(context.Background(), domain.SearchHistoryEntry{
		Query:      "AI",
		UserID:     "u1",
		SearchedAt: 12345,
	})
	if err != nil {
		t.Fatalf("Record 错误: %v", err)
	}
	if store.records[0].SearchedAt != 12345 {
		t.Errorf("SearchedAt = %d, 期望 12345（不应被覆盖）", store.records[0].SearchedAt)
	}
}

// TestHistoryService_List 验证列出用户搜索历史。
func TestHistoryService_List(t *testing.T) {
	store := &stubHistoryStore{}
	svc := NewHistoryService(store)
	for _, q := range []string{"q1", "q2", "q3"} {
		_ = svc.Record(context.Background(), domain.SearchHistoryEntry{Query: q, UserID: "u1"})
	}
	// 也记录其他用户。
	_ = svc.Record(context.Background(), domain.SearchHistoryEntry{Query: "other", UserID: "u2"})

	got, err := svc.List(context.Background(), "u1", 10)
	if err != nil {
		t.Fatalf("List 返回错误: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("期望 3 条, 实际 %d", len(got))
	}
}

// TestHistoryService_List_EmptyUser 验证空用户返回 nil。
func TestHistoryService_List_EmptyUser(t *testing.T) {
	store := &stubHistoryStore{}
	svc := NewHistoryService(store)
	_ = svc.Record(context.Background(), domain.SearchHistoryEntry{Query: "q", UserID: "u1"})
	got, err := svc.List(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("List 空用户不应返回错误, 实际 %v", err)
	}
	if got != nil && len(got) != 0 {
		t.Errorf("空用户应返回空, 实际 %d 条", len(got))
	}
}

// TestHistoryService_List_StoreError 验证 store 错误透传。
func TestHistoryService_List_StoreError(t *testing.T) {
	store := &stubHistoryStore{listErr: errors.New("list fail")}
	svc := NewHistoryService(store)
	_, err := svc.List(context.Background(), "u1", 10)
	if err == nil {
		t.Fatalf("期望返回错误, 实际 nil")
	}
}

// TestHistoryService_RecordClick 验证点击反馈。
func TestHistoryService_RecordClick(t *testing.T) {
	store := &stubHistoryStore{}
	svc := NewHistoryService(store)
	err := svc.RecordClick(context.Background(), "u1", "AI", "a1")
	if err != nil {
		t.Fatalf("RecordClick 返回错误: %v", err)
	}
	if len(store.clicks) != 1 {
		t.Fatalf("期望 1 次点击调用, 实际 %d", len(store.clicks))
	}
	if store.clicks[0].articleID != "a1" {
		t.Errorf("articleID = %q, 期望 a1", store.clicks[0].articleID)
	}
}

// TestHistoryService_RecordClick_Invalid 验证空参数返回错误。
func TestHistoryService_RecordClick_Invalid(t *testing.T) {
	store := &stubHistoryStore{}
	svc := NewHistoryService(store)
	if err := svc.RecordClick(context.Background(), "", "q", "a1"); err == nil {
		t.Errorf("空 userID 应返回错误")
	}
	if err := svc.RecordClick(context.Background(), "u1", "q", ""); err == nil {
		t.Errorf("空 articleID 应返回错误")
	}
}

// TestHistoryService_RecordClick_StoreError 验证 store 错误透传。
func TestHistoryService_RecordClick_StoreError(t *testing.T) {
	store := &stubHistoryStore{recordClickErr: errors.New("click fail")}
	svc := NewHistoryService(store)
	err := svc.RecordClick(context.Background(), "u1", "q", "a1")
	if err == nil {
		t.Fatalf("期望返回错误, 实际 nil")
	}
}

// TestMemoryHistoryStore_RecordAndList 验证内存 store 记录与按时间倒序列出。
func TestMemoryHistoryStore_RecordAndList(t *testing.T) {
	store := NewMemoryHistoryStore()
	svc := NewHistoryService(store)
	ctx := context.Background()
	_ = svc.Record(ctx, domain.SearchHistoryEntry{Query: "q1", UserID: "u1", SearchedAt: 100})
	_ = svc.Record(ctx, domain.SearchHistoryEntry{Query: "q2", UserID: "u1", SearchedAt: 300})
	_ = svc.Record(ctx, domain.SearchHistoryEntry{Query: "q3", UserID: "u1", SearchedAt: 200})
	_ = svc.Record(ctx, domain.SearchHistoryEntry{Query: "other", UserID: "u2", SearchedAt: 999})

	got, err := svc.List(ctx, "u1", 0)
	if err != nil {
		t.Fatalf("List 错误: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("期望 3 条, 实际 %d", len(got))
	}
	// 验证倒序：300 > 200 > 100
	if got[0].SearchedAt != 300 {
		t.Errorf("首位 SearchedAt = %d, 期望 300", got[0].SearchedAt)
	}
	if got[1].SearchedAt != 200 {
		t.Errorf("第二位 SearchedAt = %d, 期望 200", got[1].SearchedAt)
	}
	if got[2].SearchedAt != 100 {
		t.Errorf("第三位 SearchedAt = %d, 期望 100", got[2].SearchedAt)
	}
}

// TestMemoryHistoryStore_TopK 验证 topK 截断。
// 注意：SearchedAt 必须非 0，否则 HistoryService.Record 会自动填充当前时间戳，
// 干扰排序结果。此处用 i+1 生成 1..5 的递增时间戳。
func TestMemoryHistoryStore_TopK(t *testing.T) {
	store := NewMemoryHistoryStore()
	svc := NewHistoryService(store)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = svc.Record(ctx, domain.SearchHistoryEntry{
			Query:      "q",
			UserID:     "u1",
			SearchedAt: int64(i + 1),
		})
	}
	got, _ := svc.List(ctx, "u1", 2)
	if len(got) != 2 {
		t.Fatalf("期望 topK=2 截断, 实际 %d", len(got))
	}
	// 倒序后前两条应为 5, 4。
	if got[0].SearchedAt != 5 {
		t.Errorf("首位 SearchedAt = %d, 期望 5", got[0].SearchedAt)
	}
	if got[1].SearchedAt != 4 {
		t.Errorf("第二位 SearchedAt = %d, 期望 4", got[1].SearchedAt)
	}
}

// TestMemoryHistoryStore_EmptyUser 验证空用户返回 nil。
func TestMemoryHistoryStore_EmptyUser(t *testing.T) {
	store := NewMemoryHistoryStore()
	svc := NewHistoryService(store)
	ctx := context.Background()
	_ = svc.Record(ctx, domain.SearchHistoryEntry{Query: "q", UserID: "u1"})
	got, _ := svc.List(ctx, "", 10)
	if got != nil && len(got) != 0 {
		t.Errorf("空用户应返回空, 实际 %d 条", len(got))
	}
}

// TestMemoryHistoryStore_RecordClick_Associate 验证点击关联到已有搜索记录。
func TestMemoryHistoryStore_RecordClick_Associate(t *testing.T) {
	store := NewMemoryHistoryStore()
	svc := NewHistoryService(store)
	ctx := context.Background()
	// 先记录一次搜索。
	_ = svc.Record(ctx, domain.SearchHistoryEntry{Query: "AI", UserID: "u1", SearchedAt: 100})
	// 再记录点击。
	if err := svc.RecordClick(ctx, "u1", "AI", "a1"); err != nil {
		t.Fatalf("RecordClick 错误: %v", err)
	}
	// 验证点击已关联到搜索记录。
	got, _ := svc.List(ctx, "u1", 10)
	if len(got) != 1 {
		t.Fatalf("期望 1 条, 实际 %d", len(got))
	}
	if got[0].ClickedArticleID != "a1" {
		t.Errorf("ClickedArticleID = %q, 期望 a1", got[0].ClickedArticleID)
	}
	if got[0].ClickedAt == 0 {
		t.Errorf("ClickedAt 应被填充, 实际 0")
	}
}

// TestMemoryHistoryStore_RecordClick_NoMatchAppend 验证无匹配记录时追加点击条目。
func TestMemoryHistoryStore_RecordClick_NoMatchAppend(t *testing.T) {
	store := NewMemoryHistoryStore()
	svc := NewHistoryService(store)
	ctx := context.Background()
	// 未先记录搜索，直接点击。
	if err := svc.RecordClick(ctx, "u1", "AI", "a1"); err != nil {
		t.Fatalf("RecordClick 错误: %v", err)
	}
	got, _ := svc.List(ctx, "u1", 10)
	if len(got) != 1 {
		t.Fatalf("期望追加 1 条, 实际 %d", len(got))
	}
	if got[0].ClickedArticleID != "a1" {
		t.Errorf("ClickedArticleID = %q, 期望 a1", got[0].ClickedArticleID)
	}
	if got[0].Query != "AI" {
		t.Errorf("Query = %q, 期望 AI", got[0].Query)
	}
}

// TestMemoryHistoryStore_RecordClick_DifferentQuery 验证不同 query 不互相干扰。
func TestMemoryHistoryStore_RecordClick_DifferentQuery(t *testing.T) {
	store := NewMemoryHistoryStore()
	svc := NewHistoryService(store)
	ctx := context.Background()
	_ = svc.Record(ctx, domain.SearchHistoryEntry{Query: "AI", UserID: "u1", SearchedAt: 100})
	_ = svc.Record(ctx, domain.SearchHistoryEntry{Query: "ML", UserID: "u1", SearchedAt: 200})
	// 点击 AI 搜索。
	if err := svc.RecordClick(ctx, "u1", "AI", "a1"); err != nil {
		t.Fatalf("RecordClick 错误: %v", err)
	}
	got, _ := svc.List(ctx, "u1", 10)
	if len(got) != 2 {
		t.Fatalf("期望 2 条, 实际 %d", len(got))
	}
	// 找到 AI 那条，验证已点击；ML 那条未点击。
	for _, e := range got {
		if e.Query == "AI" {
			if e.ClickedArticleID != "a1" {
				t.Errorf("AI 条目 ClickedArticleID = %q, 期望 a1", e.ClickedArticleID)
			}
		}
		if e.Query == "ML" {
			if e.ClickedArticleID != "" {
				t.Errorf("ML 条目不应被点击, 实际 %q", e.ClickedArticleID)
			}
		}
	}
}
