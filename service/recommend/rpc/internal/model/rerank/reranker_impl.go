package rerank

import (
	"context"
	"fmt"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件实现 SelfReranker，自研 rerank 实现 domain.Reranker interface（Task 8.8）。
//
// 核心能力：
//   - 实现 domain.Reranker（Rerank + Name）
//   - 根据 req.Model 选择 self/external/ab_test 路径
//   - 特征提取（通过 FeatureProvider interface 抽象）
//   - 调用 RerankServer（自研 rerank 服务）或 ABTest（A/B 分桶）
//
// 依赖说明：
//   - RerankServer：抽象 internal/rerank/server.go 的 Server 类型（由 Task 8.4 并行创建）。
//     并行 agent 创建 *Server 后，只需 *Server 实现 RerankServer interface 即可注入。
//   - FeatureProvider：抽象 internal/rerank/features.go 的 FeatureExtractor 类型（由 Task 8.1
//     并行创建）。并行 agent 创建 *FeatureExtractor 后，只需实现 FeatureProvider interface
//     即可注入。interface 名刻意与 struct 名不同，避免同包冲突。
//
// 二开扩展点：
//   - 特征工程：实现 FeatureProvider interface 注入自定义特征
//   - rerank 服务：实现 RerankServer interface 注入自研/外部 rerank 后端
//   - 模型切换：通过 req.Model 配置 self/external/ab_test
// ============================================================================

// RerankServer 自研 rerank 服务抽象。
//
// 抽象 internal/rerank/server.go 的 Server 类型（由 Task 8.4 并行创建）。
// 二开：实现该 interface 注入自研 rerank 服务（Cross-encoder/Two-tower/LambdaMART）。
type RerankServer interface {
	// Rerank 执行自研 rerank。
	Rerank(ctx context.Context, req domain.RerankRequest) (domain.RerankResult, error)
}

// FeatureProvider 特征抽取器抽象。
//
// 抽象 internal/rerank/features.go 的 FeatureExtractor 类型（由 Task 8.1 并行创建）。
// interface 名刻意与 struct 名（FeatureExtractor）不同，避免同包类型名冲突。
// 二开：实现该 interface 注入自定义特征工程（如 Cross-encoder 特征/Two-tower 特征）。
type FeatureProvider interface {
	// Extract 抽取单个候选的特征，返回特征名到特征值的映射。
	Extract(ctx context.Context, candidate domain.Candidate) (map[string]float64, error)
}

// 模型选择常量。
const (
	// ModelSelf 自研 rerank（调 RerankServer.Rerank）。
	ModelSelf = "self"
	// ModelExternal 外部 rerank（调 ABTest.ExternalRerank）。
	ModelExternal = "external"
	// ModelABTest A/B 测试（调 ABTest.Rerank，按 userID 分桶）。
	ModelABTest = "ab_test"
)

// 编译期断言：*SelfReranker 实现 domain.Reranker interface。
var _ domain.Reranker = (*SelfReranker)(nil)

// SelfReranker 自研 rerank 实现 domain.Reranker。
//
// 根据 req.Model 选择 rerank 路径：
//   - "self"：调 server.Rerank（自研 rerank 服务）
//   - "external"：调 abTest.ExternalRerank（外部 rerank，如 DashScope）
//   - "ab_test"：调 abTest.Rerank（按 userID hash 分桶 self/external）
//   - ""（空）：默认 "self" 路径
//
// 二开扩展点：
//   - 特征工程：通过 FeatureProvider interface 注入自定义特征
//   - 模型切换：通过 req.Model 配置 rerank 路径
type SelfReranker struct {
	// server 自研 rerank 服务（RerankServer 实现）。
	server RerankServer
	// fe 特征抽取器（FeatureProvider 实现，可为 nil 跳过特征提取）。
	fe FeatureProvider
	// abTest A/B 测试（提供 external/ab_test 路径，可为 nil）。
	abTest *ABTest
}

// NewSelfReranker 创建自研 reranker。
//
// server 自研 rerank 服务；fe 特征抽取器（可为 nil，跳过特征提取）；
// abTest A/B 测试（可为 nil，"external"/"ab_test" 路径不可用）。
func NewSelfReranker(server RerankServer, fe FeatureProvider, abTest *ABTest) *SelfReranker {
	return &SelfReranker{
		server: server,
		fe:     fe,
		abTest: abTest,
	}
}

// Rerank 执行重排，实现 domain.Reranker.Rerank。
//
// 流程：
//  1. 特征提取（若 fe != nil）：对每个候选抽取特征并合并到 Candidate.Scores
//  2. 根据 req.Model 选择路径：
//     - "self" 或 "" → server.Rerank
//     - "external"   → abTest.ExternalRerank（转 RerankResult）
//     - "ab_test"    → abTest.Rerank（按 userID 分桶）
//  3. 返回 RerankResult（候选 Score 字段即 rerank_scores）
func (r *SelfReranker) Rerank(ctx context.Context, req domain.RerankRequest) (domain.RerankResult, error) {
	// 步骤 1：特征提取（若 fe 配置且候选非空）。
	if r.fe != nil && len(req.Candidates) > 0 {
		enriched := make([]domain.Candidate, len(req.Candidates))
		copy(enriched, req.Candidates)
		for i := range enriched {
			feats, err := r.fe.Extract(ctx, enriched[i])
			if err != nil {
				return domain.RerankResult{}, fmt.Errorf("self rerank: extract features failed for %s: %w", enriched[i].ArticleID, err)
			}
			if len(feats) == 0 {
				continue
			}
			if enriched[i].Scores == nil {
				enriched[i].Scores = make(map[string]float64)
			}
			for k, v := range feats {
				enriched[i].Scores[k] = v
			}
		}
		req.Candidates = enriched
	}

	// 步骤 2：根据 req.Model 选择 rerank 路径。
	switch req.Model {
	case ModelExternal:
		if r.abTest == nil {
			return domain.RerankResult{}, fmt.Errorf("self rerank: abTest not configured for external model")
		}
		cands, err := r.abTest.ExternalRerank(ctx, req.Query, req.Candidates, req.TopK)
		if err != nil {
			return domain.RerankResult{}, err
		}
		return domain.RerankResult{Candidates: cands, ModelUsed: ModelExternal}, nil

	case ModelABTest:
		if r.abTest == nil {
			return domain.RerankResult{}, fmt.Errorf("self rerank: abTest not configured for ab_test model")
		}
		return r.abTest.Rerank(ctx, req)

	case ModelSelf, "":
		if r.server == nil {
			return domain.RerankResult{}, fmt.Errorf("self rerank: server not configured")
		}
		return r.server.Rerank(ctx, req)

	default:
		return domain.RerankResult{}, fmt.Errorf("self rerank: unknown model %q", req.Model)
	}
}

// Name 返回重排器名称，实现 domain.Reranker.Name。
func (r *SelfReranker) Name() string {
	return "self_rerank"
}
