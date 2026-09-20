// ============================================================================
// 该文件测试 internal/domain/quality.go 的 ArticleQuality 辅助方法
// （IsPass / ToGrade）与 OverallToGrade / AllGrades 工具函数。
// 覆盖：阈值判定、A-T 等级映射（边界与典型值）、等级列表完整性。
// ============================================================================

package domain

import (
	"testing"
)

// TestArticleQuality_IsPass 验证综合评分阈值判定。
func TestArticleQuality_IsPass(t *testing.T) {
	cases := []struct {
		name      string
		overall   float64
		threshold float64
		want      bool
	}{
		{"刚好等于阈值", 0.8, 0.8, true},
		{"高于阈值", 0.9, 0.8, true},
		{"低于阈值", 0.7, 0.8, false},
		{"零分零阈值", 0.0, 0.0, true},
		{"满分高阈值", 1.0, 0.95, true},
		{"满分低阈值", 1.0, 0.0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := ArticleQuality{Overall: c.overall}
			if got := q.IsPass(c.threshold); got != c.want {
				t.Fatalf("IsPass(%v) = %v, want %v", c.threshold, got, c.want)
			}
		})
	}
}

// TestOverallToGrade 验证 0-1 综合分到 A-T 等级的映射。
func TestOverallToGrade(t *testing.T) {
	cases := []struct {
		name    string
		overall float64
		want    string
	}{
		{"满分映射 A", 1.0, "A"},
		{"零分映射 T", 0.0, "T"},
		{"0.5 映射中位 K", 0.5, "K"},
		{"0.95 映射 B", 0.95, "B"},
		{"0.90 映射 C", 0.90, "C"},
		{"0.05 映射 T", 0.05, "T"},
		{"0.10 映射 S", 0.10, "S"},
		{"越界负数 clamp 到 T", -0.5, "T"},
		{"越界大于 1 clamp 到 A", 1.5, "A"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := OverallToGrade(c.overall); got != c.want {
				t.Fatalf("OverallToGrade(%v) = %q, want %q", c.overall, got, c.want)
			}
		})
	}
}

// TestArticleQuality_ToGrade 验证 ToGrade 方法基于 Overall 计算，
// 不读取已设置的 Grade 字段。
func TestArticleQuality_ToGrade(t *testing.T) {
	// Overall=1.0 应映射 A，即使 Grade 字段已设为 "T"。
	q := ArticleQuality{Overall: 1.0, Grade: "T"}
	if got := q.ToGrade(); got != "A" {
		t.Fatalf("ToGrade() = %q, want A（应基于 Overall 重算，不读 Grade）", got)
	}
	// Overall=0.0 应映射 T。
	q2 := ArticleQuality{Overall: 0.0}
	if got := q2.ToGrade(); got != "T" {
		t.Fatalf("ToGrade() = %q, want T", got)
	}
}

// TestAllGrades 验证 AllGrades 返回 20 个 A-T 标签，顺序与字母序一致。
func TestAllGrades(t *testing.T) {
	grades := AllGrades()
	if len(grades) != GradeLen {
		t.Fatalf("AllGrades 长度 = %d, want %d", len(grades), GradeLen)
	}
	for i, g := range grades {
		want := string(rune('A' + i))
		if g != want {
			t.Fatalf("AllGrades[%d] = %q, want %q", i, g, want)
		}
	}
	if grades[0] != GradeBest {
		t.Fatalf("首元素 = %q, want %q", grades[0], GradeBest)
	}
	if grades[len(grades)-1] != GradeWorst {
		t.Fatalf("末元素 = %q, want %q", grades[len(grades)-1], GradeWorst)
	}
}

// TestArticleQualityReport_TypeCompleteness 验证 ArticleQualityReport 与
// QualityFeedback 结构体字段完整性（编译期保证）与字段可访问性。
func TestArticleQualityReport_TypeCompleteness(t *testing.T) {
	q := ArticleQuality{
		ArticleID:    "a1",
		Authority:    0.9,
		Depth:        0.8,
		Freshness:    0.7,
		Completeness: 0.6,
		Readability:  0.5,
		Citation:     0.4,
		Overall:      0.65,
		Grade:        "N",
	}
	report := ArticleQualityReport{
		ArticleID:    "a1",
		Quality:      q,
		ModelVersion: "qwen2.5-72b@20260601",
		Logprobs:     map[string]float64{"A": 0.2, "B": 0.3, "N": 0.5},
		Feedback: []QualityFeedback{
			{
				UserID:    "u1",
				ArticleID: "a1",
				Score:     0.8,
				Dimension: "authority",
				Comment:   "权威来源",
			},
		},
	}
	if report.ArticleID != "a1" {
		t.Fatalf("ArticleID = %q, want a1", report.ArticleID)
	}
	if report.Quality.Overall != 0.65 {
		t.Fatalf("Quality.Overall = %v, want 0.65", report.Quality.Overall)
	}
	if report.Logprobs["N"] != 0.5 {
		t.Fatalf("Logprobs[N] = %v, want 0.5", report.Logprobs["N"])
	}
	if len(report.Feedback) != 1 || report.Feedback[0].UserID != "u1" {
		t.Fatalf("Feedback 字段异常: %v", report.Feedback)
	}
}
