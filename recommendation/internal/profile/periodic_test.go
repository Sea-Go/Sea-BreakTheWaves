package profile

import (
	"testing"
	"time"

	"sea/internal/domain"
)

// ============================================================================
// 该文件测试周期模式（Periodic Pattern）的兴趣更新与 TopK 查询，覆盖：
//   - UpdatePeriodic 新建兴趣条目
//   - UpdatePeriodic 已存在兴趣强化（Weight 累加）
//   - UpdatePeriodic 防御式（nil tp / 空 slot / 空 tag）
//   - TopKBySlot 按 Weight 降序返回
//   - TopKBySlot k > len 时返回全部
//   - TopKBySlot 不修改原切片顺序
//   - TopKBySlot 防御式（nil tp / k<=0 / 不存在 slot）
// ============================================================================

// TestUpdatePeriodic_CreateNew 验证新建兴趣条目。
func TestUpdatePeriodic_CreateNew(t *testing.T) {
	tp := domain.NewTemporalProfile(time.Now())
	now := time.Now()
	UpdatePeriodic(tp, "09-12", "tech", 0.5, now)

	entries := tp.PeriodicPattern.SlotTags["09-12"]
	if len(entries) != 1 {
		t.Fatalf("SlotTags[09-12] 条目数 = %d, 期望 1", len(entries))
	}
	if entries[0].Tag != "tech" {
		t.Errorf("Tag = %q, 期望 tech", entries[0].Tag)
	}
	if entries[0].Weight != 0.5 {
		t.Errorf("Weight = %v, 期望 0.5", entries[0].Weight)
	}
	if entries[0].Strength != 0.5 {
		t.Errorf("Strength = %v, 期望 0.5", entries[0].Strength)
	}
	if !entries[0].LastReinforcedAt.Equal(now) {
		t.Errorf("LastReinforcedAt = %v, 期望 %v", entries[0].LastReinforcedAt, now)
	}
}

// TestUpdatePeriodic_ReinforceExisting 验证已存在兴趣强化（Weight 累加）。
func TestUpdatePeriodic_ReinforceExisting(t *testing.T) {
	tp := domain.NewTemporalProfile(time.Now())
	t1 := time.Now()
	UpdatePeriodic(tp, "20-23", "sports", 0.3, t1)

	// 第二次强化同一 tag
	t2 := t1.Add(time.Hour)
	UpdatePeriodic(tp, "20-23", "sports", 0.4, t2)

	entries := tp.PeriodicPattern.SlotTags["20-23"]
	if len(entries) != 1 {
		t.Fatalf("SlotTags[20-23] 条目数 = %d, 期望 1（强化不新增）", len(entries))
	}
	if entries[0].Weight != 0.7 {
		t.Errorf("Weight = %v, 期望 0.7（0.3+0.4）", entries[0].Weight)
	}
	if entries[0].Strength != 0.7 {
		t.Errorf("Strength = %v, 期望 0.7", entries[0].Strength)
	}
	if !entries[0].LastReinforcedAt.Equal(t2) {
		t.Errorf("LastReinforcedAt = %v, 期望 %v（应更新为最后一次强化时间）", entries[0].LastReinforcedAt, t2)
	}
}

// TestUpdatePeriodic_MultipleSlots 验证多槽位独立存储。
func TestUpdatePeriodic_MultipleSlots(t *testing.T) {
	tp := domain.NewTemporalProfile(time.Now())
	now := time.Now()
	UpdatePeriodic(tp, "09-12", "tech", 0.5, now)
	UpdatePeriodic(tp, "20-23", "sports", 0.3, now)

	if len(tp.PeriodicPattern.SlotTags) != 2 {
		t.Fatalf("槽位数 = %d, 期望 2", len(tp.PeriodicPattern.SlotTags))
	}
	if len(tp.PeriodicPattern.SlotTags["09-12"]) != 1 {
		t.Errorf("09-12 槽条目数 = %d, 期望 1", len(tp.PeriodicPattern.SlotTags["09-12"]))
	}
	if len(tp.PeriodicPattern.SlotTags["20-23"]) != 1 {
		t.Errorf("20-23 槽条目数 = %d, 期望 1", len(tp.PeriodicPattern.SlotTags["20-23"]))
	}
}

// TestUpdatePeriodic_NilDefense 验证 nil tp / 空 slot / 空 tag 防御。
func TestUpdatePeriodic_NilDefense(t *testing.T) {
	// nil tp 不 panic
	UpdatePeriodic(nil, "09-12", "tech", 0.5, time.Now())

	tp := domain.NewTemporalProfile(time.Now())
	// 空 slot
	UpdatePeriodic(tp, "", "tech", 0.5, time.Now())
	if len(tp.PeriodicPattern.SlotTags) != 0 {
		t.Errorf("空 slot 时不应写入, 实际 %d 个槽位", len(tp.PeriodicPattern.SlotTags))
	}
	// 空 tag
	UpdatePeriodic(tp, "09-12", "", 0.5, time.Now())
	if len(tp.PeriodicPattern.SlotTags) != 0 {
		t.Errorf("空 tag 时不应写入, 实际 %d 个槽位", len(tp.PeriodicPattern.SlotTags))
	}
}

// TestTopKBySlot_Descending 验证按 Weight 降序返回 TopK。
func TestTopKBySlot_Descending(t *testing.T) {
	tp := domain.NewTemporalProfile(time.Now())
	now := time.Now()
	// 同一槽位 3 个兴趣，权重不同
	UpdatePeriodic(tp, "09-12", "a", 0.3, now)
	UpdatePeriodic(tp, "09-12", "b", 0.8, now)
	UpdatePeriodic(tp, "09-12", "c", 0.5, now)

	top := TopKBySlot(tp, "09-12", 2)
	if len(top) != 2 {
		t.Fatalf("TopK 返回 %d 个, 期望 2", len(top))
	}
	// 降序：b(0.8) > c(0.5)
	if top[0].Tag != "b" {
		t.Errorf("首位 Tag = %q, 期望 b（Weight=0.8）", top[0].Tag)
	}
	if top[1].Tag != "c" {
		t.Errorf("第二位 Tag = %q, 期望 c（Weight=0.5）", top[1].Tag)
	}
}

// TestTopKBySlot_KExceedsLen 验证 k > len 时返回全部。
func TestTopKBySlot_KExceedsLen(t *testing.T) {
	tp := domain.NewTemporalProfile(time.Now())
	now := time.Now()
	UpdatePeriodic(tp, "09-12", "a", 0.3, now)
	UpdatePeriodic(tp, "09-12", "b", 0.8, now)

	top := TopKBySlot(tp, "09-12", 100)
	if len(top) != 2 {
		t.Fatalf("TopK 返回 %d 个, 期望 2（k>len 时返回全部）", len(top))
	}
}

// TestTopKBySlot_DoesNotMutateOriginal 验证不修改原切片顺序。
func TestTopKBySlot_DoesNotMutateOriginal(t *testing.T) {
	tp := domain.NewTemporalProfile(time.Now())
	now := time.Now()
	UpdatePeriodic(tp, "09-12", "a", 0.3, now)
	UpdatePeriodic(tp, "09-12", "b", 0.8, now)
	UpdatePeriodic(tp, "09-12", "c", 0.5, now)

	// 原始顺序：a, b, c
	original := tp.PeriodicPattern.SlotTags["09-12"]
	_ = TopKBySlot(tp, "09-12", 2)

	after := tp.PeriodicPattern.SlotTags["09-12"]
	for i := range original {
		if after[i].Tag != original[i].Tag {
			t.Errorf("原切片被修改: 位置 %d 从 %q 变为 %q", i, original[i].Tag, after[i].Tag)
		}
	}
}

// TestTopKBySlot_Defense 验证 nil tp / k<=0 / 不存在 slot 防御。
func TestTopKBySlot_Defense(t *testing.T) {
	// nil tp
	if got := TopKBySlot(nil, "09-12", 5); got != nil {
		t.Errorf("nil tp 应返回 nil, 实际 %v", got)
	}

	tp := domain.NewTemporalProfile(time.Now())
	// k <= 0
	if got := TopKBySlot(tp, "09-12", 0); got != nil {
		t.Errorf("k=0 应返回 nil, 实际 %v", got)
	}
	// 不存在的 slot
	if got := TopKBySlot(tp, "nonexistent", 5); got != nil {
		t.Errorf("不存在的 slot 应返回 nil, 实际 %v", got)
	}
}
