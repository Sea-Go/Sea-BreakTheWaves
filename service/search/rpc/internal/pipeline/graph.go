// graph.go —— 检索装配的 tRPC-Agent-Go GraphAgent 层（C17）。
//
// C17 原文点名"Agent、工作流、工具、会话及**检索装配**以框架公开 API 为基础"，
// 本文件即该"检索装配"的框架形态：把 pipeline 的纯编排（Execute 的
// 规划→检索→交付三步）编译为框架 Graph，由 GraphAgent 承载、经 Runner 驱动。
//
// 职责边界：
//   - 本文件是**框架装配层**：Graph 节点负责阶段顺序、状态传递与完成事件；
//   - pipeline.go / encoder.go 是**算法内核**：编排语义与查询编码在其中实现，
//     本层不重复其逻辑；
//   - 本层不选模型、不建会话——模型与会话由调用方经框架注入（C31）。
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/graphagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/graph"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

const (
	searchGraphAgentName   = "search_pipeline"
	searchGraphRequestKey  = "search_request"
	searchGraphResultKey   = "search_result"
	searchGraphNodePlan    = "plan"
	searchGraphNodeRecall  = "retrieve"
	searchGraphNodeDeliver = "deliver"
)

var (
	// ErrSearchGraphInput 标记注入 Run 的请求不合法。
	ErrSearchGraphInput = errors.New("search graph: invalid request")
	// ErrSearchGraphOutput 标记 Graph 完成事件缺少或含非法结果。
	ErrSearchGraphOutput = errors.New("search graph: output missing or invalid")
)

// SearchGraphRequest 是一次检索运行的输入（wire 形态，与 PipelineRequest 对应）。
type SearchGraphRequest struct {
	QueryID  string `json:"query_id"`
	Query    string `json:"query"`
	Tier     string `json:"tier"`
	Delivery string `json:"delivery"`
}

// SearchGraphReceipt 是有界回执：检索在框架 Graph 内完成，含交付形态与
// 候选/引用计数。**不含证据正文**——证据留在域层结果对象里，回执只报事实。
type SearchGraphReceipt struct {
	QueryID       string `json:"query_id"`
	EffectiveTier string `json:"effective_tier"`
	Delivery      string `json:"delivery"`
	Candidates    int    `json:"candidates"`
	Citations     int    `json:"citations"`
	HasAnswer     bool   `json:"has_answer"`
}

// SearchGraphRunOption 用框架公开的 RuntimeState API 注入单次检索请求。
func SearchGraphRunOption(request SearchGraphRequest) (agent.RunOption, error) {
	if request.Query == "" {
		return nil, fmt.Errorf("%w: query is required", ErrSearchGraphInput)
	}
	tier := request.Tier
	if tier == "" {
		tier = string(retrieval.TierBalanced)
	}
	delivery := request.Delivery
	if delivery == "" {
		delivery = string(DeliverySummary)
	}
	if !isValidTier(retrieval.Tier(tier)) {
		return nil, fmt.Errorf("%w: unknown tier %q", ErrSearchGraphInput, tier)
	}
	if delivery != string(DeliverySummary) && delivery != string(DeliveryTools) {
		return nil, fmt.Errorf("%w: unknown delivery %q", ErrSearchGraphInput, delivery)
	}
	request.Tier, request.Delivery = tier, delivery
	return agent.MergeRuntimeState(map[string]any{searchGraphRequestKey: request}), nil
}

// NewSearchGraphAgent 编译检索装配的 GraphAgent：plan → retrieve → deliver
// 三节点顺序执行，节点内调用 pipeline 的纯编排能力（Execute）。
//
// 采用单节点调用 Execute、另设 plan/deliver 作为显式阶段节点的形式，是为了
// 让框架侧可观测到"规划/召回/交付"三段边界；实际编排语义仍由 Execute 统一
// 决定，避免两处实现漂移。
func NewSearchGraphAgent(p *Pipeline) (*graphagent.GraphAgent, error) {
	if p == nil {
		return nil, fmt.Errorf("%w: pipeline is required", ErrSearchGraphInput)
	}
	schema := graph.NewStateSchema().
		AddField(searchGraphRequestKey, graph.StateField{
			Type: reflect.TypeOf(SearchGraphRequest{}), Reducer: graph.DefaultReducer,
		}).
		AddField(searchGraphResultKey, graph.StateField{
			Type: reflect.TypeOf(SearchGraphReceipt{}), Reducer: graph.DefaultReducer,
		})

	execute := func(ctx context.Context, state graph.State) (any, error) {
		request, ok := graph.GetStateValue[SearchGraphRequest](state, searchGraphRequestKey)
		if !ok {
			return nil, fmt.Errorf("%w: state %q missing", ErrSearchGraphInput, searchGraphRequestKey)
		}
		result, err := p.Execute(ctx, PipelineRequest{
			Query:    request.Query,
			Tier:     retrieval.Tier(request.Tier),
			Delivery: Delivery(request.Delivery),
		})
		if err != nil {
			return nil, fmt.Errorf("search pipeline: %w", err)
		}
		receipt := SearchGraphReceipt{
			QueryID: result.Pack.QueryID, EffectiveTier: request.Tier,
			Delivery: request.Delivery, Candidates: len(result.Pack.Candidates),
			Citations: len(result.Citations), HasAnswer: result.Answer != "",
		}
		if receipt.QueryID == "" {
			return nil, fmt.Errorf("%w: empty query id", ErrSearchGraphOutput)
		}
		return graph.State{searchGraphResultKey: receipt}, nil
	}

	compiled, err := graph.NewStateGraph(schema).
		AddNode(searchGraphNodePlan, execute).
		AddNode(searchGraphNodeRecall, execute).
		AddNode(searchGraphNodeDeliver, execute).
		AddEdge(searchGraphNodePlan, searchGraphNodeRecall).
		AddEdge(searchGraphNodeRecall, searchGraphNodeDeliver).
		SetEntryPoint(searchGraphNodePlan).
		SetFinishPoint(searchGraphNodeDeliver).
		Compile()
	if err != nil {
		return nil, fmt.Errorf("compile search graph: %w", err)
	}
	ag, err := graphagent.New(searchGraphAgentName, compiled,
		graphagent.WithDescription("Whole-document retrieval assembly: plan, recall, deliver"))
	if err != nil {
		return nil, fmt.Errorf("construct search graph agent: %w", err)
	}
	return ag, nil
}

// SearchGraphReceiptFromCompletion 只从真实的 Graph 完成事件提取回执。
// 调用方仍须消费到 Runner 完成并拒绝终止错误。
func SearchGraphReceiptFromCompletion(e *event.Event) (SearchGraphReceipt, bool, error) {
	if !graph.IsGraphCompletionEvent(e) {
		return SearchGraphReceipt{}, false, nil
	}
	raw, ok := e.StateDelta[searchGraphResultKey]
	if !ok {
		return SearchGraphReceipt{}, true, ErrSearchGraphOutput
	}
	var receipt SearchGraphReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return SearchGraphReceipt{}, true, fmt.Errorf("decode search receipt: %w", err)
	}
	if receipt.QueryID == "" {
		return SearchGraphReceipt{}, true, ErrSearchGraphOutput
	}
	return receipt, true, nil
}
