package recall

import (
	"context"

	"sea/service/recommend/rpc/internal/domain"
	"sea/service/recommend/rpc/internal/trpcagent/graph/legacy"
)

// ============================================================================
// 该文件实现 GraphRecaller，基于 Neo4j 知识图谱的多跳召回器。
// 实现 domain.Recaller interface，通过 domain.GraphQuerier 执行图谱查询，
// 将 GraphNode 转换为 Candidate。召回零 LLM token，属于 fast / hybrid 路径。
//
// 多跳模板选择（根据 req.Intent）：
//   - 无 Intent 或 Intent 无实体：走 graph.TemplateGraphRecall（用户→喜欢→文章→相似→文章），
//     通过 graph.RecallByGraph 调用，返回 []Candidate。
//   - 有 Intent 且含实体：走 graph.TemplateEntityToArticle（实体→文章），
//     通过 graph.QueryByCypher 调用，返回 []GraphNode 后转换为 []Candidate。
// ============================================================================

// GraphRecaller 图谱召回器，基于知识图谱多跳扩展召回候选文章。
// 依赖 domain.GraphQuerier interface（通常由 internal/graph.Client 注入），
// 支持按意图选择不同 Cypher 模板。
type GraphRecaller struct {
	graph domain.GraphQuerier
	topK  int
}

// NewGraphRecaller 创建图谱召回器。
// graph 为 GraphQuerier 实现（如 *graph.Client）；topK 为默认召回数量上限
// （req.TopK>0 时优先使用 req.TopK）。
func NewGraphRecaller(graph domain.GraphQuerier, topK int) *GraphRecaller {
	return &GraphRecaller{graph: graph, topK: topK}
}

// Recall 执行图谱召回，实现 domain.Recaller.Recall。
//
// 召回策略（多跳模板选择）：
//   - 无 Intent 或 Intent 无实体：走 TemplateGraphRecall，通过 graph.RecallByGraph
//     调用，返回 []Candidate。
//   - 有 Intent 且含实体：走 TemplateEntityToArticle，通过 graph.QueryByCypher
//     调用，返回 []GraphNode 后转换为 []Candidate（使用 Intent.Entities[0] 作为实体名）。
//
// 返回 RecallResult，Source 为 "graph"。
func (r *GraphRecaller) Recall(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
	topK := r.topK
	if req.TopK > 0 {
		topK = req.TopK
	}

	var cands []domain.Candidate
	// 根据意图选择 Cypher 模板：有实体意图走实体→文章，否则走默认图谱召回。
	if req.Intent != nil && len(req.Intent.Entities) > 0 {
		entityName := req.Intent.Entities[0].Name
		nodes, err := r.graph.QueryByCypher(ctx, graph.TemplateEntityToArticle, map[string]any{
			"entity_name": entityName,
			"topk":        topK,
		})
		if err != nil {
			return domain.RecallResult{}, err
		}
		cands = graph.NodesToCandidates(nodes)
	} else {
		graphReq := domain.GraphRecallRequest{
			UserKey: req.UserKey,
			Hop:     2,
			TopK:    topK,
			Pattern: "user_like_similar",
		}
		var err error
		cands, err = r.graph.RecallByGraph(ctx, graphReq)
		if err != nil {
			return domain.RecallResult{}, err
		}
	}

	// 截断到 topK
	if topK > 0 && len(cands) > topK {
		cands = cands[:topK]
	}
	return domain.RecallResult{
		Candidates: cands,
		Source:     "graph",
	}, nil
}

// Name 返回召回器名称，实现 domain.Recaller.Name。
func (r *GraphRecaller) Name() string {
	return "graph"
}
