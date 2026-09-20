// ============================================================================
// 该文件测试时间画像 TemporalProfile 的类型完整性与辅助方法行为。
// 覆盖：类型编译、字段完整性、类型别名兼容、PeriodicPattern、构造与查询方法。
// ============================================================================

package domain

import (
	"testing"
	"time"
)

// TestTemporalProfile_TypeCompleteness 验证时间画像字段完整性（编译期保证）。
func TestTemporalProfile_TypeCompleteness(t *testing.T) {
	now := time.Now()
	tp := TemporalProfile{
		LongTerm:           []Interest{{Tag: "AI", Weight: 0.8, Strength: 3, LastReinforcedAt: now, DecayTau: 604800}},
		ShortTerm:          []Interest{{Tag: "旅行", Weight: 0.5, Strength: 1, LastReinforcedAt: now, DecayTau: 86400}},
		Session:            []Interest{{Tag: "美食", Weight: 0.3, Strength: 1, LastReinforcedAt: now, DecayTau: 1800}},
		Periodic:           map[string][]Interest{"weekday-9": {{Tag: "AI", Weight: 0.6}}},
		Trending:           []Interest{{Tag: "大模型", Weight: 0.9, Strength: 2, LastReinforcedAt: now, DecayTau: 86400}},
		DecayState:         map[string]DecayState{"AI": {Tag: "AI", Score: 0.7, Updated: now, LastDecayAt: now, GlobalTau: 604800}},
		EliminationPool:    []string{"旧闻"},
		PeriodicPattern:    PeriodicPattern{SlotTags: map[string][]InterestEntry{"09-12": {{Tag: "AI", Weight: 0.6}}}},
		GlobalDecayState:   GlobalDecayState{LastDecayAt: now, GlobalTau: 604800},
		EliminationEntries: []Interest{{Tag: "旧闻", Weight: 0.01, Strength: 1, LastReinforcedAt: now.Add(-72 * time.Hour), DecayTau: 604800, Archived: true}},
		UpdatedAt:          now,
	}
	if tp.LongTerm[0].Tag != "AI" {
		t.Fatalf("LongTerm[0].Tag = %q, want AI", tp.LongTerm[0].Tag)
	}
	if tp.GlobalDecayState.GlobalTau != 604800 {
		t.Fatalf("GlobalDecayState.GlobalTau = %v, want 604800", tp.GlobalDecayState.GlobalTau)
	}
	if len(tp.EliminationEntries) != 1 {
		t.Fatalf("EliminationEntries len = %d, want 1", len(tp.EliminationEntries))
	}
}

// TestInterestEntryAlias 验证 InterestEntry 类型别名与 Interest 完全兼容。
func TestInterestEntryAlias(t *testing.T) {
	// InterestEntry 应可直接赋值给 Interest，反之亦然。
	var entry InterestEntry = Interest{Tag: "AI", Weight: 0.8}
	var i Interest = entry
	if i.Tag != "AI" {
		t.Fatalf("InterestEntry -> Interest 转换失败: Tag = %q", i.Tag)
	}

	// LongTermInterests 应与 []Interest 兼容。
	var lt LongTermInterests = []InterestEntry{entry}
	if len(lt) != 1 {
		t.Fatalf("LongTermInterests len = %d, want 1", len(lt))
	}
}

// TestNewInterest 验证兴趣条目构造（默认 tau）。
func TestNewInterest(t *testing.T) {
	now := time.Now()
	entry := NewInterest("AI", 0.8, now)
	if entry.Tag != "AI" || entry.Weight != 0.8 {
		t.Fatalf("NewInterest 字段错误: %+v", entry)
	}
	if entry.DecayTau != DefaultDecayTauSec {
		t.Fatalf("DecayTau = %v, want %v", entry.DecayTau, DefaultDecayTauSec)
	}
	if entry.Strength != 1 {
		t.Fatalf("Strength = %v, want 1", entry.Strength)
	}
	if entry.Archived {
		t.Fatalf("Archived = true, want false")
	}
}

// TestNewTemporalProfile 验证时间画像构造函数初始化各层。
func TestNewTemporalProfile(t *testing.T) {
	now := time.Now()
	tp := NewTemporalProfile(now)
	if tp == nil {
		t.Fatalf("NewTemporalProfile 返回 nil")
	}
	if tp.LongTerm == nil || tp.ShortTerm == nil || tp.Session == nil || tp.Trending == nil {
		t.Fatalf("兴趣列表未初始化")
	}
	if tp.Periodic == nil || tp.DecayState == nil {
		t.Fatalf("map 字段未初始化")
	}
	if tp.PeriodicPattern.SlotTags == nil {
		t.Fatalf("PeriodicPattern.SlotTags 未初始化")
	}
	if tp.GlobalDecayState.GlobalTau != DefaultDecayTauSec {
		t.Fatalf("GlobalTau = %v, want %v", tp.GlobalDecayState.GlobalTau, DefaultDecayTauSec)
	}
	if !tp.GlobalDecayState.LastDecayAt.Equal(now) {
		t.Fatalf("LastDecayAt = %v, want %v", tp.GlobalDecayState.LastDecayAt, now)
	}
}

// TestPeriodicPattern_AddGetSlots 验证周期模式的增查与槽位枚举。
func TestPeriodicPattern_AddGetSlots(t *testing.T) {
	p := NewPeriodicPattern()
	entry := NewInterest("AI", 0.6, time.Now())
	p.Add("09-12", entry)
	p.Add("20-23", NewInterest("旅行", 0.5, time.Now()))

	if got := p.Get("09-12"); len(got) != 1 || got[0].Tag != "AI" {
		t.Fatalf("Get(09-12) = %v, want 1 AI", got)
	}
	if got := p.Get("unknown"); got != nil {
		t.Fatalf("Get(unknown) = %v, want nil", got)
	}

	slots := p.Slots()
	if len(slots) != 2 {
		t.Fatalf("Slots len = %d, want 2", len(slots))
	}
}

// TestTemporalProfile_FindInterest 验证层级兴趣查找。
func TestTemporalProfile_FindInterest(t *testing.T) {
	now := time.Now()
	tp := NewTemporalProfile(now)
	tp.LongTerm = append(tp.LongTerm, NewInterest("AI", 0.8, now))
	tp.ShortTerm = append(tp.ShortTerm, NewInterest("旅行", 0.5, now))

	if idx, e := tp.FindInterest("long", "AI"); idx < 0 || e.Tag != "AI" {
		t.Fatalf("FindInterest(long, AI) = %d %+v", idx, e)
	}
	if idx, _ := tp.FindInterest("short", "AI"); idx >= 0 {
		t.Fatalf("FindInterest(short, AI) 应未找到, got idx=%d", idx)
	}
	if idx, _ := tp.FindInterest("unknown_layer", "AI"); idx >= 0 {
		t.Fatalf("未知 layer 应返回 -1, got %d", idx)
	}
}

// TestTemporalProfile_Upsert 验证 upsert 行为（存在更新/不存在追加）。
func TestTemporalProfile_Upsert(t *testing.T) {
	now := time.Now()
	tp := NewTemporalProfile(now)

	// 不存在 → 追加。
	tp.UpsertLongTerm(NewInterest("AI", 0.5, now))
	if len(tp.LongTerm) != 1 {
		t.Fatalf("追加后 LongTerm len = %d, want 1", len(tp.LongTerm))
	}

	// 存在 → 更新。
	tp.UpsertLongTerm(Interest{Tag: "AI", Weight: 0.9, Strength: 5, LastReinforcedAt: now, DecayTau: 999})
	if len(tp.LongTerm) != 1 {
		t.Fatalf("更新后 LongTerm len = %d, want 1（不应新增）", len(tp.LongTerm))
	}
	if tp.LongTerm[0].Weight != 0.9 || tp.LongTerm[0].Strength != 5 || tp.LongTerm[0].DecayTau != 999 {
		t.Fatalf("更新后字段错误: %+v", tp.LongTerm[0])
	}
}

// TestTemporalProfile_RemoveFromEliminationEntries 验证淘汰池移除。
func TestTemporalProfile_RemoveFromEliminationEntries(t *testing.T) {
	now := time.Now()
	tp := NewTemporalProfile(now)
	tp.EliminationEntries = []Interest{
		{Tag: "旧闻", Weight: 0.01, Archived: true},
		{Tag: "过期", Weight: 0.02, Archived: true},
	}

	entry, found := tp.RemoveFromEliminationEntries("旧闻")
	if !found || entry.Tag != "旧闻" {
		t.Fatalf("移除 旧闻 失败: found=%v entry=%+v", found, entry)
	}
	if len(tp.EliminationEntries) != 1 || tp.EliminationEntries[0].Tag != "过期" {
		t.Fatalf("移除后淘汰池剩余: %+v", tp.EliminationEntries)
	}

	// 移除不存在的 tag。
	_, found = tp.RemoveFromEliminationEntries("不存在")
	if found {
		t.Fatalf("移除不存在的 tag 应返回 found=false")
	}
}

// TestTemporalProfile_IsEmpty 验证空画像判定。
func TestTemporalProfile_IsEmpty(t *testing.T) {
	tp := NewTemporalProfile(time.Now())
	if !tp.IsEmpty() {
		t.Fatalf("空画像 IsEmpty 应为 true")
	}
	tp.LongTerm = append(tp.LongTerm, NewInterest("AI", 0.5, time.Now()))
	if tp.IsEmpty() {
		t.Fatalf("有内容后 IsEmpty 应为 false")
	}
}
