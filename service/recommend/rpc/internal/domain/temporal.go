// ============================================================================
// 该文件定义时间画像 TemporalProfile 的辅助类型与便捷方法。
// TemporalProfile / Interest / DecayState 的结构体定义见 types.go；
// 本文件补充 spec 要求的 InterestEntry / LongTermInterests 等类型别名、
// PeriodicPattern 结构化周期模式，以及时间画像的构造与查询方法。
//
// 时间分层职责：
//   - LongTerm       长期兴趣（90 天累积，衰减慢）
//   - ShortTerm      短期兴趣（7 天累积，衰减中）
//   - Session        会话内兴趣（30 分钟，衰减快）
//   - PeriodicPattern 周期模式（按小时槽位，如 "09-12"）
//   - Trending       趋势兴趣（24h 突增）
//   - GlobalDecayState 全局衰减状态（上次衰减时间 + 全局 tau）
//   - EliminationEntries 淘汰池（含完整 Interest，可复活）
//
// 二开扩展点：通过 NewTemporalProfile 覆盖默认 tau；实现 DecayEngine 自定义衰减。
// ============================================================================

package domain

import "time"

// 时间窗口常量（用于语义说明与上层裁剪，非强制约束）。
const (
	// LongTermWindow 长期兴趣累积窗口（90 天）。
	LongTermWindow = 90 * 24 * time.Hour
	// ShortTermWindow 短期兴趣累积窗口（7 天）。
	ShortTermWindow = 7 * 24 * time.Hour
	// SessionWindow 会话内兴趣累积窗口（30 分钟）。
	SessionWindow = 30 * time.Minute
	// TrendingWindow 趋势兴趣检测窗口（24 小时）。
	TrendingWindow = 24 * time.Hour
)

// 默认衰减参数（二开点：可覆盖）。
const (
	// DefaultDecayTauSec 默认衰减时间常数 tau（秒）。
	// 取 7 天（604800 秒），对应一周衰减周期，R = exp(-t/tau)。
	// 二开点：业务可按兴趣类型/用户分级自定义 tau。
	DefaultDecayTauSec = 7 * 24 * 60 * 60.0
	// DefaultArchiveThreshold 默认归档阈值：Weight 低于该值则标记 Archived 并移入淘汰池。
	DefaultArchiveThreshold = 0.05
)

// InterestEntry 兴趣条目类型别名，与 Interest 等价。
// spec 要求每兴趣条目含 Tag/Weight/Strength S/LastReinforcedAt/DecayTau/Archived，
// 这些字段已在 Interest 中定义，故此处仅声明别名以对齐 spec 命名。
type InterestEntry = Interest

// LongTermInterests 长期兴趣列表（90 天累积）。
type LongTermInterests = []InterestEntry

// ShortTermInterests 短期兴趣列表（7 天累积）。
type ShortTermInterests = []InterestEntry

// SessionInterests 会话内兴趣列表（30 分钟）。
type SessionInterests = []InterestEntry

// TrendingInterests 趋势兴趣列表（24h 突增）。
type TrendingInterests = []InterestEntry

// EliminationPoolEntries 淘汰池条目列表（含完整 Interest，可复活）。
// 注：TemporalProfile.EliminationPool 字段为 []string（仅 Tag），
// 本类型用于完整画像淘汰场景（EliminationEntries 字段）。
type EliminationPoolEntries = []InterestEntry

// PeriodicPattern 结构化周期模式，按时间槽位（如 "09-12"/"20-23"）组织兴趣。
// 与 TemporalProfile.Periodic map[string][]Interest 并存：
//   - Periodic map 用于按 weekday/weekend 等维度
//   - PeriodicPattern 用于按小时槽位（更贴近阅读时段语义）
type PeriodicPattern struct {
	// SlotTags 按时间槽位组织的兴趣条目。
	SlotTags map[string][]InterestEntry
}

// NewPeriodicPattern 构造空的周期模式。
func NewPeriodicPattern() PeriodicPattern {
	return PeriodicPattern{SlotTags: make(map[string][]InterestEntry)}
}

// Add 添加兴趣条目到指定时间槽位。
// slot 时间槽位（如 "09-12"）；entry 兴趣条目。
func (p *PeriodicPattern) Add(slot string, entry InterestEntry) {
	if p == nil {
		return
	}
	if p.SlotTags == nil {
		p.SlotTags = make(map[string][]InterestEntry)
	}
	p.SlotTags[slot] = append(p.SlotTags[slot], entry)
}

// Get 获取指定时间槽位的兴趣列表（不存在时返回 nil）。
func (p *PeriodicPattern) Get(slot string) []InterestEntry {
	if p == nil || p.SlotTags == nil {
		return nil
	}
	return p.SlotTags[slot]
}

// Slots 返回所有时间槽位键（无序）。
func (p *PeriodicPattern) Slots() []string {
	if p == nil || p.SlotTags == nil {
		return nil
	}
	slots := make([]string, 0, len(p.SlotTags))
	for slot := range p.SlotTags {
		slots = append(slots, slot)
	}
	return slots
}

// NewInterest 构造兴趣条目，使用默认衰减 tau。
// tag 兴趣标签；weight 初始权重；now 当前时间（作为 LastReinforcedAt）。
func NewInterest(tag string, weight float64, now time.Time) Interest {
	return Interest{
		Tag:              tag,
		Weight:           weight,
		Strength:         1,
		LastReinforcedAt: now,
		DecayTau:         DefaultDecayTauSec,
		Archived:         false,
	}
}

// NewTemporalProfile 构造空的时间画像，初始化各层切片与全局衰减状态。
// now 当前时间（作为 GlobalDecayState.LastDecayAt 与 UpdatedAt）。
func NewTemporalProfile(now time.Time) *TemporalProfile {
	return &TemporalProfile{
		LongTerm:           make([]Interest, 0),
		ShortTerm:          make([]Interest, 0),
		Session:            make([]Interest, 0),
		Periodic:           make(map[string][]Interest),
		Trending:           make([]Interest, 0),
		DecayState:         make(map[string]DecayState),
		EliminationPool:    make([]string, 0),
		PeriodicPattern:    NewPeriodicPattern(),
		GlobalDecayState:   GlobalDecayState{LastDecayAt: now, GlobalTau: DefaultDecayTauSec},
		EliminationEntries: make([]Interest, 0),
		UpdatedAt:          now,
	}
}

// FindInterest 在指定层级查找兴趣条目（按 Tag）。
// layer 取值 "long"/"short"/"session"/"trending"；返回索引与条目（未找到返回 -1）。
func (t *TemporalProfile) FindInterest(layer, tag string) (int, Interest) {
	if t == nil {
		return -1, Interest{}
	}
	var list []Interest
	switch layer {
	case "long":
		list = t.LongTerm
	case "short":
		list = t.ShortTerm
	case "session":
		list = t.Session
	case "trending":
		list = t.Trending
	default:
		return -1, Interest{}
	}
	for i, e := range list {
		if e.Tag == tag {
			return i, e
		}
	}
	return -1, Interest{}
}

// UpsertLongTerm 长期兴趣 upsert：存在则更新 Weight/Strength，不存在则追加。
func (t *TemporalProfile) UpsertLongTerm(entry Interest) {
	if t == nil {
		return
	}
	if idx, _ := t.FindInterest("long", entry.Tag); idx >= 0 {
		t.LongTerm[idx].Weight = entry.Weight
		t.LongTerm[idx].Strength = entry.Strength
		t.LongTerm[idx].LastReinforcedAt = entry.LastReinforcedAt
		t.LongTerm[idx].DecayTau = entry.DecayTau
		t.LongTerm[idx].Archived = entry.Archived
		return
	}
	t.LongTerm = append(t.LongTerm, entry)
}

// UpsertShortTerm 短期兴趣 upsert。
func (t *TemporalProfile) UpsertShortTerm(entry Interest) {
	if t == nil {
		return
	}
	if idx, _ := t.FindInterest("short", entry.Tag); idx >= 0 {
		t.ShortTerm[idx].Weight = entry.Weight
		t.ShortTerm[idx].Strength = entry.Strength
		t.ShortTerm[idx].LastReinforcedAt = entry.LastReinforcedAt
		t.ShortTerm[idx].DecayTau = entry.DecayTau
		t.ShortTerm[idx].Archived = entry.Archived
		return
	}
	t.ShortTerm = append(t.ShortTerm, entry)
}

// RemoveFromEliminationEntries 从淘汰池（完整条目）移除指定 tag，返回移除的条目与是否找到。
func (t *TemporalProfile) RemoveFromEliminationEntries(tag string) (Interest, bool) {
	if t == nil {
		return Interest{}, false
	}
	for i, e := range t.EliminationEntries {
		if e.Tag == tag {
			t.EliminationEntries = append(t.EliminationEntries[:i], t.EliminationEntries[i+1:]...)
			return e, true
		}
	}
	return Interest{}, false
}

// IsEmpty 判断时间画像是否全空（各层均无条目）。
func (t *TemporalProfile) IsEmpty() bool {
	if t == nil {
		return true
	}
	return len(t.LongTerm) == 0 && len(t.ShortTerm) == 0 && len(t.Session) == 0 &&
		len(t.Trending) == 0 && len(t.EliminationPool) == 0 && len(t.EliminationEntries) == 0
}
