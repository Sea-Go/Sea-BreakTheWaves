package profile

import (
	"sort"
	"time"

	"sea/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件实现周期模式（Periodic Pattern）的兴趣更新与 TopK 查询。
//
// 周期模式按时间槽位（slot，格式 "HH-HH" 如 "09-12"）维护用户兴趣，
// 用于刻画"工作日早晨看科技 / 周末晚上看娱乐"等周期性行为。
// 数据存储于 TemporalProfile.PeriodicPattern.SlotTags map[slot][]InterestEntry。
//
// 核心函数：
//   - UpdatePeriodic: 更新指定槽位的兴趣权重（已存在则强化，不存在则新建）
//   - TopKBySlot: 取指定槽位 TopK 兴趣（按 Weight 降序）
//
// 二开扩展点：
//   - 自定义槽位粒度：业务方在调用前自行决定 slot 划分（小时段/星期段等）
//   - 自定义强化公式：替换 reinforceInterest 逻辑（如改为对数增长）
// ============================================================================

// UpdatePeriodic 更新周期槽位兴趣。
//
// 在 tp.PeriodicPattern.SlotTags[slot] 中查找 tag 对应的 InterestEntry：
//   - 已存在：强化（Weight += weight，更新 LastReinforcedAt）
//   - 不存在：新建 InterestEntry 并追加
//
// slot 格式 "HH-HH"（如 "09-12"）；weight 为本次强化增量；
// now 为当前时间，用于更新 LastReinforcedAt。
//
// tp 为 nil 或 slot/tag 为空时直接返回（防御式编程）。
func UpdatePeriodic(tp *domain.TemporalProfile, slot string, tag string, weight float64, now time.Time) {
	if tp == nil || slot == "" || tag == "" {
		return
	}
	if tp.PeriodicPattern.SlotTags == nil {
		tp.PeriodicPattern.SlotTags = make(map[string][]domain.InterestEntry)
	}
	entries := tp.PeriodicPattern.SlotTags[slot]
	for i := range entries {
		if entries[i].Tag == tag {
			// 已存在：强化
			reinforceInterest(&entries[i], weight, now)
			tp.PeriodicPattern.SlotTags[slot] = entries
			return
		}
	}
	// 不存在：新建并追加
	entries = append(entries, domain.InterestEntry{
		Tag:              tag,
		Weight:           weight,
		Strength:         weight,
		LastReinforcedAt: now,
	})
	tp.PeriodicPattern.SlotTags[slot] = entries
}

// TopKBySlot 取指定槽位 TopK 兴趣，按 Weight 降序返回。
//
// 不修改 SlotTags[slot] 原始顺序（复制后排序）。
// k <= 0 返回空；k > len 时返回全部。
//
// tp 为 nil 或 slot 不存在时返回 nil。
func TopKBySlot(tp *domain.TemporalProfile, slot string, k int) []domain.InterestEntry {
	if tp == nil || k <= 0 || slot == "" {
		return nil
	}
	entries, ok := tp.PeriodicPattern.SlotTags[slot]
	if !ok {
		return nil
	}
	// 复制避免修改原切片
	out := make([]domain.InterestEntry, len(entries))
	copy(out, entries)
	// 按 Weight 降序稳定排序
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Weight > out[j].Weight
	})
	if k > len(out) {
		k = len(out)
	}
	return out[:k]
}

// reinforceInterest 强化兴趣条目（Weight 与 Strength 增加增量，更新 LastReinforcedAt）。
//
// TODO: 待 Task 4.3 DecayEngine 完成后，替换为 DecayEngine.Reinforce 调用，
// 支持自定义强化公式（如对数增长、饱和函数等）。
func reinforceInterest(it *domain.Interest, weight float64, now time.Time) {
	it.Weight += weight
	it.Strength += weight
	it.LastReinforcedAt = now
}
