// Package pipeline 把 B 域已交付的五个域包串成端到端可运行的检索管线
// （工程方案图 3 的 B2→B3→B5→B6 主链）：
//
//	query ──▶ planner（B2 判档+规划）──▶ retrieval（B3 三路+RRF）
//	      ──▶ evidence（B5 EvidencePack）──▶ summary（B6 答案+引用）
//
// 本包是装配层（编排，不是新域）：域逻辑全部来自被串接的包，管线只做
// 三件事——按档位路由规划器（fast 恒等 / balanced 注入 / deep 分解）、
// 把计划的主查询编码成三路表示（Encoder 接缝）、按交付形态分叉
// （summary 走 B6，tools 在 B5 直返）。
//
// 边界（详见本目录 README.md）：不含 B4 fusion（B3 的 Search 已含 RRF
// 融合）；无 IO、无状态、并发安全；deep 档的 K 轮逐轮检索是真实化路径，
// dev 形态以伞查询单发 + Searcher 的 deep 放宽（双倍路内候选）执行。
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/planner"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/summary"
)

// Delivery 是一次检索的交付形态（图 3 的两叉：summary 交付 / tools 交付）。
type Delivery string

// 交付形态常量。
const (
	// DeliverySummary summary 交付：EvidencePack 经 B6 摘要为
	// "带行内引用角标的答案 + 引用列表"（B5──C-10──►B6──►A1──►用户）。
	DeliverySummary Delivery = "summary"
	// DeliveryTools tools 交付：EvidencePack 在 B5 即直返 B1（不经 B6，
	// 供 Agent 工具消费，绝不调 Summarizer）。
	DeliveryTools Delivery = "tools"
)

// ErrInvalidDelivery 标记未知交付形态。
var ErrInvalidDelivery = errors.New("pipeline: 未知交付形态（合法值 summary|tools）")

// PipelineRequest 是一次端到端检索的请求：查询文本 + 检索档位 + 交付形态。
type PipelineRequest struct {
	// Query 用户查询文本（TrimSpace 后非空）。
	Query string
	// Tier 用户显式指定的检索档位（fast|balanced|deep）。作为档位下限：
	// 管线经 planner.Router 判档建议后按 MaxTier 只升不降（§3.1）。
	Tier retrieval.Tier
	// Delivery 交付形态（summary|tools）。
	Delivery Delivery
}

// PipelineResult 是一次端到端检索的结果。summary 交付时四个字段全填
// （Answer/FormattedAnswer/Citations 来自 B6，Pack 来自 B5）；tools 交付
// 时只填 Pack（summary 三字段保持零值）。
type PipelineResult struct {
	// Answer B6 答案文本，内嵌 [n] 行内引用角标（summary 交付）。
	Answer string
	// FormattedAnswer Answer 经 summary.FormatAnswer 格式化的 Markdown
	// 形态（[n] → [n](#cit-n) 锚点链接；summary 交付）。
	FormattedAnswer string
	// Citations 引用列表（角标序号 + doc_key + Locator；summary 交付）。
	Citations []summary.Citation
	// Pack B5 组装的证据包（两种交付都填，tools 交付的唯一载荷）。
	Pack evidence.EvidencePack
}

// Pipeline 是端到端检索管线：注入四个组件即可运行。构造后只读、无状态、
// 并发安全（一次 Execute 不与另一次共享可变数据）。
type Pipeline struct {
	// Planner balanced 档规划器，兼作 deep 档子计划的 Delegate（子计划
	// 以 balanced 口径递归，见 planner.Decomposer 契约）。必须能服务
	// balanced 档（如 planner.Rule）；fast 档的恒等规划与 deep 档的
	// 分解器由管线按档位路由内部装配。
	Planner planner.Planner
	// Searcher B3 检索执行器（持有只读 Store；含 RRF 融合与证据组装）。
	Searcher *retrieval.Searcher
	// Summarizer B6 摘要器（tools 交付不调用，允许为 nil）。
	Summarizer summary.Summarizer
	// Encoder 查询编码器接缝（查询文本 → 三路表示；DC representation
	// 的接缝镜像）。nil 时回退 FakeEncoder（dev 形态）。
	Encoder QueryEncoder
	// Router 判档器（B2 的 D-3 规则替身）。nil 时回退零值 Router。
	Router *planner.Router
}

// NewDefaultPipeline 装配默认组件：planner.Rule（balanced 规则规划，
// 兼 deep Delegate）+ retrieval.NewSearcher（TopN=MaxCandidates）+
// summary.NewStub（确定性摘要）+ FakeEncoder（dev 查询编码）。
// store 为 nil 时报错（后续 Search 会在 Snapshot 上 panic，宁可早失败）。
func NewDefaultPipeline(store *retrieval.Store) (*Pipeline, error) {
	if store == nil {
		return nil, errors.New("pipeline: store 不能为空（NewDefaultPipeline）")
	}
	return &Pipeline{
		Planner:    planner.Rule{},
		Searcher:   retrieval.NewSearcher(store),
		Summarizer: summary.NewStub(),
		Encoder:    FakeEncoder{},
		Router:     planner.NewRouter(),
	}, nil
}

// Execute 一步到位执行 query→plan→三档路由→search→summary/tools：
//
//  1. 入口校验：ctx、交付形态、查询非空、档位合法；
//  2. 判档合并：Router.Route 建议 × 用户档，经 planner.MaxTier 只升不降
//     （判错只多花预算、不丢质量，§3.1）；
//  3. 规划：按有效档位路由规划器（fast→Identity / balanced→Planner /
//     deep→Decomposer{Delegate: Planner}）产出 Plan 并 Validate；
//  4. 编码：Encoder 把计划的 RewrittenQuery（防漂移那一路）编码为三路
//     表示，query_id 由查询文本哈希确定性派生；
//  5. 检索：Searcher.Search 按档位执行三路召回 + RRF + 证据组装，产出
//     EvidencePack；
//  6. 交付：summary → Summarize + FormatAnswer；tools → 直返 Pack
//     （不调 Summarizer）。
//
// 同输入必得同输出（全链路确定性，有测试强制）；任一阶段失败即整体
// 失败，绝不产出部分结果。
func (p *Pipeline) Execute(ctx context.Context, req PipelineRequest) (PipelineResult, error) {
	if err := ctx.Err(); err != nil {
		return PipelineResult{}, fmt.Errorf("pipeline: 已取消: %w", err)
	}
	if req.Delivery != DeliverySummary && req.Delivery != DeliveryTools {
		return PipelineResult{}, fmt.Errorf("%w: %q", ErrInvalidDelivery, req.Delivery)
	}
	if strings.TrimSpace(req.Query) == "" {
		return PipelineResult{}, fmt.Errorf("%w: 查询为空", planner.ErrEmptyQuery)
	}
	if !isValidTier(req.Tier) {
		return PipelineResult{}, fmt.Errorf("%w: %q（合法值 fast|balanced|deep）", planner.ErrInvalidTier, req.Tier)
	}
	if err := p.checkWiring(req.Delivery); err != nil {
		return PipelineResult{}, err
	}

	// 判档合并：用户显式档 × 判档建议，只升不降。
	router := p.Router
	if router == nil {
		router = planner.NewRouter()
	}
	tier := planner.MaxTier(req.Tier, router.Route(ctx, req.Query))

	// 规划（按档位路由规划器）。
	plan, err := p.plannerFor(tier).Plan(ctx, req.Query, tier)
	if err != nil {
		return PipelineResult{}, fmt.Errorf("pipeline: 规划失败（%s）: %w", tier, err)
	}
	if err := plan.Validate(); err != nil {
		return PipelineResult{}, fmt.Errorf("pipeline: 计划出口校验失败（%s）: %w", tier, err)
	}

	// 编码 + 检索（含 EvidencePack 组装）。
	qid := queryID(req.Query)
	repr := p.encoder().EncodeQuery(plan.RewrittenQuery)
	pack, err := p.Searcher.Search(ctx, retrieval.Request{
		QueryID: qid,
		Tier:    tier,
		Dense:   repr.Dense,
		Sparse:  repr.Sparse,
		Multi:   repr.Multi,
	})
	if err != nil {
		return PipelineResult{}, fmt.Errorf("pipeline: 检索失败（%s）: %w", tier, err)
	}

	// 交付分叉：tools 在 B5 直返；summary 走 B6。
	if req.Delivery == DeliveryTools {
		return PipelineResult{Pack: pack}, nil
	}
	res, err := p.Summarizer.Summarize(ctx, summary.SummaryRequest{
		QueryID: qid,
		Query:   req.Query,
		Pack:    pack,
	})
	if err != nil {
		return PipelineResult{}, fmt.Errorf("pipeline: 摘要失败（%s）: %w", tier, err)
	}
	formatted, err := summary.FormatAnswer(res)
	if err != nil {
		return PipelineResult{}, fmt.Errorf("pipeline: 答案格式化失败（%s）: %w", tier, err)
	}
	return PipelineResult{
		Answer:          res.Answer,
		FormattedAnswer: formatted,
		Citations:       res.Citations,
		Pack:            pack,
	}, nil
}

// plannerFor 按档位路由规划器：fast 恒等（B1 fast 直通）/ balanced 注入
// 的 Planner / deep 分解器（Delegate 注入的 Planner，子计划按 balanced
// 口径递归，§3.1 "K 轮×1 次"）。
func (p *Pipeline) plannerFor(tier retrieval.Tier) planner.Planner {
	switch tier {
	case retrieval.TierFast:
		return planner.Identity{}
	case retrieval.TierBalanced:
		return p.Planner
	case retrieval.TierDeep:
		return &planner.Decomposer{Delegate: p.Planner}
	default:
		// Execute 已做档位校验，这里兜底不可达。
		return planner.Identity{}
	}
}

// encoder 返回查询编码器；nil 回退 FakeEncoder（dev 形态）。
func (p *Pipeline) encoder() QueryEncoder {
	if p.Encoder == nil {
		return FakeEncoder{}
	}
	return p.Encoder
}

// checkWiring 校验注入组件齐备：Planner/Searcher 必须注入；Summarizer
// 仅 summary 交付需要（tools 交付允许缺席）。
func (p *Pipeline) checkWiring(delivery Delivery) error {
	if p.Planner == nil {
		return errors.New("pipeline: Planner 未注入")
	}
	if p.Searcher == nil {
		return errors.New("pipeline: Searcher 未注入")
	}
	if delivery == DeliverySummary && p.Summarizer == nil {
		return errors.New("pipeline: summary 交付需要 Summarizer（tools 交付可缺席）")
	}
	return nil
}

// queryID 由查询文本确定性派生（hex(sha256(query))[:12]）：同查询同 ID，
// 供 EvidencePack/SummaryResult 的链路对账；不含档位/交付——同一查询的
// 不同档位结果共享追踪键。
func queryID(query string) string {
	sum := sha256.Sum256([]byte(query))
	return "q-" + hex.EncodeToString(sum[:6])
}

// isValidTier 判断档位是否合法（与 planner/retrieval 同口径）。
func isValidTier(tier retrieval.Tier) bool {
	return tier == retrieval.TierFast || tier == retrieval.TierBalanced || tier == retrieval.TierDeep
}
