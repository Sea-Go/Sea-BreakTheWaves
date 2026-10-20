package planner

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// TestPlanValidate 校验出口契约：rewritten_query 非空、档位合法、
// 预算与档位一致（fast=0/balanced=1/deep=K≥1）、列表项非空。
func TestPlanValidate(t *testing.T) {
	stepback := "这篇文档的整体架构是什么"
	cases := []struct {
		name string
		plan Plan
		ok   bool
	}{
		{
			name: "fast 恒等计划合法",
			plan: Plan{RewrittenQuery: "检索树", Tier: retrieval.TierFast, LLMBudget: 0},
			ok:   true,
		},
		{
			name: "balanced 规则计划合法",
			plan: Plan{RewrittenQuery: "什么是检索树", Variants: []string{"检索树"},
				Tier: retrieval.TierBalanced, LLMBudget: 1},
			ok: true,
		},
		{
			name: "deep 分解计划合法（budget=K）",
			plan: Plan{RewrittenQuery: "A；B", Subqueries: []string{"A", "B"},
				Tier: retrieval.TierDeep, LLMBudget: 2, Stepback: &stepback},
			ok: true,
		},
		{name: "rewritten_query 为空", plan: Plan{Tier: retrieval.TierFast}, ok: false},
		{name: "rewritten_query 全空白", plan: Plan{RewrittenQuery: "  ", Tier: retrieval.TierFast}, ok: false},
		{name: "未知档位", plan: Plan{RewrittenQuery: "q", Tier: retrieval.Tier("ultra")}, ok: false},
		{name: "fast 预算非 0", plan: Plan{RewrittenQuery: "q", Tier: retrieval.TierFast, LLMBudget: 1}, ok: false},
		{name: "balanced 预算非 1", plan: Plan{RewrittenQuery: "q", Tier: retrieval.TierBalanced, LLMBudget: 2}, ok: false},
		{name: "deep 预算不等于 K", plan: Plan{RewrittenQuery: "q", Subqueries: []string{"A", "B"},
			Tier: retrieval.TierDeep, LLMBudget: 1}, ok: false},
		{name: "deep 无子查询", plan: Plan{RewrittenQuery: "q", Tier: retrieval.TierDeep, LLMBudget: 0}, ok: false},
		{name: "variants 含空串", plan: Plan{RewrittenQuery: "q", Variants: []string{" "},
			Tier: retrieval.TierBalanced, LLMBudget: 1}, ok: false},
		{name: "subqueries 含空串", plan: Plan{RewrittenQuery: "q", Subqueries: []string{""},
			Tier: retrieval.TierDeep, LLMBudget: 1}, ok: false},
	}
	for _, tc := range cases {
		err := tc.plan.Validate()
		if tc.ok && err != nil {
			t.Errorf("%s: 意外报错: %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: 期望报错，得到 nil", tc.name)
		}
	}
	// 未知档位的校验错误须可 errors.Is 判别。
	err := Plan{RewrittenQuery: "q", Tier: retrieval.Tier("ultra")}.Validate()
	if !errors.Is(err, ErrInvalidTier) {
		t.Fatalf("未知档位应包 ErrInvalidTier，得到 %v", err)
	}
}

// TestTierBudgets 钉死 §3.1 三档预算：fast=0、balanced=1、deep=K。
func TestTierBudgets(t *testing.T) {
	ctx := context.Background()

	fast, err := Identity{}.Plan(ctx, "Sea 检索树", retrieval.TierFast)
	if err != nil {
		t.Fatal(err)
	}
	if fast.LLMBudget != 0 {
		t.Fatalf("fast 预算应为 0，得到 %d", fast.LLMBudget)
	}

	balanced, err := Rule{}.Plan(ctx, "什么是检索树", retrieval.TierBalanced)
	if err != nil {
		t.Fatal(err)
	}
	if balanced.LLMBudget != 1 {
		t.Fatalf("balanced 预算应为 1，得到 %d", balanced.LLMBudget)
	}

	query := "A；B？C和D以及E" // 5 个子查询
	deep, err := (&Decomposer{}).Plan(ctx, query, retrieval.TierDeep)
	if err != nil {
		t.Fatal(err)
	}
	if deep.LLMBudget != len(deep.Subqueries) {
		t.Fatalf("deep 预算应为 K=len(subqueries)=%d，得到 %d", len(deep.Subqueries), deep.LLMBudget)
	}
	if deep.LLMBudget != 5 {
		t.Fatalf("deep 预算应为 5，得到 %d", deep.LLMBudget)
	}
}

// TestPlanJSONRoundTrip 序列化往返：全字段计划 marshal→unmarshal 后
// 深相等（含 stepback 指针）；空计划的可选字段保持 nil。
func TestPlanJSONRoundTrip(t *testing.T) {
	stepback := "这篇文档的整体架构是什么"
	in := Plan{
		RewrittenQuery: "部署与回滚",
		Variants:       []string{"上线与回滚", "部署 回滚"},
		Stepback:       &stepback,
		Subqueries:     []string{"部署", "回滚"},
		Tier:           retrieval.TierDeep,
		LLMBudget:      2,
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Plan
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("往返后计划改变:\n入: %+v\n出: %+v", in, out)
	}
	if out.Stepback == nil || *out.Stepback != stepback {
		t.Fatalf("stepback 未往返: %+v", out.Stepback)
	}

	// 空计划：可选字段 omitempty，往返后保持 nil。
	empty := Plan{RewrittenQuery: "q", Tier: retrieval.TierFast}
	data, err = json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"variants", "stepback", "subqueries"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("空计划不应序列化 %s: %s", key, data)
		}
	}
	var emptyOut Plan
	if err := json.Unmarshal(data, &emptyOut); err != nil {
		t.Fatal(err)
	}
	if emptyOut.Variants != nil || emptyOut.Stepback != nil || emptyOut.Subqueries != nil {
		t.Fatalf("空计划往返后可选字段应为 nil: %+v", emptyOut)
	}
}

// TestPlanJSONContractKeys 钉死 §3.1 的线格式字段名（snake_case）。
func TestPlanJSONContractKeys(t *testing.T) {
	stepback := "s"
	in := Plan{
		RewrittenQuery: "q", Variants: []string{"v"}, Stepback: &stepback,
		Subqueries: []string{"s"}, Tier: retrieval.TierDeep, LLMBudget: 1,
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"rewritten_query": false, "variants": false, "stepback": false,
		"subqueries": false, "tier": false, "llm_budget": false,
	}
	for key := range raw {
		if _, ok := want[key]; !ok {
			t.Fatalf("出现契约外字段 %q: %s", key, data)
		}
		want[key] = true
	}
	for key, seen := range want {
		if !seen {
			t.Fatalf("缺少契约字段 %q: %s", key, data)
		}
	}
}
