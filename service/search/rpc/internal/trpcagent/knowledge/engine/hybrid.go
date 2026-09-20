// ============================================================================
// 该文件实现 HybridSearcher，语义混合检索器。
// 实现 domain.Searcher interface（Search(ctx, SearchQuery) (SearchResult, error)），
// 三路并行检索（Milvus dense + Milvus sparse + BM25）后用 RRF 融合去重。
//
// 职责：
//   - 并行调用 VectorRepo（dense/sparse）与 BM25Repo，元数据过滤透传给底层 repo
//   - RRF（Reciprocal Rank Fusion）融合：score = sum(weight * 1/(k+rank))，默认 k=60
//   - 可选 RerankFieldFunc 对融合后命中做二次打分
//   - 任一后端失败不影响其余（弹性降级）；全部失败才返回错误
//
// 并发方案：errgroup.Group + ants.Pool 限制并发度（与 internal/recall/hybrid.go 一致）。
//
// 二开扩展点：
//   - 替换 VectorRepo / BM25Repo 实现接入自研向量库 / ES
//   - 通过 WithHybridWeights 调整三路权重，WithRRFK 调整融合参数
//   - 注入 WithRerankField 实现业务二次打分（如 freshness/quality boost）
// ============================================================================

package search

import (
	"context"
	"errors"
	"sort"

	"github.com/panjf2000/ants/v2"
	"golang.org/x/sync/errgroup"

	"sea/service/search/rpc/internal/domain"
)

// VectorRepo 向量检索 repo（Milvus dense + sparse），由业务实现注入。
// 禁止在本包直接 import milvus，全部通过该 interface 抽象。
type VectorRepo interface {
	// SearchDense 稠密向量检索。
	SearchDense(ctx context.Context, query string, topK int, filter domain.SearchFilter) ([]domain.SearchHit, error)
	// SearchSparse 稀疏向量检索。
	SearchSparse(ctx context.Context, query string, topK int, filter domain.SearchFilter) ([]domain.SearchHit, error)
}

// BM25Repo 关键词检索 repo（PostgreSQL BM25），由业务实现注入。
type BM25Repo interface {
	// Search 关键词检索。
	Search(ctx context.Context, query string, topK int, filter domain.SearchFilter) ([]domain.SearchHit, error)
}

// RerankFieldFunc 可选的 rerank field 二次打分函数，对融合后单条命中返回附加分。
type RerankFieldFunc func(hit domain.SearchHit) float64

// HybridSearcher 语义混合检索器，实现 domain.Searcher interface。
//
// 三路并行：VectorRepo.SearchDense / SearchSparse / BM25Repo.Search。
// 融合策略：RRF，score = sum(weight * 1/(k+rank))，rank 为 1-based。
// 弹性：单路失败不阻断整体；全部失败返回聚合错误。
type HybridSearcher struct {
	vector      VectorRepo
	bm25        BM25Repo
	pool        *ants.Pool
	poolSize    int
	rrfK        int
	rerankField RerankFieldFunc
	wDense      float64
	wSparse     float64
	wBm25       float64
	defaultTopK int
}

// HybridOption HybridSearcher 构造选项。
type HybridOption func(*HybridSearcher)

// WithRRFK 设置 RRF 融合参数 k（默认 60）。
func WithRRFK(k int) HybridOption {
	return func(h *HybridSearcher) {
		if k > 0 {
			h.rrfK = k
		}
	}
}

// WithRerankField 注入 rerank field 二次打分函数。
func WithRerankField(fn RerankFieldFunc) HybridOption {
	return func(h *HybridSearcher) {
		h.rerankField = fn
	}
}

// WithHybridWeights 设置三路检索权重（dense/sparse/bm25，默认均 1.0）。
//
// 说明：本包内 personalize 组件亦需权重配置，为避免同名函数冲突，
// hybrid 的权重配置命名为 WithHybridWeights。
func WithHybridWeights(dense, sparse, bm25 float64) HybridOption {
	return func(h *HybridSearcher) {
		if dense >= 0 {
			h.wDense = dense
		}
		if sparse >= 0 {
			h.wSparse = sparse
		}
		if bm25 >= 0 {
			h.wBm25 = bm25
		}
	}
}

// WithPoolSize 设置 ants 并发池大小（默认 16）。
func WithPoolSize(size int) HybridOption {
	return func(h *HybridSearcher) {
		if size > 0 {
			h.poolSize = size
		}
	}
}

// WithDefaultTopK 设置默认 TopK（SearchQuery.TopK<=0 时使用，默认 50）。
func WithDefaultTopK(topK int) HybridOption {
	return func(h *HybridSearcher) {
		if topK > 0 {
			h.defaultTopK = topK
		}
	}
}

// NewHybridSearcher 创建语义混合检索器。
// vector / bm25 均可为 nil（nil 的后端被跳过）；opts 为构造选项。
// 返回具体类型 *HybridSearcher，便于二开替换。
func NewHybridSearcher(vector VectorRepo, bm25 BM25Repo, opts ...HybridOption) *HybridSearcher {
	h := &HybridSearcher{
		vector:      vector,
		bm25:        bm25,
		poolSize:    16,
		rrfK:        60,
		wDense:      1.0,
		wSparse:     1.0,
		wBm25:       1.0,
		defaultTopK: 50,
	}
	for _, o := range opts {
		o(h)
	}
	// ants 池创建失败时降级为直接 goroutine（pool=nil），保证可运行
	if pool, err := ants.NewPool(h.poolSize); err == nil {
		h.pool = pool
	}
	return h
}

// Search 执行混合检索，实现 domain.Searcher.Search。
//
// 流程：
//  1. 三路并行检索（errgroup + ants Pool 控制并发）；单路失败仅记录，不取消其余。
//  2. RRF 融合去重：按 ArticleID 聚合，score = sum(weight * 1/(k+rank))。
//  3. 若注入了 RerankFieldFunc，对融合后命中追加二次打分。
//  4. 按分数降序截断到 TopK。
//  5. 若全部已调用后端均失败，返回聚合错误。
func (h *HybridSearcher) Search(ctx context.Context, q domain.SearchQuery) (domain.SearchResult, error) {
	topK := q.TopK
	if topK <= 0 {
		topK = h.defaultTopK
	}

	lists, errs := h.parallelSearch(ctx, q.Query, topK, q.Filter)

	// 统计调用数与失败数
	called, failed := 0, 0
	if h.vector != nil {
		called += 2
		if errs[0] != nil {
			failed++
		}
		if errs[1] != nil {
			failed++
		}
	}
	if h.bm25 != nil {
		called++
		if errs[2] != nil {
			failed++
		}
	}
	// 全部失败才返回错误
	if called > 0 && failed == called {
		return domain.SearchResult{}, joinErrors(errs)
	}

	// 收集成功路（list 非空且无错误）及其权重
	allWeights := []float64{h.wDense, h.wSparse, h.wBm25}
	var lists2 [][]domain.SearchHit
	var weights2 []float64
	for i, l := range lists {
		if errs[i] == nil && len(l) > 0 {
			lists2 = append(lists2, l)
			weights2 = append(weights2, allWeights[i])
		}
	}

	fused := rrfFuse(lists2, weights2, h.rrfK)

	// 二次打分
	if h.rerankField != nil {
		for i := range fused {
			fused[i].Score += h.rerankField(fused[i])
		}
	}

	// 降序排序
	sort.Slice(fused, func(i, j int) bool {
		return fused[i].Score > fused[j].Score
	})

	// 截断到 topK
	if topK > 0 && len(fused) > topK {
		fused = fused[:topK]
	}

	return domain.SearchResult{Hits: fused}, nil
}

// Name 返回检索器名称。
func (h *HybridSearcher) Name() string {
	return "hybrid"
}

// parallelSearch 三路并行检索，返回 3 路结果与错误（按索引：0=dense 1=sparse 2=bm25）。
// 单路失败不取消其余（g.Go 永远返回 nil，错误存入 errs）。
func (h *HybridSearcher) parallelSearch(ctx context.Context, query string, topK int, filter domain.SearchFilter) ([][]domain.SearchHit, []error) {
	lists := make([][]domain.SearchHit, 3)
	errs := make([]error, 3)

	g, gctx := errgroup.WithContext(ctx)
	if h.vector != nil {
		h.runTask(g, gctx, 0, func(c context.Context) ([]domain.SearchHit, error) {
			return h.vector.SearchDense(c, query, topK, filter)
		}, lists, errs)
		h.runTask(g, gctx, 1, func(c context.Context) ([]domain.SearchHit, error) {
			return h.vector.SearchSparse(c, query, topK, filter)
		}, lists, errs)
	}
	if h.bm25 != nil {
		h.runTask(g, gctx, 2, func(c context.Context) ([]domain.SearchHit, error) {
			return h.bm25.Search(c, query, topK, filter)
		}, lists, errs)
	}
	_ = g.Wait()
	return lists, errs
}

// runTask 提交单路检索任务到 ants Pool（pool 为 nil 时直接 goroutine），
// 结果写入 lists[idx]，错误写入 errs[idx]，g.Go 永远返回 nil（不取消兄弟任务）。
func (h *HybridSearcher) runTask(g *errgroup.Group, ctx context.Context, idx int,
	fn func(context.Context) ([]domain.SearchHit, error),
	lists [][]domain.SearchHit, errs []error) {
	g.Go(func() error {
		done := make(chan error, 1)
		run := func() {
			res, err := fn(ctx)
			lists[idx] = res
			done <- err
		}
		if h.pool == nil {
			go run()
		} else if err := h.pool.Submit(run); err != nil {
			// 池提交失败（如池已关闭），降级直接执行
			run()
		}
		errs[idx] = <-done
		return nil
	})
}

// rrfFuse RRF 融合去重。
// lists 为各路命中（已按各自分数降序）；weights 与 lists 一一对应（默认 1.0）；
// k 为 RRF 参数。返回按融合分数降序的命中列表。
//
// score(article) = sum_i( weight_i * 1/(k + rank_i) )，rank_i 为 1-based。
// 同一 ArticleID 在多路出现 → source 标记为 "hybrid"；单路出现 → 保留原 source。
func rrfFuse(lists [][]domain.SearchHit, weights []float64, k int) []domain.SearchHit {
	type accum struct {
		score   float64
		source  string
		sources map[string]struct{}
	}
	acc := make(map[string]*accum, 64)
	for li, list := range lists {
		w := 1.0
		if li < len(weights) {
			w = weights[li]
		}
		for rank, hit := range list {
			if hit.ArticleID == "" {
				continue
			}
			a, ok := acc[hit.ArticleID]
			if !ok {
				a = &accum{sources: make(map[string]struct{})}
				acc[hit.ArticleID] = a
				a.source = hit.Source
			}
			a.score += w / float64(k+rank+1) // rank 0-based → +1 转 1-based
			if hit.Source != "" {
				a.sources[hit.Source] = struct{}{}
			}
		}
	}
	out := make([]domain.SearchHit, 0, len(acc))
	for id, a := range acc {
		src := a.source
		if len(a.sources) > 1 {
			src = "hybrid"
		}
		out = append(out, domain.SearchHit{ArticleID: id, Score: a.score, Source: src})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}

// joinErrors 聚合非 nil 错误。
func joinErrors(errs []error) error {
	var es []error
	for _, e := range errs {
		if e != nil {
			es = append(es, e)
		}
	}
	if len(es) == 0 {
		return nil
	}
	return errors.Join(es...)
}
