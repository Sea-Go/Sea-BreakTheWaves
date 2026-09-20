// internal/eval/cases_test.go — 评估集管理单测（Task 15.1）。
//
// 覆盖：
//   - CaseService CRUD 全流程
//   - Validate 各字段校验（Query/Expected/Surface）
//   - CaseFilter 过滤（Surface/Channel/Intent/Limit/Offset）
//   - MemoryCaseStore 边界（重复创建/不存在更新/不存在删除）
//
// 使用 stdlib 手写 stub，不依赖 testify。
package eval

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ============================================================================
// stub 实现
// ============================================================================

// stubCaseStore 测试用 CaseStore，记录调用并可控返回错误。
type stubCaseStore struct {
	createCalls int
	getCalls    int
	listCalls   int
	updateCalls int
	deleteCalls int

	byID map[string]EvalCase

	createErr error
	getErr    error
	listErr   error
	updateErr error
	deleteErr error
}

func newStubCaseStore() *stubCaseStore {
	return &stubCaseStore{byID: make(map[string]EvalCase)}
}

func (s *stubCaseStore) Create(_ context.Context, c EvalCase) error {
	s.createCalls++
	if s.createErr != nil {
		return s.createErr
	}
	s.byID[c.ID] = c
	return nil
}

func (s *stubCaseStore) Get(_ context.Context, id string) (EvalCase, error) {
	s.getCalls++
	if s.getErr != nil {
		return EvalCase{}, s.getErr
	}
	c, ok := s.byID[id]
	if !ok {
		return EvalCase{}, errors.New("not found")
	}
	return c, nil
}

func (s *stubCaseStore) List(_ context.Context, filter CaseFilter) ([]EvalCase, error) {
	s.listCalls++
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]EvalCase, 0, len(s.byID))
	for _, c := range s.byID {
		if filter.Surface != "" && c.Surface != filter.Surface {
			continue
		}
		if filter.Channel != "" && c.Channel != filter.Channel {
			continue
		}
		if filter.Intent != "" && c.Intent != filter.Intent {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *stubCaseStore) Update(_ context.Context, c EvalCase) error {
	s.updateCalls++
	if s.updateErr != nil {
		return s.updateErr
	}
	s.byID[c.ID] = c
	return nil
}

func (s *stubCaseStore) Delete(_ context.Context, id string) error {
	s.deleteCalls++
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.byID, id)
	return nil
}

// makeValidCase 构造一个合法 EvalCase。
func makeValidCase(id string) EvalCase {
	return EvalCase{
		ID:       id,
		Query:    "test query",
		Expected: []string{"art-1", "art-2"},
		Surface:  "home",
		Channel:  "tech",
		Intent:   "explore",
		Tags:     []string{"smoke"},
	}
}

// ============================================================================
// 测试用例 — Validate
// ============================================================================

// TestValidate_ValidCase 验证合法用例校验通过。
func TestValidate_ValidCase(t *testing.T) {
	s := NewCaseService(NewMemoryCaseStore())
	if err := s.Validate(makeValidCase("c1")); err != nil {
		t.Errorf("合法用例校验失败: %v", err)
	}
}

// TestValidate_EmptyQuery 验证空 Query 报错。
func TestValidate_EmptyQuery(t *testing.T) {
	s := NewCaseService(NewMemoryCaseStore())
	c := makeValidCase("c1")
	c.Query = ""
	err := s.Validate(c)
	if err == nil || !strings.Contains(err.Error(), "query") {
		t.Errorf("空 Query 应报错, got %v", err)
	}
}

// TestValidate_EmptyExpected 验证空 Expected 报错。
func TestValidate_EmptyExpected(t *testing.T) {
	s := NewCaseService(NewMemoryCaseStore())
	c := makeValidCase("c1")
	c.Expected = nil
	err := s.Validate(c)
	if err == nil || !strings.Contains(err.Error(), "expected") {
		t.Errorf("空 Expected 应报错, got %v", err)
	}
}

// TestValidate_EmptySurface 验证空 Surface 报错。
func TestValidate_EmptySurface(t *testing.T) {
	s := NewCaseService(NewMemoryCaseStore())
	c := makeValidCase("c1")
	c.Surface = ""
	err := s.Validate(c)
	if err == nil || !strings.Contains(err.Error(), "surface") {
		t.Errorf("空 Surface 应报错, got %v", err)
	}
}

// TestValidate_InvalidSurface 验证非法 Surface 报错。
func TestValidate_InvalidSurface(t *testing.T) {
	s := NewCaseService(NewMemoryCaseStore())
	c := makeValidCase("c1")
	c.Surface = "unknown-surface"
	err := s.Validate(c)
	if err == nil || !strings.Contains(err.Error(), "非法") {
		t.Errorf("非法 Surface 应报错, got %v", err)
	}
}

// ============================================================================
// 测试用例 — CaseService CRUD（基于 MemoryCaseStore）
// ============================================================================

// TestCaseService_CreateAndGet 验证 Create + Get 全流程（含自动生成 ID）。
func TestCaseService_CreateAndGet(t *testing.T) {
	store := NewMemoryCaseStore()
	svc := NewCaseService(store)
	ctx := context.Background()

	c := makeValidCase("") // ID 留空，由 service 自动生成
	if err := svc.Create(ctx, c); err != nil {
		t.Fatalf("Create 错误: %v", err)
	}

	// 从 store 中取出实际生成的 ID。
	all, _ := store.List(ctx, CaseFilter{})
	if len(all) != 1 {
		t.Fatalf("期望 1 条用例, 实际 %d", len(all))
	}
	id := all[0].ID
	if id == "" {
		t.Fatalf("ID 应自动生成, 实际空")
	}
	if all[0].CreatedAt == 0 {
		t.Errorf("CreatedAt 应被填充")
	}
	if all[0].UpdatedAt == 0 {
		t.Errorf("UpdatedAt 应被填充")
	}

	got, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get 错误: %v", err)
	}
	if got.Query != "test query" {
		t.Errorf("Query = %q, 期望 test query", got.Query)
	}
}

// TestCaseService_Create_ValidateFail 验证 Create 时 Validate 失败不调用 store。
func TestCaseService_Create_ValidateFail(t *testing.T) {
	store := newStubCaseStore()
	svc := NewCaseService(store)
	ctx := context.Background()

	c := makeValidCase("c1")
	c.Query = "" // 触发 Validate 失败
	if err := svc.Create(ctx, c); err == nil {
		t.Fatalf("期望 Validate 失败错误, 实际 nil")
	}
	if store.createCalls != 0 {
		t.Errorf("Validate 失败时不应调用 store.Create, 实际调用 %d 次", store.createCalls)
	}
}

// TestCaseService_Update 验证 Update 流程（自动更新 UpdatedAt）。
func TestCaseService_Update(t *testing.T) {
	store := NewMemoryCaseStore()
	svc := NewCaseService(store)
	ctx := context.Background()

	c := makeValidCase("c1")
	c.CreatedAt = 1000
	c.UpdatedAt = 1000
	if err := svc.Create(ctx, c); err != nil {
		t.Fatalf("Create 错误: %v", err)
	}

	c.Query = "updated query"
	if err := svc.Update(ctx, c); err != nil {
		t.Fatalf("Update 错误: %v", err)
	}

	got, _ := svc.Get(ctx, "c1")
	if got.Query != "updated query" {
		t.Errorf("Query = %q, 期望 updated query", got.Query)
	}
	if got.UpdatedAt <= got.CreatedAt {
		t.Errorf("UpdatedAt (%d) 应 > CreatedAt (%d)", got.UpdatedAt, got.CreatedAt)
	}
	if got.CreatedAt != 1000 {
		t.Errorf("CreatedAt 应保留为 1000, 实际 %d", got.CreatedAt)
	}
}

// TestCaseService_Update_NotExist 验证更新不存在用例报错。
func TestCaseService_Update_NotExist(t *testing.T) {
	svc := NewCaseService(NewMemoryCaseStore())
	ctx := context.Background()
	err := svc.Update(ctx, makeValidCase("not-exist"))
	if err == nil {
		t.Errorf("更新不存在的用例应报错")
	}
}

// TestCaseService_Delete 验证 Delete 流程。
func TestCaseService_Delete(t *testing.T) {
	store := NewMemoryCaseStore()
	svc := NewCaseService(store)
	ctx := context.Background()

	c := makeValidCase("c1")
	_ = svc.Create(ctx, c)
	if err := svc.Delete(ctx, "c1"); err != nil {
		t.Fatalf("Delete 错误: %v", err)
	}
	if _, err := svc.Get(ctx, "c1"); err == nil {
		t.Errorf("删除后 Get 应报错")
	}
}

// TestCaseService_Delete_NotExist 验证删除不存在用例报错。
func TestCaseService_Delete_NotExist(t *testing.T) {
	svc := NewCaseService(NewMemoryCaseStore())
	if err := svc.Delete(context.Background(), "not-exist"); err == nil {
		t.Errorf("删除不存在的用例应报错")
	}
}

// TestCaseService_Get_EmptyID 验证空 ID 报错。
func TestCaseService_Get_EmptyID(t *testing.T) {
	svc := NewCaseService(NewMemoryCaseStore())
	if _, err := svc.Get(context.Background(), ""); err == nil {
		t.Errorf("空 ID 应报错")
	}
}

// ============================================================================
// 测试用例 — CaseFilter 过滤（MemoryCaseStore.List）
// ============================================================================

// TestMemoryCaseStore_List_Filter 验证 Surface/Channel/Intent 过滤。
func TestMemoryCaseStore_List_Filter(t *testing.T) {
	store := NewMemoryCaseStore()
	ctx := context.Background()

	cases := []EvalCase{
		{ID: "1", Query: "q1", Expected: []string{"a"}, Surface: "home", Channel: "tech", Intent: "explore"},
		{ID: "2", Query: "q2", Expected: []string{"a"}, Surface: "feed", Channel: "tech", Intent: "exploit"},
		{ID: "3", Query: "q3", Expected: []string{"a"}, Surface: "home", Channel: "life", Intent: "explore"},
		{ID: "4", Query: "q4", Expected: []string{"a"}, Surface: "home", Channel: "tech", Intent: "explore"},
	}
	for _, c := range cases {
		_ = store.Create(ctx, c)
	}

	// 按 Surface 过滤。
	got, _ := store.List(ctx, CaseFilter{Surface: "home"})
	if len(got) != 3 {
		t.Errorf("Surface=home 期望 3 条, 实际 %d", len(got))
	}

	// 按 Surface + Channel 过滤。
	got, _ = store.List(ctx, CaseFilter{Surface: "home", Channel: "tech"})
	if len(got) != 2 {
		t.Errorf("Surface=home&Channel=tech 期望 2 条, 实际 %d", len(got))
	}

	// 按 Intent 过滤。
	got, _ = store.List(ctx, CaseFilter{Intent: "exploit"})
	if len(got) != 1 {
		t.Errorf("Intent=exploit 期望 1 条, 实际 %d", len(got))
	}

	// 无过滤。
	got, _ = store.List(ctx, CaseFilter{})
	if len(got) != 4 {
		t.Errorf("无过滤期望 4 条, 实际 %d", len(got))
	}
}

// TestMemoryCaseStore_List_LimitOffset 验证 Limit/Offset 分页。
func TestMemoryCaseStore_List_LimitOffset(t *testing.T) {
	store := NewMemoryCaseStore()
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		c := EvalCase{
			ID:       string(rune('a' + i)),
			Query:    "q",
			Expected: []string{"a"},
			Surface:  "home",
		}
		_ = store.Create(ctx, c)
	}

	got, _ := store.List(ctx, CaseFilter{Limit: 3})
	if len(got) != 3 {
		t.Errorf("Limit=3 期望 3 条, 实际 %d", len(got))
	}

	got, _ = store.List(ctx, CaseFilter{Offset: 8})
	if len(got) != 2 {
		t.Errorf("Offset=8 期望 2 条, 实际 %d", len(got))
	}

	got, _ = store.List(ctx, CaseFilter{Offset: 2, Limit: 3})
	if len(got) != 3 {
		t.Errorf("Offset=2&Limit=3 期望 3 条, 实际 %d", len(got))
	}
}

// TestMemoryCaseStore_Create_Duplicate 验证重复创建报错。
func TestMemoryCaseStore_Create_Duplicate(t *testing.T) {
	store := NewMemoryCaseStore()
	ctx := context.Background()
	c := makeValidCase("c1")
	_ = store.Create(ctx, c)
	if err := store.Create(ctx, c); err == nil {
		t.Errorf("重复创建应报错")
	}
}

// TestMemoryCaseStore_Get_NotExist 验证 Get 不存在报错。
func TestMemoryCaseStore_Get_NotExist(t *testing.T) {
	store := NewMemoryCaseStore()
	if _, err := store.Get(context.Background(), "nope"); err == nil {
		t.Errorf("Get 不存在应报错")
	}
}
