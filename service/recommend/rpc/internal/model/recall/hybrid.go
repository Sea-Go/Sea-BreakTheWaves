package recall

import (
	"context"
	"errors"
	"sort"

	"golang.org/x/sync/errgroup"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件实现 HybridRecaller，混合召回编排器。
// 实现 domain.Recaller interface，并行调用多个召回器，融合去重后返回 topK。
// 召回零 LLM token，属于 fast / hybrid 路径。
//
// 并发方案：errgroup.Group 负责错误传播与 ctx 取消，
// 带缓冲 channel 信号量限制并发召回数（避免召回源过多打爆下游）。
// 注意：曾用 ants.Pool，但池满时 Submit 会在提交者协程内联执行任务，
// poolSize=1 时实际并发度可达 2（CI 时序用例捕获），故改为显式信号量。
// ============================================================================

// errInvalidPoolSize 在 poolSize < 1 时由 NewHybridRecaller 返回。
var errInvalidPoolSize = errors.New("recall: poolSize must be >= 1")

// HybridRecaller 混合召回器，并行调用多个召回器并融合去重。
//
// 并发：errgroup + 信号量控制并发度；任一召回器出错则取消其余。
// 融合：按 ArticleID 去重，分数取 max，按分数降序取 topK。
type HybridRecaller struct {
	recallers []domain.Recaller
	poolSize  int
	topK      int
}

// NewHybridRecaller 创建混合召回器。
// recallers 为待并行调用的召回器列表；poolSize 为最大并发召回数；
// topK 为默认召回数量上限（req.TopK>0 时优先使用 req.TopK）。
func NewHybridRecaller(recallers []domain.Recaller, poolSize, topK int) (*HybridRecaller, error) {
	if poolSize < 1 {
		return nil, errInvalidPoolSize
	}
	return &HybridRecaller{
		recallers: recallers,
		poolSize:  poolSize,
		topK:      topK,
	}, nil
}

// Recall 执行混合召回，实现 domain.Recaller.Recall。
//
// 流程：
//  1. 并行调用所有 recallers（errgroup + 信号量控制并发）；
//     任一召回器返回 error 则取消其余并向上传播。
//  2. 融合去重：按 ArticleID 去重，分数取 max，按分数降序排序。
//  3. 截断到 topK。
//
// 返回 RecallResult，Source 为 "hybrid"。
func (h *HybridRecaller) Recall(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
	topK := h.topK
	if req.TopK > 0 {
		topK = req.TopK
	}

	// 无召回器时直接返回空结果
	if len(h.recallers) == 0 {
		return domain.RecallResult{Candidates: nil, Source: "hybrid"}, nil
	}

	// 预分配结果槽位，各召回器按索引写入，无需加锁
	results := make([]domain.RecallResult, len(h.recallers))

	// errgroup 负责错误传播与 ctx 取消；信号量限制实际并发度
	sem := make(chan struct{}, h.poolSize)
	g, gctx := errgroup.WithContext(ctx)
	for i, r := range h.recallers {
		i, r := i, r // 捕获循环变量
		g.Go(func() error {
			select {
			case sem <- struct{}{}:
			case <-gctx.Done():
				return gctx.Err()
			}
			defer func() { <-sem }()
			res, err := r.Recall(gctx, req)
			results[i] = res
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return domain.RecallResult{}, err
	}

	// 融合去重
	merged := mergeAndDedup(results)

	// 截断到 topK
	if topK > 0 && len(merged) > topK {
		merged = merged[:topK]
	}

	return domain.RecallResult{
		Candidates: merged,
		Source:     "hybrid",
	}, nil
}

// mergeAndDedup 融合去重：按 ArticleID 去重，分数取 max，按分数降序排序。
//
// 二开扩展点：可替换为加权 sum（按 Source 加权）、归一化融合
// （如 RRF Reciprocal Rank Fusion）或其他业务融合策略。
func mergeAndDedup(results []domain.RecallResult) []domain.Candidate {
	merged := make(map[string]domain.Candidate, len(results)*4)
	var noID []domain.Candidate // 无 ArticleID 的候选直接保留，不参与去重
	for _, res := range results {
		for _, c := range res.Candidates {
			if c.ArticleID == "" {
				noID = append(noID, c)
				continue
			}
			if existing, ok := merged[c.ArticleID]; ok {
				// 分数取 max
				if c.Score > existing.Score {
					merged[c.ArticleID] = c
				}
			} else {
				merged[c.ArticleID] = c
			}
		}
	}

	out := make([]domain.Candidate, 0, len(merged)+len(noID))
	for _, c := range merged {
		out = append(out, c)
	}
	out = append(out, noID...)
	// 按分数降序排序
	sort.Slice(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}

// Name 返回召回器名称，实现 domain.Recaller.Name。
func (h *HybridRecaller) Name() string {
	return "hybrid"
}
