// internal/eval/annotate_test.go — 人工标注闭环单测（Task 15.5）。
//
// 覆盖：
//   - Annotate 创建标注 + 自动入评估集（Rating≥4）
//   - 低评分标注不入评估集
//   - 校验失败（UserID/Query/Rating 范围）
//   - List 过滤（CaseID/UserID/Limit/Offset）
//   - Calibrate 批量校准（含未注入 rubrics、空标注）
//   - PromoteToCase 标注转用例（新建/已存在）
//   - MemoryAnnotationStore 边界
//
// 使用 stdlib 手写 stub，不依赖 testify。
package eval

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// ============================================================================
// stub 实现
// ============================================================================

// stubAnnotationStore 测试用 AnnotationStore。
type stubAnnotationStore struct {
	mu        sync.Mutex
	items     []Annotation
	createErr error
	listErr   error
}

func newStubAnnotationStore() *stubAnnotationStore {
	return &stubAnnotationStore{}
}

func (s *stubAnnotationStore) Create(_ context.Context, a Annotation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return s.createErr
	}
	s.items = append(s.items, a)
	return nil
}

func (s *stubAnnotationStore) List(_ context.Context, filter AnnotationFilter) ([]Annotation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]Annotation, 0, len(s.items))
	for _, a := range s.items {
		if filter.CaseID != "" && a.CaseID != filter.CaseID {
			continue
		}
		if filter.UserID != "" && a.UserID != filter.UserID {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// stubRubricCalibrator 测试用 RubricCalibrator。
type stubRubricCalibrator struct {
	calls     int
	lastInput []Annotation
	err       error
}

func (s *stubRubricCalibrator) Calibrate(_ context.Context, annotations []Annotation) error {
	s.calls++
	s.lastInput = annotations
	return s.err
}

// makeValidAnnotation 构造一个合法 Annotation。
func makeValidAnnotation(rating int) Annotation {
	return Annotation{
		UserID: "u1",
		Query:  "test query",
		Actual: []string{"art-1", "art-2"},
		Rating: rating,
	}
}

// ============================================================================
// 测试用例 — Annotate + 自动入评估集
// ============================================================================

// TestAnnotationService_Annotate_HighRatingPromotes 验证高评分标注自动入评估集。
func TestAnnotationService_Annotate_HighRatingPromotes(t *testing.T) {
	store := newStubAnnotationStore()
	caseStore := NewMemoryCaseStore()
	svc := NewAnnotationService(store, caseStore, nil)
	ctx := context.Background()

	a := makeValidAnnotation(5) // Rating=5 ≥ 4 → 自动入评估集
	if err := svc.Annotate(ctx, a); err != nil {
		t.Fatalf("Annotate 错误: %v", err)
	}

	// 验证标注已创建。
	if len(store.items) != 1 {
		t.Fatalf("标注数 = %d, 期望 1", len(store.items))
	}
	// 验证评估集已写入。
	cases, _ := caseStore.List(ctx, CaseFilter{})
	if len(cases) != 1 {
		t.Fatalf("评估集用例数 = %d, 期望 1", len(cases))
	}
	if cases[0].Query != "test query" {
		t.Errorf("用例 Query = %q, 期望 test query", cases[0].Query)
	}
	if len(cases[0].Expected) != 2 || cases[0].Expected[0] != "art-1" {
		t.Errorf("用例 Expected = %v, 期望 [art-1 art-2]", cases[0].Expected)
	}
}

// TestAnnotationService_Annotate_LowRatingNoPromote 验证低评分标注不入评估集。
func TestAnnotationService_Annotate_LowRatingNoPromote(t *testing.T) {
	store := newStubAnnotationStore()
	caseStore := NewMemoryCaseStore()
	svc := NewAnnotationService(store, caseStore, nil)
	ctx := context.Background()

	a := makeValidAnnotation(3) // Rating=3 < 4 → 不入评估集
	if err := svc.Annotate(ctx, a); err != nil {
		t.Fatalf("Annotate 错误: %v", err)
	}
	if len(store.items) != 1 {
		t.Errorf("标注数 = %d, 期望 1", len(store.items))
	}
	cases, _ := caseStore.List(ctx, CaseFilter{})
	if len(cases) != 0 {
		t.Errorf("低评分不应入评估集, 实际 %d 条", len(cases))
	}
}

// TestAnnotationService_Annotate_ValidateFail 验证校验失败。
func TestAnnotationService_Annotate_ValidateFail(t *testing.T) {
	store := newStubAnnotationStore()
	caseStore := NewMemoryCaseStore()
	svc := NewAnnotationService(store, caseStore, nil)
	ctx := context.Background()

	cases := []struct {
		name string
		a    Annotation
	}{
		{"empty user id", Annotation{UserID: "", Query: "q", Rating: 5}},
		{"empty query", Annotation{UserID: "u1", Query: "", Rating: 5}},
		{"rating too low", Annotation{UserID: "u1", Query: "q", Rating: 0}},
		{"rating too high", Annotation{UserID: "u1", Query: "q", Rating: 6}},
	}
	for _, c := range cases {
		err := svc.Annotate(ctx, c.a)
		if err == nil {
			t.Errorf("%s: 期望校验失败错误, 实际 nil", c.name)
		}
	}
}

// TestAnnotationService_Annotate_AutoID 验证 ID 自动生成。
func TestAnnotationService_Annotate_AutoID(t *testing.T) {
	store := newStubAnnotationStore()
	caseStore := NewMemoryCaseStore()
	svc := NewAnnotationService(store, caseStore, nil)

	a := makeValidAnnotation(3)
	a.ID = ""
	if err := svc.Annotate(context.Background(), a); err != nil {
		t.Fatalf("Annotate 错误: %v", err)
	}
	if store.items[0].ID == "" {
		t.Errorf("ID 应自动生成, 实际空")
	}
	if store.items[0].CreatedAt == 0 {
		t.Errorf("CreatedAt 应被填充")
	}
}

// ============================================================================
// 测试用例 — List 过滤
// ============================================================================

// TestAnnotationService_List_Filter 验证 CaseID/UserID 过滤。
func TestAnnotationService_List_Filter(t *testing.T) {
	store := NewMemoryAnnotationStore()
	svc := NewAnnotationService(store, NewMemoryCaseStore(), nil)
	ctx := context.Background()

	annotations := []Annotation{
		{ID: "1", UserID: "u1", Query: "q1", CaseID: "c1", Rating: 5, Actual: []string{"a"}},
		{ID: "2", UserID: "u2", Query: "q2", CaseID: "c1", Rating: 4, Actual: []string{"b"}},
		{ID: "3", UserID: "u1", Query: "q3", CaseID: "c2", Rating: 3, Actual: []string{"c"}},
	}
	for _, a := range annotations {
		// 直接写 store 避免触发自动入评估集逻辑。
		_ = store.Create(ctx, a)
	}

	// 按 UserID 过滤。
	got, _ := svc.List(ctx, AnnotationFilter{UserID: "u1"})
	if len(got) != 2 {
		t.Errorf("UserID=u1 期望 2 条, 实际 %d", len(got))
	}

	// 按 CaseID 过滤。
	got, _ = svc.List(ctx, AnnotationFilter{CaseID: "c1"})
	if len(got) != 2 {
		t.Errorf("CaseID=c1 期望 2 条, 实际 %d", len(got))
	}

	// 按 UserID + CaseID 过滤。
	got, _ = svc.List(ctx, AnnotationFilter{UserID: "u1", CaseID: "c1"})
	if len(got) != 1 {
		t.Errorf("UserID=u1&CaseID=c1 期望 1 条, 实际 %d", len(got))
	}
}

// TestMemoryAnnotationStore_List_LimitOffset 验证 Limit/Offset 分页。
func TestMemoryAnnotationStore_List_LimitOffset(t *testing.T) {
	store := NewMemoryAnnotationStore()
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		_ = store.Create(ctx, Annotation{ID: string(rune('a' + i)), UserID: "u", Query: "q", Rating: 5})
	}

	got, _ := store.List(ctx, AnnotationFilter{Limit: 3})
	if len(got) != 3 {
		t.Errorf("Limit=3 期望 3 条, 实际 %d", len(got))
	}

	got, _ = store.List(ctx, AnnotationFilter{Offset: 8})
	if len(got) != 2 {
		t.Errorf("Offset=8 期望 2 条, 实际 %d", len(got))
	}
}

// ============================================================================
// 测试用例 — Calibrate
// ============================================================================

// TestAnnotationService_Calibrate 验证批量校准 rubrics。
func TestAnnotationService_Calibrate(t *testing.T) {
	store := newStubAnnotationStore()
	rc := &stubRubricCalibrator{}
	svc := NewAnnotationService(store, NewMemoryCaseStore(), rc)

	// 准备标注数据（绕开 Annotate 的自动入评估集逻辑）。
	_ = store.Create(context.Background(), makeValidAnnotation(5))
	_ = store.Create(context.Background(), makeValidAnnotation(3))

	if err := svc.Calibrate(context.Background()); err != nil {
		t.Fatalf("Calibrate 错误: %v", err)
	}
	if rc.calls != 1 {
		t.Errorf("RubricCalibrator 调用 %d 次, 期望 1 次", rc.calls)
	}
	if len(rc.lastInput) != 2 {
		t.Errorf("校准输入标注数 = %d, 期望 2", len(rc.lastInput))
	}
}

// TestAnnotationService_Calibrate_NilRubrics 验证未注入 RubricCalibrator 时跳过。
func TestAnnotationService_Calibrate_NilRubrics(t *testing.T) {
	store := newStubAnnotationStore()
	svc := NewAnnotationService(store, NewMemoryCaseStore(), nil)
	if err := svc.Calibrate(context.Background()); err != nil {
		t.Errorf("未注入 rubrics 时应返回 nil, 实际 %v", err)
	}
}

// TestAnnotationService_Calibrate_EmptyAnnotations 验证空标注时不调用 calibrator。
func TestAnnotationService_Calibrate_EmptyAnnotations(t *testing.T) {
	store := newStubAnnotationStore()
	rc := &stubRubricCalibrator{}
	svc := NewAnnotationService(store, NewMemoryCaseStore(), rc)
	if err := svc.Calibrate(context.Background()); err != nil {
		t.Errorf("空标注时应返回 nil, 实际 %v", err)
	}
	if rc.calls != 0 {
		t.Errorf("空标注时不应调用 calibrator, 实际 %d 次", rc.calls)
	}
}

// TestAnnotationService_Calibrate_Error 验证 calibrator 错误传播。
func TestAnnotationService_Calibrate_Error(t *testing.T) {
	store := newStubAnnotationStore()
	_ = store.Create(context.Background(), makeValidAnnotation(5))
	rc := &stubRubricCalibrator{err: errors.New("calibrate failed")}
	svc := NewAnnotationService(store, NewMemoryCaseStore(), rc)
	if err := svc.Calibrate(context.Background()); err == nil {
		t.Errorf("calibrator 错误应传播")
	}
}

// ============================================================================
// 测试用例 — PromoteToCase
// ============================================================================

// TestAnnotationService_PromoteToCase_NewCase 验证新标注转用例（CaseID 为空）。
func TestAnnotationService_PromoteToCase_NewCase(t *testing.T) {
	caseStore := NewMemoryCaseStore()
	svc := NewAnnotationService(NewMemoryAnnotationStore(), caseStore, nil)
	ctx := context.Background()

	a := Annotation{
		UserID: "u1",
		Query:  "new query",
		Actual: []string{"a", "b"},
		Rating: 5,
	}
	if err := svc.PromoteToCase(ctx, a); err != nil {
		t.Fatalf("PromoteToCase 错误: %v", err)
	}
	cases, _ := caseStore.List(ctx, CaseFilter{})
	if len(cases) != 1 {
		t.Fatalf("用例数 = %d, 期望 1", len(cases))
	}
	if cases[0].Query != "new query" {
		t.Errorf("Query = %q, 期望 new query", cases[0].Query)
	}
	if cases[0].Surface != "default" {
		t.Errorf("Surface = %q, 期望 default", cases[0].Surface)
	}
}

// TestAnnotationService_PromoteToCase_ExistingCase 验证已有用例更新。
func TestAnnotationService_PromoteToCase_ExistingCase(t *testing.T) {
	caseStore := NewMemoryCaseStore()
	ctx := context.Background()
	// 预置一个用例。
	_ = caseStore.Create(ctx, EvalCase{
		ID:       "case-1",
		Query:    "old query",
		Expected: []string{"old"},
		Surface:  "home",
	})

	svc := NewAnnotationService(NewMemoryAnnotationStore(), caseStore, nil)
	a := Annotation{
		CaseID: "case-1",
		UserID: "u1",
		Query:  "updated query",
		Actual: []string{"new-a", "new-b"},
		Rating: 5,
	}
	if err := svc.PromoteToCase(ctx, a); err != nil {
		t.Fatalf("PromoteToCase 错误: %v", err)
	}
	got, _ := caseStore.Get(ctx, "case-1")
	if got.Query != "updated query" {
		t.Errorf("Query = %q, 期望 updated query", got.Query)
	}
	if len(got.Expected) != 2 || got.Expected[0] != "new-a" {
		t.Errorf("Expected = %v, 期望 [new-a new-b]", got.Expected)
	}
}

// TestAnnotationService_PromoteToCase_EmptyQuery 验证空 Query 报错。
func TestAnnotationService_PromoteToCase_EmptyQuery(t *testing.T) {
	svc := NewAnnotationService(NewMemoryAnnotationStore(), NewMemoryCaseStore(), nil)
	err := svc.PromoteToCase(context.Background(), Annotation{UserID: "u1", Actual: []string{"a"}})
	if err == nil || !strings.Contains(err.Error(), "query") {
		t.Errorf("空 Query 应报错, got %v", err)
	}
}

// TestAnnotationService_PromoteToCase_EmptyActual 验证空 Actual 报错。
func TestAnnotationService_PromoteToCase_EmptyActual(t *testing.T) {
	svc := NewAnnotationService(NewMemoryAnnotationStore(), NewMemoryCaseStore(), nil)
	err := svc.PromoteToCase(context.Background(), Annotation{UserID: "u1", Query: "q"})
	if err == nil || !strings.Contains(err.Error(), "actual") {
		t.Errorf("空 Actual 应报错, got %v", err)
	}
}

// ============================================================================
// 测试用例 — store 错误传播
// ============================================================================

// TestAnnotationService_Annotate_StoreError 验证 store.Create 错误传播。
func TestAnnotationService_Annotate_StoreError(t *testing.T) {
	store := newStubAnnotationStore()
	store.createErr = errors.New("db error")
	svc := NewAnnotationService(store, NewMemoryCaseStore(), nil)
	if err := svc.Annotate(context.Background(), makeValidAnnotation(3)); err == nil {
		t.Errorf("store.Create 错误应传播")
	}
}
