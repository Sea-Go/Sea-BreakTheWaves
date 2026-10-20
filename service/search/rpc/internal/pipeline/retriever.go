// retriever.go —— 框架 knowledge/retriever.Retriever 的装配层（C17）。
//
// 框架定义了检索器的公开接缝（Retrieve(ctx, *Query) (*Result, error)），
// 此前仓库内无任何实现（2026-10-08 复用审计确认的空白）。本文件把
// pipeline 的检索装配适配到该接口，使框架 Agent 可经统一接缝消费整篇
// 三路检索。
//
// 映射约定（dev 形态）：
//   - 检索经 Pipeline.Execute 的 fast 档 + tools 交付（检索器不做摘要）；
//   - Query.Text → 查询文本；Query.Limit 截断候选；Query.Filter.DocumentIDs
//     过滤候选；Query.MinScore 作用于归一化分数（见下）；
//   - RRF 分数是 1/(k+rank) 量级（非 [0,1]），映射到框架的 [0,1] 口径时
//     以本次检索的最高分为 1.0 线性归一（top 之后单调递减）；
//   - Document.ID = doc_key；Content 不回填全文（整篇可能很大），以首条
//     证据 quote 作内容预览，完整正文经 locator 定位获取；
//   - History/UserID/SessionID/SearchMode 当前忽略（个性化与多轮属后续
//     里程碑），不静默改变检索结果。
package pipeline

import (
	"context"
	"fmt"

	frameworkdoc "trpc.group/trpc-go/trpc-agent-go/knowledge/document"
	frameworkretriever "trpc.group/trpc-go/trpc-agent-go/knowledge/retriever"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// 编译期断言：本适配器完整实现框架 Retriever 接口。
var _ frameworkretriever.Retriever = (*SearchRetriever)(nil)

// ErrRetrieverInput 标记框架查询到管线请求的映射失败。
var ErrRetrieverInput = fmt.Errorf("pipeline retriever: invalid query")

// SearchRetriever 把 Pipeline 适配为框架 Retriever。零值不可用，经
// NewSearchRetriever 构造。
type SearchRetriever struct {
	pipeline *Pipeline
}

// NewSearchRetriever 构造框架检索器。pipeline 为 nil 即拒。
func NewSearchRetriever(p *Pipeline) (*SearchRetriever, error) {
	if p == nil {
		return nil, fmt.Errorf("%w: pipeline is required", ErrRetrieverInput)
	}
	return &SearchRetriever{pipeline: p}, nil
}

// Retrieve 经管线执行整篇三路检索并映射为框架结果。
func (r *SearchRetriever) Retrieve(ctx context.Context, query *frameworkretriever.Query) (*frameworkretriever.Result, error) {
	if query == nil || query.Text == "" {
		return nil, fmt.Errorf("%w: text is required", ErrRetrieverInput)
	}
	result, err := r.pipeline.Execute(ctx, PipelineRequest{
		Query:    query.Text,
		Tier:     retrieval.TierFast,
		Delivery: DeliveryTools,
	})
	if err != nil {
		return nil, fmt.Errorf("pipeline retriever: %w", err)
	}

	allowed := map[string]bool{}
	if query.Filter != nil && len(query.Filter.DocumentIDs) > 0 {
		for _, id := range query.Filter.DocumentIDs {
			allowed[id] = true
		}
	}

	// RRF 分数归一化基准：本次最高分（top 之后线性递减，保持单调序）。
	maxRRF := float32(0)
	for _, c := range result.Pack.Candidates {
		if allowedEmpty(allowed) || allowed[c.DocKey] {
			if c.RRFScore > maxRRF {
				maxRRF = c.RRFScore
			}
		}
	}

	docs := make([]*frameworkretriever.RelevantDocument, 0, len(result.Pack.Candidates))
	for _, c := range result.Pack.Candidates {
		if !allowedEmpty(allowed) && !allowed[c.DocKey] {
			continue
		}
		score := 0.0
		if maxRRF > 0 {
			score = float64(c.RRFScore / maxRRF)
		}
		if query.MinScore > 0 && score < query.MinScore {
			continue
		}
		doc := &frameworkdoc.Document{ID: c.DocKey, Name: c.DocKey}
		if len(c.Evidence) > 0 {
			doc.Content = c.Evidence[0].Quote
		}
		docs = append(docs, &frameworkretriever.RelevantDocument{Document: doc, Score: score})
		if query.Limit > 0 && len(docs) >= query.Limit {
			break
		}
	}
	return &frameworkretriever.Result{Documents: docs}, nil
}

// Close 释放资源（管线无持有资源，返回 nil）。
func (r *SearchRetriever) Close() error { return nil }

// allowedEmpty 报告过滤集合是否为空（空 = 不过滤）。
func allowedEmpty(allowed map[string]bool) bool { return len(allowed) == 0 }
