package orchestrator

import (
	"encoding/json"
	"fmt"

	workflowruntime "agent_v3/internal/workflow/runtime"
)

func buildIntakePrompt(userMessage string, rt workflowruntime.TravelSkillRuntime) string {
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
