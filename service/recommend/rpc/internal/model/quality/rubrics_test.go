// ============================================================================
// 该文件测试 internal/quality/rubrics.go 的 DefaultRubrics / Get / Calibrate。
// 覆盖：默认 rubric 集完整性、维度查询、反馈校准占位逻辑。
// ============================================================================

package quality

import (
	"testing"
	"time"

	"sea/service/recommend/rpc/internal/domain"
)

// TestDefaultRubrics_Completeness 验证默认 rubric 集含 7 个维度，顺序与权重。
func TestDefaultRubrics_Completeness(t *testing.T) {
	rs := DefaultRubrics()
	if rs == nil {
		t.Fatal("DefaultRubrics 返回 nil")
	}
	if len(rs.Rubrics) != 7 {
		t.Fatalf("默认 rubric 数量 = %d, want 7", len(rs.Rubrics))
	}
	// 验证 7 个维度名（顺序固定）。
	wantDims := []string{
		DimensionAccuracy, DimensionAuthority, DimensionDepth,
		DimensionFreshness, DimensionCompleteness, DimensionReadability,
		DimensionCitation,
	}
	for i, want := range wantDims {
		if rs.Rubrics[i].Dimension != want {
			t.Fatalf("Rubrics[%d].Dimension = %q, want %q", i, rs.Rubrics[i].Dimension, want)
		}
	}
	// 每个维度应有 3 档评分标准（1.0/0.5/0.0）。
	for i, r := range rs.Rubrics {
		if len(r.Criteria) != 3 {
			t.Fatalf("Rubrics[%d] (%s) Criteria 数量 = %d, want 3", i, r.Dimension, len(r.Criteria))
		}
		if r.Description == "" {
			t.Fatalf("Rubrics[%d] (%s) Description 为空", i, r.Dimension)
		}
	}
}

// TestDefaultRubrics_Weights 验证 6 维（除 accuracy）权重和为 1.0，
// accuracy 权重为 0（裁判元维度不参与 Overall 加权）。
func TestDefaultRubrics_Weights(t *testing.T) {
	rs := DefaultRubrics()
	var sum float64
	for _, r := range rs.Rubrics {
		if r.Dimension == DimensionAccuracy {
			if r.Weight != 0.0 {
				t.Fatalf("accuracy 权重 = %v, want 0", r.Weight)
			}
			continue
		}
		sum += r.Weight
	}
	// 浮点比较容差。
	if sum < 0.999 || sum > 1.001 {
		t.Fatalf("6 维权重和 = %v, want 1.0", sum)
	}
}

// TestRubricSet_Get 验证按维度名查询 rubric。
func TestRubricSet_Get(t *testing.T) {
	rs := DefaultRubrics()
	// 查询存在的维度。
	r := rs.Get(DimensionAuthority)
	if r == nil {
		t.Fatal("Get(authority) 返回 nil")
	}
	if r.Dimension != DimensionAuthority {
		t.Fatalf("Get(authority).Dimension = %q, want authority", r.Dimension)
	}
	// 查询不存在的维度。
	if r := rs.Get("not_exist"); r != nil {
		t.Fatalf("Get(not_exist) 应返回 nil, 实际 %v", r)
	}
	// nil RubricSet 安全性。
	var nilRS *RubricSet
	if r := nilRS.Get(DimensionAuthority); r != nil {
		t.Fatalf("nil RubricSet.Get 应返回 nil, 实际 %v", r)
	}
}

// TestRubricSet_Calibrate 验证反馈校准占位逻辑。
func TestRubricSet_Calibrate(t *testing.T) {
	rs := DefaultRubrics()
	// 空反馈列表：直接返回 nil，不修改 rubric。
	if err := rs.Calibrate(nil); err != nil {
		t.Fatalf("Calibrate(nil) 错误: %v", err)
	}
	// 合法反馈：维度在 RubricSet 中，返回 nil。
	feedback := []domain.QualityFeedback{
		{
			UserID:    "u1",
			ArticleID: "a1",
			Score:     0.9,
			Dimension: DimensionAuthority,
			Comment:   "权威来源",
			CreatedAt: time.Now(),
		},
		{
			UserID:    "u2",
			ArticleID: "a1",
			Score:     0.3,
			Dimension: DimensionCitation,
			Comment:   "引用不足",
			CreatedAt: time.Now(),
		},
	}
	if err := rs.Calibrate(feedback); err != nil {
		t.Fatalf("Calibrate 合法反馈错误: %v", err)
	}
	// 含空维度的反馈：跳过校验（综合反馈），返回 nil。
	mixedFeedback := append(feedback, domain.QualityFeedback{
		UserID:    "u3",
		ArticleID: "a1",
		Score:     0.5,
		Dimension: "", // 综合反馈，跳过校验
		CreatedAt: time.Now(),
	})
	if err := rs.Calibrate(mixedFeedback); err != nil {
		t.Fatalf("Calibrate 含空维度反馈错误: %v", err)
	}
	// 非法维度反馈：返回错误。
	badFeedback := []domain.QualityFeedback{
		{
			UserID:    "u1",
			ArticleID: "a1",
			Score:     0.5,
			Dimension: "not_exist_dim",
			CreatedAt: time.Now(),
		},
	}
	if err := rs.Calibrate(badFeedback); err == nil {
		t.Fatal("Calibrate 非法维度反馈应返回错误, 实际 nil")
	}
	// nil RubricSet：返回错误。
	var nilRS *RubricSet
	if err := nilRS.Calibrate(feedback); err == nil {
		t.Fatal("nil RubricSet.Calibrate 应返回错误, 实际 nil")
	}
}
