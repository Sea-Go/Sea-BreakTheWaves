package orchestrator

import (
	"context"
	"fmt"

	workflowruntime "agent_v3/internal/workflow/runtime"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/log"
)

func (o *Orchestrator) runRequirementIntake(
	ctx context.Context,
	userID, sessionID string,
	userMessage string,
	intakeAgent agentcore.Agent,
) (*workflowruntime.SkillResult, error) {
	rt := o.LoadOrInitRuntime(userID, sessionID)
	log.Infof("[orchestrator] intake: start userID=%s sessionID=%s", userID, sessionID)

	prompt := buildIntakePrompt(userMessage, rt)

	rawOutput, err := o.runAgentAndCollect(ctx, intakeAgent, sessionID, prompt)
	if err != nil {
		log.Errorf("[orchestrator] intake: agent error: %v", err)
		return nil, fmt.Errorf("intake agent: %w", err)
	}

	o.updateRuntime(userID, sessionID, func(r *workflowruntime.TravelSkillRuntime) {
		r.LastSkillOutput = rawOutput
	})

	result := parseSkillResult(rawOutput)
	if result == nil {
		log.Errorf("[orchestrator] intake: parse failed, outputLen=%d", len(rawOutput))
		return &workflowruntime.SkillResult{
			SkillName:    "travel-requirement-intake",
			Status:       "failed",
			ErrorCode:    workflowruntime.ErrCodeParseFailed,
			StopWorkflow: true,
			Output:       "鎴戞殏鏃舵病鑳界ǔ瀹氬垎鏋愪綘鐨勯渶姹傦紝璇蜂綘鎸夊嚭鍙戝湴銆佹椂闂淬€侀绠椼€佷氦閫氭柟寮忋€佸亸濂借繖鍑犱釜鐐圭畝鍗曡涓€涓嬨€?",
		}, nil
	}

	if snap, ok := result.Result["requirement"].(map[string]any); ok {
		o.updateRuntime(userID, sessionID, func(r *workflowruntime.TravelSkillRuntime) {
			mergeSnapshotFromMap(&r.Requirement, snap)
			enrichRequirementWithDeterministicFields(&r.Requirement, userMessage)
		})
	} else {
		o.updateRuntime(userID, sessionID, func(r *workflowruntime.TravelSkillRuntime) {
			enrichRequirementWithDeterministicFields(&r.Requirement, userMessage)
		})
	}

	rt = o.LoadOrInitRuntime(userID, sessionID)
	decision := buildPlanningDecision(rt.Requirement, rt.AskedRounds, rt.MaxAskRounds, userMessage)

	log.Infof("[orchestrator] intake: decision ready=%v missingP0=%v missingP1=%v askedRounds=%d",
		decision.Ready, decision.MissingP0, decision.MissingP1, rt.AskedRounds)

	if decision.Ready {
		o.updateRuntime(userID, sessionID, func(r *workflowruntime.TravelSkillRuntime) {
			applyDefaultsForOptionalFields(&r.Requirement)
			r.Requirement.RequirementReady = true
			r.CurrentStage = workflowruntime.StageMacroPlanning
		})
		result.RequirementReady = true
		result.NextStage = workflowruntime.StageMacroPlanning
		result.StopWorkflow = false
		log.Infof("[orchestrator] intake: ready -> macro_planning")
	} else {
		o.updateRuntime(userID, sessionID, func(r *workflowruntime.TravelSkillRuntime) {
			r.AskedRounds++
			r.Requirement.MissingFields = append(append(decision.MissingP0, decision.MissingP1...), decision.MissingP2...)
			r.CurrentStage = workflowruntime.StageAwaitingUserInfo
		})
		result.RequirementReady = false
		result.MissingFields = append(append(decision.MissingP0, decision.MissingP1...), decision.MissingP2...)
		result.FollowUpQuestions = decision.Questions
		result.NextStage = workflowruntime.StageAwaitingUserInfo
		result.StopWorkflow = true
		result.Output = formatPlanningQuestions(decision.Questions)
		log.Infof("[orchestrator] intake: not ready -> askedRounds=%d questions=%d", rt.AskedRounds+1, len(decision.Questions))
	}

	return result, nil
}
