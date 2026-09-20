package recall

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"sea/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件实现 ContentRecaller，基于向量（Milvus）+ BM25（Postgres）双路召回
// 并用 RRF（Reciprocal Rank Fusion）融合的召回器。实现 domain.Recaller
// interface，零 LLM token，属于 fast / hybrid 路径。
//
// 召回流程：
//  1. 从 req.Intent（实体/标签）或 req.Profile（动态兴趣主题）派生查询文本
//  2. 通过 Embedder 将查询文本向量化
//  3. 并行调用 VectorRepo.Search（向量召回）与 BM25Repo.Search（BM25 召回）
//  4. 用 RRF 公式融合两路结果：score = sum(1/(k+rank))，k=60
//  5. 按融合分数倒序排序并截断到 topK
//
// 外部依赖（Milvus/Postgres/Embedding 模型）全部以 interface 抽象，确保
// 离线编译通过；运行时若后端不可用则返回 error。
//
// 二开扩展点：实现 VectorRepo/BM25Repo/Embedder interface 注入自研后端；
// 或调整 RRF 的 k 参数（通过新增构造选项）改变融合偏好。
// ============================================================================

// rrfK RRF（Reciprocal Rank Fusion）常数 k，标准值 60。
// k 越大，排名靠后项的得分衰减越缓，融合越平滑。
const rrfK = 60

// VectorRepo 向量召回仓储抽象（通常由 Milvus 实现）。
//
// 二开说明：实现该 interface 注入自研向量库（如 Faiss/自建 ANN），
// 通过依赖注入替换默认 Milvus 实现。
type VectorRepo interface {
	// Search 基于查询向量召回候选文章。
	// query 查询向量；topK 返回数量上限。
	Search(ctx context.Context, query []float32, topK int) ([]domain.Candidate, error)
}

// BM25Repo BM25 召回仓储抽象（通常由 Postgres 全文检索实现）。
//
// 二开说明：实现该 interface 注入自研倒排索引（如 Elasticsearch），
// 通过依赖注入替换默认 Postgres 实现。
type BM25Repo interface {
	// Search 基于查询文本做 BM25 召回。
	// query 查询文本；topK 返回数量上限。
	Search(ctx context.Context, query string, topK int) ([]domain.Candidate, error)
}

// Embedder 文本向量化抽象，将查询文本转为向量供 VectorRepo 使用。
//
// 二开说明：实现该 interface 接入自研 Embedding 模型（如 BGE/自研双塔），
// 通过依赖注入替换默认实现。
type Embedder interface {
	// Embed 将文本转为向量。
	Embed(ctx context.Context, text string) ([]float32, error)
}

// ContentRecaller 内容召回器，并行调用向量与 BM25 召回并用 RRF 融合。
type ContentRecaller struct {
	vecRepo  VectorRepo
	bm25Repo BM25Repo
	embedder Embedder
	topK     int
}

// NewContentRecaller 创建内容召回器。
// vecRepo 向量召回仓储（Milvus）；bm25Repo BM25 召回仓储（Postgres）；
// embedder 文本向量化器；topK 默认召回数量上限（req.TopK>0 时优先使用 req.TopK）。
func NewContentRecaller(vecRepo VectorRepo, bm25Repo BM25Repo, embedder Embedder, topK int) *ContentRecaller {
	return &ContentRecaller{
		vecRepo:  vecRepo,
		bm25Repo: bm25Repo,
		embedder: embedder,
		topK:     topK,
	}
}

// Recall 执行内容召回，实现 domain.Recaller.Recall。
//
// 流程：派生查询文本 → 向量化 → 并行向量 + BM25 召回 → RRF 融合 → 截断。
// 若向量与 BM25 均失败则返回聚合错误；若仅一方失败则用另一方结果。
// 返回 RecallResult，Source 为 "content"。
func (r *ContentRecaller) Recall(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
	topK := r.topK
	if req.TopK > 0 {
		topK = req.TopK
	}

	query := deriveQuery(req)

	// 向量化查询文本；embedder 缺失或失败则向量召回不可用。
	var vec []float32
	var vecErr error
	if r.embedder != nil {
		vec, vecErr = r.embedder.Embed(ctx, query)
	} else {
		vecErr = fmt.Errorf("embedder 未注入")
	}

	// 并行执行向量与 BM25 召回。
	var (
		vecCands []domain.Candidate
		bmCands  []domain.Candidate
		vecSErr  error
		bmErr    error
		wg       sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		if vecErr != nil {
			vecSErr = vecErr
			return
		}
		vecCands, vecSErr = r.vecRepo.Search(ctx, vec, topK)
	}()
	go func() {
		defer wg.Done()
		bmCands, bmErr = r.bm25Repo.Search(ctx, query, topK)
	}()
	wg.Wait()

	// 双路均失败则返回错误。
	if vecSErr != nil && bmErr != nil {
		return domain.RecallResult{}, fmt.Errorf("content recaller: 向量与 BM25 召回均失败: vec=%v, bm25=%v", vecSErr, bmErr)
	}

	// RRF 融合可用的召回列表。
	var lists [][]domain.Candidate
	if vecSErr == nil {
		lists = append(lists, vecCands)
	}
	if bmErr == nil {
		lists = append(lists, bmCands)
	}
	fused := fuseRRF(lists, rrfK)

	// 截断到 topK。
	if topK > 0 && len(fused) > topK {
		fused = fused[:topK]
	}
	return domain.RecallResult{
		Candidates: fused,
		Source:     string(domain.RecallSourceContent),
	}, nil
}

// Name 返回召回器名称，实现 domain.Recaller.Name。
func (r *ContentRecaller) Name() string {
	return string(domain.RecallSourceContent)
}

// fuseRRF 用 Reciprocal Rank Fusion 公式融合多路召回结果。
// 公式：score(d) = sum( 1/(k+rank) )，rank 为文档在该路列表中的排名（1 起始）。
// k 为平滑常数（标准 60）。融合后按分数倒序排序。
func fuseRRF(lists [][]domain.Candidate, k int) []domain.Candidate {
	type entry struct {
		cand  domain.Candidate
		score float64
	}
	m := make(map[string]*entry, 0)
	for _, list := range lists {
		for i, c := range list {
			if c.ArticleID == "" {
				continue
			}
			e, ok := m[c.ArticleID]
			if !ok {
				e = &entry{cand: c}
				m[c.ArticleID] = e
			}
			// rank 从 1 开始（i 从 0 开始，故 rank = i+1）。
			e.score += 1.0 / float64(k+i+1)
		}
	}
	result := make([]domain.Candidate, 0, len(m))
	for _, e := range m {
		e.cand.Score = e.score
		e.cand.Source = string(domain.RecallSourceContent)
		result = append(result, e.cand)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Score > result[j].Score
	})
	return result
}

// deriveQuery 从召回请求派生查询文本，供 BM25 与向量化使用。
// 优先级：Intent 实体名 > Intent 标签 > 动态画像主题 > 用户 ID 兜底。
func deriveQuery(req domain.RecallRequest) string {
	// Intent 实体名拼接。
	if req.Intent != nil && len(req.Intent.Entities) > 0 {
		names := make([]string, 0, len(req.Intent.Entities))
		for _, e := range req.Intent.Entities {
			if e.Name != "" {
				names = append(names, e.Name)
			}
		}
		if len(names) > 0 {
			return strings.Join(names, " ")
		}
	}
	// Intent 标签。
	if req.Intent != nil && req.Intent.Label != "" {
		return req.Intent.Label
	}
	// 动态画像兴趣主题。
	if req.Profile != nil && req.Profile.Dynamic != nil && len(req.Profile.Dynamic.Topics) > 0 {
		return strings.Join(req.Profile.Dynamic.Topics, " ")
	}
	// 兜底：用户 ID。
	return req.UserKey.UserID
}
