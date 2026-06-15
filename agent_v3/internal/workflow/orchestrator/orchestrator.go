package orchestrator

import (
	"context"
	"github.com/google/uuid"
	"sync"
	"time"
	workflowruntime "agent_v3/internal/workflow/runtime"
	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/log"
)

// ═══════════════════════════════════════════════════════════════
// TravelSkillOrchestrator — skills 编排中央控制器
// ═══════════════════════════════════════════════════════════════

// TravelSkillOrchestrator 是旅行规划 skill 编排的中央控制器。
// 它决定每轮 Run() 应该执行哪个 skill、是否可以进入 graph workflow。
// 它是唯一有权推进 stage 的模块。
type Orchestrator struct {
	mu       sync.Mutex
	runtimes map[string]*workflowruntime.TravelSkillRuntime // key = userID + ":" + sessionID
}

// NewTravelSkillOrchestrator 创建编排器并启动 TTL 清理 loop。
// 开发期 TTL 为 24 小时。正式版本应迁移到 PostgreSQL 管理 runtime。
func New() *Orchestrator {
	o := &Orchestrator{
		runtimes: make(map[string]*workflowruntime.TravelSkillRuntime),
	}
	go o.startCleanupLoop(24 * time.Hour)
	return o
}

// ═══════════════════════════════════════════════════════════════
// Runtime 访问（并发安全）
// ═══════════════════════════════════════════════════════════════

// LoadOrInitRuntime 返回 runtime 的**值副本**。读取这个副本是安全的。
// 要修改 runtime 必须通过 updateRuntime()。
func (o *Orchestrator) LoadOrInitRuntime(userID, sessionID string) workflowruntime.TravelSkillRuntime {
	key := userID + ":" + sessionID
	o.mu.Lock()
	defer o.mu.Unlock()

	rt, ok := o.runtimes[key]
	if !ok {
		now := time.Now().Unix()
		rt = &workflowruntime.TravelSkillRuntime{
			RunID:        uuid.NewString(),
			UserID:       userID,
			SessionID:    sessionID,
			CurrentStage: workflowruntime.StageRequirementIntake,
			MaxAskRounds: 2,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		o.runtimes[key] = rt
	}
	return *rt
}

// updateRuntime 在锁内执行一次原子更新。
// 所有 runtime 字段的修改必须通过此方法。
func (o *Orchestrator) updateRuntime(userID, sessionID string, fn func(rt *workflowruntime.TravelSkillRuntime)) {
	key := userID + ":" + sessionID
	o.mu.Lock()
	defer o.mu.Unlock()
	rt := o.runtimes[key]
	if rt == nil {
		return
	}
	rt.UpdatedAt = time.Now().Unix()
	fn(rt)
}

func (o *Orchestrator) resetRuntimeForNewPlanningIntent(userID, sessionID string) {
	key := userID + ":" + sessionID
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now().Unix()
	o.runtimes[key] = &workflowruntime.TravelSkillRuntime{
		RunID:        uuid.NewString(),
		UserID:       userID,
		SessionID:    sessionID,
		CurrentStage: workflowruntime.StageRequirementIntake,
		MaxAskRounds: 2,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

// ═══════════════════════════════════════════════════════════════
// Handle — 核心路由
// ═══════════════════════════════════════════════════════════════

// Handle 是编排器的核心入口。根据 runtime.CurrentStage 路由到对应处理器。
// 返回的 SkillResult 中 StopWorkflow=true 时，Run() 必须立即停止。
func (o *Orchestrator) Handle(
	ctx context.Context,
	userID, sessionID string,
	userMessage string,
	intakeAgent agentcore.Agent,
) (*workflowruntime.SkillResult, error) {

	rt := o.LoadOrInitRuntime(userID, sessionID)
	latestUserMessage := latestUserTurnText(userMessage)
	if rt.CurrentStage != workflowruntime.StageRequirementIntake && rt.CurrentStage != "" && isLikelyNewPlanningRequest(latestUserMessage) {
		log.Infof("[orchestrator] detected new planning intent, resetting runtime: userID=%s sessionID=%s oldStage=%s",
			userID, sessionID, rt.CurrentStage)
		o.resetRuntimeForNewPlanningIntent(userID, sessionID)
		rt = o.LoadOrInitRuntime(userID, sessionID)
	}
	o.updateRuntime(userID, sessionID, func(r *workflowruntime.TravelSkillRuntime) {
		r.LastUserMessage = latestUserMessage
	})

	log.Infof("[orchestrator] handle: userID=%s sessionID=%s stage=%s msgLen=%d",
		userID, sessionID, rt.CurrentStage, len(latestUserMessage))

	switch rt.CurrentStage {
	case workflowruntime.StageRequirementIntake, "":
		return o.runRequirementIntake(ctx, userID, sessionID, latestUserMessage, intakeAgent)

	case workflowruntime.StageAwaitingUserInfo:
		o.updateRuntime(userID, sessionID, func(r *workflowruntime.TravelSkillRuntime) {
			r.PreviousStage = r.CurrentStage
			r.CurrentStage = workflowruntime.StageRequirementMerge
		})
		return o.runRequirementMerge(ctx, userID, sessionID, latestUserMessage, intakeAgent)

	case workflowruntime.StageRequirementMerge:
		return o.runRequirementMerge(ctx, userID, sessionID, latestUserMessage, intakeAgent)

	case workflowruntime.StageMacroPlanning, workflowruntime.StageGraphSplitting, workflowruntime.StageDayExpansion, workflowruntime.StageReview, workflowruntime.StageFinalOutput:
		rt = o.LoadOrInitRuntime(userID, sessionID)
		return &workflowruntime.SkillResult{
			SkillName:        "travel-skill-orchestrator",
			Stage:            rt.CurrentStage,
			Status:           "ready",
			RequirementReady: rt.Requirement.RequirementReady,
			StopWorkflow:     false,
			NextStage:        rt.CurrentStage,
		}, nil

	default:
		o.updateRuntime(userID, sessionID, func(r *workflowruntime.TravelSkillRuntime) {
			r.CurrentStage = workflowruntime.StageRequirementIntake
		})
		return o.runRequirementIntake(ctx, userID, sessionID, latestUserMessage, intakeAgent)
	}
}

// ═══════════════════════════════════════════════════════════════
// runRequirementIntake — 需求准入
// ═══════════════════════════════════════════════════════════════

func (o *Orchestrator) startCleanupLoop(ttl time.Duration) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		o.mu.Lock()
		now := time.Now().Unix()
		threshold := now - int64(ttl.Seconds())
		for k, rt := range o.runtimes {
			if rt.UpdatedAt < threshold {
				delete(o.runtimes, k)
			}
		}
		o.mu.Unlock()
	}
}

func (o *Orchestrator) UpdateRuntime(userID, sessionID string, fn func(rt *workflowruntime.TravelSkillRuntime)) {
	o.updateRuntime(userID, sessionID, fn)
}
