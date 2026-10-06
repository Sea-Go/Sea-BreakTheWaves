package planner

import (
	"context"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// TestRouteBoundaries 长度边界（按 rune 计）：>100 → deep、>30 →
// balanced、否则 fast。构造串不含触发词与实体标记。
func TestRouteBoundaries(t *testing.T) {
	ctx := context.Background()
	r := NewRouter()
	cases := []struct {
		n    int
		want retrieval.Tier
	}{
		{n: 1, want: retrieval.TierFast},
		{n: 30, want: retrieval.TierFast}, // 30 不大于 30
		{n: 31, want: retrieval.TierBalanced},
		{n: 100, want: retrieval.TierBalanced}, // 100 不大于 100
		{n: 101, want: retrieval.TierDeep},
	}
	for _, tc := range cases {
		q := strings.Repeat("搜", tc.n)
		if got := r.Route(ctx, q); got != tc.want {
			t.Errorf("长度 %d 应判 %q，得到 %q", tc.n, tc.want, got)
		}
	}
}

// TestRouteTriggers deep 触发词：比较/分析/为什么——短查询也直升 deep。
func TestRouteTriggers(t *testing.T) {
	ctx := context.Background()
	r := NewRouter()
	for _, q := range []string{
		"比较 A 和 B",
		"分析下这篇文档",
		"为什么需要树折叠",
	} {
		if got := r.Route(ctx, q); got != retrieval.TierDeep {
			t.Errorf("触发词查询 %q 应判 deep，得到 %q", q, got)
		}
	}
}

// TestRouteEntities 多实体近似：≥2 个 ASCII 词元或书名号 → balanced。
func TestRouteEntities(t *testing.T) {
	ctx := context.Background()
	r := NewRouter()
	if got := r.Route(ctx, "Milvus 和 Qdrant 哪个好"); got != retrieval.TierBalanced {
		t.Errorf("双英文实体应判 balanced，得到 %q", got)
	}
	if got := r.Route(ctx, "《检索》与《融合》的关系"); got != retrieval.TierBalanced {
		t.Errorf("双书名号应判 balanced，得到 %q", got)
	}
	// 单实体 + 短文本 → fast。
	if got := r.Route(ctx, "Sea 是什么"); got != retrieval.TierFast {
		t.Errorf("单实体短查询应判 fast，得到 %q", got)
	}
}

// TestRouteDegenerate 退化输入：空查询判 fast（下游规划器显式报错，
// 判档层不重复拦截）；ctx 取消兜底 fast（最低成本档）。
func TestRouteDegenerate(t *testing.T) {
	if got := NewRouter().Route(context.Background(), ""); got != retrieval.TierFast {
		t.Errorf("空查询应判 fast，得到 %q", got)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := NewRouter().Route(canceled, "比较复杂的查询"); got != retrieval.TierFast {
		t.Errorf("ctx 取消应兜底 fast，得到 %q", got)
	}
}

// TestMaxTier 档间只升不降（§3.1）：取两档中更高者，对称。
func TestMaxTier(t *testing.T) {
	cases := []struct {
		a, b, want retrieval.Tier
	}{
		{retrieval.TierFast, retrieval.TierBalanced, retrieval.TierBalanced},
		{retrieval.TierBalanced, retrieval.TierFast, retrieval.TierBalanced},
		{retrieval.TierBalanced, retrieval.TierDeep, retrieval.TierDeep},
		{retrieval.TierDeep, retrieval.TierFast, retrieval.TierDeep},
		{retrieval.TierFast, retrieval.TierFast, retrieval.TierFast},
		{retrieval.TierDeep, retrieval.TierDeep, retrieval.TierDeep},
	}
	for _, tc := range cases {
		if got := MaxTier(tc.a, tc.b); got != tc.want {
			t.Errorf("MaxTier(%q,%q) = %q，期望 %q", tc.a, tc.b, got, tc.want)
		}
	}
}
