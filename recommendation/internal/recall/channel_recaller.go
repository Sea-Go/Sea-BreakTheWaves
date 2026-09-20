package recall

import (
	"context"

	"sea/internal/domain"
)

// ============================================================================
// 该文件实现 ChannelRecaller，基于 IP 频道独立召回池的召回器。
// 实现 domain.Recaller interface，从频道绑定的独立候选池读取候选文章。
// 召回零 LLM token，属于 fast / hybrid 路径。
//
// 频道分桶：每个 IP 频道有独立召回池（由 ChannelManager 维护水位），
// 本召回器只读取，不维护池。频道隔离确保不同频道间候选不交叉。
// ============================================================================

// ChannelPoolRepo 频道独立召回池仓储 interface。
//
// 职责：按频道名读取该频道绑定的独立召回池候选。
//
// 二开扩展点：实现该 interface 替换频道池数据源
// （如 Redis/Milvus/Neo4j 独立池或离线预计算池）。
type ChannelPoolRepo interface {
	// GetChannelPool 获取指定频道的独立召回池候选。
	// ctx 上下文；channel 频道名；topK 返回数量上限。
	// 返回候选列表与 error。
	GetChannelPool(ctx context.Context, channel string, topK int) ([]domain.Candidate, error)
}

// ChannelRecaller 频道召回器，基于频道独立召回池召回候选文章。
// 依赖 ChannelPoolRepo interface（频道池仓储），实现 domain.Recaller。
//
// 频道分桶：每个频道有独立候选池，由 ChannelManager 维护，本召回器只读取。
type ChannelRecaller struct {
	poolRepo ChannelPoolRepo
	topK     int
}

// NewChannelRecaller 创建频道召回器。
// poolRepo 为频道池仓储实现；topK 为默认召回数量上限
// （req.TopK>0 时优先使用 req.TopK）。
func NewChannelRecaller(poolRepo ChannelPoolRepo, topK int) *ChannelRecaller {
	return &ChannelRecaller{poolRepo: poolRepo, topK: topK}
}

// Recall 执行频道召回，实现 domain.Recaller.Recall。
//
// 召回策略：根据 req.Channel 取该频道的独立召回池候选，
// 标记来源为 "channel"，截断到 topK。
//
// 返回 RecallResult，Source 为 "channel"。
func (r *ChannelRecaller) Recall(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
	topK := r.topK
	if req.TopK > 0 {
		topK = req.TopK
	}

	cands, err := r.poolRepo.GetChannelPool(ctx, req.Channel, topK)
	if err != nil {
		return domain.RecallResult{}, err
	}

	// 标记来源：仓储返回的候选若未设置 Source，则补 "channel"
	for i := range cands {
		if cands[i].Source == "" {
			cands[i].Source = "channel"
		}
	}

	// 截断到 topK
	if topK > 0 && len(cands) > topK {
		cands = cands[:topK]
	}

	return domain.RecallResult{
		Candidates: cands,
		Source:     "channel",
	}, nil
}

// Name 返回召回器名称，实现 domain.Recaller.Name。
func (r *ChannelRecaller) Name() string {
	return "channel"
}
