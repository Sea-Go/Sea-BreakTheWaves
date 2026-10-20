package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/pipeline"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// seedsRelPath 是从本包目录到仓库根种子集的相对路径（Go 工具链忽略
// testdata 通配，需显式路径）。
const seedsRelPath = "../../../../../testdata/index/seeds"

// 冒烟：单档单交付跑通完整演示（装载→装配→执行→打印），输出含
// 装配横幅、路由行、格式化答案锚点与引用列表。
func TestSearchDemoSmoke(t *testing.T) {
	var buf bytes.Buffer
	tiers, err := parseTiers("fast")
	if err != nil {
		t.Fatal(err)
	}
	deliveries, err := parseDeliveries("summary")
	if err != nil {
		t.Fatal(err)
	}
	err = runDemo(context.Background(), filepath.Clean(seedsRelPath), tiers, deliveries,
		[]string{"海洋观测的核心要点"}, true, &buf)
	if err != nil {
		t.Fatalf("runDemo: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"search_demo dev:", "docs=20",
		"> 海洋观测的核心要点",
		"--- tier=fast delivery=summary ---",
		"route  : suggest=fast × user=fast → effective=fast",
		"answer : 根据",
		"[1](#cit-1)",
		"cites  : [1] doc-",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺少 %q；实际输出前 400 字节: %q", want, out[:min(400, len(out))])
		}
	}
}

// 参数解析：合法值展开、非法值报错。
func TestParseFlags(t *testing.T) {
	if ts, _ := parseTiers("all"); len(ts) != 3 || ts[0] != retrieval.TierFast || ts[2] != retrieval.TierDeep {
		t.Fatalf("--tier all 应展开三档: %v", ts)
	}
	if ds, _ := parseDeliveries("all"); len(ds) != 2 || ds[0] != pipeline.DeliverySummary || ds[1] != pipeline.DeliveryTools {
		t.Fatalf("--delivery all 应展开两交付: %v", ds)
	}
	if _, err := parseTiers("turbo"); err == nil {
		t.Fatal("--tier turbo 应报错")
	}
	if _, err := parseDeliveries("chat"); err == nil {
		t.Fatal("--delivery chat 应报错")
	}
}

// indentLines：多行文本的非首行缩进对齐。
func TestIndentLines(t *testing.T) {
	if got := indentLines("a\nb\nc", "  "); got != "a\n  b\n  c" {
		t.Fatalf("indentLines: %q", got)
	}
	if got := indentLines("", "  "); got != "" {
		t.Fatalf("空文本应原样返回: %q", got)
	}
}
