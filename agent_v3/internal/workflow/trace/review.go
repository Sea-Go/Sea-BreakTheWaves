package trace

import (
	"agent_v3/internal/graph"
	"context"
	"fmt"
	"strings"
)

func emitReviewAnnotation(ctx context.Context, emitter *TraceEmitter, level, nodeID, label, agentName, anchorType string, review graph.ReviewInput) {
	if emitter == nil {
		return
	}
	status := "review"
	if review.Passed {
		status = "selected"
	}
	title := defaultIfEmpty(agentName, "审核结果")
	summary := review.Summary
	if summary == "" {
		summary = fmt.Sprintf("%s：%s，得分 %d。", defaultIfEmpty(label, nodeID), passLabel(review.Passed), review.Score)
	}
	evidence := reviewEvidence(review)
	reasons := []string{defaultIfEmpty(review.Level, defaultIfEmpty(level, "day")), defaultIfEmpty(review.Dimension, "综合审核")}
	anchor := PublicMapAnnotationAnchor{Type: defaultIfEmpty(anchorType, "scope"), NodeID: nodeID, Label: defaultIfEmpty(label, nodeID)}
	if anchor.Type == "point" {
		anchor.NodeID = nodeID
	}
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapAnnotationAdded,
		Level:          defaultIfEmpty(level, "day"),
		NodeID:         nodeID,
		Status:         status,
		PublicAction:   "展示审核结果",
		ThoughtSummary: "审核结果以公开摘要展示，用于说明规划质量和需要注意的问题。",
		Annotation: &PublicMapAnnotation{
			ID:       stablePlanningAnnotationID("review", nodeID, review.Level, review.Dimension, fmt.Sprint(review.Score), summary),
			Kind:     "review",
			Source:   "review",
			Title:    title,
			Summary:  truncateGuideText(summary, maxGuideAnnotationSummary),
			Score:    float64(review.Score),
			Status:   status,
			Tags:     []string{"审核", passLabel(review.Passed), defaultIfEmpty(review.Dimension, "综合")},
			Reasons:  reasons,
			Evidence: evidence,
			Anchor:   anchor,
		},
	})
}

func reviewEvidence(review graph.ReviewInput) []string {
	values := []string{}
	values = append(values, review.CriticalIssues...)
	values = append(values, review.Issues...)
	values = append(values, review.Suggestions...)
	for _, violation := range review.ConstraintViolations {
		text := strings.TrimSpace(strings.Join([]string{
			violation.Dimension,
			violation.Rule,
			violation.Actual,
			violation.Threshold,
			violation.Severity,
		}, " "))
		if text != "" {
			values = append(values, text)
		}
	}
	return limitStrings(values, maxGuideAnnotationEvidence)
}

func passLabel(passed bool) string {
	if passed {
		return "通过"
	}
	return "待调整"
}

func publicReviewAgentLabel(name string) string {
	switch name {
	case "workflow":
		return "流程审核"
	case "thinking":
		return "思路审核"
	case "content":
		return "内容审核"
	case "output":
		return "输出审核"
	case "laziness":
		return "完整性审核"
	default:
		return defaultIfEmpty(name, "审核结果")
	}
}
