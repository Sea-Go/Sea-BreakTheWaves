package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"

	"github.com/google/uuid"
)

// ═══════════════════════════════════════════════════════════════
// TravelSkillOrchestrator — skills 编排中央控制器
// ═══════════════════════════════════════════════════════════════

// TravelSkillOrchestrator 是旅行规划 skill 编排的中央控制器。
// 它决定每轮 Run() 应该执行哪个 skill、是否可以进入 graph workflow。
// 它是唯一有权推进 stage 的模块。
type TravelSkillOrchestrator struct {
	mu       sync.Mutex
	runtimes map[string]*TravelSkillRuntime // key = userID + ":" + sessionID
}

// NewTravelSkillOrchestrator 创建编排器并启动 TTL 清理 loop。
// 开发期 TTL 为 24 小时。正式版本应迁移到 PostgreSQL 管理 runtime。
func NewTravelSkillOrchestrator() *TravelSkillOrchestrator {
	o := &TravelSkillOrchestrator{
		runtimes: make(map[string]*TravelSkillRuntime),
	}
	go o.startCleanupLoop(24 * time.Hour)
	return o
}

// ═══════════════════════════════════════════════════════════════
// Runtime 访问（并发安全）
// ═══════════════════════════════════════════════════════════════

// LoadOrInitRuntime 返回 runtime 的**值副本**。读取这个副本是安全的。
// 要修改 runtime 必须通过 updateRuntime()。
func (o *TravelSkillOrchestrator) LoadOrInitRuntime(userID, sessionID string) TravelSkillRuntime {
	key := userID + ":" + sessionID
	o.mu.Lock()
	defer o.mu.Unlock()

	rt, ok := o.runtimes[key]
	if !ok {
		now := time.Now().Unix()
		rt = &TravelSkillRuntime{
			RunID:        uuid.NewString(),
			UserID:       userID,
			SessionID:    sessionID,
			CurrentStage: StageRequirementIntake,
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
func (o *TravelSkillOrchestrator) updateRuntime(userID, sessionID string, fn func(rt *TravelSkillRuntime)) {
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

func (o *TravelSkillOrchestrator) resetRuntimeForNewPlanningIntent(userID, sessionID string) {
	key := userID + ":" + sessionID
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now().Unix()
	o.runtimes[key] = &TravelSkillRuntime{
		RunID:        uuid.NewString(),
		UserID:       userID,
		SessionID:    sessionID,
		CurrentStage: StageRequirementIntake,
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
func (o *TravelSkillOrchestrator) Handle(
	ctx context.Context,
	userID, sessionID string,
	userMessage string,
	intakeAgent agentcore.Agent,
) (*SkillResult, error) {

	rt := o.LoadOrInitRuntime(userID, sessionID)
	latestUserMessage := latestUserTurnText(userMessage)
	resetForNewPlanning := false
	if shouldClassifyNewPlanningIntent(rt.CurrentStage) {
		isNewPlanning, err := o.classifyNewPlanningIntent(ctx, sessionID, userMessage, rt, intakeAgent)
		if err != nil {
			log.Warnf("[orchestrator] new planning classifier failed: %v", err)
		} else if isNewPlanning {
			log.Infof("[orchestrator] detected new planning intent by prompt, resetting runtime: userID=%s sessionID=%s oldStage=%s",
				userID, sessionID, rt.CurrentStage)
			o.resetRuntimeForNewPlanningIntent(userID, sessionID)
			rt = o.LoadOrInitRuntime(userID, sessionID)
			resetForNewPlanning = true
		}
	}
	o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
		r.LastUserMessage = latestUserMessage
	})

	log.Infof("[orchestrator] handle: userID=%s sessionID=%s stage=%s msgLen=%d",
		userID, sessionID, rt.CurrentStage, len(latestUserMessage))

	switch rt.CurrentStage {
	case StageRequirementIntake, "":
		intakeMessage := latestUserMessage
		if !resetForNewPlanning && len(extractUserTurnTexts(userMessage)) > 1 {
			intakeMessage = userMessage
		}
		return o.runRequirementIntake(ctx, userID, sessionID, intakeMessage, intakeAgent)

	case StageAwaitingUserInfo:
		o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
			r.PreviousStage = r.CurrentStage
			r.CurrentStage = StageRequirementMerge
		})
		return o.runRequirementMerge(ctx, userID, sessionID, latestUserMessage, intakeAgent)

	case StageRequirementMerge:
		return o.runRequirementMerge(ctx, userID, sessionID, latestUserMessage, intakeAgent)

	case StageMacroPlanning, StageGraphSplitting, StageDayExpansion, StageReview, StageFinalOutput:
		rt = o.LoadOrInitRuntime(userID, sessionID)
		return &SkillResult{
			SkillName:        "travel-skill-orchestrator",
			Stage:            rt.CurrentStage,
			Status:           "ready",
			RequirementReady: rt.Requirement.RequirementReady,
			StopWorkflow:     false,
			NextStage:        rt.CurrentStage,
		}, nil

	default:
		o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
			r.CurrentStage = StageRequirementIntake
		})
		intakeMessage := latestUserMessage
		if !resetForNewPlanning && len(extractUserTurnTexts(userMessage)) > 1 {
			intakeMessage = userMessage
		}
		return o.runRequirementIntake(ctx, userID, sessionID, intakeMessage, intakeAgent)
	}
}

// ═══════════════════════════════════════════════════════════════
// runRequirementIntake — 需求准入
// ═══════════════════════════════════════════════════════════════

func (o *TravelSkillOrchestrator) runRequirementIntake(
	ctx context.Context,
	userID, sessionID string,
	userMessage string,
	intakeAgent agentcore.Agent,
) (*SkillResult, error) {

	rt := o.LoadOrInitRuntime(userID, sessionID)
	log.Infof("[orchestrator] intake: start userID=%s sessionID=%s", userID, sessionID)

	// Step 1: 构建 prompt
	prompt := buildIntakePrompt(userMessage, rt)

	// Step 2: 运行 intakeAgent（只有 skill 工具，不能调用图/地图/攻略）
	rawOutput, err := o.runAgentAndCollect(ctx, intakeAgent, sessionID, prompt)
	if err != nil {
		log.Errorf("[orchestrator] intake: agent error: %v", err)
		return nil, fmt.Errorf("intake agent: %w", err)
	}

	// Step 3: 保存原始输出
	o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
		r.LastSkillOutput = rawOutput
	})

	// Step 4: 解析 SkillResult — 失败不回退，返回稳定 fallback
	result := parseSkillResult(rawOutput)
	if result == nil {
		log.Errorf("[orchestrator] intake: parse failed, outputLen=%d", len(rawOutput))
		return &SkillResult{
			SkillName:    "travel-requirement-intake",
			Status:       "failed",
			ErrorCode:    ErrCodeParseFailed,
			StopWorkflow: true,
			Output:       "我暂时没能稳定分析你的需求，请你按出发地、时间、预算、交通方式、偏好这几个点简单说一下。",
		}, nil
	}

	// Step 5: 合并 LLM 抽取的字段到 snapshot
	if snap, ok := result.Result["requirement"].(map[string]any); ok {
		o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
			mergeSnapshotFromMap(&r.Requirement, snap)
			enrichRequirementPlanningAnchors(&r.Requirement)
		})
	} else {
		o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
			enrichRequirementPlanningAnchors(&r.Requirement)
		})
	}

	return o.resolveRequirementDecision(ctx, userID, sessionID, userMessage, result, intakeAgent, "intake")
}

// ═══════════════════════════════════════════════════════════════
// runRequirementMerge — 用户回复合并
// ═══════════════════════════════════════════════════════════════

func (o *TravelSkillOrchestrator) runRequirementMerge(
	ctx context.Context,
	userID, sessionID string,
	userMessage string,
	intakeAgent agentcore.Agent,
) (*SkillResult, error) {

	rt := o.LoadOrInitRuntime(userID, sessionID)
	log.Infof("[orchestrator] merge: start userID=%s sessionID=%s askedRounds=%d",
		userID, sessionID, rt.AskedRounds)

	// Step 1: 序列化已有 snapshot
	snapJSON, _ := json.Marshal(rt.Requirement)
	missingBefore := rt.Requirement.MissingFields
	if len(missingBefore) == 0 {
		missingBefore = requirementMissingFields(rt.Requirement)
	}

	// Step 2: 构建 merge prompt
	prompt := buildMergePrompt(userMessage, string(snapJSON), missingBefore, rt.LastFollowUpQuestions)

	// Step 3: 运行 intakeAgent
	rawOutput, err := o.runAgentAndCollect(ctx, intakeAgent, sessionID, prompt)
	if err != nil {
		log.Errorf("[orchestrator] merge: agent error: %v", err)
		return nil, fmt.Errorf("merge agent: %w", err)
	}

	// Step 4: 保存原始输出
	o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
		r.LastSkillOutput = rawOutput
	})

	// Step 5: 解析 — 失败不回退 intake，返回稳定 fallback
	result := parseSkillResult(rawOutput)
	if result == nil {
		log.Errorf("[orchestrator] merge: parse failed, outputLen=%d", len(rawOutput))
		return &SkillResult{
			SkillName:    "travel-requirement-merge",
			Status:       "failed",
			ErrorCode:    ErrCodeParseFailed,
			StopWorkflow: true,
			Output:       "我刚才没有稳定识别你的补充信息，请你按出发地、时间、预算、交通方式、偏好这几个点再简单发一次。",
		}, nil
	}

	// Step 6: 合并新字段到 snapshot
	if snap, ok := result.Result["requirement"].(map[string]any); ok {
		o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
			mergeSnapshotFromMap(&r.Requirement, snap)
			enrichRequirementPlanningAnchors(&r.Requirement)
		})
	} else {
		o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
			enrichRequirementPlanningAnchors(&r.Requirement)
		})
	}

	return o.resolveRequirementDecision(ctx, userID, sessionID, userMessage, result, intakeAgent, "merge")
}

// ═══════════════════════════════════════════════════════════════
// 代码层决策函数
// ═══════════════════════════════════════════════════════════════

// buildPlanningDecision 统一决策入口。
// 不依赖自然语言解析，纯粹基于 snapshot 的结构化字段完整度计算。
func buildPlanningDecision(snap TravelRequirementSnapshot) TravelPlanningDecision {

	missingP0 := computeMissingP0Fields(snap)
	missingP1 := computeMissingP1Fields(snap)
	missingP2 := computeMissingP2Fields(snap)

	decision := TravelPlanningDecision{
		MissingP0: missingP0,
		MissingP1: missingP1,
		MissingP2: missingP2,
	}

	if len(missingP0) > 0 {
		decision.Ready = false
		decision.ShouldAskUser = true
		return decision
	}

	missingDetailFields := append(append([]string{}, missingP1...), missingP2...)
	if len(missingDetailFields) > 0 {
		decision.Ready = false
		decision.ShouldAskUser = true
		return decision
	}

	decision.Ready = true
	return decision
}

// ═══════════════════════════════════════════════════════════════
// 字段缺失计算（代码层，不依赖 LLM）
// ═══════════════════════════════════════════════════════════════

func computeMissingP0Fields(snap TravelRequirementSnapshot) []string {
	var m []string
	if snap.StartCity == "" {
		m = append(m, "start_city")
	}
	if snap.TotalDays == 0 {
		m = append(m, "total_days")
	}
	if snap.DestinationScope == "" {
		m = append(m, "destination_scope")
	}
	return m
}

func computeMissingP1Fields(snap TravelRequirementSnapshot) []string {
	var m []string
	if snap.BudgetTotal == "" && snap.BudgetMonthly == "" {
		m = append(m, "budget")
	}
	if snap.TransportMode == "" {
		m = append(m, "transport_mode")
	}
	if len(snap.TravelStyle) == 0 {
		m = append(m, "travel_style")
	}
	if snap.Pace == "" {
		m = append(m, "pace")
	}
	if snap.StartDate == "" {
		m = append(m, "start_date")
	}
	if requiresHighAltitudeCheck(snap) && snap.HighAltitudeAcceptance == "" {
		m = append(m, "high_altitude_acceptance")
	}
	if requiresDrivingIntensityCheck(snap) && snap.DailyDrivingPreference == "" {
		m = append(m, "daily_driving_preference")
	}
	return m
}

func computeMissingP2Fields(snap TravelRequirementSnapshot) []string {
	var m []string
	if snap.AccommodationStyle == "" {
		m = append(m, "accommodation_style")
	}
	if len(snap.FoodPreference) == 0 {
		m = append(m, "food_preference")
	}
	return m
}

func requirementMissingFields(snap TravelRequirementSnapshot) []string {
	return append(append(computeMissingP0Fields(snap), computeMissingP1Fields(snap)...), computeMissingP2Fields(snap)...)
}

// ═══════════════════════════════════════════════════════════════
// Prompt 构造函数
// ═══════════════════════════════════════════════════════════════

func buildIntakePrompt(userMessage string, rt TravelSkillRuntime) string {
	runtimeJSON, _ := json.Marshal(rt.Requirement)
	return fmt.Sprintf(`你是旅行需求准入分析 Agent。

请加载并遵循 travel-requirement-intake skill。

当前已有需求快照：
%s

用户消息或历史片段：
%s

当前日期：
%s

约束：
- 只输出一个 SkillResult JSON，不要 markdown，不要解释。
- 只输出 TravelRequirementSnapshot 的合法字段。
- 已有非空字段不得重复追问；不要用空值覆盖已有值。
- P0 字段不能默认；是否默认由 result.default_intent 输出。`, string(runtimeJSON), userMessage, time.Now().Format("2006-01-02"))
}

func buildMergePrompt(userMessage string, snapshotJSON string, missingFields []string, lastQuestions []string) string {
	missingJSON, _ := json.Marshal(missingFields)
	questionsJSON, _ := json.Marshal(lastQuestions)
	return fmt.Sprintf(`你是旅行需求合并 Agent。

请加载并遵循 travel-requirement-merge skill。

已有需求快照：
%s

当前仍缺字段（按上一轮追问顺序）：
%s

上一轮追问问题：
%s

用户新回复：
%s

当前日期：
%s

约束：
- 只输出一个 SkillResult JSON，不要 markdown，不要解释。
- 只输出 TravelRequirementSnapshot 的合法字段。
- 只合并用户新回复明确表达的字段；不要用空值覆盖已有非空字段。
- 已有非空字段不得重复追问。
- P0 字段不能默认；是否默认由 result.default_intent 输出。`, snapshotJSON, string(missingJSON), string(questionsJSON), userMessage, time.Now().Format("2006-01-02"))
}

// ═══════════════════════════════════════════════════════════════
// Prompt 驱动辅助任务
// ═══════════════════════════════════════════════════════════════

func shouldClassifyNewPlanningIntent(stage TravelSkillStage) bool {
	switch stage {
	case StageMacroPlanning, StageGraphSplitting, StageDayExpansion, StageReview, StageFinalOutput, StageDone:
		return true
	default:
		return false
	}
}

func (o *TravelSkillOrchestrator) classifyNewPlanningIntent(
	ctx context.Context,
	sessionID string,
	userMessage string,
	rt TravelSkillRuntime,
	intakeAgent agentcore.Agent,
) (bool, error) {
	prompt := buildNewPlanningIntentPrompt(userMessage, rt)
	rawOutput, err := o.runAgentAndCollect(ctx, intakeAgent, sessionID, prompt)
	if err != nil {
		return false, err
	}
	return parseNewPlanningIntentResult(rawOutput)
}

func buildNewPlanningIntentPrompt(userMessage string, rt TravelSkillRuntime) string {
	snapshotJSON, _ := json.Marshal(rt.Requirement)
	return fmt.Sprintf(`你是旅行规划会话意图分类 Agent。

任务：判断用户最新消息是否是在已有规划会话中发起一个全新的旅行规划需求。

当前阶段：
%s

当前需求快照：
%s

用户最新消息：
%s

只输出 JSON：
{"new_planning_intent": true|false}

如果不确定，输出 false。`, rt.CurrentStage, string(snapshotJSON), latestUserTurnText(userMessage))
}

func parseNewPlanningIntentResult(output string) (bool, error) {
	s := strings.Index(output, "{")
	e := strings.LastIndex(output, "}")
	if s < 0 || e <= s {
		return false, fmt.Errorf("new planning intent parse failed")
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(output[s:e+1]), &root); err != nil {
		return false, err
	}
	if v, ok := root["new_planning_intent"].(bool); ok {
		return v, nil
	}
	if result, ok := root["result"].(map[string]any); ok {
		if v, ok := result["new_planning_intent"].(bool); ok {
			return v, nil
		}
	}
	return false, fmt.Errorf("new_planning_intent missing")
}

func (o *TravelSkillOrchestrator) resolveRequirementDecision(
	ctx context.Context,
	userID, sessionID string,
	userMessage string,
	result *SkillResult,
	intakeAgent agentcore.Agent,
	source string,
) (*SkillResult, error) {
	rt := o.LoadOrInitRuntime(userID, sessionID)
	decision := buildPlanningDecision(rt.Requirement)
	defaultIntent := resultDefaultIntent(result)
	maxAskRounds := rt.MaxAskRounds
	if maxAskRounds <= 0 {
		maxAskRounds = 2
	}

	if decision.Ready {
		return o.finishRequirementReady(userID, sessionID, result), nil
	}

	if len(decision.MissingP0) == 0 && defaultableMissingCount(decision) > 0 {
		if defaultIntent != DefaultIntentNone || rt.AskedRounds >= maxAskRounds {
			defaultResult, err := o.runRequirementDefaultCompletion(ctx, userID, sessionID, decision, defaultIntent, intakeAgent)
			if err != nil {
				log.Warnf("[orchestrator] %s: default completion failed: %v", source, err)
			} else if defaultResult != nil {
				result = defaultResult
				if snap, ok := defaultResult.Result["requirement"].(map[string]any); ok {
					o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
						mergeSnapshotFromMap(&r.Requirement, snap)
						enrichRequirementPlanningAnchors(&r.Requirement)
					})
				}
				decision = buildPlanningDecision(o.LoadOrInitRuntime(userID, sessionID).Requirement)
				if decision.Ready {
					return o.finishRequirementReady(userID, sessionID, defaultResult), nil
				}
			}
		}
	}

	missing := missingFieldsFromDecision(decision)
	questions := sanitizeFollowUpQuestions(result.FollowUpQuestions, missing)
	output := strings.TrimSpace(result.Output)
	if len(questions) == 0 {
		questionResult, err := o.runRequirementQuestionGeneration(ctx, userID, sessionID, missing, intakeAgent)
		if err != nil {
			log.Warnf("[orchestrator] %s: question generation failed: %v", source, err)
		} else if questionResult != nil {
			questions = sanitizeFollowUpQuestions(questionResult.FollowUpQuestions, missing)
			if strings.TrimSpace(questionResult.Output) != "" {
				output = strings.TrimSpace(questionResult.Output)
			}
		}
	}
	if output == "" {
		output = strings.Join(questions, "\n")
	}
	if output == "" {
		output = "我还需要再确认几项信息才能继续规划，请补充你还没有提到的旅行需求。"
	}

	o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
		r.PreviousStage = r.CurrentStage
		r.CurrentStage = StageAwaitingUserInfo
		r.Requirement.MissingFields = missing
		r.Requirement.RequirementReady = false
		r.LastFollowUpQuestions = questions
		r.AskedRounds++
	})

	return &SkillResult{
		SkillName:         result.SkillName,
		Stage:             StageAwaitingUserInfo,
		Status:            "need_user_input",
		RequirementReady:  false,
		InsufficientInfo:  true,
		MissingFields:     missing,
		FollowUpQuestions: questions,
		Result:            result.Result,
		NextStage:         StageAwaitingUserInfo,
		StopWorkflow:      true,
		ErrorCode:         ErrCodeRequirementNotReady,
		Output:            output,
	}, nil
}

func (o *TravelSkillOrchestrator) finishRequirementReady(
	userID, sessionID string,
	result *SkillResult,
) *SkillResult {
	output := strings.TrimSpace(result.Output)
	if output == "" {
		output = "需求已确认，开始规划。"
	}
	o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
		r.PreviousStage = r.CurrentStage
		r.CurrentStage = StageMacroPlanning
		r.Requirement.MissingFields = nil
		r.Requirement.RequirementReady = true
		r.LastFollowUpQuestions = nil
	})
	return &SkillResult{
		SkillName:         result.SkillName,
		Stage:             StageMacroPlanning,
		Status:            "ready",
		RequirementReady:  true,
		MissingFields:     nil,
		FollowUpQuestions: nil,
		Result:            result.Result,
		NextStage:         StageMacroPlanning,
		StopWorkflow:      false,
		Output:            output,
	}
}

func (o *TravelSkillOrchestrator) runRequirementDefaultCompletion(
	ctx context.Context,
	userID, sessionID string,
	decision TravelPlanningDecision,
	defaultIntent TravelDefaultIntent,
	intakeAgent agentcore.Agent,
) (*SkillResult, error) {
	rt := o.LoadOrInitRuntime(userID, sessionID)
	snapshotJSON, _ := json.Marshal(rt.Requirement)
	missing := append(append([]string{}, decision.MissingP1...), decision.MissingP2...)
	prompt := buildDefaultCompletionPrompt(string(snapshotJSON), missing, defaultIntent)
	rawOutput, err := o.runAgentAndCollect(ctx, intakeAgent, sessionID, prompt)
	if err != nil {
		return nil, err
	}
	o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
		r.LastSkillOutput = rawOutput
	})
	parsed := parseSkillResult(rawOutput)
	if parsed == nil {
		return nil, fmt.Errorf("default completion parse failed")
	}
	return parsed, nil
}

func buildDefaultCompletionPrompt(snapshotJSON string, missingFields []string, defaultIntent TravelDefaultIntent) string {
	missingJSON, _ := json.Marshal(missingFields)
	return fmt.Sprintf(`你是旅行需求默认补齐 Agent。

请加载并遵循 travel-requirement-merge skill 中的默认策略。

已有需求快照：
%s

需要默认补齐的非 P0 字段：
%s

用户默认意图：
%s

约束：
- 只输出一个 SkillResult JSON，不要 markdown，不要解释。
- P0 字段不能默认；如果发现 P0 缺失，必须保持缺失并追问。
- 只能补齐 TravelRequirementSnapshot 的合法字段。
- 不要用空值覆盖已有非空字段。
- 在 result.default_intent 中输出默认意图。`, snapshotJSON, string(missingJSON), defaultIntent)
}

func (o *TravelSkillOrchestrator) runRequirementQuestionGeneration(
	ctx context.Context,
	userID, sessionID string,
	missingFields []string,
	intakeAgent agentcore.Agent,
) (*SkillResult, error) {
	rt := o.LoadOrInitRuntime(userID, sessionID)
	snapshotJSON, _ := json.Marshal(rt.Requirement)
	prompt := buildQuestionPrompt(string(snapshotJSON), missingFields)
	rawOutput, err := o.runAgentAndCollect(ctx, intakeAgent, sessionID, prompt)
	if err != nil {
		return nil, err
	}
	o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
		r.LastSkillOutput = rawOutput
	})
	parsed := parseSkillResult(rawOutput)
	if parsed == nil {
		return nil, fmt.Errorf("question generation parse failed")
	}
	return parsed, nil
}

func buildQuestionPrompt(snapshotJSON string, missingFields []string) string {
	missingJSON, _ := json.Marshal(missingFields)
	return fmt.Sprintf(`你是旅行需求追问生成 Agent。

请加载并遵循 travel-requirement-intake skill 的追问策略。

已有需求快照：
%s

当前缺失字段：
%s

约束：
- 只输出一个 SkillResult JSON，不要 markdown，不要解释。
- follow_up_questions 只能询问当前缺失字段。
- 已有非空字段不得重复追问。
- P0 字段不能默认。
- output 使用 follow_up_questions 的自然语言内容。`, snapshotJSON, string(missingJSON))
}

func resultDefaultIntent(result *SkillResult) TravelDefaultIntent {
	if result == nil || result.Result == nil {
		return DefaultIntentNone
	}
	value, ok := result.Result["default_intent"]
	if !ok {
		return DefaultIntentNone
	}
	switch TravelDefaultIntent(strings.TrimSpace(fmt.Sprint(value))) {
	case DefaultIntentExplicit:
		return DefaultIntentExplicit
	case DefaultIntentImplicit:
		return DefaultIntentImplicit
	default:
		return DefaultIntentNone
	}
}

func sanitizeFollowUpQuestions(questions []string, missingFields []string) []string {
	if len(missingFields) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(questions))
	for _, question := range questions {
		question = strings.TrimSpace(question)
		if question == "" || seen[question] {
			continue
		}
		seen[question] = true
		out = append(out, question)
	}
	if len(out) == 0 || len(out) > len(missingFields) {
		return nil
	}
	return out
}

func missingFieldsFromDecision(decision TravelPlanningDecision) []string {
	return appendUniqueStrings(nil,
		append(append(append([]string{}, decision.MissingP0...), decision.MissingP1...), decision.MissingP2...)...,
	)
}

func defaultableMissingCount(decision TravelPlanningDecision) int {
	return len(decision.MissingP1) + len(decision.MissingP2)
}

func enrichRequirementPlanningAnchors(snap *TravelRequirementSnapshot) {
	if snap == nil {
		return
	}
	derived := deriveDestinationAnchors(*snap, "")
	if len(derived) == 0 {
		return
	}
	snap.DestinationAnchors = dedupeDestinationAnchors(append(snap.DestinationAnchors, derived...))
}

// ═══════════════════════════════════════════════════════════════
// 字段合并函数
// ═══════════════════════════════════════════════════════════════

// mergeSnapshotFromMap 将 LLM 抽取的 map 合并到 requirement snapshot。
// 只更新非空字段，不覆盖已有值。
func mergeSnapshotFromMap(snap *TravelRequirementSnapshot, m map[string]any) {
	setString := func(key string, setter func(string)) {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			setter(strings.TrimSpace(v))
		}
	}

	setString("destination_scope", func(v string) { snap.DestinationScope = v })
	setString("start_date", func(v string) { snap.StartDate = v })
	setString("end_date", func(v string) { snap.EndDate = v })
	setString("start_city", func(v string) { snap.StartCity = v })
	setString("end_city", func(v string) { snap.EndCity = v })
	setString("budget_total", func(v string) { snap.BudgetTotal = v })
	setString("budget_monthly", func(v string) { snap.BudgetMonthly = v })
	setString("transport_mode", func(v string) { snap.TransportMode = v })
	setString("pace", func(v string) { snap.Pace = v })
	setString("high_altitude_acceptance", func(v string) { snap.HighAltitudeAcceptance = v })
	setString("daily_driving_preference", func(v string) { snap.DailyDrivingPreference = v })
	setString("accommodation_style", func(v string) { snap.AccommodationStyle = v })

	if v, ok := m["total_days"].(float64); ok && v > 0 {
		snap.TotalDays = int(v)
	}
	if v, ok := m["total_days"].(int); ok && v > 0 {
		snap.TotalDays = v
	}

	if arr, ok := m["travel_style"].([]any); ok {
		snap.TravelStyle = anySliceToStringSlice(arr)
	}
	if arr, ok := m["food_preference"].([]any); ok {
		snap.FoodPreference = anySliceToStringSlice(arr)
	}
	if arr, ok := m["must_visit"].([]any); ok {
		snap.MustVisit = anySliceToStringSlice(arr)
	}
	if arr, ok := m["avoid_places"].([]any); ok {
		snap.AvoidPlaces = anySliceToStringSlice(arr)
	}
	if arr, ok := m["special_constraints"].([]any); ok {
		snap.SpecialConstraints = anySliceToStringSlice(arr)
	}
	if arr, ok := m["destination_anchors"].([]any); ok {
		snap.DestinationAnchors = anySliceToDestinationAnchors(arr)
	}
}

func anySliceToStringSlice(arr []any) []string {
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s := strings.TrimSpace(fmt.Sprint(item))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func anySliceToDestinationAnchors(arr []any) []DestinationAnchorSnapshot {
	out := make([]DestinationAnchorSnapshot, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		mapString := func(key string) string {
			if v, ok := m[key]; ok && v != nil {
				return strings.TrimSpace(fmt.Sprint(v))
			}
			return ""
		}
		anchor := DestinationAnchorSnapshot{
			Destination: mapString("destination"),
			Name:        mapString("name"),
			Kind:        mapString("kind"),
			Origin:      mapString("origin"),
			Query:       mapString("query"),
			Reason:      mapString("reason"),
		}
		if anchor.Name == "" {
			continue
		}
		if v, ok := m["priority"].(float64); ok {
			anchor.Priority = int(v)
		}
		if v, ok := m["must_cover"].(bool); ok {
			anchor.MustCover = v
		}
		if arr, ok := m["themes"].([]any); ok {
			anchor.Themes = anySliceToStringSlice(arr)
		}
		out = append(out, anchor)
	}
	return out
}

// ═══════════════════════════════════════════════════════════════
// 辅助方法
// ═══════════════════════════════════════════════════════════════

// runAgentAndCollect 运行 Agent 并收集完整文本输出。
// 不流式透传给用户 — intake/merge 阶段只展示 result.Output。
func (o *TravelSkillOrchestrator) runAgentAndCollect(
	ctx context.Context,
	ag agentcore.Agent,
	sessionID string,
	prompt string,
) (string, error) {
	rn := runner.NewRunner("orchestrator-"+sessionID, ag)
	defer rn.Close()

	eventCh, err := rn.Run(ctx, "orchestrator-system",
		sessionID+"-"+uuid.NewString()[:8],
		model.NewUserMessage(prompt), agentcore.WithStream(true))
	if err != nil {
		return "", err
	}

	var out strings.Builder
	for evt := range eventCh {
		if evt == nil || evt.Response == nil {
			continue
		}
		for _, c := range evt.Response.Choices {
			if c.Delta.Content != "" {
				out.WriteString(c.Delta.Content)
			}
			if c.Message.Content != "" && out.Len() == 0 {
				out.WriteString(c.Message.Content)
			}
		}
	}
	return out.String(), nil
}

// parseSkillResult 从 LLM 文本输出中解析 SkillResult JSON。
func parseSkillResult(output string) *SkillResult {
	s := strings.Index(output, "{")
	e := strings.LastIndex(output, "}")
	if s < 0 || e <= s {
		return nil
	}
	var r SkillResult
	if json.Unmarshal([]byte(output[s:e+1]), &r) != nil {
		return nil
	}
	if r.SkillName == "" {
		return nil
	}
	return &r
}

// startCleanupLoop 定期清理过期的 runtime。
func (o *TravelSkillOrchestrator) startCleanupLoop(ttl time.Duration) {
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
