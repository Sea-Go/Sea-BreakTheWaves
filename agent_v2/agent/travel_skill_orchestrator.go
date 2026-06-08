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
	if rt.CurrentStage != StageRequirementIntake && rt.CurrentStage != "" && isLikelyNewPlanningRequest(latestUserMessage) {
		log.Infof("[orchestrator] detected new planning intent, resetting runtime: userID=%s sessionID=%s oldStage=%s",
			userID, sessionID, rt.CurrentStage)
		o.resetRuntimeForNewPlanningIntent(userID, sessionID)
		rt = o.LoadOrInitRuntime(userID, sessionID)
	}
	o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
		r.LastUserMessage = latestUserMessage
	})

	log.Infof("[orchestrator] handle: userID=%s sessionID=%s stage=%s msgLen=%d",
		userID, sessionID, rt.CurrentStage, len(latestUserMessage))

	switch rt.CurrentStage {
	case StageRequirementIntake, "":
		return o.runRequirementIntake(ctx, userID, sessionID, latestUserMessage, intakeAgent)

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
		return o.runRequirementIntake(ctx, userID, sessionID, latestUserMessage, intakeAgent)
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
			enrichRequirementWithDeterministicFields(&r.Requirement, userMessage)
		})
	} else {
		o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
			enrichRequirementWithDeterministicFields(&r.Requirement, userMessage)
		})
	}

	// Step 6: 重新读取 runtime 获取合并后的最新状态
	rt = o.LoadOrInitRuntime(userID, sessionID)

	// Step 7: 代码层决策（不依赖 LLM 的 requirement_ready）
	decision := buildPlanningDecision(rt.Requirement, rt.AskedRounds, rt.MaxAskRounds, userMessage)

	log.Infof("[orchestrator] intake: decision ready=%v missingP0=%v missingP1=%v askedRounds=%d",
		decision.Ready, decision.MissingP0, decision.MissingP1, rt.AskedRounds)

	if decision.Ready {
		o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
			applyDefaultsForOptionalFields(&r.Requirement)
			r.Requirement.RequirementReady = true
			r.CurrentStage = StageMacroPlanning
		})
		result.RequirementReady = true
		result.NextStage = StageMacroPlanning
		result.StopWorkflow = false
		log.Infof("[orchestrator] intake: ready → macro_planning")
	} else {
		o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
			r.AskedRounds++
			r.Requirement.MissingFields = append(append(decision.MissingP0, decision.MissingP1...), decision.MissingP2...)
			r.CurrentStage = StageAwaitingUserInfo
		})
		result.RequirementReady = false
		result.MissingFields = append(append(decision.MissingP0, decision.MissingP1...), decision.MissingP2...)
		result.FollowUpQuestions = decision.Questions
		result.NextStage = StageAwaitingUserInfo
		result.StopWorkflow = true
		result.Output = formatPlanningQuestions(decision.Questions)
		log.Infof("[orchestrator] intake: not ready → askedRounds=%d questions=%d",
			rt.AskedRounds+1, len(decision.Questions))
	}

	return result, nil
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

	// Step 2: 构建 merge prompt
	prompt := buildMergePrompt(userMessage, string(snapJSON))

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
			enrichRequirementWithDeterministicFields(&r.Requirement, userMessage)
		})
	} else {
		o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
			enrichRequirementWithDeterministicFields(&r.Requirement, userMessage)
		})
	}

	// Step 7: 重新读取
	rt = o.LoadOrInitRuntime(userID, sessionID)

	// Step 8: 代码层决策
	decision := buildPlanningDecision(rt.Requirement, rt.AskedRounds, rt.MaxAskRounds, userMessage)

	log.Infof("[orchestrator] merge: decision ready=%v missingP0=%v missingP1=%v askedRounds=%d maxRounds=%d",
		decision.Ready, decision.MissingP0, decision.MissingP1, rt.AskedRounds, rt.MaxAskRounds)

	if decision.Ready {
		o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
			applyDefaultsForOptionalFields(&r.Requirement)
			r.Requirement.RequirementReady = true
			r.CurrentStage = StageMacroPlanning
		})
		result.RequirementReady = true
		result.NextStage = StageMacroPlanning
		result.StopWorkflow = false
		log.Infof("[orchestrator] merge: ready → macro_planning")
		return result, nil
	}

	// Step 9: 不 ready — 检查追问轮数
	if rt.AskedRounds >= rt.MaxAskRounds {
		// 达上限 + P0 仍缺失 → 不能默认，只能继续追问 P0
		if len(decision.MissingP0) > 0 {
			o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
				r.CurrentStage = StageAwaitingUserInfo
			})
			return &SkillResult{
				SkillName:         "travel-requirement-merge",
				Status:            "need_user_input",
				ErrorCode:         ErrCodeRequirementNotReady,
				MissingFields:     decision.MissingP0,
				FollowUpQuestions: decision.Questions,
				StopWorkflow:      true,
				Output:            formatPlanningQuestions(decision.Questions),
			}, nil
		}
		// 达上限 + P0 满足 + P1 缺失 → 默认 P1/P2，进入 planning
		o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
			applyDefaultsForOptionalFields(&r.Requirement)
			r.Requirement.RequirementReady = true
			r.CurrentStage = StageMacroPlanning
		})
		result.RequirementReady = true
		result.NextStage = StageMacroPlanning
		result.StopWorkflow = false
		log.Infof("[orchestrator] merge: maxRounds reached, defaulting P1 → macro_planning")
		return result, nil
	}

	// Step 10: 未达上限 → 继续追问
	o.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
		r.AskedRounds++ // 只在发起新追问时 +1
		r.Requirement.MissingFields = append(append(decision.MissingP0, decision.MissingP1...), decision.MissingP2...)
		r.CurrentStage = StageAwaitingUserInfo
	})
	result.RequirementReady = false
	result.MissingFields = append(append(decision.MissingP0, decision.MissingP1...), decision.MissingP2...)
	result.FollowUpQuestions = decision.Questions
	result.NextStage = StageAwaitingUserInfo
	result.StopWorkflow = true
	result.Output = formatPlanningQuestions(decision.Questions)
	log.Infof("[orchestrator] merge: not ready → askedRounds=%d questions=%d",
		rt.AskedRounds+1, len(decision.Questions))

	return result, nil
}

// ═══════════════════════════════════════════════════════════════
// 代码层决策函数
// ═══════════════════════════════════════════════════════════════

// buildPlanningDecision 统一决策入口。
// 不依赖 LLM 输出，纯粹基于 snapshot + askedRounds + userMessage 计算。
func buildPlanningDecision(
	snap TravelRequirementSnapshot,
	askedRounds int,
	maxAskRounds int,
	userMessage string,
) TravelPlanningDecision {

	missingP0 := computeMissingP0Fields(snap)
	missingP1 := computeMissingP1Fields(snap)
	missingP2 := computeMissingP2Fields(snap)

	defaultIntent := detectDefaultIntent(latestUserTurnText(userMessage))

	decision := TravelPlanningDecision{
		MissingP0:     missingP0,
		MissingP1:     missingP1,
		MissingP2:     missingP2,
		DefaultIntent: defaultIntent,
	}

	// ── P0 缺失 → 绝对不能 ready ──
	if len(missingP0) > 0 {
		decision.Ready = false
		decision.ShouldAskUser = true
		decision.Questions = buildPlanningQuestions(append(missingP0, missingP1...))
		return decision
	}

	// ── P1/P2 缺失 → 所有行程至少追问一轮 ──
	missingDetailFields := append(append([]string{}, missingP1...), missingP2...)
	if len(missingDetailFields) > 0 {
		if defaultIntent == DefaultIntentExplicit {
			decision.Ready = true
			decision.ShouldApplyDefault = true
			return decision
		}
		if askedRounds >= 1 || askedRounds >= maxAskRounds {
			decision.Ready = true
			decision.ShouldApplyDefault = true
			return decision
		}
		decision.Ready = false
		decision.ShouldAskUser = true
		decision.Questions = buildPlanningQuestions(missingDetailFields)
		return decision
	}

	// ── P0 + P1 都满足 ──
	decision.Ready = true
	decision.ShouldApplyDefault = true
	return decision
}

// detectDefaultIntent 识别用户是否表达了"不用追问，按默认来"的意图。
func detectDefaultIntent(userMessage string) TravelDefaultIntent {
	msg := strings.TrimSpace(userMessage)
	keywords := []string{
		"按默认", "直接规划", "别问了", "不用问", "你决定",
		"你看着安排", "都可以", "无所谓", "默认来", "按你推荐",
	}
	for _, kw := range keywords {
		if strings.Contains(msg, kw) {
			return DefaultIntentExplicit
		}
	}
	return DefaultIntentNone
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

func applyDefaultsForOptionalFields(snap *TravelRequirementSnapshot) {
	if snap.AccommodationStyle == "" {
		snap.AccommodationStyle = "经济舒适型"
	}
	if len(snap.FoodPreference) == 0 {
		snap.FoodPreference = []string{"当地特色"}
	}
	if snap.Pace == "" {
		snap.Pace = "均衡"
	}
	if snap.TransportMode == "" {
		snap.TransportMode = "高铁/火车为主"
	}
	if snap.BudgetTotal == "" {
		snap.BudgetTotal = "中等"
	}
	if len(snap.TravelStyle) == 0 {
		snap.TravelStyle = []string{"自然风光", "历史文化"}
	}
	if snap.HighAltitudeAcceptance == "" && requiresHighAltitudeCheck(*snap) {
		snap.HighAltitudeAcceptance = "默认可接受常规高原行程，出现高反风险时降低强度"
	}
	if snap.DailyDrivingPreference == "" && requiresDrivingIntensityCheck(*snap) {
		snap.DailyDrivingPreference = "默认日均驾驶控制在4-6小时，必要转移日可更长"
	}
}

// ═══════════════════════════════════════════════════════════════
// Prompt 构造函数
// ═══════════════════════════════════════════════════════════════

func buildIntakePrompt(userMessage string, rt TravelSkillRuntime) string {
	runtimeJSON, _ := json.Marshal(rt.Requirement)
	return fmt.Sprintf(`你是旅行需求准入分析 Agent。

你只能做三件事：
1. 加载 travel-requirement-intake skill
2. 从用户消息中抽取结构化字段
3. 输出 SkillResult JSON

禁止：创建 TripPlan、调用地图工具、调用攻略工具、调用天气工具、生成旅行方案。

当前已有需求快照：
%s

用户最新消息：
%s

需要抽取的字段：
- destination_scope: 目的地范围（如"全国""云南""大理"）
- total_days: 总天数（如 365, 7, 30）
- start_city: 出发城市
- start_date: 开始日期
- budget_total / budget_monthly: 预算
- transport_mode: 交通方式（自驾/高铁/飞机/混合）
- travel_style: 旅行风格（自然风光/历史文化/美食/摄影/慢旅行/亲子/城市打卡）
- pace: 节奏（轻松/均衡/紧凑）
- high_altitude_acceptance: 高海拔接受度（可接受/不接受/待确认）
- daily_driving_preference: 日均驾驶强度偏好
- accommodation_style: 住宿偏好
- food_preference: 饮食偏好
- must_visit: 必去地点
- avoid_places: 不想去的地点

只输出一个 SkillResult JSON，不要 markdown，不要解释。`, string(runtimeJSON), userMessage)
}

func buildMergePrompt(userMessage string, snapshotJSON string) string {
	return fmt.Sprintf(`你是旅行需求合并 Agent。

任务：
1. 读取已有 TravelRequirementSnapshot
2. 从用户新回复中抽取字段
3. 只更新用户明确提到的字段
4. 不要用空值覆盖已有值

已有需求快照：
%s

用户新回复：
%s

注意：
- "哈尔滨出发"→ start_city=哈尔滨
- "6月开始"→ start_date=6月
- "10万预算"→ budget_total=10万
- "高铁为主"→ transport_mode=高铁为主
- "慢一点"→ pace=轻松/慢节奏
- "能接受高海拔/怕高反"→ high_altitude_acceptance
- "能接受长途/不想开太久"→ daily_driving_preference
- "就这样吧/按默认/别问了"→ 在 result 中标记 default_intent

只输出 JSON，不要 markdown，不要解释。`, snapshotJSON, userMessage)
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
// 问题生成函数
// ═══════════════════════════════════════════════════════════════

func buildPlanningQuestions(missing []string) []string {
	seen := map[string]bool{}
	questions := make([]string, 0, len(missing))
	for _, field := range missing {
		if seen[field] {
			continue
		}
		seen[field] = true
		switch field {
		case "start_city":
			questions = append(questions, "你计划从哪个城市出发？")
		case "total_days":
			questions = append(questions, "这次旅行总共计划多少天？")
		case "destination_scope":
			questions = append(questions, "你想去哪个区域、省份、国家，还是全国范围？")
		case "budget":
			questions = append(questions, "总预算或每月预算大概是多少？例如3万、10万、每月8000等。")
		case "transport_mode":
			questions = append(questions, "主要交通方式是什么？自驾、高铁火车、飞机，还是混合？")
		case "travel_style":
			questions = append(questions, "你更喜欢什么旅行风格？自然风光、历史文化、美食、摄影、慢旅行、亲子，还是城市打卡？")
		case "pace":
			questions = append(questions, "每日节奏希望轻松、均衡，还是尽量多打卡？")
		case "start_date":
			questions = append(questions, "计划什么时候开始？如果还没确定，我可以默认从下个月开始。")
		case "high_altitude_acceptance":
			questions = append(questions, "这条路线会涉及高海拔和高反风险，你能接受高海拔行程吗？")
		case "daily_driving_preference":
			questions = append(questions, "自驾部分你能接受每天大概多长驾驶时间？例如4小时内、4-6小时，还是可接受更长转移日？")
		case "accommodation_style":
			questions = append(questions, "住宿更偏好经济舒适、精品民宿，还是酒店为主？")
		case "food_preference":
			questions = append(questions, "饮食上有什么偏好或忌口？比如当地特色、清淡、素食、亲子友好等。")
		}
	}
	return questions
}

func formatPlanningQuestions(questions []string) string {
	if len(questions) == 0 {
		return "你的基础需求已经足够，我会按默认偏好继续规划。"
	}
	var b strings.Builder
	b.WriteString("在开始规划前，我需要先确认几项关键信息：\n\n")
	for i, q := range questions {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, q))
	}
	return b.String()
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
