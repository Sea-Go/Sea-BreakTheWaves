package main

import (
	"bytes"
	"context"
	"math"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// seedsRelPath 是从本包目录到仓库根种子集的相对路径（Go 工具链忽略
// testdata 通配，需显式路径）。
const seedsRelPath = "../../../../../testdata/index/seeds"

// 冒烟：跑通完整评测，三档指标均非零，且同输入两次 Report 深相等
// （确定性）。
func TestRetrEvalSmoke(t *testing.T) {
	seeds := filepath.Clean(seedsRelPath)
	ctx := context.Background()

	var buf bytes.Buffer
	reports, err := runAll(ctx, seeds, &buf)
	if err != nil {
		t.Fatalf("runAll: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("档位数 %d want 3", len(reports))
	}
	for _, tier := range []retrieval.Tier{retrieval.TierFast, retrieval.TierBalanced, retrieval.TierDeep} {
		rep, ok := reports[tier]
		if !ok {
			t.Fatalf("缺 %s 档报告", tier)
		}
		if rep.NumQueries != 50 {
			t.Fatalf("%s NumQueries=%d want 50", tier, rep.NumQueries)
		}
		// 非零指标：dev 口径下查询表示来自 gold，三档都应显著优于随机。
		if rep.NDCGAt10 <= 0 || math.IsNaN(rep.NDCGAt10) {
			t.Fatalf("%s nDCG@10=%v 应非零", tier, rep.NDCGAt10)
		}
		if rep.CappedRecallAt100 <= 0 {
			t.Fatalf("%s R_cap@100=%v 应非零", tier, rep.CappedRecallAt100)
		}
		if rep.MRRAt10 <= 0 {
			t.Fatalf("%s MRR@10=%v 应非零", tier, rep.MRRAt10)
		}
	}

	out := buf.String()
	for _, want := range []string{"retr_eval dev:", "tier      ndcg_at10", "fast", "balanced", "deep", "q-00"} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Fatalf("输出缺少 %q；实际输出前 200 字节: %q", want, out[:min(200, len(out))])
		}
	}

	// 确定性：同输入两次评测 Report 深相等。
	reports2, err := runAll(ctx, seeds, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("第二次 runAll: %v", err)
	}
	if !reflect.DeepEqual(reports, reports2) {
		t.Fatal("同输入两次评测的 Report 不相等（确定性被破坏）")
	}

	// 单档抽查：三档指标都在合理区间（[0,1]）。
	for tier, rep := range reports {
		for _, v := range []float64{rep.NDCGAt10, rep.RecallAt100, rep.CappedRecallAt100, rep.MRRAt10} {
			if v < 0 || v > 1 || math.IsNaN(v) {
				t.Fatalf("%s 指标越界: %v", tier, v)
			}
		}
	}
}
