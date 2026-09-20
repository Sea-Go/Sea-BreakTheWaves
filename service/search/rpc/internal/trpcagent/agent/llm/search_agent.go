// Package agent search_agent.go — SearchAgent 慢路径搜索子图。
//
// 该文件实现 SearchAgent：作为慢路径搜索子图（GraphAgent 风格），
// 编排 Searcher.Search + GraphSearcher.Search + HistoryService.Record，
// 组装 AgentOutput 返回。不直接 import trpc-agent-go，通过 AgentExecutor
// interface 抽象 LLM 循环入口，DefaultExecutor 作为兜底实现。
//
// 二开扩展点：
//   - 实现 AgentExecutor interface 注入真实 trpc-agent-go GraphAgent 循环
//   - 替换 Searcher/GraphSearcher/HistoryService 为自研实现
package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/domain"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/trpcagent/knowledge/engine"
)

// AgentExecutor 抽象 trpc-agent-go 的 Agent 执行入口。
//
// 真实实现可包装 trpc-agent-go 的 GraphAgent/LLMAgent/Runner；
// 默认实现 DefaultExecutor 直接调 Searcher，不真用 LLM 循环。
//
// 二开扩展点：实现该 interface 注入 LLM 推理循环。
type AgentExecutor interface {
	// Run 执行 Agent 主循环。
	// ctx 上下文；input Agent 输入（查询/用户/TopK/工具/最大迭代）。
	// 返回 Agent 输出与 error。
	Run(ctx context.Context, input AgentInput) (AgentOutput, error)
}

// AgentInput SearchAgent 输入。
type AgentInput struct {
	// Query 查询文本。
	Query string
	// UserKey 用户标识（个性化搜索）。
	UserKey domain.UserKey
	// TopK 返回数量上限。
	TopK int
	// Tools 工具名清单（供 LLM 循环调用）。
	Tools []string
	// MaxIter 最大迭代次数（LLM 循环上限）。
	MaxIter int
}

// AgentOutput SearchAgent 输出。
type AgentOutput struct {
	// Answer 汇总答案（LLM 生成，默认实现为空）。
	Answer string
	// Hits 搜索命中列表。
	Hits []domain.SearchHit
	// Graph 图谱知识（可为 nil）。
	Graph *domain.GraphKnowledge
	// Trace 执行轨迹（每步一个 map）。
	Trace []map[string]any
}

// SearchAgent 搜索子图 Agent，作为慢路径搜索编排器。
//
// 职责：
//   - 调用 AgentExecutor（默认 DefaultExecutor → Searcher.Search）跑搜索拿基础结果
//   - 调用 GraphSearcher.Search 补图谱知识
//   - 调用 HistoryService.Record 记录搜索历史
//   - 组装 AgentOutput 返回
//
// 任一子步骤失败不阻断主流程，返回部分结果。
type SearchAgent struct {
	// Searcher 基础搜索器（实现 domain.Searcher interface）。
	Searcher domain.Searcher
	// GraphSearcher 图谱知识搜索器。
	GraphSearcher *search.GraphSearcher
	// HistoryService 搜索历史服务。
	HistoryService *search.HistoryService
	// Executor Agent 执行器（nil 时使用 DefaultExecutor）。
	Executor AgentExecutor
}

// NewSearchAgent 构造 SearchAgent。
// searcher 基础搜索器；graphSearcher 图谱搜索器；historyService 历史服务；
// executor Agent 执行器（nil 时使用 DefaultExecutor）。
func NewSearchAgent(
	searcher domain.Searcher,
	graphSearcher *search.GraphSearcher,
	historyService *search.HistoryService,
	executor AgentExecutor,
) *SearchAgent {
	return &SearchAgent{
		Searcher:       searcher,
		GraphSearcher:  graphSearcher,
		HistoryService: historyService,
		Executor:       executor,
	}
}

// Execute 执行搜索子图。
//
// 流程：
//  1. 调用 AgentExecutor.Run（默认走 DefaultExecutor → Searcher.Search）拿基础结果
//  2. 调用 GraphSearcher.Search 补图谱知识
//  3. 调用 HistoryService.Record 记录搜索历史
//  4. 组装 AgentOutput 返回
//
// 搜索失败或图谱失败不阻断流程，返回部分结果。
func (a *SearchAgent) Execute(ctx context.Context, input AgentInput) (AgentOutput, error) {
	if a == nil {
		return AgentOutput{}, fmt.Errorf("search agent: nil agent")
	}
	trace := make([]map[string]any, 0, 3)

	// 1. 调用 AgentExecutor（默认 DefaultExecutor → Searcher.Search）拿基础结果。
	executor := a.Executor
	if executor == nil {
		executor = &DefaultExecutor{Searcher: a.Searcher}
	}
	output, searchErr := executor.Run(ctx, input)
	if searchErr != nil {
		// 搜索失败不阻断，记录 trace 并继续补图谱知识。
		trace = append(trace, map[string]any{
			"step":   "search",
			"status": "error",
			"error":  searchErr.Error(),
		})
		output = AgentOutput{}
	} else {
		trace = append(trace, map[string]any{
			"step":   "search",
			"status": "ok",
			"hits":   len(output.Hits),
		})
	}

	// 2. 调用 GraphSearcher.Search 补图谱知识。
	if a.GraphSearcher != nil && input.Query != "" {
		gk, gErr := a.GraphSearcher.Search(ctx, input.Query, input.TopK)
		if gErr != nil {
			// 图谱失败不阻断主流程。
			trace = append(trace, map[string]any{
				"step":   "graph",
				"status": "error",
				"error":  gErr.Error(),
			})
		} else {
			output.Graph = &gk
			trace = append(trace, map[string]any{
				"step":     "graph",
				"status":   "ok",
				"articles": len(gk.Articles),
				"entities": len(gk.Entities),
			})
		}
	}

	// 3. 调用 HistoryService.Record 记录搜索历史。
	if a.HistoryService != nil {
		entry := domain.SearchHistoryEntry{
			Query:      input.Query,
			UserID:     input.UserKey.UserID,
			SearchedAt: time.Now().UnixMilli(),
		}
		if hErr := a.HistoryService.Record(ctx, entry); hErr != nil {
			trace = append(trace, map[string]any{
				"step":   "history",
				"status": "error",
				"error":  hErr.Error(),
			})
		} else {
			trace = append(trace, map[string]any{
				"step":   "history",
				"status": "ok",
			})
		}
	}

	// 4. 组装最终输出。
	output.Trace = trace
	return output, nil
}

// DefaultExecutor 默认 AgentExecutor 实现，直接调 Searcher，不真用 LLM 循环。
//
// 作为 SearchAgent.Executor 字段未注入时的兜底，封装 Searcher.Search 调用
// 并返回基础 AgentOutput（Hits + 简单 Trace）。
type DefaultExecutor struct {
	// Searcher 基础搜索器。
	Searcher domain.Searcher
}

// Run 执行默认搜索流程。
// 实现 AgentExecutor.Run，内部调用 Searcher.Search 并包装为 AgentOutput。
func (e *DefaultExecutor) Run(ctx context.Context, input AgentInput) (AgentOutput, error) {
	if e == nil || e.Searcher == nil {
		return AgentOutput{}, fmt.Errorf("default executor: searcher 未注入")
	}
	result, err := e.Searcher.Search(ctx, domain.SearchQuery{
		Query:   input.Query,
		UserKey: input.UserKey,
		TopK:    input.TopK,
	})
	if err != nil {
		return AgentOutput{}, fmt.Errorf("default executor: search 失败: %w", err)
	}
	return AgentOutput{
		Hits: result.Hits,
		Trace: []map[string]any{
			{
				"step":            "default_executor_search",
				"rewritten_query": result.RewrittenQuery,
				"hits":            len(result.Hits),
			},
		},
	}, nil
}
