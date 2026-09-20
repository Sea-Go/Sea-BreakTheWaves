// ============================================================================
// 该文件测试衰减淘汰引擎 DecayEngine 的核心行为。
// 覆盖：
//   - EbbinghausR 计算正确性（t=0 时 R=1，t=tau 时 R=e^-1≈0.368）
//   - Decay 衰减后 Weight 下降
//   - Reinforce 强化后 Weight 上升 + LastReinforcedAt 更新
//   - Weaken 弱化后 Weight 下降（不低于 0）
//   - TickElimination 低权重移入淘汰池
//   - Revive 从淘汰池复活
// ============================================================================

package profile

import (
	"math"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// approxEqual 浮点近似比较（容忍 1e-9 误差）。
func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// TestEbbinghausR_Boundary 验证 EbbinghausR 边界值。
func TestEbbinghausR_Boundary(t *testing.T) {
	tau := 604800.0 // 7 天（秒）

	// t=0 → R=1（无衰减）。
	if r := EbbinghausR(0, tau); !approxEqual(r, 1.0) {
		t.Fatalf("EbbinghausR(0, tau) = %v, want 1.0", r)
	}

	// t=tau → R=e^-1 ≈ 0.36787944117144233。
	want := math.Exp(-1)
	r := EbbinghausR(time.Duration(tau)*time.Second, tau)
	if !approxEqual(r, want) {
		t.Fatalf("EbbinghausR(tau, tau) = %v, want %v (e^-1)", r, want)
	}
	// 显式断言 ≈0.368（容忍 1e-3）。
	if math.Abs(r-0.368) > 1e-3 {
		t.Fatalf("EbbinghausR(tau, tau) = %v, want ≈0.368", r)
	}

	// t=2*tau → R=e^-2 ≈ 0.1353。
	r2 := EbbinghausR(2*time.Duration(tau)*time.Second, tau)
	if !approxEqual(r2, math.Exp(-2)) {
		t.Fatalf("EbbinghausR(2*tau, tau) = %v, want %v (e^-2)", r2, math.Exp(-2))
	}

	// tau<=0 → 返回 0（立即遗忘）。
	if r := EbbinghausR(time.Hour, 0); r != 0 {
		t.Fatalf("EbbinghausR(t, 0) = %v, want 0", r)
	}
	if r := EbbinghausR(time.Hour, -1); r != 0 {
		t.Fatalf("EbbinghausR(t, -1) = %v, want 0", r)
	}
}

// TestEbbinghausR_Decreasing 验证 R 随时间单调递减。
func TestEbbinghausR_Decreasing(t *testing.T) {
	tau := 3600.0
	r1 := EbbinghausR(1*time.Second, tau)
	r2 := EbbinghausR(60*time.Second, tau)
	r3 := EbbinghausR(3600*time.Second, tau)
	if !(r1 > r2 && r2 > r3) {
		t.Fatalf("R 应单调递减: r1=%v r2=%v r3=%v", r1, r2, r3)
	}
	if !(r1 <= 1.0 && r3 > 0) {
		t.Fatalf("R 范围错误: r1=%v r3=%v", r1, r3)
	}
}

// TestDecayEngine_Decay 验证衰减后 Weight 下降，且超阈值时 Archived。
func TestDecayEngine_Decay(t *testing.T) {
	e := NewDecayEngine()
	now := time.Now()

	// 初始 Weight=1.0，tau=3600 秒，经过 1 小时后 R=e^-1≈0.368。
	entry := &domain.Interest{
		Tag:              "AI",
		Weight:           1.0,
		Strength:         3,
		LastReinforcedAt: now.Add(-time.Hour),
		DecayTau:         3600,
	}
	original := entry.Weight
	e.Decay(entry, now)

	// Weight 应下降（*= e^-1）。
	want := original * math.Exp(-1)
	if !approxEqual(entry.Weight, want) {
		t.Fatalf("Decay 后 Weight = %v, want %v", entry.Weight, want)
	}
	if entry.Weight >= original {
		t.Fatalf("Decay 后 Weight 应下降: got %v, original %v", entry.Weight, original)
	}
	// Weight≈0.368 > DefaultArchiveThreshold(0.05)，不应 Archived。
	if entry.Archived {
		t.Fatalf("Weight=%v 不应 Archived", entry.Weight)
	}
}

// TestDecayEngine_Decay_Archived 验证 Weight 低于阈值时标记 Archived。
func TestDecayEngine_Decay_Archived(t *testing.T) {
	e := NewDecayEngine()
	now := time.Now()

	// 经过很长时间，R 极小，Weight 应低于阈值。
	entry := &domain.Interest{
		Tag:              "AI",
		Weight:           1.0,
		LastReinforcedAt: now.Add(-365 * 24 * time.Hour), // 1 年前
		DecayTau:         3600,                           // 1 小时 tau
	}
	e.Decay(entry, now)

	if !entry.Archived {
		t.Fatalf("Weight=%v 应标记 Archived (threshold=%v)", entry.Weight, DefaultArchiveThreshold)
	}
	if entry.Weight >= DefaultArchiveThreshold {
		t.Fatalf("Weight=%v 应低于阈值 %v", entry.Weight, DefaultArchiveThreshold)
	}
}

// TestDecayEngine_Decay_ZeroLastReinforced 验证未强化过的条目不衰减。
func TestDecayEngine_Decay_ZeroLastReinforced(t *testing.T) {
	e := NewDecayEngine()
	now := time.Now()

	entry := &domain.Interest{
		Tag:    "AI",
		Weight: 1.0,
		// LastReinforcedAt 零值
		DecayTau: 3600,
	}
	e.Decay(entry, now)
	if entry.Weight != 1.0 {
		t.Fatalf("未强化条目不应衰减: Weight=%v, want 1.0", entry.Weight)
	}
}

// TestDecayEngine_Decay_DefaultTau 验证 DecayTau<=0 时回退到默认 tau。
func TestDecayEngine_Decay_DefaultTau(t *testing.T) {
	e := NewDecayEngine()
	now := time.Now()

	entry := &domain.Interest{
		Tag:              "AI",
		Weight:           1.0,
		LastReinforcedAt: now.Add(-time.Hour),
		DecayTau:         0, // 应回退到 DefaultDecayTauSec（7 天）
	}
	e.Decay(entry, now)

	// 1 小时 vs 7 天 tau，R 应接近 1（衰减很慢）。
	want := 1.0 * math.Exp(-(time.Hour.Seconds())/domain.DefaultDecayTauSec)
	if !approxEqual(entry.Weight, want) {
		t.Fatalf("默认 tau 衰减后 Weight = %v, want %v", entry.Weight, want)
	}
	if entry.Weight < 0.9 {
		t.Fatalf("1 小时相对 7 天 tau 衰减应很小: Weight=%v", entry.Weight)
	}
}

// TestDecayEngine_Reinforce 验证强化：Weight 上升 + LastReinforcedAt 更新 + Strength+1。
func TestDecayEngine_Reinforce(t *testing.T) {
	e := NewDecayEngine()
	old := time.Now().Add(-time.Hour)
	now := time.Now()

	entry := &domain.Interest{
		Tag:              "AI",
		Weight:           0.5,
		Strength:         2,
		LastReinforcedAt: old,
		Archived:         true,
	}
	e.Reinforce(entry, 0.3, now)

	if entry.Weight != 0.8 {
		t.Fatalf("Reinforce 后 Weight = %v, want 0.8", entry.Weight)
	}
	if !entry.LastReinforcedAt.Equal(now) {
		t.Fatalf("LastReinforcedAt = %v, want %v", entry.LastReinforcedAt, now)
	}
	if entry.Strength != 3 {
		t.Fatalf("Strength = %v, want 3", entry.Strength)
	}
	if entry.Archived {
		t.Fatalf("Reinforce 后 Archived 应为 false（复活语义）")
	}
}

// TestDecayEngine_Reinforce_ZeroDelta 验证 delta<=0 时仅刷新时间不增权重。
func TestDecayEngine_Reinforce_ZeroDelta(t *testing.T) {
	e := NewDecayEngine()
	now := time.Now()
	entry := &domain.Interest{Tag: "AI", Weight: 0.5, LastReinforcedAt: now.Add(-time.Hour)}

	e.Reinforce(entry, 0, now)
	if entry.Weight != 0.5 {
		t.Fatalf("delta=0 时 Weight 不应变: got %v, want 0.5", entry.Weight)
	}
	if !entry.LastReinforcedAt.Equal(now) {
		t.Fatalf("LastReinforcedAt 应刷新")
	}
}

// TestDecayEngine_Weaken 验证弱化：Weight 下降但不低于 0。
func TestDecayEngine_Weaken(t *testing.T) {
	e := NewDecayEngine()
	now := time.Now()

	// 正常弱化。
	entry := &domain.Interest{Tag: "AI", Weight: 0.8}
	e.Weaken(entry, 0.3, now)
	if entry.Weight != 0.5 {
		t.Fatalf("Weaken 后 Weight = %v, want 0.5", entry.Weight)
	}

	// 弱化到负数应截断到 0。
	entry2 := &domain.Interest{Tag: "AI", Weight: 0.2}
	e.Weaken(entry2, 0.5, now)
	if entry2.Weight != 0 {
		t.Fatalf("Weaken 后 Weight = %v, want 0（不低于 0）", entry2.Weight)
	}

	// delta<=0 无操作。
	entry3 := &domain.Interest{Tag: "AI", Weight: 0.5}
	e.Weaken(entry3, 0, now)
	if entry3.Weight != 0.5 {
		t.Fatalf("delta=0 时 Weight 不应变: got %v", entry3.Weight)
	}
}

// TestDecayEngine_TickElimination 验证低权重移入淘汰池。
func TestDecayEngine_TickElimination(t *testing.T) {
	e := NewDecayEngine()
	now := time.Now()
	tp := domain.NewTemporalProfile(now)

	// LongTerm: 一个高权重（保留），一个低权重（淘汰）。
	tp.LongTerm = append(tp.LongTerm, domain.Interest{Tag: "AI", Weight: 0.8, LastReinforcedAt: now})
	tp.LongTerm = append(tp.LongTerm, domain.Interest{Tag: "旧闻", Weight: 0.01, LastReinforcedAt: now})
	// ShortTerm: 一个低权重（淘汰）。
	tp.ShortTerm = append(tp.ShortTerm, domain.Interest{Tag: "过期", Weight: 0.02, LastReinforcedAt: now})

	e.TickElimination(tp, 0.05, now)

	// LongTerm 应剩 1 条（AI）。
	if len(tp.LongTerm) != 1 || tp.LongTerm[0].Tag != "AI" {
		t.Fatalf("TickElimination 后 LongTerm = %+v, want [AI]", tp.LongTerm)
	}
	// ShortTerm 应空。
	if len(tp.ShortTerm) != 0 {
		t.Fatalf("TickElimination 后 ShortTerm = %+v, want 空", tp.ShortTerm)
	}
	// EliminationEntries 应含 2 条（旧闻 + 过期）。
	if len(tp.EliminationEntries) != 2 {
		t.Fatalf("EliminationEntries len = %d, want 2", len(tp.EliminationEntries))
	}
	// EliminationPool（[]string）也应含 2 个 tag。
	if len(tp.EliminationPool) != 2 {
		t.Fatalf("EliminationPool len = %d, want 2", len(tp.EliminationPool))
	}
	// 淘汰条目应标记 Archived。
	for _, en := range tp.EliminationEntries {
		if !en.Archived {
			t.Fatalf("淘汰条目 %s 应 Archived=true", en.Tag)
		}
	}
	// GlobalDecayState.LastDecayAt 应刷新。
	if !tp.GlobalDecayState.LastDecayAt.Equal(now) {
		t.Fatalf("GlobalDecayState.LastDecayAt = %v, want %v", tp.GlobalDecayState.LastDecayAt, now)
	}
}

// TestDecayEngine_TickElimination_DefaultThreshold 验证 threshold<=0 时使用默认阈值。
func TestDecayEngine_TickElimination_DefaultThreshold(t *testing.T) {
	e := NewDecayEngine()
	now := time.Now()
	tp := domain.NewTemporalProfile(now)
	tp.LongTerm = append(tp.LongTerm, domain.Interest{Tag: "低权重", Weight: 0.01, LastReinforcedAt: now})

	e.TickElimination(tp, 0, now) // threshold=0 → 使用 DefaultArchiveThreshold(0.05)

	if len(tp.LongTerm) != 0 {
		t.Fatalf("默认阈值淘汰后 LongTerm 应空: got %+v", tp.LongTerm)
	}
	if len(tp.EliminationEntries) != 1 {
		t.Fatalf("EliminationEntries len = %d, want 1", len(tp.EliminationEntries))
	}
}

// TestDecayEngine_Revive 验证从淘汰池复活到 ShortTerm。
func TestDecayEngine_Revive(t *testing.T) {
	e := NewDecayEngine()
	now := time.Now()
	tp := domain.NewTemporalProfile(now)

	// 准备淘汰池：一个已淘汰条目。
	tp.EliminationEntries = append(tp.EliminationEntries, domain.Interest{
		Tag:              "旧闻",
		Weight:           0.01,
		Strength:         2,
		LastReinforcedAt: now.Add(-72 * time.Hour),
		DecayTau:         604800,
		Archived:         true,
	})
	tp.EliminationPool = append(tp.EliminationPool, "旧闻")

	ok := e.Revive(tp, "旧闻", now)
	if !ok {
		t.Fatalf("Revive 应成功返回 true")
	}

	// 淘汰池应清空。
	if len(tp.EliminationEntries) != 0 || len(tp.EliminationPool) != 0 {
		t.Fatalf("Revive 后淘汰池应清空: entries=%d pool=%d", len(tp.EliminationEntries), len(tp.EliminationPool))
	}
	// ShortTerm 应含复活条目。
	if len(tp.ShortTerm) != 1 {
		t.Fatalf("ShortTerm len = %d, want 1", len(tp.ShortTerm))
	}
	revived := tp.ShortTerm[0]
	if revived.Tag != "旧闻" {
		t.Fatalf("复活条目 Tag = %q, want 旧闻", revived.Tag)
	}
	if revived.Archived {
		t.Fatalf("复活后 Archived 应为 false")
	}
	if !revived.LastReinforcedAt.Equal(now) {
		t.Fatalf("复活后 LastReinforcedAt 应刷新为 now")
	}
	// Weight 应被提升到至少 DefaultArchiveThreshold。
	if revived.Weight < DefaultArchiveThreshold {
		t.Fatalf("复活后 Weight = %v, 应 >= %v", revived.Weight, DefaultArchiveThreshold)
	}
}

// TestDecayEngine_Revive_NotFound 验证复活不存在的 tag 返回 false。
func TestDecayEngine_Revive_NotFound(t *testing.T) {
	e := NewDecayEngine()
	tp := domain.NewTemporalProfile(time.Now())

	if ok := e.Revive(tp, "不存在", time.Now()); ok {
		t.Fatalf("复活不存在的 tag 应返回 false")
	}
}

// TestDecayEngine_FullLifecycle 验证完整生命周期：强化 → 衰减 → 淘汰 → 复活。
func TestDecayEngine_FullLifecycle(t *testing.T) {
	e := NewDecayEngine()
	t0 := time.Now()

	tp := domain.NewTemporalProfile(t0)
	entry := domain.NewInterest("AI", 0.8, t0)
	tp.LongTerm = append(tp.LongTerm, entry)

	// 1. 强化。
	e.Reinforce(&tp.LongTerm[0], 0.2, t0)
	if tp.LongTerm[0].Weight != 1.0 {
		t.Fatalf("强化后 Weight = %v, want 1.0", tp.LongTerm[0].Weight)
	}

	// 2. 衰减（模拟 1 年后，R 极小）。
	future := t0.Add(365 * 24 * time.Hour)
	e.Decay(&tp.LongTerm[0], future)
	if tp.LongTerm[0].Weight >= 1.0 {
		t.Fatalf("衰减后 Weight 应下降: got %v", tp.LongTerm[0].Weight)
	}

	// 3. 淘汰（应被移入淘汰池）。
	e.TickElimination(tp, 0.05, future)
	if len(tp.LongTerm) != 0 {
		t.Fatalf("淘汰后 LongTerm 应空: got %+v", tp.LongTerm)
	}
	if len(tp.EliminationEntries) != 1 {
		t.Fatalf("淘汰池应有 1 条: got %d", len(tp.EliminationEntries))
	}

	// 4. 复活。
	ok := e.Revive(tp, "AI", future.Add(time.Hour))
	if !ok {
		t.Fatalf("复活应成功")
	}
	if len(tp.ShortTerm) != 1 || tp.ShortTerm[0].Tag != "AI" {
		t.Fatalf("复活后 ShortTerm = %+v, want [AI]", tp.ShortTerm)
	}
}

// TestNewDecayEngineWithClock 验证时钟注入。
func TestNewDecayEngineWithClock(t *testing.T) {
	fixed := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	e := NewDecayEngineWithClock(func() time.Time { return fixed })

	if !e.Now().Equal(fixed) {
		t.Fatalf("Now() = %v, want %v", e.Now(), fixed)
	}
}

// TestNewDecayEngineWithClock_Nil 验证 nil 时钟回退到 time.Now。
func TestNewDecayEngineWithClock_Nil(t *testing.T) {
	e := NewDecayEngineWithClock(nil)
	if e.Now().IsZero() {
		t.Fatalf("nil 时钟回退后 Now() 不应为零值")
	}
}

// TestDecayEngine_NilSafety 验证 nil 安全（不 panic）。
func TestDecayEngine_NilSafety(t *testing.T) {
	var e *DecayEngine
	now := time.Now()

	// 所有方法在 nil 引擎或 nil entry 时应安全返回。
	e.Decay(nil, now)
	e.Reinforce(nil, 0.1, now)
	e.Weaken(nil, 0.1, now)
	e.TickElimination(nil, 0.05, now)
	if e.Revive(nil, "AI", now) {
		t.Fatalf("nil 引擎 Revive 应返回 false")
	}

	e2 := NewDecayEngine()
	e2.Decay(nil, now)
	e2.Reinforce(nil, 0.1, now)
	e2.Weaken(nil, 0.1, now)
	e2.TickElimination(nil, 0.05, now)
	if e2.Revive(nil, "AI", now) {
		t.Fatalf("nil profile Revive 应返回 false")
	}

	entry := &domain.Interest{Tag: "AI", Weight: 1.0}
	// 不应 panic。
	e2.Decay(entry, now)
}
