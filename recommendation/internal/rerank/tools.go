package rerank

import (
	"context"
	"fmt"

	"sea/internal/domain"
)

// ============================================================================
// 该文件实现 RerankTools，供 trpc-agent-go Agent/Skill 调用的 rerank 工具集（Task 8.8）。
//
// 核心能力：
//   - SelfRerank：自研 rerank 工具（rerank.self_rerank）
//   - ExternalRerank：外部 rerank 工具（rerank.external_rerank）
//   - ExtractFeatures：特征提取工具（rerank.features）
//   - ABTestStatus：A/B 测试状态查询工具（rerank.ab_test）
//
// 每个方法对应一个 trpc-agent-go tool，签名简单（ctx + 入参 → 出参 + error），
// 便于注册为 Function Call 供 RerankAgent 调用。
//
// 二开扩展点：
//   - 工具注册：TODO 对接 trpc-agent-go tool 注册（签名待确认）
//   - 工具扩展：新增工具方法（如 rerank.train/rerank.evaluate）
// ============================================================================

// RerankTools rerank 工具集，供 trpc-agent-go Agent/Skill 调用。
//
// 字段说明：
//   - reranker：自研 reranker（domain.Reranker 实现，如 SelfReranker）
//   - abTest：A/B 测试（提供 ExternalRerank/ABTestStatus 工具能力）
//   - fe：特征抽取器（FeatureProvider 实现，提供 ExtractFeatures 工具能力）
//
// TODO: 对接 trpc-agent-go tool 注册（待 trpc-agent-go tool API 确认）。
type RerankTools struct {
	// reranker 自研 reranker（domain.Reranker 实现，如 SelfReranker）。
	reranker domain.Reranker
	// abTest A/B 测试（提供 ExternalRerank/ABTestStatus 工具能力）。
	abTest *ABTest
	// fe 特征抽取器（FeatureProvider 实现）。
	fe FeatureProvider
}

// NewRerankTools 创建 rerank 工具集。
//
// reranker 自研 reranker；abTest A/B 测试（可为 nil，ExternalRerank/ABTestStatus 不可用）；
// fe 特征抽取器（可为 nil，ExtractFeatures 不可用）。
func NewRerankTools(reranker domain.Reranker, abTest *ABTest, fe FeatureProvider) *RerankTools {
	return &RerankTools{
		reranker: reranker,
		abTest:   abTest,
		fe:       fe,
	}
}

// SelfRerank 自研 rerank 工具（rerank.self_rerank）。
//
// 调用 reranker.Rerank 执行自研 rerank（含特征提取 + 模型选择）。
// req.Model 决定 rerank 路径（self/external/ab_test）。
//
// TODO 对接 trpc-agent-go tool 注册。
func (t *RerankTools) SelfRerank(ctx context.Context, req domain.RerankRequest) (domain.RerankResult, error) {
	if t.reranker == nil {
		return domain.RerankResult{}, fmt.Errorf("rerank tools: reranker not configured")
	}
	return t.reranker.Rerank(ctx, req)
}

// ExternalRerank 外部 rerank 工具（rerank.external_rerank）。
//
// 调用 abTest.ExternalRerank 执行外部 rerank（如 DashScope）。
// query 搜索查询；candidates 待重排候选；topK 返回数量。
//
// TODO 对接 trpc-agent-go tool 注册。
func (t *RerankTools) ExternalRerank(ctx context.Context, query string, candidates []domain.Candidate, topK int) ([]domain.Candidate, error) {
	if t.abTest == nil {
		return nil, fmt.Errorf("rerank tools: abTest not configured")
	}
	return t.abTest.ExternalRerank(ctx, query, candidates, topK)
}

// ExtractFeatures 特征提取工具（rerank.features）。
//
// 调用 fe.Extract 抽取单个候选的特征，返回特征名到特征值的映射。
// TODO 对接 trpc-agent-go tool 注册。
func (t *RerankTools) ExtractFeatures(ctx context.Context, candidate domain.Candidate) (map[string]float64, error) {
	if t.fe == nil {
		return nil, fmt.Errorf("rerank tools: feature extractor not configured")
	}
	return t.fe.Extract(ctx, candidate)
}

// ABTestStatus A/B 测试状态查询工具（rerank.ab_test）。
//
// 返回当前 A/B 指标快照（CTR/延迟/成本/胜率）。
// TODO 对接 trpc-agent-go tool 注册。
func (t *RerankTools) ABTestStatus(ctx context.Context) (ABMetrics, error) {
	if t.abTest == nil {
		return ABMetrics{}, fmt.Errorf("rerank tools: abTest not configured")
	}
	return t.abTest.GetMetrics(), nil
}
