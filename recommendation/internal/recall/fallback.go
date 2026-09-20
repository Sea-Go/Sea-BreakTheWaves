package recall

import (
	"context"

	"sea/internal/domain"
)

// ============================================================================
// 该文件实现 Fallback 兜底策略。
//
// 当召回候选数量不足阈值时，从热门文章 + 频道默认池补足到 topK，
// 保证推荐流不空窗。兜底零 LLM token，属于 fast 路径的保底机制。
//
// 依赖两个 interface（供二开注入）：
//   - HotRepo: 热门文章仓库，ListHot 返回全局热门候选
//   - ChannelDefaultPool: 频道默认池，GetDefault 返回频道默认候选
//
// 二开扩展点：
//   - 自定义热门源：实现 HotRepo 接入 Redis ZSet / MySQL 等存储
//   - 自定义频道默认池：实现 ChannelDefaultPool 接入编辑推荐 / 运营配置
//   - 阈值调整：通过 threshold 参数控制兜底触发时机
// ============================================================================

// HotRepo 热门文章仓库 interface。
//
// ListHot 返回全局热门候选（按热度降序），topK 限制返回数量。
//
// 二开扩展点：业务方可实现该 interface，接入 Redis ZSet / MySQL /
// 离线计算的热门榜，数据源对 Fallback 透明。
type HotRepo interface {
	// ListHot 列出热门候选。
	// ctx 上下文；topK 返回数量上限。
	// 返回热门候选列表与 error。
	ListHot(ctx context.Context, topK int) ([]domain.Candidate, error)
}

// ChannelDefaultPool 频道默认池 interface。
//
// GetDefault 返回指定频道的默认候选（编辑推荐 / 运营配置），
// topK 限制返回数量。
//
// 二开扩展点：业务方可实现该 interface，接入运营配置中心 /
// 编辑推荐系统，按频道提供保底内容。
type ChannelDefaultPool interface {
	// GetDefault 获取频道默认候选。
	// ctx 上下文；channel 频道名；topK 返回数量上限。
	// 返回频道默认候选列表与 error。
	GetDefault(ctx context.Context, channel string, topK int) ([]domain.Candidate, error)
}

// Fallback 兜底策略，保证推荐流候选数量充足。
//
// 当候选数量低于 threshold 时，从频道默认池 + 热门文章补足到 topK，
// 按 ArticleID 去重，避免重复推荐。
//
// 二开扩展点：通过 NewFallback 注入 HotRepo 与 ChannelDefaultPool 实现，
// threshold 控制兜底触发时机（建议设为 topK 的 50%-80%）。
type Fallback struct {
	hotRepo            HotRepo
	channelDefaultPool ChannelDefaultPool
	threshold          int
}

// NewFallback 创建兜底策略。
//
// hotRepo 热门文章仓库；channelDefaultPool 频道默认池；
// threshold 触发兜底的最小候选数量阈值（候选数 < threshold 时触发补足）。
func NewFallback(hotRepo HotRepo, channelDefaultPool ChannelDefaultPool, threshold int) *Fallback {
	return &Fallback{
		hotRepo:            hotRepo,
		channelDefaultPool: channelDefaultPool,
		threshold:          threshold,
	}
}

// Ensure 确保候选数量充足，不足时从兜底源补足。
//
// 策略：
//  1. 若 len(candidates) >= threshold，直接返回（候选充足，无需兜底）
//  2. 若 len(candidates) >= topK，直接返回（已达目标数量，无需补足）
//  3. 否则从频道默认池补足（更精准），仍不足再从热门补足（更宽泛）
//  4. 全程按 ArticleID 去重，避免重复
//
// channel 为当前频道名（可空，频道默认池可能返回空）；topK 为目标数量。
// 返回补足后的候选列表与 error。
func (f *Fallback) Ensure(ctx context.Context, candidates []domain.Candidate, channel string, topK int) ([]domain.Candidate, error) {
	// 候选数达到阈值，直接返回
	if len(candidates) >= f.threshold {
		return candidates, nil
	}
	// 候选数已达 topK，无需补足
	if len(candidates) >= topK {
		return candidates, nil
	}

	// 去重集合，记录已有文章 ID
	seen := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		seen[c.ArticleID] = struct{}{}
	}

	result := make([]domain.Candidate, 0, topK)
	result = append(result, candidates...)

	// 第一优先级：从频道默认池补足（频道内更精准的兜底）
	need := topK - len(result)
	if need > 0 {
		defaults, err := f.channelDefaultPool.GetDefault(ctx, channel, need)
		if err != nil {
			return nil, err
		}
		for _, c := range defaults {
			if _, ok := seen[c.ArticleID]; ok {
				continue
			}
			seen[c.ArticleID] = struct{}{}
			result = append(result, c)
			if len(result) >= topK {
				return result, nil
			}
		}
	}

	// 第二优先级：从热门补足（全局兜底，覆盖面广）
	need = topK - len(result)
	if need > 0 {
		hot, err := f.hotRepo.ListHot(ctx, need)
		if err != nil {
			return nil, err
		}
		for _, c := range hot {
			if _, ok := seen[c.ArticleID]; ok {
				continue
			}
			seen[c.ArticleID] = struct{}{}
			result = append(result, c)
			if len(result) >= topK {
				break
			}
		}
	}

	return result, nil
}
