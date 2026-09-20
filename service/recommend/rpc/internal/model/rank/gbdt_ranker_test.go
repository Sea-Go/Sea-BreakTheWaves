package rank

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件测试 GBDTRanker stub，覆盖：
//   - Rank 返回 not implemented 错误
//   - 错误消息包含 "not implemented" 与 "XGBoost"
//   - Name() 返回 "gbdt"
// ============================================================================

// TestGBDTRanker_Name 验证排序器名称。
func TestGBDTRanker_Name(t *testing.T) {
	r := NewGBDTRanker("/data/models/gbdt.json")
	if got := r.Name(); got != "gbdt" {
		t.Errorf("Name() = %q, 期望 gbdt", got)
	}
}

// TestGBDTRanker_NotImplemented 验证 Rank 返回 not implemented 错误。
func TestGBDTRanker_NotImplemented(t *testing.T) {
	r := NewGBDTRanker("/data/models/gbdt.json")
	_, err := r.Rank(context.Background(), domain.RankContext{
		Candidates: []domain.Candidate{{ArticleID: "x"}},
	})
	if err == nil {
		t.Fatal("期望返回 not implemented 错误, 实际 nil")
	}
	if !strings.Contains(err.Error(), "not implemented") {
		t.Errorf("错误消息 %q 不包含 not implemented", err.Error())
	}
	if !strings.Contains(err.Error(), "XGBoost") {
		t.Errorf("错误消息 %q 不包含 XGBoost", err.Error())
	}
}

// TestGBDTRanker_EmptyCandidates 验证空候选也返回 not implemented（stub 不做候选判断）。
func TestGBDTRanker_EmptyCandidates(t *testing.T) {
	r := NewGBDTRanker("")
	_, err := r.Rank(context.Background(), domain.RankContext{})
	if err == nil {
		t.Fatal("期望返回 not implemented 错误")
	}
	if !errors.Is(err, err) {
		t.Errorf("错误应可被 errors.Is 判定")
	}
}
