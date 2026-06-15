package orchestrator

import (
	"strings"

	workflowruntime "agent_v3/internal/workflow/runtime"
)

func buildPlanningDecision(
	snap workflowruntime.TravelRequirementSnapshot,
	askedRounds int,
	maxAskRounds int,
	userMessage string,
) workflowruntime.TravelPlanningDecision {

	missingP0 := computeMissingP0Fields(snap)
	missingP1 := computeMissingP1Fields(snap)
	missingP2 := computeMissingP2Fields(snap)

	defaultIntent := detectDefaultIntent(latestUserTurnText(userMessage))

	decision := workflowruntime.TravelPlanningDecision{
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
		if defaultIntent == workflowruntime.DefaultIntentExplicit {
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
func detectDefaultIntent(userMessage string) workflowruntime.TravelDefaultIntent {
	msg := strings.TrimSpace(userMessage)
	keywords := []string{
		"按默认", "直接规划", "别问了", "不用问", "你决定",
		"你看着安排", "都可以", "无所谓", "默认来", "按你推荐",
	}
	for _, kw := range keywords {
		if strings.Contains(msg, kw) {
			return workflowruntime.DefaultIntentExplicit
		}
	}
	return workflowruntime.DefaultIntentNone
}

// ═══════════════════════════════════════════════════════════════
// 字段缺失计算（代码层，不依赖 LLM）
// ═══════════════════════════════════════════════════════════════

func computeMissingP0Fields(snap workflowruntime.TravelRequirementSnapshot) []string {
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

func computeMissingP1Fields(snap workflowruntime.TravelRequirementSnapshot) []string {
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

func computeMissingP2Fields(snap workflowruntime.TravelRequirementSnapshot) []string {
	var m []string
	if snap.AccommodationStyle == "" {
		m = append(m, "accommodation_style")
	}
	if len(snap.FoodPreference) == 0 {
		m = append(m, "food_preference")
	}
	return m
}

func applyDefaultsForOptionalFields(snap *workflowruntime.TravelRequirementSnapshot) {
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

func BuildPlanningDecision(
	snap workflowruntime.TravelRequirementSnapshot,
	askedRounds int,
	maxAskRounds int,
	userMessage string,
) workflowruntime.TravelPlanningDecision {
	return buildPlanningDecision(snap, askedRounds, maxAskRounds, userMessage)
}

// ═══════════════════════════════════════════════════════════════
// Prompt 构造函数
// ═══════════════════════════════════════════════════════════════
