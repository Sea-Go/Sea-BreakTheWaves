package profile

import (
	"math"
	"sort"
	"time"

	"sea/internal/domain"
)

// ============================================================================
// 该文件实现趋势检测（Trending Detection）。
//
// 对比 ShortTerm（24h）与 LongTerm（7d 基线），通过 z-score 检测突增兴趣。
// z-score = (short_weight - long_mean) / long_std，若 z-score > 2（2σ 突增）
// 则标记为 Trending。
//
// 核心函数：
//   - DetectTrending: 趋势检测主入口
//   - mean / std: 辅助统计函数
//
// 二开扩展点：
//   - 自定义阈值：替换 trendingZThreshold 常量（如改为 3σ 减少误报）
//   - 自定义基线：替换 LongTerm 基线为更长时间窗口
//   - 实现 TrendingDetector interface（见 doc.go）注入自定义检测策略
// ============================================================================

// trendingZThreshold 趋势检测 z-score 阈值（2σ 突增）。
// 二开：业务方可调整该常量或通过参数注入。
const trendingZThreshold = 2.0

// trendingDegenerateAbsThreshold 退化判定（long_std=0）时的绝对差阈值。
// 当长期基线权重完全相同（std=0）时，z-score 公式失效（除零），
// 退化为绝对差判定：仅当 short_weight - long_mean > 该阈值时才视为突增。
// 二开：业务方可按数据分布调整（如改为相对比例阈值）。
const trendingDegenerateAbsThreshold = 0.1

// DetectTrending 趋势检测，对比 ShortTerm 与 LongTerm 基线。
//
// 算法：
//  1. 计算 LongTerm 所有兴趣权重的均值（long_mean）与标准差（long_std）作为基线
//  2. 对每个 ShortTerm 兴趣，计算 z-score = (short_weight - long_mean) / long_std
//  3. 若 z-score > 2（2σ 突增），加入 Trending 列表
//
// shortTerm 短期兴趣（24h）；longTerm 长期基线（7d）；now 当前时间（用于更新
// LastReinforcedAt）。返回突增的兴趣列表（按 Weight 降序）。
//
// 边界处理：
//   - long_std 为 0（所有长期兴趣权重相同或只有一个）时，退化为绝对差判定
//     （short_weight > long_mean 即视为突增）
//   - shortTerm 为空时返回 nil
func DetectTrending(shortTerm []domain.InterestEntry, longTerm []domain.InterestEntry, now time.Time) []domain.InterestEntry {
	if len(shortTerm) == 0 {
		return nil
	}
	// 计算 LongTerm 基线统计量（整体均值与标准差）
	longWeights := make([]float64, 0, len(longTerm))
	for _, it := range longTerm {
		longWeights = append(longWeights, it.Weight)
	}
	baseMean := mean(longWeights)
	baseStd := std(longWeights, baseMean)

	var trending []domain.InterestEntry
	for _, st := range shortTerm {
		z := 0.0
		if baseStd > 0 {
			z = (st.Weight - baseMean) / baseStd
		} else if st.Weight > baseMean+trendingDegenerateAbsThreshold {
			// 标准差为 0（长期权重全相同或仅一个），退化为绝对差判定：
			// 仅当短期权重显著超出基线均值（绝对差 > 阈值）时才视为突增，
			// 避免微小波动被误判为趋势。
			z = trendingZThreshold + 1
		}
		if z > trendingZThreshold {
			// 标记为 Trending，复制并更新时间戳
			it := st
			it.LastReinforcedAt = now
			trending = append(trending, it)
		}
	}

	// 按 Weight 降序稳定排序
	sort.SliceStable(trending, func(i, j int) bool {
		return trending[i].Weight > trending[j].Weight
	})
	return trending
}

// mean 计算浮点数切片的均值。
// 空切片返回 0。
func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// std 计算浮点数切片的总体标准差（除以 N）。
// m 为预先计算的均值；空切片返回 0。
func std(xs []float64, m float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sumSq float64
	for _, x := range xs {
		d := x - m
		sumSq += d * d
	}
	return math.Sqrt(sumSq / float64(len(xs)))
}
