package rank

import (
	"testing"

	"sea/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件测试 Registry 排序器注册表，覆盖：
//   - Register + Get 正常流程
//   - 重复注册返回 error
//   - Get 不存在名称返回 error
//   - List 返回字典序排列的名称列表
//   - Register 空名称 / nil ranker 返回 error
// ============================================================================

// TestRegistry_RegisterAndGet 验证注册与获取。
func TestRegistry_RegisterAndGet(t *testing.T) {
	reg := NewRegistry()
	r := NewWeightedRanker(map[string]float64{"a": 1.0})
	if err := reg.Register("weighted", r); err != nil {
		t.Fatalf("Register 返回错误: %v", err)
	}
	got, err := reg.Get("weighted")
	if err != nil {
		t.Fatalf("Get 返回错误: %v", err)
	}
	if got != r {
		t.Errorf("Get 返回的排序器与注册的不一致")
	}
	if got.Name() != "weighted" {
		t.Errorf("Name() = %q, 期望 weighted", got.Name())
	}
}

// TestRegistry_DuplicateRegister 验证重复注册返回 error。
func TestRegistry_DuplicateRegister(t *testing.T) {
	reg := NewRegistry()
	r1 := NewWeightedRanker(nil)
	r2 := NewLRRanker(nil, 0)
	if err := reg.Register("dup", r1); err != nil {
		t.Fatalf("首次 Register 返回错误: %v", err)
	}
	if err := reg.Register("dup", r2); err == nil {
		t.Fatal("重复 Register 期望返回 error, 实际 nil")
	}
}

// TestRegistry_GetNotFound 验证获取不存在的名称返回 error。
func TestRegistry_GetNotFound(t *testing.T) {
	reg := NewRegistry()
	_, err := reg.Get("nonexistent")
	if err == nil {
		t.Fatal("期望返回 not found error, 实际 nil")
	}
}

// TestRegistry_List 验证 List 返回字典序排列的名称列表。
func TestRegistry_List(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register("gbdt", NewGBDTRanker(""))
	_ = reg.Register("weighted", NewWeightedRanker(nil))
	_ = reg.Register("lr", NewLRRanker(nil, 0))

	names := reg.List()
	if len(names) != 3 {
		t.Fatalf("List 返回 %d 个, 期望 3", len(names))
	}
	// 字典序: gbdt < lr < weighted
	if names[0] != "gbdt" || names[1] != "lr" || names[2] != "weighted" {
		t.Errorf("List = %v, 期望 [gbdt lr weighted]", names)
	}
}

// TestRegistry_ListEmpty 验证空注册表 List 返回空切片。
func TestRegistry_ListEmpty(t *testing.T) {
	reg := NewRegistry()
	names := reg.List()
	if len(names) != 0 {
		t.Errorf("空注册表 List 返回 %d 个, 期望 0", len(names))
	}
}

// TestRegistry_RegisterEmptyName 验证空名称注册返回 error。
func TestRegistry_RegisterEmptyName(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register("", NewWeightedRanker(nil)); err == nil {
		t.Fatal("空名称 Register 期望返回 error")
	}
}

// TestRegistry_RegisterNilRanker 验证 nil ranker 注册返回 error。
func TestRegistry_RegisterNilRanker(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register("nil", nil); err == nil {
		t.Fatal("nil ranker Register 期望返回 error")
	}
}

// TestRegistry_RegisterAllBuiltins 验证三种内置排序器均可注册并满足 interface。
func TestRegistry_RegisterAllBuiltins(t *testing.T) {
	reg := NewRegistry()
	var rankers []domain.Ranker = []domain.Ranker{
		NewWeightedRanker(map[string]float64{"a": 1.0}),
		NewLRRanker(map[string]float64{"a": 1.0}, 0),
		NewGBDTRanker("/models/gbdt.json"),
	}
	for _, r := range rankers {
		if err := reg.Register(r.Name(), r); err != nil {
			t.Fatalf("Register %q 返回错误: %v", r.Name(), err)
		}
	}
	if len(reg.List()) != 3 {
		t.Errorf("已注册 %d 个, 期望 3", len(reg.List()))
	}
}
