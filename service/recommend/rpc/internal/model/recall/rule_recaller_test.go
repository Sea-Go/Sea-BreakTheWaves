package recall

import (
	"context"
	"errors"
	"testing"

	"sea/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件使用 mock Repository 测试 RuleRecaller，覆盖：
//   - 默认热点路径（无 Intent）
//   - 三类规则选择（hot/latest/editor，通过 Intent.Signals 与 TimeIntent）
//   - TopK 优先级（req.TopK > recaller.topK）
//   - TopK 截断
//   - 错误传播
//   - 候选 Source 标记
//   - Name()
// ============================================================================

// mockRuleRepo 模拟 Repository，记录调用方法与 topK。
type mockRuleRepo struct {
	hotFn    func(ctx context.Context, topK int) ([]domain.Candidate, error)
	latestFn func(ctx context.Context, topK int) ([]domain.Candidate, error)
	editorFn func(ctx context.Context, topK int) ([]domain.Candidate, error)

	hotCalls    int
	latestCalls int
	editorCalls int
	lastTopK    int
}

func (m *mockRuleRepo) ListHotArticles(ctx context.Context, topK int) ([]domain.Candidate, error) {
	m.hotCalls++
	m.lastTopK = topK
	if m.hotFn != nil {
		return m.hotFn(ctx, topK)
	}
	return nil, nil
}

func (m *mockRuleRepo) ListLatestArticles(ctx context.Context, topK int) ([]domain.Candidate, error) {
	m.latestCalls++
	m.lastTopK = topK
	if m.latestFn != nil {
		return m.latestFn(ctx, topK)
	}
	return nil, nil
}

func (m *mockRuleRepo) ListEditorPicks(ctx context.Context, topK int) ([]domain.Candidate, error) {
	m.editorCalls++
	m.lastTopK = topK
	if m.editorFn != nil {
		return m.editorFn(ctx, topK)
	}
	return nil, nil
}

// TestRuleRecaller_Name 验证召回器名称。
func TestRuleRecaller_Name(t *testing.T) {
	r := NewRuleRecaller(&mockRuleRepo{}, 10)
	if got := r.Name(); got != "rule" {
		t.Errorf("Name() = %q, 期望 rule", got)
	}
}

// TestRuleRecaller_DefaultHot 验证无 Intent 时默认走热点规则。
func TestRuleRecaller_DefaultHot(t *testing.T) {
	mock := &mockRuleRepo{
		hotFn: func(ctx context.Context, topK int) ([]domain.Candidate, error) {
			if topK != 10 {
				t.Errorf("topK = %d, 期望 10", topK)
			}
			return []domain.Candidate{
				{ArticleID: "h1", Score: 0.9},
				{ArticleID: "h2", Score: 0.8},
			}, nil
		},
	}
	r := NewRuleRecaller(mock, 10)
	res, err := r.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if res.Source != "rule" {
		t.Errorf("Source = %q, 期望 rule", res.Source)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("候选数 = %d, 期望 2", len(res.Candidates))
	}
	if mock.hotCalls != 1 {
		t.Errorf("ListHotArticles 调用次数 = %d, 期望 1", mock.hotCalls)
	}
	if mock.latestCalls != 0 || mock.editorCalls != 0 {
		t.Errorf("默认热点不应调用 latest/editor")
	}
}

// TestRuleRecaller_SignalLatest 验证 Intent.Signals["rule"]=latest 走最新规则。
func TestRuleRecaller_SignalLatest(t *testing.T) {
	mock := &mockRuleRepo{
		latestFn: func(ctx context.Context, topK int) ([]domain.Candidate, error) {
			return []domain.Candidate{{ArticleID: "l1"}}, nil
		},
	}
	r := NewRuleRecaller(mock, 10)
	_, err := r.Recall(context.Background(), domain.RecallRequest{
		Intent: &domain.Intent{Signals: map[string]string{"rule": "latest"}},
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if mock.latestCalls != 1 {
		t.Errorf("ListLatestArticles 调用次数 = %d, 期望 1", mock.latestCalls)
	}
	if mock.hotCalls != 0 {
		t.Errorf("latest 规则不应调用 hot")
	}
}

// TestRuleRecaller_SignalEditor 验证 Intent.Signals["rule"]=editor 走编辑精选规则。
func TestRuleRecaller_SignalEditor(t *testing.T) {
	mock := &mockRuleRepo{
		editorFn: func(ctx context.Context, topK int) ([]domain.Candidate, error) {
			return []domain.Candidate{{ArticleID: "e1"}}, nil
		},
	}
	r := NewRuleRecaller(mock, 10)
	_, err := r.Recall(context.Background(), domain.RecallRequest{
		Intent: &domain.Intent{Signals: map[string]string{"rule": "editor"}},
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if mock.editorCalls != 1 {
		t.Errorf("ListEditorPicks 调用次数 = %d, 期望 1", mock.editorCalls)
	}
	if mock.hotCalls != 0 {
		t.Errorf("editor 规则不应调用 hot")
	}
}

// TestRuleRecaller_SignalHot 验证 Intent.Signals["rule"]=hot 显式走热点。
func TestRuleRecaller_SignalHot(t *testing.T) {
	mock := &mockRuleRepo{}
	r := NewRuleRecaller(mock, 10)
	_, err := r.Recall(context.Background(), domain.RecallRequest{
		Intent: &domain.Intent{
			Signals:    map[string]string{"rule": "hot"},
			TimeIntent: "latest", // 显式 hot 应优先于 TimeIntent
		},
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if mock.hotCalls != 1 {
		t.Errorf("显式 hot 应调用 ListHotArticles, 实际 %d 次", mock.hotCalls)
	}
	if mock.latestCalls != 0 {
		t.Errorf("显式 hot 不应因 TimeIntent=latest 调用 latest")
	}
}

// TestRuleRecaller_TimeIntentLatest 验证 TimeIntent=latest 走最新规则。
func TestRuleRecaller_TimeIntentLatest(t *testing.T) {
	mock := &mockRuleRepo{}
	r := NewRuleRecaller(mock, 10)
	_, err := r.Recall(context.Background(), domain.RecallRequest{
		Intent: &domain.Intent{TimeIntent: "latest"},
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if mock.latestCalls != 1 {
		t.Errorf("TimeIntent=latest 应走 latest, 调用 %d 次", mock.latestCalls)
	}
}

// TestRuleRecaller_InvalidSignalFallsBack 验证未知 rule Signal 回退到热点。
func TestRuleRecaller_InvalidSignalFallsBack(t *testing.T) {
	mock := &mockRuleRepo{}
	r := NewRuleRecaller(mock, 10)
	_, err := r.Recall(context.Background(), domain.RecallRequest{
		Intent: &domain.Intent{Signals: map[string]string{"rule": "unknown"}},
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if mock.hotCalls != 1 {
		t.Errorf("未知 rule 应回退到 hot, 调用 %d 次", mock.hotCalls)
	}
}

// TestRuleRecaller_TopKFromRequest 验证 req.TopK 优先于 recaller 默认 topK。
func TestRuleRecaller_TopKFromRequest(t *testing.T) {
	mock := &mockRuleRepo{}
	r := NewRuleRecaller(mock, 50)
	_, err := r.Recall(context.Background(), domain.RecallRequest{
		TopK: 5,
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if mock.lastTopK != 5 {
		t.Errorf("topK = %d, 期望使用 req.TopK=5", mock.lastTopK)
	}
}

// TestRuleRecaller_TopKCap 验证仓储返回超额时被截断。
func TestRuleRecaller_TopKCap(t *testing.T) {
	mock := &mockRuleRepo{
		hotFn: func(ctx context.Context, topK int) ([]domain.Candidate, error) {
			cands := make([]domain.Candidate, 0, 8)
			for i := 0; i < 8; i++ {
				cands = append(cands, domain.Candidate{ArticleID: "h"})
			}
			return cands, nil
		},
	}
	r := NewRuleRecaller(mock, 3)
	res, err := r.Recall(context.Background(), domain.RecallRequest{})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if len(res.Candidates) != 3 {
		t.Errorf("候选数 = %d, 期望截断到 3", len(res.Candidates))
	}
}

// TestRuleRecaller_SourceTagging 验证候选 Source 被标记为具体规则。
func TestRuleRecaller_SourceTagging(t *testing.T) {
	mock := &mockRuleRepo{
		latestFn: func(ctx context.Context, topK int) ([]domain.Candidate, error) {
			return []domain.Candidate{
				{ArticleID: "l1", Source: ""},
				{ArticleID: "l2", Source: "preset"}, // 已有 Source 不应被覆盖
			}, nil
		},
	}
	r := NewRuleRecaller(mock, 10)
	res, err := r.Recall(context.Background(), domain.RecallRequest{
		Intent: &domain.Intent{Signals: map[string]string{"rule": "latest"}},
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if res.Candidates[0].Source != "latest" {
		t.Errorf("空 Source 应被标记为 latest, 实际 %q", res.Candidates[0].Source)
	}
	if res.Candidates[1].Source != "preset" {
		t.Errorf("已有 Source 不应被覆盖, 实际 %q", res.Candidates[1].Source)
	}
}

// TestRuleRecaller_ErrorPropagation 验证仓储错误向上传播。
func TestRuleRecaller_ErrorPropagation(t *testing.T) {
	mock := &mockRuleRepo{
		hotFn: func(ctx context.Context, topK int) ([]domain.Candidate, error) {
			return nil, errSentinel
		},
	}
	r := NewRuleRecaller(mock, 10)
	_, err := r.Recall(context.Background(), domain.RecallRequest{})
	if !errors.Is(err, errSentinel) {
		t.Errorf("期望错误 %v 传播, 实际 %v", errSentinel, err)
	}
}
