// graph.go —— C4/C5 编制流水线的 tRPC-Agent-Go GraphAgent 装配层（C17）。
//
// 职责边界（与 service/async/rpc/internal/content/graph.go 同构）：
//   - 本文件是**框架装配层**：把 compile 包的纯域编排（Orchestrator.Advance +
//     四阶段 StageFunc）编译成框架 Graph，由 GraphAgent 承载、经 Runner 驱动；
//   - compile 包的其余文件是**算法内核**：纯函数、无 IO、无框架依赖，四阶段
//     语义与幂等推进在其中实现，本层不重复其逻辑；
//   - 本层不启动 worker、不落盘、不发布——发布归 A5（C-1 唯一写者）。
//
// 为什么需要这层：C17 要求 BTW 的 Agent、工作流、工具、会话及检索装配以
// 框架公开 API 为基础。纯算法域层不走框架是合理边界（框架不该管文本编码与
// 聚类），但**编排与运行必须经框架**——故四阶段状态机在此以 Graph 节点、
// GraphAgent 与完成事件回执的形式暴露。
package compile

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
)

const (
	compileGraphAgentName  = "compile_pipeline"
	compileGraphRequestKey = "compile_request"
	compileJobStateKey     = "compile_job"
	compileGraphReceiptKey = "compile_receipt"
)

var (
	// ErrCompileGraphInput 标记注入 Run 的请求不合法。
	ErrCompileGraphInput = errors.New("compile graph: invalid request")
	// ErrCompileGraphOutput 标记 Graph 完成事件缺少或含非法回执。
	ErrCompileGraphOutput = errors.New("compile graph: output missing or invalid")
)

// CompileGraphRequest 是一次编制运行的全部输入。调用方传入后不得再改
// Sources（Graph 节点读取它重建 CompileJob）。
type CompileGraphRequest struct {
	JobID    string      `json:"job_id"`
	ModuleID string      `json:"module_id"`
	Sources  []SourceRef `json:"sources"`
	// SkipPolish 对应 STORM 的 do_polish_article=false（工程方案 §4.8 决策 8）。
	SkipPolish bool `json:"skip_polish"`
}

// CompileGraphReceipt 是有界输出：证明四阶段在框架 Graph 内按序完成，
// **不**证明内容已发布或落盘。正文不进入 Graph state（沿 content/graph.go
// 的 "chunk text never enters Graph state" 惯例），只回传计数与状态。
type CompileGraphReceipt struct {
	JobID         string `json:"job_id"`
	ModuleID      string `json:"module_id"`
	Status        string `json:"status"`
	StageCount    int    `json:"stage_count"`
	SectionCount  int    `json:"section_count"`
	CitationCount int    `json:"citation_count"`
	ClaimCount    int    `json:"claim_count"`
	Unevidenced   int    `json:"unevidenced_claims"`
}

// CompileGraphRunOption 用框架公开的 RuntimeState API 注入单次运行请求；
// MergeRuntimeState 保留调用方安装的其他运行态。
func CompileGraphRunOption(request CompileGraphRequest) (agent.RunOption, error) {
	if _, err := NewCompileJob(request.JobID, request.ModuleID, request.Sources); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCompileGraphInput, err)
	}
	return agent.MergeRuntimeState(map[string]any{compileGraphRequestKey: request}), nil
}

// NewCompileGraphAgent 编译一个真实的 tRPC-Agent-Go GraphAgent：四个阶段各为
// 一个节点，curate → outline → article → polish 顺序执行。节点内调用 compile
// 包的纯域函数（Orchestrator.Advance + StageFunc），Graph 负责顺序与状态传递，
// Runner 由调用方持有并每次 Run 一个请求。
func NewCompileGraphAgent(executor Executor) (*graphagent.GraphAgent, error) {
	if executor.Curate == nil || executor.Outline == nil ||
		executor.Article == nil || executor.Polish == nil {
		return nil, fmt.Errorf("%w: executor missing a stage", ErrCompileGraphInput)
	}

	schema := graph.NewStateSchema().
		AddField(compileGraphRequestKey, graph.StateField{
			Type: reflect.TypeOf(CompileGraphRequest{}), Reducer: graph.DefaultReducer,
		}).
		AddField(compileJobStateKey, graph.StateField{
			Type: reflect.TypeOf(CompileJob{}), Reducer: graph.DefaultReducer,
		}).
		AddField(compileGraphReceiptKey, graph.StateField{
			Type: reflect.TypeOf(CompileGraphReceipt{}), Reducer: graph.DefaultReducer,
		})

	builder := graph.NewStateGraph(schema)
	for _, stage := range []Stage{StageCurate, StageOutline, StageArticle} {
		st := stage
		builder = builder.AddNode(string(st), compileStageNode(st, executor.StageFunc(st)))
	}
	builder = builder.AddNode(string(StagePolish), compileStageNode(StagePolish, executor.StageFunc(StagePolish)))

	compiled, err := builder.
		AddEdge(string(StageCurate), string(StageOutline)).
		AddEdge(string(StageOutline), string(StageArticle)).
		AddEdge(string(StageArticle), string(StagePolish)).
		SetEntryPoint(string(StageCurate)).
		SetFinishPoint(string(StagePolish)).
		Compile()
	if err != nil {
		return nil, fmt.Errorf("compile pipeline graph: %w", err)
	}
	ag, err := graphagent.New(compileGraphAgentName, compiled,
		graphagent.WithDescription("Run STORM-style curation stages over one compile job"))
	if err != nil {
		return nil, fmt.Errorf("construct compile graph agent: %w", err)
	}
	return ag, nil
}

// compileStageNode 生成一个阶段节点：读取 Graph state 中的请求与在途 job，
// 调域层 Advance 推进一个阶段，写回 job（polish 阶段另写回执）。
//
// 注意（实测约束）：StateSchema 声明了 compileJobStateKey 字段，Graph 会在
// 首个节点注入该字段的**零值** CompileJob{}——因此"是否需要初始化任务"必须
// 以业务不变量（Sources 为空）判定，不能以 GetStateValue 的 ok 判定，否则会
// 拿到空任务并在 stageInput 处报"curate 需要 sources"。
func compileStageNode(stage Stage, fn StageFunc) func(context.Context, graph.State) (any, error) {
	return func(ctx context.Context, state graph.State) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		request, ok := graph.GetStateValue[CompileGraphRequest](state, compileGraphRequestKey)
		if !ok {
			return nil, fmt.Errorf("%w: state %q missing", ErrCompileGraphInput, compileGraphRequestKey)
		}
		job, _ := graph.GetStateValue[CompileJob](state, compileJobStateKey)
		if len(job.Sources) == 0 {
			// 首节点（或零值态）：由本次请求重建初始任务。
			built, err := NewCompileJob(request.JobID, request.ModuleID, request.Sources)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrCompileGraphInput, err)
			}
			job = built
		}
		// do_polish=false：以 nil 执行器表达跳过（域层仅允许 Polish 跳过）。
		exec := fn
		if stage == StagePolish && request.SkipPolish {
			exec = nil
		}
		advanced, err := (&Orchestrator{}).Advance(ctx, job, stage, exec)
		if err != nil {
			return nil, fmt.Errorf("compile stage %s: %w", stage, err)
		}
		if stage != StagePolish {
			return graph.State{compileJobStateKey: advanced}, nil
		}
		receipt, err := compileReceipt(request, advanced)
		if err != nil {
			return nil, err
		}
		return graph.State{compileJobStateKey: advanced, compileGraphReceiptKey: receipt}, nil
	}
}

// compileReceipt 从完成的 job 提取有界回执（只含计数与状态，不含正文）。
func compileReceipt(request CompileGraphRequest, job CompileJob) (CompileGraphReceipt, error) {
	final := job.FinalArticle()
	if final == nil {
		return CompileGraphReceipt{}, fmt.Errorf("%w: no final article", ErrCompileGraphOutput)
	}
	claims := ExtractClaims(final.FullText(), final.Citations)
	receipt := CompileGraphReceipt{
		JobID: request.JobID, ModuleID: request.ModuleID, Status: string(job.Status),
		StageCount: len(job.Stages), SectionCount: len(final.Sections),
		CitationCount: len(final.Citations), ClaimCount: len(claims),
		Unevidenced: len(UnevidencedClaims(claims)),
	}
	if receipt.JobID == "" || receipt.ModuleID == "" || receipt.Status != string(StatusDone) ||
		receipt.SectionCount <= 0 {
		return CompileGraphReceipt{}, ErrCompileGraphOutput
	}
	return receipt, nil
}

// CompileGraphReceiptFromCompletion 只从真实的 Graph 完成事件提取回执。
// 调用方仍须消费到 Runner 完成并拒绝终止错误——Graph 完成本身不是成功回执
// （沿 content/graph.go 的同一约束）。
func CompileGraphReceiptFromCompletion(e *event.Event) (CompileGraphReceipt, bool, error) {
	if !graph.IsGraphCompletionEvent(e) {
		return CompileGraphReceipt{}, false, nil
	}
	raw, ok := e.StateDelta[compileGraphReceiptKey]
	if !ok {
		return CompileGraphReceipt{}, true, ErrCompileGraphOutput
	}
	var receipt CompileGraphReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return CompileGraphReceipt{}, true, fmt.Errorf("decode compile receipt: %w", err)
	}
	if receipt.JobID == "" || receipt.ModuleID == "" || receipt.Status != string(StatusDone) ||
		receipt.SectionCount <= 0 {
		return CompileGraphReceipt{}, true, ErrCompileGraphOutput
	}
	return receipt, true, nil
}
