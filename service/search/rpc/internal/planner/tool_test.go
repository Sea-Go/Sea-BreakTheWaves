package planner

import (
	"context"
	"encoding/json"
	"testing"

	trpctool "trpc.group/trpc-go/trpc-agent-go/tool"
)

// callTool 走框架的真实调用路径（CallableTool.Call + JSON 参数）。
func callTool(t *testing.T, tool trpctool.Tool, args any) PlanToolResponse {
	t.Helper()
	callable, ok := tool.(trpctool.CallableTool)
	if !ok {
		t.Fatalf("tool %T is not a framework CallableTool", tool)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	out, err := callable.Call(context.Background(), raw)
	if err != nil {
		t.Fatalf("tool call: %v", err)
	}
	// 框架 function tool 返回 JSON 编码后的结构体。
	switch v := out.(type) {
	case PlanToolResponse:
		return v
	case []byte:
		var resp PlanToolResponse
		if err := json.Unmarshal(v, &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return resp
	case string:
		var resp PlanToolResponse
		if err := json.Unmarshal([]byte(v), &resp); err != nil {
			t.Fatalf("decode response string: %v", err)
		}
		return resp
	default:
		encoded, err := json.Marshal(out)
		if err != nil {
			t.Fatalf("re-encode response %T: %v", out, err)
		}
		var resp PlanToolResponse
		if err := json.Unmarshal(encoded, &resp); err != nil {
			t.Fatalf("decode re-encoded response: %v", err)
		}
		return resp
	}
}

// TestPlanToolDeclaresFrameworkMetadata 工具在框架里注册为命名工具。
func TestPlanToolDeclaresFrameworkMetadata(t *testing.T) {
	tool, err := NewDefaultPlanTool()
	if err != nil {
		t.Fatalf("NewDefaultPlanTool: %v", err)
	}
	decl := tool.Declaration()
	if decl == nil {
		t.Fatal("tool declaration is nil")
	}
	if decl.Name != "sea_query_plan" {
		t.Fatalf("tool name = %q, want sea_query_plan", decl.Name)
	}
	if decl.Description == "" {
		t.Fatal("tool description is empty")
	}
	if _, ok := tool.(trpctool.CallableTool); !ok {
		t.Fatal("tool must be callable through the framework")
	}
}

// TestPlanToolFastTier 短查询走 fast 档，预算 0（§3.1：fast 档 0 次 LLM）。
func TestPlanToolFastTier(t *testing.T) {
	tool, err := NewDefaultPlanTool()
	if err != nil {
		t.Fatalf("NewDefaultPlanTool: %v", err)
	}
	resp := callTool(t, tool, PlanToolRequest{Query: "海洋观测要点"})
	if resp.EffectiveTier != "fast" {
		t.Fatalf("effective tier = %q, want fast", resp.EffectiveTier)
	}
	if resp.LLMBudget != 0 {
		t.Fatalf("fast budget = %d, want 0", resp.LLMBudget)
	}
	if resp.RewrittenQuery == "" {
		t.Fatal("rewritten query is empty")
	}
}

// TestPlanToolUpgradesTier 含 deep 触发词时档位只升不降（§3.1）。
func TestPlanToolUpgradesTier(t *testing.T) {
	tool, err := NewDefaultPlanTool()
	if err != nil {
		t.Fatalf("NewDefaultPlanTool: %v", err)
	}
	resp := callTool(t, tool, PlanToolRequest{Query: "比较海洋观测与气候能源的差异", Tier: "fast"})
	if resp.SuggestedTier != "deep" {
		t.Fatalf("suggested tier = %q, want deep", resp.SuggestedTier)
	}
	if resp.EffectiveTier != "deep" {
		t.Fatalf("effective tier = %q, want deep（MaxTier 只升不降）", resp.EffectiveTier)
	}
}

// TestPlanToolDeepProducesSubqueries deep 档产出子查询并列预算 K。
func TestPlanToolDeepProducesSubqueries(t *testing.T) {
	tool, err := NewDefaultPlanTool()
	if err != nil {
		t.Fatalf("NewDefaultPlanTool: %v", err)
	}
	resp := callTool(t, tool, PlanToolRequest{Query: "分析海洋观测；并比较气候能源；最后给出结论", Tier: "deep"})
	if resp.EffectiveTier != "deep" {
		t.Fatalf("effective tier = %q, want deep", resp.EffectiveTier)
	}
	if len(resp.Subqueries) == 0 {
		t.Fatal("deep plan must carry subqueries")
	}
	if resp.LLMBudget != len(resp.Subqueries) {
		t.Fatalf("budget = %d, want K = %d", resp.LLMBudget, len(resp.Subqueries))
	}
}

// TestPlanToolBalancedProducesVariants balanced 档产变体并列预算 1。
func TestPlanToolBalancedProducesVariants(t *testing.T) {
	tool, err := NewDefaultPlanTool()
	if err != nil {
		t.Fatalf("NewDefaultPlanTool: %v", err)
	}
	resp := callTool(t, tool, PlanToolRequest{Query: "请检索海洋观测的文档配置与部署方式", Tier: "balanced"})
	if resp.EffectiveTier != "balanced" {
		t.Fatalf("effective tier = %q, want balanced", resp.EffectiveTier)
	}
	if len(resp.Variants) == 0 {
		t.Fatal("balanced plan must carry variants")
	}
	if resp.LLMBudget != 1 {
		t.Fatalf("balanced budget = %d, want 1", resp.LLMBudget)
	}
}

// TestPlanToolRejectsUnknownTier 未知档位明确报错，不静默降级。
func TestPlanToolRejectsUnknownTier(t *testing.T) {
	tool, err := NewDefaultPlanTool()
	if err != nil {
		t.Fatalf("NewDefaultPlanTool: %v", err)
	}
	callable := tool.(trpctool.CallableTool)
	raw, _ := json.Marshal(PlanToolRequest{Query: "海洋观测", Tier: "turbo"})
	if _, err := callable.Call(context.Background(), raw); err == nil {
		t.Fatal("expected error for unknown tier")
	}
}

// TestPlanToolRejectsEmptyQuery 空查询明确报错。
func TestPlanToolRejectsEmptyQuery(t *testing.T) {
	tool, err := NewDefaultPlanTool()
	if err != nil {
		t.Fatalf("NewDefaultPlanTool: %v", err)
	}
	callable := tool.(trpctool.CallableTool)
	raw, _ := json.Marshal(PlanToolRequest{Query: ""})
	if _, err := callable.Call(context.Background(), raw); err == nil {
		t.Fatal("expected error for empty query")
	}
}

// TestPlanToolRequiresAtLeastOnePlanner 无规划器即构造失败。
func TestPlanToolRequiresAtLeastOnePlanner(t *testing.T) {
	if _, err := NewPlanTool(PlannerToolDeps{}); err == nil {
		t.Fatal("expected error when no planner is configured")
	}
}

// TestPlanToolReportsMissingTierPlanner 某档缺规划器时明确报错。
func TestPlanToolReportsMissingTierPlanner(t *testing.T) {
	tool, err := NewPlanTool(PlannerToolDeps{Balanced: Rule{}})
	if err != nil {
		t.Fatalf("NewPlanTool: %v", err)
	}
	callable := tool.(trpctool.CallableTool)
	raw, _ := json.Marshal(PlanToolRequest{Query: "海洋观测", Tier: "fast"})
	if _, err := callable.Call(context.Background(), raw); err == nil {
		t.Fatal("expected error for tier without planner")
	}
}

// TestPlanToolDeterministic 同输入同输出（域层确定性经框架不丢失）。
func TestPlanToolDeterministic(t *testing.T) {
	tool, err := NewDefaultPlanTool()
	if err != nil {
		t.Fatalf("NewDefaultPlanTool: %v", err)
	}
	req := PlanToolRequest{Query: "检索海洋观测的文档配置", Tier: "balanced"}
	first := callTool(t, tool, req)
	for i := 0; i < 5; i++ {
		again := callTool(t, tool, req)
		if again.RewrittenQuery != first.RewrittenQuery || again.LLMBudget != first.LLMBudget ||
			len(again.Variants) != len(first.Variants) {
			t.Fatalf("run %d differs from first: %+v vs %+v", i, again, first)
		}
		for j := range first.Variants {
			if again.Variants[j] != first.Variants[j] {
				t.Fatalf("variant %d differs: %q vs %q", j, again.Variants[j], first.Variants[j])
			}
		}
	}
}
