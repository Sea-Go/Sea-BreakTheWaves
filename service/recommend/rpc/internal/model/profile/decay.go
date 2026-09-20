// ============================================================================
// 该文件实现衰减淘汰引擎 DecayEngine，基于 Ebbinghaus 遗忘曲线。
//
// 衰减公式：R = exp(-t/tau)
//   - t   = 自上次强化以来的时间（now - LastReinforcedAt）
//   - tau = 衰减时间常数（秒），tau 越大衰减越慢
//   - R   = 记忆保持率（0-1），乘到 Weight 上得到衰减后权重
//
// 引擎职责：
//   - Decay           应用衰减到单个兴趣条目（Weight *= R，低于阈值则 Archived）
//   - Reinforce       强化兴趣（Weight += delta，刷新 LastReinforcedAt，Strength += 1）
//   - Weaken          弱化兴趣（Weight -= delta，不低于 0）
//   - TickElimination 遍历 LongTerm/ShortTerm，Weight < threshold 的移入淘汰池
//   - Revive          从淘汰池复活兴趣到 ShortTerm
//
// 二开扩展点：
//   - 自定义 tau：覆盖 Interest.DecayTau 字段或 GlobalDecayState.GlobalTau
//   - 自定义阈值：调用 TickElimination 时传入 threshold
//   - 自定义衰减公式：实现新的衰减函数替换 EbbinghausR（注入到 DecayEngine）
//
// 注：本 task 仅实现衰减逻辑；ProfileManager interface 的 GetTemporalProfile/
// UpdateTemporalProfile 仓储委托在 Task 4.5 实现。
// ============================================================================

package profile

import (
	"math"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// 默认归档阈值（Weight 低于该值标记 Archived）。
const DefaultArchiveThreshold = 0.05

// DecayEngine 衰减淘汰引擎，基于 Ebbinghaus 遗忘曲线。
// now 可注入便于测试（控制时间推进）。
type DecayEngine struct {
	// now 返回当前时间，默认 time.Now，测试中可注入固定时钟。
	now func() time.Time
}

// NewDecayEngine 构造衰减引擎，使用 time.Now 作为默认时钟。
func NewDecayEngine() *DecayEngine {
	return &DecayEngine{now: time.Now}
}

// NewDecayEngineWithClock 构造衰减引擎并注入自定义时钟（便于测试）。
// clock 返回当前时间的函数。
func NewDecayEngineWithClock(clock func() time.Time) *DecayEngine {
	if clock == nil {
		clock = time.Now
	}
	return &DecayEngine{now: clock}
}

// Now 返回引擎当前时间。
func (e *DecayEngine) Now() time.Time {
	if e == nil || e.now == nil {
		return time.Now()
	}
	return e.now()
}

// EbbinghausR 计算 Ebbinghaus 遗忘曲线保持率 R = exp(-t/tau)。
// elapsed 自上次强化以来的时间；tau 衰减时间常数（秒）。
// 当 t=0 时 R=1（无衰减）；当 t=tau 时 R=e^-1≈0.368。
// tau <= 0 时返回 0（视为立即遗忘）。
func EbbinghausR(elapsed time.Duration, tau float64) float64 {
	if tau <= 0 {
		return 0
	}
	// elapsed 转秒（float64），与 tau 单位对齐。
	tSec := elapsed.Seconds()
	return math.Exp(-tSec / tau)
}

// Decay 应用衰减到单个兴趣条目。
// 计算 R = exp(-(now - LastReinforcedAt)/DecayTau)，Weight *= R；
// 若 Weight < threshold 则标记 Archived=true。
// threshold <= 0 时使用 DefaultArchiveThreshold。
// 注：若 LastReinforcedAt 为零值，跳过衰减（未强化过的条目不衰减）。
func (e *DecayEngine) Decay(entry *domain.Interest, now time.Time) {
	if e == nil || entry == nil {
		return
	}
	if entry.LastReinforcedAt.IsZero() {
		return
	}
	tau := entry.DecayTau
	if tau <= 0 {
		// 回退到全局默认 tau。
		tau = domain.DefaultDecayTauSec
	}
	r := EbbinghausR(now.Sub(entry.LastReinforcedAt), tau)
	entry.Weight *= r
	if entry.Weight < DefaultArchiveThreshold {
		entry.Archived = true
	}
}

// DecayWithThreshold 应用衰减并使用自定义归档阈值。
// threshold 归档阈值（Weight 低于该值标记 Archived）。
func (e *DecayEngine) DecayWithThreshold(entry *domain.Interest, now time.Time, threshold float64) {
	if e == nil || entry == nil {
		return
	}
	if entry.LastReinforcedAt.IsZero() {
		return
	}
	tau := entry.DecayTau
	if tau <= 0 {
		tau = domain.DefaultDecayTauSec
	}
	r := EbbinghausR(now.Sub(entry.LastReinforcedAt), tau)
	entry.Weight *= r
	if threshold <= 0 {
		threshold = DefaultArchiveThreshold
	}
	if entry.Weight < threshold {
		entry.Archived = true
	}
}

// Reinforce 强化兴趣条目。
// Weight += delta；LastReinforcedAt = now；Strength += 1；Archived 置 false（复活语义）。
// delta 强化增量（<=0 时视为仅刷新时间，不增加权重）。
func (e *DecayEngine) Reinforce(entry *domain.Interest, delta float64, now time.Time) {
	if e == nil || entry == nil {
		return
	}
	if delta > 0 {
		entry.Weight += delta
	}
	entry.LastReinforcedAt = now
	entry.Strength += 1
	entry.Archived = false
}

// Weaken 弱化兴趣条目。
// Weight -= delta（不低于 0）；不修改 LastReinforcedAt（弱化不等同遗忘）。
// delta 弱化减量（<=0 时无操作）。
func (e *DecayEngine) Weaken(entry *domain.Interest, delta float64, now time.Time) {
	if e == nil || entry == nil {
		return
	}
	_ = now // 保留 now 参数以对齐 spec 签名与未来扩展（如记录弱化时间）
	if delta <= 0 {
		return
	}
	entry.Weight -= delta
	if entry.Weight < 0 {
		entry.Weight = 0
	}
}

// TickElimination 遍历 LongTerm/ShortTerm，将 Weight < threshold 的条目移入淘汰池。
// 移动策略：
//  1. 从原列表移除该条目
//  2. 标记 Archived=true
//  3. 追加到 EliminationEntries（完整 Interest，便于复活）
//  4. 同时追加 Tag 到 EliminationPool（[]string，兼容已有字段）
//
// threshold 淘汰阈值（<=0 时使用 DefaultArchiveThreshold）。
// now 当前时间（用于刷新 UpdatedAt 与 GlobalDecayState.LastDecayAt）。
func (e *DecayEngine) TickElimination(tp *domain.TemporalProfile, threshold float64, now time.Time) {
	if e == nil || tp == nil {
		return
	}
	if threshold <= 0 {
		threshold = DefaultArchiveThreshold
	}

	// 过滤 LongTerm。
	tp.LongTerm = filterEliminated(tp.LongTerm, threshold, tp)
	// 过滤 ShortTerm。
	tp.ShortTerm = filterEliminated(tp.ShortTerm, threshold, tp)

	// 刷新全局衰减状态。
	tp.GlobalDecayState.LastDecayAt = now
	tp.UpdatedAt = now
}

// filterEliminated 过滤列表中 Weight < threshold 的条目，移入淘汰池。
// 返回过滤后的列表。
func filterEliminated(list []domain.Interest, threshold float64, tp *domain.TemporalProfile) []domain.Interest {
	if len(list) == 0 {
		return list
	}
	kept := make([]domain.Interest, 0, len(list))
	for i := range list {
		entry := &list[i]
		if entry.Weight < threshold {
			entry.Archived = true
			// 追加到淘汰池（完整条目 + Tag 字符串）。
			tp.EliminationEntries = append(tp.EliminationEntries, *entry)
			tp.EliminationPool = append(tp.EliminationPool, entry.Tag)
			continue
		}
		kept = append(kept, *entry)
	}
	return kept
}

// Revive 从淘汰池复活指定 tag 的兴趣到 ShortTerm。
// 复活策略：
//  1. 从 EliminationEntries 中查找并移除该条目
//  2. 从 EliminationPool 中移除对应 Tag
//  3. 重置 Archived=false，刷新 LastReinforcedAt = now
//  4. 追加到 ShortTerm 列表
//
// tag 待复活的兴趣标签；now 复活时间。
// 返回是否成功复活（淘汰池中存在该 tag）。
func (e *DecayEngine) Revive(tp *domain.TemporalProfile, tag string, now time.Time) bool {
	if e == nil || tp == nil {
		return false
	}

	// 1. 从 EliminationEntries 查找并移除。
	entry, found := tp.RemoveFromEliminationEntries(tag)
	if !found {
		return false
	}

	// 2. 从 EliminationPool（[]string）移除该 Tag。
	tp.EliminationPool = removeString(tp.EliminationPool, tag)

	// 3. 重置复活状态。
	entry.Archived = false
	entry.LastReinforcedAt = now
	// 复活时给一个最小权重，避免再次立即被淘汰（取 threshold 与 0.1 的较大值）。
	if entry.Weight < DefaultArchiveThreshold {
		entry.Weight = DefaultArchiveThreshold
	}

	// 4. 追加到 ShortTerm。
	tp.ShortTerm = append(tp.ShortTerm, entry)
	tp.UpdatedAt = now
	return true
}

// removeString 从字符串切片移除首个匹配元素。
func removeString(s []string, v string) []string {
	for i, x := range s {
		if x == v {
			return append(s[:i], s[i+1:]...)
		}
	}
	return s
}
