package planner

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// TestIdentityFast 恒等计划：RewrittenQuery 原样透传（含首尾空白）、
// 变体/子查询/stepback 全空、预算 0、Validate 通过、输出确定。
func TestIdentityFast(t *testing.T) {
	ctx := context.Background()
	query := "  Sea 检索树  "
	p, err := Identity{}.Plan(ctx, query, retrieval.TierFast)
	if err != nil {
		t.Fatal(err)
	}
	if p.RewrittenQuery != query {
		t.Fatalf("fast 档应原样透传，得到 %q", p.RewrittenQuery)
	}
	if p.Variants != nil || p.Subqueries != nil || p.Stepback != nil {
		t.Fatalf("恒等计划不应携带变体/子查询/stepback: %+v", p)
	}
	if p.Tier != retrieval.TierFast || p.LLMBudget != 0 {
		t.Fatalf("档位/预算错误: %+v", p)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("恒等计划应通过校验: %v", err)
	}

	// 输出确定性：同查询两次规划深相等。
	again, err := Identity{}.Plan(ctx, query, retrieval.TierFast)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, again) {
		t.Fatalf("恒等规划不确定:\n%+v\n%+v", p, again)
	}
}

// TestIdentityRejects 恒等规划器的错误契约：非 fast 档、未知档位、
// 空查询、ctx 取消。
func TestIdentityRejects(t *testing.T) {
	ctx := context.Background()
	for _, tier := range []retrieval.Tier{retrieval.TierBalanced, retrieval.TierDeep} {
		if _, err := (Identity{}).Plan(ctx, "q", tier); !errors.Is(err, ErrTierMismatch) {
			t.Fatalf("档位 %q 应报 ErrTierMismatch，得到 %v", tier, err)
		}
	}
	if _, err := (Identity{}).Plan(ctx, "q", retrieval.Tier("ultra")); !errors.Is(err, ErrInvalidTier) {
		t.Fatalf("未知档位应报 ErrInvalidTier，得到 %v", err)
	}
	if _, err := (Identity{}).Plan(ctx, "   ", retrieval.TierFast); !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("空查询应报 ErrEmptyQuery，得到 %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Identity{}).Plan(canceled, "q", retrieval.TierFast); !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx 取消应返回 context.Canceled，得到 %v", err)
	}
}
