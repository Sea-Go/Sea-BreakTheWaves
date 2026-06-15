package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"

	workflowruntime "agent_v3/internal/workflow/runtime"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/log"
)

func (o *Orchestrator) runRequirementMerge(
	ctx context.Context,
	userID, sessionID string,
	userMessage string,
	intakeAgent agentcore.Agent,
) (*workflowruntime.SkillResult, error) {
	rt := o.LoadOrInitRuntime(userID, sessionID)
	log.Infof("[orchestrator] merge: start userID=%s sessionID=%s askedRounds=%d", userID, sessionID, rt.AskedRounds)

	snapJSON, _ := json.Marshal(rt.Requirement)
	prompt := buildMergePrompt(userMessage, string(snapJSON))

	rawOutput, err := o.runAgentAndCollect(ctx, intakeAgent, sessionID, prompt)
	if err != nil {
		log.Errorf("[orchestrator] merge: agent error: %v", err)
		return nil, fmt.Errorf("merge agent: %w", err)
	}

	o.updateRuntime(userID, sessionID, func(r *workflowruntime.TravelSkillRuntime) {
		r.LastSkillOutput = rawOutput
	})

	result := parseSkillResult(rawOutput)
	if result == nil {
		log.Errorf("[orchestrator] merge: parse failed, outputLen=%d", len(rawOutput))
		return &workflowruntime.SkillResult{
			SkillName:    "travel-requirement-merge",
			Status:       "failed",
			ErrorCode:    workflowruntime.ErrCodeParseFailed,
			StopWorkflow: true,
			Output:       "鎴戝垰鎵嶆病鏈夌ǔ瀹氳瘑鍒綘鐨勮ˉ鍏呬俊鎭紝璇蜂綘鎸夊嚭鍙戝湴銆佹椂闂淬€侀绠椼€佷氦閫氭柟寮忋€佸亸濂借繖鍑犱釜鐐瑰啀绠€鍗曞彂涓€娆°€?",
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

	log.Infof("[orchestrator] merge: decision ready=%v missingP0=%v missingP1=%v askedRounds=%d maxRounds=%d",
		decision.Ready, decision.MissingP0, decision.MissingP1, rt.AskedRounds, rt.MaxAskRounds)

	if decision.Ready {
		o.updateRuntime(userID, sessionID, func(r *workflowruntime.TravelSkillRuntime) {
			applyDefaultsForOptionalFields(&r.Requirement)
			r.Requirement.RequirementReady = true
			r.CurrentStage = workflowruntime.StageMacroPlanning
		})
		result.RequirementReady = true
		result.NextStage = workflowruntime.StageMacroPlanning
		result.StopWorkflow = false
		log.Infof("[orchestrator] merge: ready -> macro_planning")
		return result, nil
	}

	if rt.AskedRounds >= rt.MaxAskRounds {
		if len(decision.MissingP0) > 0 {
			o.updateRuntime(userID, sessionID, func(r *workflowruntime.TravelSkillRuntime) {
				r.CurrentStage = workflowruntime.StageAwaitingUserInfo
			})
			return &workflowruntime.SkillResult{
				SkillName:         "travel-requirement-merge",
				Status:            "need_user_input",
				ErrorCode:         workflowruntime.ErrCodeRequirementNotReady,
				MissingFields:     decision.MissingP0,
				FollowUpQuestions: decision.Questions,
				StopWorkflow:      true,
				Output:            formatPlanningQuestions(decision.Questions),
			}, nil
		}

		o.updateRuntime(userID, sessionID, func(r *workflowruntime.TravelSkillRuntime) {
			applyDefaultsForOptionalFields(&r.Requirement)
			r.Requirement.RequirementReady = true
			r.CurrentStage = workflowruntime.StageMacroPlanning
		})
		result.RequirementReady = true
		result.NextStage = workflowruntime.StageMacroPlanning
		result.StopWorkflow = false
		log.Infof("[orchestrator] merge: maxRounds reached, defaulting P1 -> macro_planning")
		return result, nil
	}

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
	log.Infof("[orchestrator] merge: not ready -> askedRounds=%d questions=%d", rt.AskedRounds+1, len(decision.Questions))

	return result, nil
}
