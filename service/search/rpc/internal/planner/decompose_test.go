package planner

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// errPlanner 是注入错误的假规划器，用于断言分解器的递归错误传播。
type errPlanner struct{ err error }

func (p errPlanner) Plan(ctx context.Context, query string, tier retrieval.Tier) (Plan, error) {
	return Plan{}, p.err
}

// TestSplitSubqueries 切分规则：句界（分号/问号，中英）→ 连接词 →
// 去空 → 有界（≤5，超出取前 5）。
func TestSplitSubqueries(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{name: "句界+连接词混合", query: "A；B？C和D以及E", want: []string{"A", "B", "C", "D", "E"}},
		{name: "英文分号问号", query: "deploy;rollback?verify", want: []string{"deploy", "rollback", "verify"}},
		{name: "首尾空段剔除", query: "；部署流程；如何回滚？ ", want: []string{"部署流程", "如何回滚"}},
		{name: "无分隔符整句", query: "单一查询", want: []string{"单一查询"}},
		{name: "段内空白剔除", query: " 部署 ；  回滚 ", want: []string{"部署", "回滚"}},
		{name: "超过上限取前5", query: "一；二；三；四；五；六；七", want: []string{"一", "二", "三", "四", "五"}},
		{name: "纯分隔符为空", query: "？？？；；", want: nil},
	}
	for _, tc := range cases {
		if got := SplitSubqueries(tc.query); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: SplitSubqueries(%q) = %q，期望 %q", tc.name, tc.query, got, tc.want)
		}
	}
}

// TestDecomposeDeep deep 顶层计划：伞查询 + 子查询列表 + 预算 K，
// Validate 通过、输出确定。
func TestDecomposeDeep(t *testing.T) {
	ctx := context.Background()
	d := &Decomposer{}
	p, err := d.Plan(ctx, " 部署流程；如何回滚？ ", retrieval.TierDeep)
	if err != nil {
		t.Fatal(err)
	}
	if p.RewrittenQuery != "部署流程；如何回滚？" {
		t.Fatalf("伞查询应为 TrimSpace 后原句，得到 %q", p.RewrittenQuery)
	}
	if !reflect.DeepEqual(p.Subqueries, []string{"部署流程", "如何回滚"}) {
		t.Fatalf("子查询不符: %q", p.Subqueries)
	}
	if p.LLMBudget != 2 || p.Tier != retrieval.TierDeep {
		t.Fatalf("预算/档位错误: %+v", p)
	}
	if p.Variants != nil || p.Stepback != nil {
		t.Fatalf("顶层计划不应携带变体/stepback（在各子 Plan 内）: %+v", p)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("deep 计划应通过校验: %v", err)
	}

	again, err := d.Plan(ctx, " 部署流程；如何回滚？ ", retrieval.TierDeep)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, again) {
		t.Fatalf("分解规划不确定:\n%+v\n%+v", p, again)
	}
}

// TestDecomposeSingle 无分隔符查询退化为单子查询（K=1）。
func TestDecomposeSingle(t *testing.T) {
	ctx := context.Background()
	p, err := (&Decomposer{}).Plan(ctx, "为什么需要树折叠", retrieval.TierDeep)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Subqueries, []string{"为什么需要树折叠"}) {
		t.Fatalf("单子查询不符: %q", p.Subqueries)
	}
	if p.LLMBudget != 1 {
		t.Fatalf("K 应为 1，得到 %d", p.LLMBudget)
	}
}

// TestDecomposeSubPlans 子 Plan 契约：每个子查询经 Delegate（默认
// Rule）递归产"1 次规划"口径的子 Plan（Tier=balanced、预算 1），
// 对应 §3.1 deep 行"K 轮×1 次"。
func TestDecomposeSubPlans(t *testing.T) {
	ctx := context.Background()
	subs, err := (&Decomposer{}).SubPlans(ctx, "部署流程；如何回滚")
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 2 {
		t.Fatalf("子 Plan 数应为 2，得到 %d", len(subs))
	}
	first, second := subs[0], subs[1]
	if first.RewrittenQuery != "部署流程" || second.RewrittenQuery != "如何回滚" {
		t.Fatalf("子 Plan 的 rewritten_query 不符: %+v %+v", first, second)
	}
	for _, sp := range subs {
		if sp.Tier != retrieval.TierBalanced || sp.LLMBudget != 1 {
			t.Fatalf("子 Plan 应为 1 次规划口径: %+v", sp)
		}
		if err := sp.Validate(); err != nil {
			t.Fatalf("子 Plan 应通过校验: %v", err)
		}
	}
	// 子 Plan 内的变体随 Delegate 规则产出（如"部署流程"→"上线流程"）。
	if !reflect.DeepEqual(first.Variants, []string{"上线流程"}) {
		t.Fatalf("子 Plan 变体不符: %q", first.Variants)
	}
	if !reflect.DeepEqual(second.Variants, []string{"回滚"}) {
		t.Fatalf("子 Plan 变体不符: %q", second.Variants)
	}
}

// TestDecomposeErrorPropagation Delegate 递归报错时顶层失败并保留原错误。
func TestDecomposeErrorPropagation(t *testing.T) {
	sentinel := errors.New("boom")
	d := &Decomposer{Delegate: errPlanner{err: sentinel}}
	if _, err := d.Plan(context.Background(), "A；B", retrieval.TierDeep); !errors.Is(err, sentinel) {
		t.Fatalf("应传播 Delegate 错误，得到 %v", err)
	}
}

// TestDecomposeRejects 分解器的错误契约：非 deep 档、未知档位、
// 空查询、分解后无有效子查询、ctx 取消。
func TestDecomposeRejects(t *testing.T) {
	ctx := context.Background()
	d := &Decomposer{}
	for _, tier := range []retrieval.Tier{retrieval.TierFast, retrieval.TierBalanced} {
		if _, err := d.Plan(ctx, "q", tier); !errors.Is(err, ErrTierMismatch) {
			t.Fatalf("档位 %q 应报 ErrTierMismatch，得到 %v", tier, err)
		}
	}
	if _, err := d.Plan(ctx, "q", retrieval.Tier("ultra")); !errors.Is(err, ErrInvalidTier) {
		t.Fatalf("未知档位应报 ErrInvalidTier，得到 %v", err)
	}
	if _, err := d.Plan(ctx, "   ", retrieval.TierDeep); !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("空查询应报 ErrEmptyQuery，得到 %v", err)
	}
	if _, err := d.Plan(ctx, "？？？；；", retrieval.TierDeep); !errors.Is(err, ErrNoSubqueries) {
		t.Fatalf("纯分隔符应报 ErrNoSubqueries，得到 %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Plan(canceled, "q", retrieval.TierDeep); !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx 取消应返回 context.Canceled，得到 %v", err)
	}
}
