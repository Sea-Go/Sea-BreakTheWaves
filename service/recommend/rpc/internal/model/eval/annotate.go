// internal/eval/annotate.go — 人工标注闭环（Task 15.5）。
//
// 职责：
//   - 实现 AnnotationService 管理人工标注数据
//   - 高质量标注（Rating≥4）自动入评估集（PromoteToCase）
//   - 批量校准 LLM Judge rubrics（Calibrate，通过 RubricCalibrator 抽象）
//   - 内置 MemoryAnnotationStore 默认实现
//
// 二开扩展点：
//   - 实现 AnnotationStore interface 替换持久化后端
//   - 实现 RubricCalibrator interface 接入实际 LLM rubric 校准
//   - 调整 promoteRatingThreshold 改变自动入评估集的评分门槛
package eval

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ----------------------------------------------------------------------------
// 类型定义
// ----------------------------------------------------------------------------

// Annotation 人工标注数据。
//
// 字段语义：
//   - ID：标注 ID（创建时自动生成 UUID）
//   - CaseID：关联的评估用例 ID（可为空，表示新标注）
//   - UserID：标注人 ID
//   - Query：标注的查询文本
//   - Actual：被标注的推荐结果列表
//   - Rating：评分（1-5，5 为最佳）
//   - Comment：标注评论
//   - CreatedAt：创建时间（Unix 秒）
type Annotation struct {
	// ID 标注 ID。
	ID string `json:"id"`
	// CaseID 关联用例 ID。
	CaseID string `json:"case_id"`
	// UserID 标注人 ID。
	UserID string `json:"user_id"`
	// Query 查询文本。
	Query string `json:"query"`
	// Actual 推荐结果列表。
	Actual []string `json:"actual"`
	// Rating 评分（1-5）。
	Rating int `json:"rating"`
	// Comment 评论。
	Comment string `json:"comment"`
	// CreatedAt 创建时间。
	CreatedAt int64 `json:"created_at"`
}

// AnnotationFilter 标注过滤条件。
type AnnotationFilter struct {
	// CaseID 按用例 ID 过滤。
	CaseID string
	// UserID 按标注人过滤。
	UserID string
	// Limit 返回条数上限（<=0 表示不限）。
	Limit int
	// Offset 偏移量。
	Offset int
}

// ----------------------------------------------------------------------------
// AnnotationStore interface
// ----------------------------------------------------------------------------

// AnnotationStore 标注持久化抽象。
//
// 二开：实现该 interface 替换为 PG/Mongo 等后端。
type AnnotationStore interface {
	// Create 创建标注。
	Create(ctx context.Context, a Annotation) error
	// List 按过滤条件列出标注。
	List(ctx context.Context, filter AnnotationFilter) ([]Annotation, error)
}

// ----------------------------------------------------------------------------
// RubricCalibrator interface
// ----------------------------------------------------------------------------

// RubricCalibrator rubric 校准抽象，用于根据标注数据校准 LLM Judge rubrics。
//
// 二开：实现该 interface 接入实际 LLM rubric 校准逻辑。
type RubricCalibrator interface {
	// Calibrate 根据标注数据校准 rubrics。
	Calibrate(ctx context.Context, annotations []Annotation) error
}

// ----------------------------------------------------------------------------
// AnnotationService
// ----------------------------------------------------------------------------

// promoteRatingThreshold 自动入评估集的评分门槛（Rating≥4 视为高质量）。
const promoteRatingThreshold = 4

// AnnotationService 人工标注业务层。
//
// 工作流程：
//  1. Annotate：创建标注；若 Rating≥promoteRatingThreshold 自动调用 PromoteToCase
//  2. PromoteToCase：将标注转为 EvalCase 入 CaseStore
//  3. Calibrate：批量拉取标注，调用 RubricCalibrator 校准 rubrics
//
// 二开扩展点：调整 promoteRatingThreshold；替换 AnnotationStore/RubricCalibrator。
type AnnotationService struct {
	// store 标注存储。
	store AnnotationStore
	// cases 评估集存储。
	cases CaseStore
	// rubrics rubric 校准器。
	rubrics RubricCalibrator
}

// NewAnnotationService 构造 AnnotationService。
// store 标注存储；cases 评估集存储；rubrics rubric 校准器（可为 nil）。
func NewAnnotationService(store AnnotationStore, cases CaseStore, rubrics RubricCalibrator) *AnnotationService {
	return &AnnotationService{store: store, cases: cases, rubrics: rubrics}
}

// Annotate 创建人工标注。
// 若 Rating≥promoteRatingThreshold 且 Query/Actual 非空，自动调用 PromoteToCase 入评估集。
// a.ID 为空时自动生成 UUID；CreatedAt 自动填充。
func (s *AnnotationService) Annotate(ctx context.Context, a Annotation) error {
	if a.UserID == "" {
		return fmt.Errorf("user_id 不能为空")
	}
	if a.Query == "" {
		return fmt.Errorf("query 不能为空")
	}
	if a.Rating < 1 || a.Rating > 5 {
		return fmt.Errorf("rating 必须在 1-5 之间, 实际 %d", a.Rating)
	}
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	if a.CreatedAt == 0 {
		a.CreatedAt = time.Now().Unix()
	}
	if err := s.store.Create(ctx, a); err != nil {
		return err
	}
	// 高质量标注自动入评估集。
	if a.Rating >= promoteRatingThreshold && len(a.Actual) > 0 {
		if err := s.PromoteToCase(ctx, a); err != nil {
			// 自动入评估集失败不影响标注创建，仅返回错误信息。
			return fmt.Errorf("标注已创建但自动入评估集失败: %w", err)
		}
	}
	return nil
}

// List 按过滤条件列出标注。
func (s *AnnotationService) List(ctx context.Context, filter AnnotationFilter) ([]Annotation, error) {
	return s.store.List(ctx, filter)
}

// Calibrate 批量校准 rubrics。
// 拉取所有标注，调用 RubricCalibrator.Calibrate。
// 未注入 RubricCalibrator 时跳过（返回 nil）。
func (s *AnnotationService) Calibrate(ctx context.Context) error {
	if s.rubrics == nil {
		return nil
	}
	annotations, err := s.store.List(ctx, AnnotationFilter{})
	if err != nil {
		return fmt.Errorf("拉取标注失败: %w", err)
	}
	if len(annotations) == 0 {
		return nil
	}
	return s.rubrics.Calibrate(ctx, annotations)
}

// PromoteToCase 将标注转为评估用例并写入 CaseStore。
// 用 Actual 作为 Expected（管理员认可的高质量推荐结果）。
// Surface 默认 "default"；若标注关联了 CaseID，会更新已有用例。
func (s *AnnotationService) PromoteToCase(ctx context.Context, a Annotation) error {
	if a.Query == "" {
		return fmt.Errorf("query 不能为空")
	}
	if len(a.Actual) == 0 {
		return fmt.Errorf("actual 不能为空")
	}
	c := EvalCase{
		ID:       a.CaseID,
		Query:    a.Query,
		Expected: a.Actual,
		Surface:  "default",
		Tags:     []string{"promoted", "rating:" + itoa(a.Rating)},
	}
	if c.ID == "" {
		c.ID = uuid.NewString()
		// 新建用例：通过 CaseService 走 Validate 流程。
		svc := &CaseService{store: s.cases}
		return svc.Create(ctx, c)
	}
	// 已有用例：直接 Update。
	return s.cases.Update(ctx, c)
}

// ----------------------------------------------------------------------------
// MemoryAnnotationStore 默认实现
// ----------------------------------------------------------------------------

// MemoryAnnotationStore 内存版 AnnotationStore，sync.RWMutex + slice。
type MemoryAnnotationStore struct {
	mu          sync.RWMutex
	annotations []Annotation
}

// NewMemoryAnnotationStore 构造 MemoryAnnotationStore。
func NewMemoryAnnotationStore() *MemoryAnnotationStore {
	return &MemoryAnnotationStore{}
}

// Create 创建标注。
func (m *MemoryAnnotationStore) Create(_ context.Context, a Annotation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.annotations = append(m.annotations, a)
	return nil
}

// List 按过滤条件列出标注（支持 CaseID/UserID + Limit/Offset）。
func (m *MemoryAnnotationStore) List(_ context.Context, filter AnnotationFilter) ([]Annotation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Annotation, 0, len(m.annotations))
	for _, a := range m.annotations {
		if filter.CaseID != "" && a.CaseID != filter.CaseID {
			continue
		}
		if filter.UserID != "" && a.UserID != filter.UserID {
			continue
		}
		out = append(out, a)
	}
	// Offset/Limit 分页。
	start := 0
	if filter.Offset > 0 && filter.Offset < len(out) {
		start = filter.Offset
	}
	out = out[start:]
	if filter.Limit > 0 && filter.Limit < len(out) {
		out = out[:filter.Limit]
	}
	return out, nil
}
