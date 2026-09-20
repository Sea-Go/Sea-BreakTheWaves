package rerank

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件测试 RerankTools（Task 8.8），覆盖 4 个工具方法：
//   - SelfRerank（rerank.self_rerank）
//   - ExternalRerank（rerank.external_rerank）
//   - ExtractFeatures（rerank.features）
//   - ABTestStatus（rerank.ab_test）
//
// mock 复用说明：
//   - mockSelfReranker（定义于 abtest_test.go）满足 domain.Reranker interface。
//   - mockExternalReranker（定义于 server_test.go）满足 ExternalReranker interface。
//   - mockFeatureProvider（定义于 reranker_impl_test.go）满足 FeatureProvider interface。
// ============================================================================

// ---- 测试用例 ----

// TestRerankTools_SelfRerank 验证 SelfRerank 工具调用 reranker.Rerank。
func TestRerankTools_SelfRerank(t *testing.T) {
	self := &mockSelfReranker{}
	tools := NewRerankTools(self, nil, nil)

	req := domain.RerankRequest{
		UserKey:    domain.UserKey{UserID: "u1"},
		Candidates: []domain.Candidate{{ArticleID: "a1"}, {ArticleID: "a2"}},
		Model:      ModelSelf,
		TopK:       2,
	}

	result, err := tools.SelfRerank(context.Background(), req)
	if err != nil {
		t.Fatalf("SelfRerank 失败: %v", err)
	}
	if self.called != 1 {
		t.Errorf("reranker 调用次数 = %d, 期望 1", self.called)
	}
	if len(result.Candidates) != 2 {
		t.Errorf("返回候选数 = %d, 期望 2", len(result.Candidates))
	}
}

// TestRerankTools_SelfRerankNil 验证 nil reranker 时报错。
func TestRerankTools_SelfRerankNil(t *testing.T) {
	tools := NewRerankTools(nil, nil, nil)

	req := domain.RerankRequest{
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Model:      ModelSelf,
		TopK:       1,
	}

	_, err := tools.SelfRerank(context.Background(), req)
	if err == nil {
		t.Fatal("nil reranker 时应返回错误")
	}
}

// TestRerankTools_ExternalRerank 验证 ExternalRerank 工具调用 abTest.ExternalRerank。
func TestRerankTools_ExternalRerank(t *testing.T) {
	external := &mockExternalReranker{}
	ab := NewABTest(&mockSelfReranker{}, external, 0.5)
	tools := NewRerankTools(nil, ab, nil)

	cands := []domain.Candidate{{ArticleID: "a1"}, {ArticleID: "a2"}, {ArticleID: "a3"}}
	result, err := tools.ExternalRerank(context.Background(), "测试查询", cands, 2)
	if err != nil {
		t.Fatalf("ExternalRerank 失败: %v", err)
	}
	if external.called != 1 {
		t.Errorf("externalReranker 调用次数 = %d, 期望 1", external.called)
	}
	if len(result) != 2 {
		t.Errorf("返回候选数 = %d, 期望 2", len(result))
	}
}

// TestRerankTools_ExternalRerankNil 验证 nil abTest 时报错。
func TestRerankTools_ExternalRerankNil(t *testing.T) {
	tools := NewRerankTools(nil, nil, nil)

	_, err := tools.ExternalRerank(context.Background(), "测试", []domain.Candidate{{ArticleID: "a1"}}, 1)
	if err == nil {
		t.Fatal("nil abTest 时 ExternalRerank 应返回错误")
	}
}

// TestRerankTools_ExtractFeatures 验证 ExtractFeatures 工具调用 fe.Extract。
func TestRerankTools_ExtractFeatures(t *testing.T) {
	fe := &mockFeatureProvider{feats: map[string]float64{"f1": 0.5, "f2": 0.8}}
	tools := NewRerankTools(nil, nil, fe)

	candidate := domain.Candidate{ArticleID: "a1"}
	feats, err := tools.ExtractFeatures(context.Background(), candidate)
	if err != nil {
		t.Fatalf("ExtractFeatures 失败: %v", err)
	}
	if fe.called != 1 {
		t.Errorf("FeatureProvider 调用次数 = %d, 期望 1", fe.called)
	}
	if feats["f1"] != 0.5 {
		t.Errorf("feats[f1] = %.2f, 期望 0.5", feats["f1"])
	}
	if feats["f2"] != 0.8 {
		t.Errorf("feats[f2] = %.2f, 期望 0.8", feats["f2"])
	}
}

// TestRerankTools_ExtractFeaturesNil 验证 nil fe 时报错。
func TestRerankTools_ExtractFeaturesNil(t *testing.T) {
	tools := NewRerankTools(nil, nil, nil)

	_, err := tools.ExtractFeatures(context.Background(), domain.Candidate{ArticleID: "a1"})
	if err == nil {
		t.Fatal("nil fe 时 ExtractFeatures 应返回错误")
	}
}

// TestRerankTools_ABTestStatus 验证 ABTestStatus 工具返回 A/B 指标快照。
func TestRerankTools_ABTestStatus(t *testing.T) {
	ab := NewABTest(&mockSelfReranker{}, &mockExternalReranker{}, 0.5)
	// 记录一些数据，使指标非空。
	ab.RecordOutcome(bucketSelf, "a1", true, 100*time.Millisecond, 0.01)
	ab.RecordOutcome(bucketExternal, "a1", false, 50*time.Millisecond, 0.02)
	tools := NewRerankTools(nil, ab, nil)

	metrics, err := tools.ABTestStatus(context.Background())
	if err != nil {
		t.Fatalf("ABTestStatus 失败: %v", err)
	}
	// 验证返回的指标快照包含已记录的数据。
	if metrics.SelfCTR["a1"] != 1.0 {
		t.Errorf("SelfCTR[a1] = %.2f, 期望 1.0", metrics.SelfCTR["a1"])
	}
	if metrics.ExternalCTR["a1"] != 0.0 {
		t.Errorf("ExternalCTR[a1] = %.2f, 期望 0.0", metrics.ExternalCTR["a1"])
	}
	if metrics.SelfCost != 0.01 {
		t.Errorf("SelfCost = %.2f, 期望 0.01", metrics.SelfCost)
	}
	if metrics.ExternalCost != 0.02 {
		t.Errorf("ExternalCost = %.2f, 期望 0.02", metrics.ExternalCost)
	}
}

// TestRerankTools_ABTestStatusNil 验证 nil abTest 时报错。
func TestRerankTools_ABTestStatusNil(t *testing.T) {
	tools := NewRerankTools(nil, nil, nil)

	_, err := tools.ABTestStatus(context.Background())
	if err == nil {
		t.Fatal("nil abTest 时 ABTestStatus 应返回错误")
	}
}

// TestRerankTools_SelfRerankError 验证 reranker 错误传播。
func TestRerankTools_SelfRerankError(t *testing.T) {
	self := &mockSelfReranker{err: errors.New("reranker error")}
	tools := NewRerankTools(self, nil, nil)

	req := domain.RerankRequest{
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Model:      ModelSelf,
		TopK:       1,
	}

	_, err := tools.SelfRerank(context.Background(), req)
	if err == nil {
		t.Fatal("reranker 错误应传播")
	}
}

// TestRerankTools_ExtractFeaturesError 验证 fe 错误传播。
func TestRerankTools_ExtractFeaturesError(t *testing.T) {
	fe := &mockFeatureProvider{err: errors.New("extract error")}
	tools := NewRerankTools(nil, nil, fe)

	_, err := tools.ExtractFeatures(context.Background(), domain.Candidate{ArticleID: "a1"})
	if err == nil {
		t.Fatal("fe 错误应传播")
	}
}
