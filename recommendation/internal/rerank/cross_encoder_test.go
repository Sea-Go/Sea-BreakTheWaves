package rerank

import (
	"context"
	"errors"
	"testing"

	"sea/internal/domain"
)

// stubModel 实现 Model interface，用于测试（cross_encoder/two_tower/ltr 共享）。
type stubModel struct {
	predict func(input []float64) (float64, error)
	loadErr error
	loaded  bool
	calls   int
}

func (s *stubModel) Predict(input []float64) (float64, error) {
	s.calls++
	if s.predict == nil {
		return 0.5, nil
	}
	return s.predict(input)
}

func (s *stubModel) Load(path string) error {
	if s.loadErr != nil {
		return s.loadErr
	}
	s.loaded = true
	return nil
}

var errStubLoad = errors.New("stub load error")

// TestCrossEncoder_StubScore 验证 model 为 nil 时走 stub 评分。
func TestCrossEncoder_StubScore(t *testing.T) {
	ce := NewCrossEncoder("/tmp/model.onnx")
	c := domain.Candidate{
		ArticleID: "art-1",
		Score:     0.5,
		Extra: map[string]any{
			"title":   "深度学习推荐系统",
			"summary": "本文介绍深度学习在推荐中的应用",
			"tags":    []string{"AI", "推荐"},
		},
	}
	score, err := ce.Score(context.Background(), "深度学习 推荐", c)
	if err != nil {
		t.Fatalf("Score 错误: %v", err)
	}
	if score < 0 || score > 1 {
		t.Fatalf("stub score 应在 [0,1], 实际 %v", score)
	}
	if score <= 0 {
		t.Errorf("命中查询时分数应为正, 实际 %v", score)
	}
}

// TestCrossEncoder_EmptyQuery 验证空查询走原分数+文本长度分支。
func TestCrossEncoder_EmptyQuery(t *testing.T) {
	ce := NewCrossEncoder("/tmp/model.onnx")
	c := domain.Candidate{
		ArticleID: "art-1",
		Score:     0.8,
		Extra:     map[string]any{"title": "AI 深度学习推荐系统", "summary": "深度学习"},
	}
	score, err := ce.Score(context.Background(), "", c)
	if err != nil {
		t.Fatalf("Score 错误: %v", err)
	}
	if score <= 0 {
		t.Errorf("空查询分数应为正, 实际 %v", score)
	}
}

// TestCrossEncoder_WithModel 验证模型加载后走 Predict 路径。
func TestCrossEncoder_WithModel(t *testing.T) {
	ce := NewCrossEncoder("/tmp/model.onnx")
	m := &stubModel{predict: func(input []float64) (float64, error) {
		// 返回输入向量均值，sigmoid 输出可预测
		sum := 0.0
		for _, v := range input {
			sum += v
		}
		return sum / float64(len(input)), nil
	}}
	if err := ce.LoadModel(m); err != nil {
		t.Fatalf("LoadModel 错误: %v", err)
	}
	if !m.loaded {
		t.Fatalf("模型未标记为已加载")
	}
	c := domain.Candidate{
		ArticleID: "art-1",
		Extra:     map[string]any{"title": "AI 推荐", "summary": "深度学习"},
	}
	score, err := ce.Score(context.Background(), "AI", c)
	if err != nil {
		t.Fatalf("Score 错误: %v", err)
	}
	if score < 0 || score > 1 {
		t.Fatalf("sigmoid score 应在 [0,1], 实际 %v", score)
	}
	if m.calls != 1 {
		t.Errorf("Predict 调用次数期望 1, 实际 %d", m.calls)
	}
}

// TestCrossEncoder_LoadModelError 验证 Load 失败时返回错误。
func TestCrossEncoder_LoadModelError(t *testing.T) {
	ce := NewCrossEncoder("/tmp/model.onnx")
	m := &stubModel{loadErr: errStubLoad}
	if err := ce.LoadModel(m); err == nil {
		t.Fatalf("LoadModel 应返回错误")
	}
	if ce.model != nil {
		t.Errorf("Load 失败时 model 不应被赋值")
	}
}

// TestCrossEncoder_Rerank 验证批量重排与 topK 截断、降序。
func TestCrossEncoder_Rerank(t *testing.T) {
	ce := NewCrossEncoder("/tmp/model.onnx")
	candidates := []domain.Candidate{
		{ArticleID: "a1", Extra: map[string]any{"title": "AI 深度学习", "tags": []string{"AI"}}},
		{ArticleID: "a2", Extra: map[string]any{"title": "美食烹饪", "tags": []string{"美食"}}},
		{ArticleID: "a3", Extra: map[string]any{"title": "AI 推荐系统", "tags": []string{"AI", "推荐"}}},
	}
	out, err := ce.Rerank(context.Background(), "AI 推荐", candidates, 2)
	if err != nil {
		t.Fatalf("Rerank 错误: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("Rerank 返回数量期望 2, 实际 %d", len(out))
	}
	// 与查询更相关的 a1/a3 应排在前面
	if out[0].ArticleID != "a1" && out[0].ArticleID != "a3" {
		t.Errorf("首位应为 a1 或 a3, 实际 %s", out[0].ArticleID)
	}
	if out[0].Score < out[1].Score {
		t.Errorf("应按分数降序, out[0]=%v < out[1]=%v", out[0].Score, out[1].Score)
	}
}

// TestCrossEncoder_RerankTopKNoTrunc 验证 topK<=0 时不截断。
func TestCrossEncoder_RerankTopKNoTrunc(t *testing.T) {
	ce := NewCrossEncoder("/tmp/model.onnx")
	candidates := []domain.Candidate{
		{ArticleID: "a1", Extra: map[string]any{"title": "AI"}},
		{ArticleID: "a2", Extra: map[string]any{"title": "AI"}},
	}
	out, err := ce.Rerank(context.Background(), "AI", candidates, 0)
	if err != nil {
		t.Fatalf("Rerank 错误: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("topK=0 不截断, 期望 2, 实际 %d", len(out))
	}
}
