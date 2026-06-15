package events

import "strings"

func SanitizePublicPlanningEvent(ev PublicPlanningEvent) PublicPlanningEvent {
	ev.PublicAction = sanitizePublicText(ev.PublicAction)
	ev.ThoughtSummary = sanitizePublicText(ev.ThoughtSummary)
	ev.Reason = sanitizePublicText(ev.Reason)
	ev.Message = sanitizePublicText(ev.Message)
	if ev.Popup != nil {
		ev.Popup.Title = sanitizePublicText(ev.Popup.Title)
		ev.Popup.Content = sanitizePublicText(ev.Popup.Content)
	}
	if ev.Point != nil {
		ev.Point.Label = sanitizePublicText(ev.Point.Label)
		ev.Point.Kind = sanitizePublicText(ev.Point.Kind)
		ev.Point.Accuracy = sanitizePublicText(ev.Point.Accuracy)
		ev.Point.Source = sanitizePublicText(ev.Point.Source)
		ev.Point.Address = sanitizePublicText(ev.Point.Address)
		ev.Point.City = sanitizePublicText(ev.Point.City)
		ev.Point.District = sanitizePublicText(ev.Point.District)
		ev.Point.Category = sanitizePublicText(ev.Point.Category)
		ev.Point.Description = sanitizePublicText(ev.Point.Description)
		ev.Point.Notes = sanitizePublicText(ev.Point.Notes)
		ev.Point.StartTime = sanitizePublicText(ev.Point.StartTime)
		ev.Point.EndTime = sanitizePublicText(ev.Point.EndTime)
		ev.Point.PhaseID = sanitizePublicText(ev.Point.PhaseID)
		ev.Point.PhaseName = sanitizePublicText(ev.Point.PhaseName)
		ev.Point.DayID = sanitizePublicText(ev.Point.DayID)
	}
	if ev.Route != nil {
		ev.Route.Label = sanitizePublicText(ev.Route.Label)
		ev.Route.Accuracy = sanitizePublicText(ev.Route.Accuracy)
		ev.Route.Source = sanitizePublicText(ev.Route.Source)
		ev.Route.PhaseID = sanitizePublicText(ev.Route.PhaseID)
		ev.Route.PhaseName = sanitizePublicText(ev.Route.PhaseName)
		ev.Route.DayID = sanitizePublicText(ev.Route.DayID)
		ev.Route.FromNodeID = sanitizePublicText(ev.Route.FromNodeID)
		ev.Route.ToNodeID = sanitizePublicText(ev.Route.ToNodeID)
		ev.Route.ConnectionType = sanitizePublicText(ev.Route.ConnectionType)
		ev.Route.Reason = sanitizePublicText(ev.Route.Reason)
	}
	if ev.Usage != nil {
		ev.Usage.AgentLabel = sanitizePublicText(ev.Usage.AgentLabel)
		ev.Usage.Model = sanitizePublicText(ev.Usage.Model)
		ev.Usage.ModelLevel = sanitizePublicText(ev.Usage.ModelLevel)
	}
	if ev.Annotation != nil {
		ev.Annotation.ID = sanitizePublicText(ev.Annotation.ID)
		ev.Annotation.Kind = sanitizePublicText(ev.Annotation.Kind)
		ev.Annotation.Source = sanitizePublicText(ev.Annotation.Source)
		ev.Annotation.Title = sanitizePublicText(ev.Annotation.Title)
		ev.Annotation.Summary = sanitizePublicText(ev.Annotation.Summary)
		ev.Annotation.AuthorName = sanitizePublicText(ev.Annotation.AuthorName)
		ev.Annotation.Status = sanitizePublicText(ev.Annotation.Status)
		for i := range ev.Annotation.Tags {
			ev.Annotation.Tags[i] = sanitizePublicText(ev.Annotation.Tags[i])
		}
		for i := range ev.Annotation.Reasons {
			ev.Annotation.Reasons[i] = sanitizePublicText(ev.Annotation.Reasons[i])
		}
		for i := range ev.Annotation.Evidence {
			ev.Annotation.Evidence[i] = sanitizePublicText(ev.Annotation.Evidence[i])
		}
		ev.Annotation.Anchor.Type = sanitizePublicText(ev.Annotation.Anchor.Type)
		ev.Annotation.Anchor.NodeID = sanitizePublicText(ev.Annotation.Anchor.NodeID)
		ev.Annotation.Anchor.RouteID = sanitizePublicText(ev.Annotation.Anchor.RouteID)
		ev.Annotation.Anchor.Label = sanitizePublicText(ev.Annotation.Anchor.Label)
		if ev.Annotation.Anchor.Point != nil {
			ev.Annotation.Anchor.Point.Label = sanitizePublicText(ev.Annotation.Anchor.Point.Label)
			ev.Annotation.Anchor.Point.Kind = sanitizePublicText(ev.Annotation.Anchor.Point.Kind)
			ev.Annotation.Anchor.Point.Accuracy = sanitizePublicText(ev.Annotation.Anchor.Point.Accuracy)
			ev.Annotation.Anchor.Point.Source = sanitizePublicText(ev.Annotation.Anchor.Point.Source)
			ev.Annotation.Anchor.Point.Address = sanitizePublicText(ev.Annotation.Anchor.Point.Address)
			ev.Annotation.Anchor.Point.City = sanitizePublicText(ev.Annotation.Anchor.Point.City)
			ev.Annotation.Anchor.Point.District = sanitizePublicText(ev.Annotation.Anchor.Point.District)
			ev.Annotation.Anchor.Point.Category = sanitizePublicText(ev.Annotation.Anchor.Point.Category)
			ev.Annotation.Anchor.Point.Description = sanitizePublicText(ev.Annotation.Anchor.Point.Description)
			ev.Annotation.Anchor.Point.Notes = sanitizePublicText(ev.Annotation.Anchor.Point.Notes)
			ev.Annotation.Anchor.Point.StartTime = sanitizePublicText(ev.Annotation.Anchor.Point.StartTime)
			ev.Annotation.Anchor.Point.EndTime = sanitizePublicText(ev.Annotation.Anchor.Point.EndTime)
		}
	}
	for i := range ev.RecordedFacts {
		ev.RecordedFacts[i] = sanitizePublicText(ev.RecordedFacts[i])
	}
	for i := range ev.Events {
		ev.Events[i] = SanitizePublicPlanningEvent(ev.Events[i])
	}
	return ev
}

func PublicLevelForStage(stage string) string {
	switch stage {
	case "day_expansion", "review", "final_output":
		return "day"
	case "graph_splitting":
		return "phase"
	case "macro_planning":
		return "overview"
	default:
		return "overview"
	}
}

func DefaultPublicAnnotationTitle(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func sanitizePublicText(text string) string {
	if text == "" {
		return ""
	}
	replacements := map[string]string{
		"amap_":                "地图能力",
		"zhihu_guide_material": "攻略素材能力",
		"write_guide_insight":  "攻略洞察能力",
		"create_trip_plan":     "规划写入能力",
		"split_parent_node":    "层级拆分能力",
		"get_weather_context":  "天气查询能力",
		"get_trip_overview":    "规划读取能力",
		"review-workflow":      "流程审核",
		"review-thinking":      "思路审核",
		"review-content":       "内容审核",
		"review-output":        "输出审核",
		"review-laziness":      "完整性审核",
		"review-poi":           "地点审核",
		"review-week":          "周级审核",
		"tool":                 "能力",
		"Tool":                 "能力",
	}
	out := text
	for old, next := range replacements {
		out = strings.ReplaceAll(out, old, next)
	}
	return out
}
