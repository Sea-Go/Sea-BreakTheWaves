package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/devseed"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/planner"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/summary"
)

// seedsRelPath 是从本包目录到仓库根种子集的相对路径（Go 工具链忽略
// testdata 通配，需显式路径）。
const seedsRelPath = "../../../../../testdata/index/seeds"

// matrixQuery 是矩阵测试的固定查询：短、纯中文、无 deep 触发词、无
// 多实体标记——判档建议为 fast，三档请求各自保持原档（互异可断言）。
const matrixQuery = "海洋观测的核心要点"

var (
	seedsOnce sync.Once
	seedsPipe *Pipeline
	seedsErr  error
)

// newSeedsPipeline 用冻结种子集建默认管线（全链路：语料→假编码→工件
// 量化→装载→补源→装配；进程内只装载一次）。
func newSeedsPipeline(t *testing.T) *Pipeline {
	t.Helper()
	seedsOnce.Do(func() {
		corpus, err := devseed.LoadCorpus(filepath.Clean(seedsRelPath))
		if err != nil {
			seedsErr = err
			return
		}
		p, err := NewDefaultPipeline(corpus.Store)
		if err != nil {
			seedsErr = err
			return
		}
		seedsPipe = p
	})
	if seedsErr != nil {
		t.Fatalf("种子集装载: %v", seedsErr)
	}
	return seedsPipe
}

// 三档 × 两种交付全通过：summary 交付四字段全填且口径正确；tools 交付
// 只填 Pack。
func TestExecuteTierDeliveryMatrix(t *testing.T) {
	p := newSeedsPipeline(t)
	ctx := context.Background()

	for _, tier := range []retrieval.Tier{retrieval.TierFast, retrieval.TierBalanced, retrieval.TierDeep} {
		// 检索矩阵口径：fast 档候选不带 multi 路分数（跳 multi）；
		// balanced/deep 三路全开。
		var multiSeen bool
		for _, delivery := range []Delivery{DeliverySummary, DeliveryTools} {
			res, err := p.Execute(ctx, PipelineRequest{Query: matrixQuery, Tier: tier, Delivery: delivery})
			if err != nil {
				t.Fatalf("%s/%s: %v", tier, delivery, err)
			}

			// Pack（两种交付共有）：非空、query_id 确定性派生、候选有序。
			if len(res.Pack.Candidates) == 0 {
				t.Fatalf("%s/%s: 证据包无候选", tier, delivery)
			}
			if want := queryID(matrixQuery); res.Pack.QueryID != want {
				t.Fatalf("%s/%s: query_id=%q want %q", tier, delivery, res.Pack.QueryID, want)
			}
			if err := res.Pack.Validate(); err != nil {
				t.Fatalf("%s/%s: EvidencePack 校验: %v", tier, delivery, err)
			}
			for i, c := range res.Pack.Candidates {
				if tier == retrieval.TierFast && c.Lanes.Multi != 0 {
					t.Fatalf("fast 档候选 %s 不应有 multi 路分数: %+v", c.DocKey, c.Lanes)
				}
				if tier != retrieval.TierFast && c.Lanes.Multi > 0 {
					multiSeen = true
				}
				if i > 0 && res.Pack.Candidates[i-1].RRFScore < c.RRFScore {
					t.Fatalf("候选 RRF 降序被破坏: %v", res.Pack.Candidates)
				}
			}

			switch delivery {
			case DeliverySummary:
				if res.Answer == "" || !strings.Contains(res.Answer, "[1]") {
					t.Fatalf("%s/summary: 答案缺 [1] 角标: %q", tier, res.Answer)
				}
				if !strings.Contains(res.FormattedAnswer, "[1](#cit-1)") {
					t.Fatalf("%s/summary: 格式化答案缺锚点链接: %q", tier, res.FormattedAnswer)
				}
				if len(res.Citations) < 1 || len(res.Citations) > summary.MaxCitations {
					t.Fatalf("%s/summary: 引用数 %d 越界", tier, len(res.Citations))
				}
				if len(res.Pack.Candidates) >= summary.StubTopCandidates &&
					len(res.Citations) != summary.StubTopCandidates {
					t.Fatalf("%s/summary: 候选 ≥3 时引用应取前 3，得到 %d", tier, len(res.Citations))
				}
				for i, c := range res.Citations {
					if c.Index != i+1 || c.DocKey == "" || c.Locator.Quote == "" {
						t.Fatalf("%s/summary: 引用[%d] 口径不符: %+v", tier, i, c)
					}
				}
			case DeliveryTools:
				if res.Answer != "" || res.FormattedAnswer != "" || res.Citations != nil {
					t.Fatalf("%s/tools: 只应填 Pack，summary 字段应零值: %+v", tier, res)
				}
			}
		}
		if tier != retrieval.TierFast && !multiSeen {
			t.Fatalf("%s 档应有 multi 路分数为正的候选", tier)
		}
	}
}

// 确定性：同输入两次 Execute 产出深相等的完整结果（含 Pack 与引用）。
func TestExecuteDeterministic(t *testing.T) {
	p := newSeedsPipeline(t)
	ctx := context.Background()
	req := PipelineRequest{Query: matrixQuery, Tier: retrieval.TierBalanced, Delivery: DeliverySummary}

	first, err := p.Execute(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		again, err := p.Execute(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("第 %d 次执行结果不深相等（确定性被破坏）", i+1)
		}
	}
}

// 空查询（含全空白）拒绝；未知档位/交付形态拒绝；ctx 取消立即失败。
func TestExecuteRequestValidation(t *testing.T) {
	p := newSeedsPipeline(t)
	ctx := context.Background()

	for _, q := range []string{"", "   ", "\n\t"} {
		if _, err := p.Execute(ctx, PipelineRequest{Query: q, Tier: retrieval.TierFast, Delivery: DeliverySummary}); !errors.Is(err, planner.ErrEmptyQuery) {
			t.Fatalf("空查询 %q 应报 planner.ErrEmptyQuery: %v", q, err)
		}
	}
	if _, err := p.Execute(ctx, PipelineRequest{Query: matrixQuery, Tier: retrieval.Tier("turbo"), Delivery: DeliverySummary}); !errors.Is(err, planner.ErrInvalidTier) {
		t.Fatalf("未知档位应报 planner.ErrInvalidTier: %v", err)
	}
	if _, err := p.Execute(ctx, PipelineRequest{Query: matrixQuery, Tier: retrieval.TierFast, Delivery: Delivery("chat")}); !errors.Is(err, ErrInvalidDelivery) {
		t.Fatalf("未知交付形态应报 ErrInvalidDelivery: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.Execute(cancelled, PipelineRequest{Query: matrixQuery, Tier: retrieval.TierFast, Delivery: DeliveryTools}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 ctx 应返回 Canceled: %v", err)
	}

	// 注入缺位：Planner/Searcher 未注入、summary 交付缺 Summarizer 均报错。
	if _, err := (&Pipeline{Searcher: p.Searcher}).Execute(ctx, PipelineRequest{Query: matrixQuery, Tier: retrieval.TierFast, Delivery: DeliveryTools}); err == nil {
		t.Fatal("缺 Planner 应报错")
	}
	if _, err := (&Pipeline{Planner: planner.Rule{}}).Execute(ctx, PipelineRequest{Query: matrixQuery, Tier: retrieval.TierFast, Delivery: DeliveryTools}); err == nil {
		t.Fatal("缺 Searcher 应报错")
	}
	if _, err := NewDefaultPipeline(nil); err == nil {
		t.Fatal("nil store 应报错")
	}
}

// recordingSummarizer 记录是否被调用（tools 交付不得调 Summarizer）。
type recordingSummarizer struct {
	called bool
}

func (r *recordingSummarizer) Summarize(ctx context.Context, req summary.SummaryRequest) (summary.SummaryResult, error) {
	r.called = true
	return summary.NewStub().Summarize(ctx, req)
}

// tools 交付不调 Summarizer（B5 直返，不经 B6）；且 tools 交付允许
// Summarizer 缺席（nil 注入仍可执行）。
func TestExecuteToolsSkipsSummarizer(t *testing.T) {
	base := newSeedsPipeline(t)
	ctx := context.Background()

	rec := &recordingSummarizer{}
	p := &Pipeline{
		Planner:    planner.Rule{},
		Searcher:   base.Searcher,
		Summarizer: rec,
		Encoder:    FakeEncoder{},
	}
	if _, err := p.Execute(ctx, PipelineRequest{Query: matrixQuery, Tier: retrieval.TierBalanced, Delivery: DeliveryTools}); err != nil {
		t.Fatalf("tools 交付: %v", err)
	}
	if rec.called {
		t.Fatal("tools 交付不应调用 Summarizer")
	}

	bare := &Pipeline{Planner: planner.Rule{}, Searcher: base.Searcher, Encoder: FakeEncoder{}}
	if _, err := bare.Execute(ctx, PipelineRequest{Query: matrixQuery, Tier: retrieval.TierFast, Delivery: DeliveryTools}); err != nil {
		t.Fatalf("tools 交付应允许 Summarizer 缺席: %v", err)
	}
	if _, err := bare.Execute(ctx, PipelineRequest{Query: matrixQuery, Tier: retrieval.TierFast, Delivery: DeliverySummary}); err == nil {
		t.Fatal("summary 交付缺 Summarizer 应报错")
	}
}

// staticEncoder 返回固定表示（路由测试不依赖哈希编码的偶然相似度）。
type staticEncoder struct {
	repr QueryRepr
}

func (e staticEncoder) EncodeQuery(string) QueryRepr { return e.repr }

// 判档路由（§3.1 只升不降）：TopN=1 的合成库上，dense 路恰有两个正分
// 候选——fast/balanced 截断只留首位；deep 触发词（比较）使用户 fast 档
// 被升到 deep（双倍路内候选）后第二位浮出，MaxTier 升档可观察。
func TestExecuteRoutesTierUpgrade(t *testing.T) {
	ctx := context.Background()
	enc := staticEncoder{repr: QueryRepr{Dense: []float32{1, 0}}}

	docs := map[string]retrieval.LoadedDoc{}
	for key, dense := range map[string][]float32{
		"doc-a": {1, 0},       // cos 1.0
		"doc-b": {0.28, 0.96}, // cos 0.28（deep 放宽才浮出）
		"doc-c": {-1, 0},      // cos -1，无候选
	} {
		source := []byte("# " + key + " 的标题\n\n「" + key + "」正文段落，用于证据定位。\n")
		tree, err := devseed.DeriveTree(source, "rev-"+key)
		if err != nil {
			t.Fatal(err)
		}
		docs[key] = retrieval.LoadedDoc{Dense: dense, Tree: tree, Source: source}
	}
	store, err := retrieval.NewStore(docs)
	if err != nil {
		t.Fatal(err)
	}
	sh := retrieval.NewSearcher(store)
	sh.TopN = 1
	p := &Pipeline{Planner: planner.Rule{}, Searcher: sh, Encoder: enc}

	// 短查询无触发词：判档建议 fast，用户 fast 档保持 fast → 只留首位。
	res, err := p.Execute(ctx, PipelineRequest{Query: "甲乙丙的观测要点", Tier: retrieval.TierFast, Delivery: DeliveryTools})
	if err != nil {
		t.Fatal(err)
	}
	if keys := packKeys(res.Pack); len(keys) != 1 || keys[0] != "doc-a" {
		t.Fatalf("未升档的 fast 应只含 doc-a: %v", keys)
	}

	// deep 触发词（比较）：用户 fast 档被 MaxTier 升到 deep → 双倍候选，
	// doc-b 浮出。
	res, err = p.Execute(ctx, PipelineRequest{Query: "甲与乙方案的比较", Tier: retrieval.TierFast, Delivery: DeliveryTools})
	if err != nil {
		t.Fatal(err)
	}
	if keys := packKeys(res.Pack); len(keys) != 2 || keys[0] != "doc-a" || keys[1] != "doc-b" {
		t.Fatalf("升档后的 deep 应含 [doc-a doc-b]: %v", keys)
	}
}

// FakeEncoder 口径：与文档侧同维（dense 64 / multi 行宽 8）、行数 ≤8、
// 同文本同表示。
func TestFakeEncoderShape(t *testing.T) {
	e := FakeEncoder{}
	a, b := e.EncodeQuery(matrixQuery), e.EncodeQuery(matrixQuery)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("同文本两次编码不一致")
	}
	if len(a.Dense) != 64 {
		t.Fatalf("dense 维度 %d want 64（与文档侧同维）", len(a.Dense))
	}
	if len(a.Multi) < 1 || len(a.Multi) > queryMultiRows {
		t.Fatalf("multi 行数 %d 越界 [1,%d]", len(a.Multi), queryMultiRows)
	}
	for _, row := range a.Multi {
		if len(row) != 8 {
			t.Fatalf("multi 行宽 %d want 8（与文档侧同宽）", len(row))
		}
	}
}

// packKeys 提取候选 doc_key 序列。
func packKeys(pack evidence.EvidencePack) []string {
	keys := make([]string, 0, len(pack.Candidates))
	for _, c := range pack.Candidates {
		keys = append(keys, c.DocKey)
	}
	return keys
}
