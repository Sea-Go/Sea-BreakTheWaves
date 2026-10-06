package planner

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// TestRuleBalanced 规则计划：RewrittenQuery=TrimSpace 后原查询、
// 3 个确定性变体（去停用词/前 N 关键词/同义词查换）、预算 1、
// 不产 stepback、Validate 通过、输出确定。
func TestRuleBalanced(t *testing.T) {
	ctx := context.Background()
	p, err := Rule{}.Plan(ctx, "  什么是 Sea 的 检索树 设计  ", retrieval.TierBalanced)
	if err != nil {
		t.Fatal(err)
	}
	if p.RewrittenQuery != "什么是 Sea 的 检索树 设计" {
		t.Fatalf("RewrittenQuery 应为 TrimSpace 后原查询，得到 %q", p.RewrittenQuery)
	}
	want := []string{
		"Sea 检索树 设计",       // 变体一：去停用词（什么是/的 剔除，空白折叠）
		"什么是 Sea 检索树 设计",   // 变体二：前 N 关键词（词元"的"剔除，取前 4）
		"什么是 Sea 的 搜索树 设计", // 变体三：同义词查换（检索→搜索）
	}
	if !reflect.DeepEqual(p.Variants, want) {
		t.Fatalf("变体不符:\n得 %q\n期 %q", p.Variants, want)
	}
	if p.Tier != retrieval.TierBalanced || p.LLMBudget != 1 {
		t.Fatalf("档位/预算错误: %+v", p)
	}
	if p.Stepback != nil || p.Subqueries != nil {
		t.Fatalf("规则计划不应携带 stepback/subqueries: %+v", p)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("规则计划应通过校验: %v", err)
	}

	again, err := Rule{}.Plan(ctx, "  什么是 Sea 的 检索树 设计  ", retrieval.TierBalanced)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, again) {
		t.Fatalf("规则规划不确定:\n%+v\n%+v", p, again)
	}
}

// TestRuleVariantDedup 变体去重：与原查询相同的变体没有召回价值
// （§3.1 ④ 原查询永远保留为一路），应被剔除。
func TestRuleVariantDedup(t *testing.T) {
	ctx := context.Background()
	// "检索树"：去停用词/前 N 关键词两变体均退化为原句，仅同义词变体存活。
	p, err := Rule{}.Plan(ctx, "检索树", retrieval.TierBalanced)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Variants, []string{"搜索树"}) {
		t.Fatalf("去重后变体应为 [搜索树]，得到 %q", p.Variants)
	}
}

// TestRuleStopwordLongestFirst 停用词剔除最长优先："为什么"整词先于
// "什么"处理，不被短词拆散。
func TestRuleStopwordLongestFirst(t *testing.T) {
	p := Rule{}
	if got := p.dropStopwords("为什么需要树折叠"); got != "需要树折叠" {
		t.Fatalf("最长优先剔除应得 %q，得到 %q", "需要树折叠", got)
	}
}

// TestRuleSynonymSinglePass 同义词查换是单趟替换：替换产物不被重扫，
// "检索→搜索"的结果不会再被"搜索→检索"换回。
func TestRuleSynonymSinglePass(t *testing.T) {
	p := Rule{}
	if got := p.swapSynonyms("检索与搜索的取舍"); got != "搜索与检索的取舍" {
		t.Fatalf("单趟查换应得 %q，得到 %q", "搜索与检索的取舍", got)
	}
}

// TestRuleRejects 规则规划器的错误契约：非 balanced 档、未知档位、
// 空查询、ctx 取消。
func TestRuleRejects(t *testing.T) {
	ctx := context.Background()
	for _, tier := range []retrieval.Tier{retrieval.TierFast, retrieval.TierDeep} {
		if _, err := (Rule{}).Plan(ctx, "q", tier); !errors.Is(err, ErrTierMismatch) {
			t.Fatalf("档位 %q 应报 ErrTierMismatch，得到 %v", tier, err)
		}
	}
	if _, err := (Rule{}).Plan(ctx, "q", retrieval.Tier("ultra")); !errors.Is(err, ErrInvalidTier) {
		t.Fatalf("未知档位应报 ErrInvalidTier，得到 %v", err)
	}
	if _, err := (Rule{}).Plan(ctx, " \t ", retrieval.TierBalanced); !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("空查询应报 ErrEmptyQuery，得到 %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Rule{}).Plan(canceled, "q", retrieval.TierBalanced); !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx 取消应返回 context.Canceled，得到 %v", err)
	}
}
