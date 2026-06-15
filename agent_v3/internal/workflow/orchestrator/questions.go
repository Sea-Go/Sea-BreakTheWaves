package orchestrator

import (
	"fmt"
	"strings"
)

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
