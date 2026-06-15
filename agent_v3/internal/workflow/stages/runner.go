package stages

import (
	"context"
	"fmt"
	"time"

	"agent_v3/internal/graph"
	"agent_v3/internal/review"
	workfloworchestrator "agent_v3/internal/workflow/orchestrator"
	workflowruntime "agent_v3/internal/workflow/runtime"
	workflowtrace "agent_v3/internal/workflow/trace"

	"github.com/google/uuid"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

type MacroAgentFactory func(expectedTripPlanID, userID, sessionID, requestID string) agentcore.Agent

type Config struct {
	Coordinator       agentcore.Agent
	IntakeAgent       agentcore.Agent
	GraphClient       *graph.Client
	ReviewAgents      []review.NamedAgent
	MacroAgentFactory MacroAgentFactory
}

// graphWorkflowAgent implements agentcore.Agent for the hybrid graph workflow.
// Orchestrator controls stage progression; coordinator handles macro planning.
type graphWorkflowAgent struct {
	name          string
	description   string
	coordinator   agentcore.Agent
	intakeAgent   agentcore.Agent
	graphClient   *graph.Client
	reviewAgents  []review.NamedAgent
	orchestrator  *workfloworchestrator.Orchestrator
	newMacroAgent MacroAgentFactory
}

func (a *graphWorkflowAgent) Info() agentcore.Info {
	return agentcore.Info{
		Name:        a.name,
		Description: a.description,
	}
}

func (a *graphWorkflowAgent) Tools() []tool.Tool {
	return a.coordinator.Tools()
}

func (a *graphWorkflowAgent) SubAgents() []agentcore.Agent {
	return a.coordinator.SubAgents()
}

func (a *graphWorkflowAgent) FindSubAgent(name string) agentcore.Agent {
	return a.coordinator.FindSubAgent(name)
}

func (a *graphWorkflowAgent) Run(ctx context.Context, invocation *agentcore.Invocation) (<-chan *event.Event, error) {
	outCh := make(chan *event.Event, 64)
	msg := invocation.Message.Content

	go func() {
		defer close(outCh)

		userID := "workflow-user"
		sessionID := fmt.Sprintf("workflow-%d", time.Now().UnixNano())
		if invocation.Session != nil {
			userID = invocation.Session.UserID
			sessionID = invocation.Session.ID
		}

		trace := workflowtrace.TraceEmitterFromInvocation(invocation)

		result, err := a.orchestrator.Handle(ctx, userID, sessionID, msg, a.intakeAgent)
		if err != nil {
			a.emitErrorEvent(outCh, invocation, fmt.Sprintf("编排错误: %v", err))
			return
		}

		if result.StopWorkflow {
			if trace != nil {
				trace.EmitStage(ctx, string(result.NextStage), "waiting", "等待补充信息", "当前信息还不足以进入地图规划，先在对话中补齐关键条件。")
			}
			if result.Output != "" {
				a.emitTextEvent(outCh, result.Output)
			}
			return
		}

		const maxStageSteps = 5
		for step := 0; step < maxStageSteps; step++ {
			switch result.NextStage {
			case workflowruntime.StageMacroPlanning:
				rt := a.orchestrator.LoadOrInitRuntime(userID, sessionID)
				if !rt.Requirement.RequirementReady {
					a.emitErrorEvent(outCh, invocation, "需求未完成，不能进入宏观规划")
					return
				}
				if trace != nil {
					trace.EmitStage(ctx, string(workflowruntime.StageMacroPlanning), "running", "建立大规划", "需求已经足够，开始建立区域级大方向。")
					emitRequirementMapEvents(ctx, trace, rt.Requirement)
				}

				expectedTripPlanID := uuid.NewString()
				augmentedMsg := buildMacroPrompt(msg, rt, expectedTripPlanID)
				tripPlanID, err := a.runMacroPlanningOnly(ctx, userID, sessionID, rt.RunID, expectedTripPlanID, augmentedMsg, outCh, invocation)
				if err != nil {
					a.emitErrorEvent(outCh, invocation, fmt.Sprintf("宏观规划失败: %v", err))
					return
				}
				a.orchestrator.UpdateRuntime(userID, sessionID, func(r *workflowruntime.TravelSkillRuntime) {
					r.TripPlanID = tripPlanID
					r.CurrentStage = workflowruntime.StageGraphSplitting
				})
				result.NextStage = workflowruntime.StageGraphSplitting

			case workflowruntime.StageGraphSplitting:
				rt2 := a.orchestrator.LoadOrInitRuntime(userID, sessionID)
				if rt2.TripPlanID == "" {
					a.emitErrorEvent(outCh, invocation, "TripPlanID 未找到，无法拆分")
					return
				}
				if trace != nil {
					trace.EmitStage(ctx, string(workflowruntime.StageGraphSplitting), "running", "拆分小规划", "开始把大规划拆成月、周、日，地图会保留上层方向并逐步展开。")
				}
				if err := a.runGraphSplitting(ctx, rt2.TripPlanID, rt2.Requirement); err != nil {
					a.emitErrorEvent(outCh, invocation, fmt.Sprintf("图拆分失败: %v", err))
					return
				}
				if trace != nil {
					if overview, err := a.graphClient.GetTripOverview(ctx, rt2.TripPlanID); err == nil {
						emitGraphSplittingMapEvents(ctx, trace, overview)
						emitGuideEvidenceForTrip(ctx, trace, a.graphClient, rt2.TripPlanID, overview)
					}
				}
				a.orchestrator.UpdateRuntime(userID, sessionID, func(r *workflowruntime.TravelSkillRuntime) {
					r.CurrentStage = workflowruntime.StageDayExpansion
				})
				a.emitTextEvent(outCh, "图拆分完成，已创建 Month/Week/Day 层级。开始逐日验证地点和路线。")
				result.NextStage = workflowruntime.StageDayExpansion

			case workflowruntime.StageDayExpansion:
				rtDay := a.orchestrator.LoadOrInitRuntime(userID, sessionID)
				if rtDay.TripPlanID == "" {
					a.emitErrorEvent(outCh, invocation, "TripPlanID 未找到，无法展开日级规划")
					return
				}
				if trace != nil {
					trace.EmitStage(ctx, string(workflowruntime.StageDayExpansion), "running", "展开日级地点和路线", "开始逐日验证 POI、坐标和路线，地图会实时加入真实地点和路线。")
				}
				if err := a.runPhase2(ctx, rtDay.TripPlanID, trace); err != nil {
					a.emitErrorEvent(outCh, invocation, fmt.Sprintf("日级地点和路线展开失败: %v", err))
					return
				}
				a.orchestrator.UpdateRuntime(userID, sessionID, func(r *workflowruntime.TravelSkillRuntime) {
					r.CurrentStage = workflowruntime.StageReview
				})
				if trace != nil {
					trace.EmitStage(ctx, string(workflowruntime.StageDayExpansion), "completed", "地点和路线已展开", "已完成日级地点与路线验证，准备进入审核。")
				}
				a.emitTextEvent(outCh, "日级地点和路线已展开，开始审核规划质量。")
				result.NextStage = workflowruntime.StageReview

			case workflowruntime.StageReview:
				rtReview := a.orchestrator.LoadOrInitRuntime(userID, sessionID)
				if rtReview.TripPlanID == "" {
					a.emitErrorEvent(outCh, invocation, "TripPlanID 未找到，无法审核")
					return
				}
				if trace != nil {
					trace.EmitStage(ctx, string(workflowruntime.StageReview), "running", "审核规划质量", "开始检查地点、日程、路线和约束是否合理，审核结果会进入地图证据层。")
				}
				if err := a.runPhase3(ctx, rtReview.TripPlanID, trace, rtReview.Requirement); err != nil {
					a.emitErrorEvent(outCh, invocation, fmt.Sprintf("审核失败: %v", err))
					return
				}
				a.orchestrator.UpdateRuntime(userID, sessionID, func(r *workflowruntime.TravelSkillRuntime) {
					r.CurrentStage = workflowruntime.StageFinalOutput
				})
				if trace != nil {
					trace.EmitStage(ctx, string(workflowruntime.StageReview), "completed", "审核完成", "审核结果已经写入地图证据层，开始生成最终方案。")
				}
				a.emitTextEvent(outCh, "审核完成，开始生成最终方案。")
				result.NextStage = workflowruntime.StageFinalOutput

			case workflowruntime.StageFinalOutput:
				rt3 := a.orchestrator.LoadOrInitRuntime(userID, sessionID)
				if rt3.TripPlanID == "" {
					a.emitErrorEvent(outCh, invocation, "TripPlanID 未找到")
					return
				}
				if trace != nil {
					trace.EmitStage(ctx, string(workflowruntime.StageFinalOutput), "running", "生成最终方案", "地图结构已经建立，开始组织最终旅行方案文本。")
				}
				bgCtx := context.Background()
				finalJSON, err := a.runFinalOutput(bgCtx, rt3.TripPlanID, outCh, invocation)
				if err != nil {
					a.emitErrorEvent(outCh, invocation, fmt.Sprintf("最终输出失败: %v", err))
					return
				}
				a.emitFinalEvent(outCh, finalJSON)
				log.Infof("[workflow-runner] final output complete: %d chars", len(finalJSON))
				return

			default:
				a.emitTextEvent(outCh, fmt.Sprintf("规划完成: stage=%s", result.NextStage))
				return
			}
		}

		a.emitErrorEvent(outCh, invocation, "达到最大连续推进步数，停止。")
	}()

	return outCh, nil
}

func (a *graphWorkflowAgent) emitTextEvent(outCh chan<- *event.Event, text string) {
	msgID := fmt.Sprintf("wf-msg-%d", time.Now().UnixNano())
	outCh <- &event.Event{
		Response: &model.Response{
			ID:     msgID,
			Object: model.ObjectTypeChatCompletionChunk,
			Choices: []model.Choice{{
				Delta: model.Message{Content: text + "\n"},
			}},
		},
	}
}

func (a *graphWorkflowAgent) emitErrorEvent(outCh chan<- *event.Event, inv *agentcore.Invocation, errMsg string) {
	if trace := workflowtrace.TraceEmitterFromInvocation(inv); trace != nil {
		trace.EmitError(context.Background(), errMsg)
	}
	msgID := fmt.Sprintf("wf-err-%d", time.Now().UnixNano())
	outCh <- &event.Event{
		Response: &model.Response{
			ID:     msgID,
			Object: model.ObjectTypeChatCompletion,
			Choices: []model.Choice{{
				Message: model.Message{Role: model.RoleAssistant, Content: fmt.Sprintf("错误：%s", errMsg)},
			}},
		},
	}
}

func (a *graphWorkflowAgent) emitFinalEvent(outCh chan<- *event.Event, content string) {
	msgID := fmt.Sprintf("wf-final-%d", time.Now().UnixNano())
	outCh <- &event.Event{
		Response: &model.Response{
			ID:     msgID,
			Object: model.ObjectTypeChatCompletion,
			Choices: []model.Choice{{
				Message: model.Message{Role: model.RoleAssistant, Content: content},
			}},
		},
	}
}

func NewGraphWorkflowAgent(cfg Config) agentcore.Agent {
	if cfg.GraphClient == nil || !cfg.GraphClient.IsEnabled() {
		return nil
	}
	if cfg.Coordinator == nil || cfg.IntakeAgent == nil || cfg.MacroAgentFactory == nil {
		return nil
	}
	if len(cfg.ReviewAgents) == 0 {
		cfg.ReviewAgents = review.DefaultDayReviewAgents()
	}
	return &graphWorkflowAgent{
		name:          "graph-workflow-agent",
		description:   "混合图工作流 Agent：Orchestrator 编排 skills + LLM 宏观规划 + Go 层逐日执行",
		coordinator:   cfg.Coordinator,
		intakeAgent:   cfg.IntakeAgent,
		graphClient:   cfg.GraphClient,
		reviewAgents:  cfg.ReviewAgents,
		orchestrator:  workfloworchestrator.New(),
		newMacroAgent: cfg.MacroAgentFactory,
	}
}
