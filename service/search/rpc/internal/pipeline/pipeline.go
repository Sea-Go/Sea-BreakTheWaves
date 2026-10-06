// Package pipeline 串联 B 域五个域包为端到端检索管线：
// query → planner(B2) → retriever(B3) → evidence(B5) → summary(B6)。
//
// 这是图 3"检索全链路（在线）"的 Go 实现——一步到位的 Execute 把
// 五个组件的调用序固化在一个函数内，调用方只看 PipelineRequest/
// PipelineResult 两个类型。组件全部经接口注入，NewDefaultPipeline
// 提供最简装配（规则规划器+检索器+确定性摘要器）。
package pipeline

import (
	"context"
	"fmt"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/planner"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/summary"
)

// Delivery 是交付方式。
type Delivery string

const (
	// DeliverySummary summary 交付：模型生成答案+引用。
	DeliverySummary Delivery = "summary"
	// DeliveryTools tools 交付：EvidencePack 直返调用方。
	DeliveryTools Delivery = "tools"
)

// PipelineRequest 是一次检索的完整输入。
type PipelineRequest struct {
	Query   string
	Tier    retrieval.Tier
	Delivery Delivery
	// Dense/Sparse/Multi 是查询的三路表示（由调用方编码；dev 形态可
	// 从 retr_eval 的 fake 编码器取得）。
	Dense  []float32
	Sparse map[uint32]float32
	Multi  [][]float32
}

// PipelineResult 是一次检索的完整输出。
type PipelineResult struct {
	Pack evidence.EvidencePack
	// summary 交付时填充：
	Answer         string
	FormattedAnswer string
	Citations      []summary.Citation
	// Plan 是规划器产出的结构化计划（可解释性）。
	Plan planner.Plan
}

// Pipeline 串联五组件。零值不可用，经 NewDefaultPipeline 或手工装配。
type Pipeline struct {
	Planner   planner.Planner
	Searcher  *retrieval.Searcher
	Summarizer summary.Summarizer
}

// tierPlanner 按档位分派到对应规划器（fast→恒等、balanced→规则、deep→分解）。
type tierPlanner struct {
	fast     planner.Planner
	balanced planner.Planner
	deep     planner.Planner
}

func (tp tierPlanner) Plan(ctx context.Context, query string, tier retrieval.Tier) (planner.Plan, error) {
	var p planner.Planner
	switch tier {
	case retrieval.TierFast:
		p = tp.fast
	case retrieval.TierBalanced:
		p = tp.balanced
	case retrieval.TierDeep:
		p = tp.deep
	default:
		return planner.Plan{}, fmt.Errorf("pipeline: unknown tier %q", tier)
	}
	if p == nil {
		return planner.Plan{}, fmt.Errorf("pipeline: no planner for tier %q", tier)
	}
	return p.Plan(ctx, query, tier)
}

// NewDefaultPipeline 装配最简可用管线（恒等/规则/分解三档规划器+检索器+确定性摘要器）。
func NewDefaultPipeline(store *retrieval.Store) *Pipeline {
	return &Pipeline{
		Planner: tierPlanner{
			fast:     planner.Identity{},
			balanced: planner.Rule{},
			deep:     &planner.Decomposer{Delegate: planner.Rule{}},
		},
		Searcher:   retrieval.NewSearcher(store),
		Summarizer: summary.StubSummarizer{},
	}
}

// Execute 执行一次完整检索。tools 交付在 evidence 组装后直返；
// summary 交付继续调 Summarizer 并格式化引用。
func (p *Pipeline) Execute(ctx context.Context, req PipelineRequest) (PipelineResult, error) {
	if req.Query == "" {
		return PipelineResult{}, fmt.Errorf("pipeline: query is empty")
	}
	// B2: 规划
	plan, err := p.Planner.Plan(ctx, req.Query, req.Tier)
	if err != nil {
		return PipelineResult{}, fmt.Errorf("pipeline: plan: %w", err)
	}
	// B3: 检索（含 RRF 融合 + evidence 组装）；QueryID 由调用方在
	// PipelineRequest 层传入（Plan 没有 QueryID 字段——规划器是纯域
	// 层，查询 ID 是链路追踪键，属装配层职责）。
	pack, err := p.Searcher.Search(ctx, retrieval.Request{
		QueryID: fmt.Sprintf("q-%s-%s", req.Tier, req.Query[:min(16, len(req.Query))]),
		Tier:    req.Tier,
		Dense:   req.Dense,
		Sparse:  req.Sparse,
		Multi:   req.Multi,
	})
	if err != nil {
		return PipelineResult{}, fmt.Errorf("pipeline: search: %w", err)
	}
	result := PipelineResult{Pack: pack, Plan: plan}
	// tools 交付：直返
	if req.Delivery == DeliveryTools {
		return result, nil
	}
	// B6: summary
	sr, err := p.Summarizer.Summarize(ctx, summary.SummaryRequest{
		QueryID: pack.QueryID,
		Query:   req.Query,
		Pack:    pack,
	})
	if err != nil {
		return PipelineResult{}, fmt.Errorf("pipeline: summarize: %w", err)
	}
	result.Answer = sr.Answer
	result.Citations = sr.Citations
	result.FormattedAnswer, err = summary.FormatAnswer(sr)
	if err != nil {
		return PipelineResult{}, fmt.Errorf("pipeline: format: %w", err)
	}
	return result, nil
}
