package review

import (
	"context"
	"fmt"
	"strings"

	domaingeo "agent_v3/internal/domain/geo"
	domaintravel "agent_v3/internal/domain/travel"
	"agent_v3/internal/graph"
	workflowtrace "agent_v3/internal/workflow/trace"
)

func EmitGeoScopeViolationAnnotation(ctx context.Context, emitter *workflowtrace.TraceEmitter, level, nodeID string, violation domaingeo.TravelGeoViolation) {
	if emitter == nil {
		return
	}
	emitter.Emit(ctx, workflowtrace.PublicPlanningEvent{
		Type:           workflowtrace.EventMapAnnotationAdded,
		Level:          defaultIfEmpty(level, "overview"),
		NodeID:         nodeID,
		Status:         "rejected",
		PublicAction:   "范围审查",
		ThoughtSummary: "发现规划内容偏离用户限定的目的地范围，已阻止继续展开。",
		RecordedFacts:  []string{fmt.Sprintf("%s: %s", violation.Label, violation.Reason)},
		Annotation: &workflowtrace.PublicMapAnnotation{
			ID:       workflowtrace.StablePlanningAnnotationID("geo-scope", nodeID, violation.Label, violation.MatchedKeyword),
			Kind:     "review",
			Source:   "review",
			Title:    "范围越界审查",
			Summary:  fmt.Sprintf("%s: %s", violation.Label, violation.Reason),
			Status:   "rejected",
			Tags:     []string{"范围", "审查", "越界"},
			Reasons:  []string{"用户已限定目的地范围，规划不能引入明显无关城市。"},
			Evidence: []string{workflowtrace.TruncateGuideText(violation.Text, workflowtrace.MaxGuideAnnotationSummary)},
			Anchor: workflowtrace.PublicMapAnnotationAnchor{
				Type:   "scope",
				NodeID: nodeID,
				Label:  defaultIfEmpty(violation.Label, "范围审查"),
			},
		},
	})
}

func EmitGeoScopeViolationAnnotations(ctx context.Context, emitter *workflowtrace.TraceEmitter, level, nodeID string, violations []domaingeo.TravelGeoViolation) {
	for _, violation := range violations {
		EmitGeoScopeViolationAnnotation(ctx, emitter, level, nodeID, violation)
	}
}

func FilterPOIsByGeoConstraint(ctx context.Context, emitter *workflowtrace.TraceEmitter, dayID string, pois []graph.POIInput, constraint domaingeo.TravelGeoConstraint) []graph.POIInput {
	if !constraint.Enabled || len(pois) == 0 {
		return pois
	}
	out := make([]graph.POIInput, 0, len(pois))
	for _, poi := range pois {
		if violation := constraint.CheckPOI(poi); violation != nil {
			EmitGeoScopeViolationAnnotation(ctx, emitter, "day", dayID, *violation)
			continue
		}
		out = append(out, poi)
	}
	return out
}

func ReviewGeoScope(ctx context.Context, graphClient *graph.Client, tripPlanID string, overview *graph.TripOverview, emitter *workflowtrace.TraceEmitter, requirements ...domaintravel.TravelRequirementSnapshot) error {
	constraint := domaingeo.BuildTravelGeoConstraintFromOverview(overview)
	if len(requirements) > 0 {
		candidate := domaingeo.BuildTravelGeoConstraint(requirements[0], overview.TripPlan.RawRequirements)
		if candidate.Enabled {
			constraint = candidate
		}
	}
	if !constraint.Enabled {
		return nil
	}

	var violations []domaingeo.TravelGeoViolation
	for _, phase := range overview.Phases {
		label := firstNonEmptyString(getStr(phase, "region"), getStr(phase, "name"))
		text := strings.Join([]string{getStr(phase, "region"), getStr(phase, "name"), getStr(phase, "theme")}, " ")
		if violation := constraint.CheckText(label, text, true); violation != nil {
			violations = append(violations, *violation)
		}
	}
	for _, day := range overview.Days {
		dayID := getStr(day, "id")
		if dayID == "" {
			continue
		}
		subgraph, err := graphClient.GetDaySubgraph(ctx, dayID)
		if err != nil || subgraph == nil {
			continue
		}
		for _, poi := range subgraph.POIs {
			if violation := constraint.CheckPOINode(poi); violation != nil {
				violations = append(violations, *violation)
			}
		}
	}

	if len(violations) == 0 {
		review := graph.ReviewInput{
			Level:       "trip",
			Dimension:   "geo_scope",
			Score:       95,
			Passed:      true,
			Summary:     "目的地范围审查通过，未发现明显越界城市。",
			Suggestions: []string{"继续保持路线围绕用户指定区域展开。"},
		}
		_, _ = graphClient.WriteReviewResult(ctx, tripPlanID, review)
		workflowtrace.EmitReviewAnnotation(ctx, emitter, "overview", tripPlanID, "范围审查", "范围审查", "scope", review)
		return nil
	}

	EmitGeoScopeViolationAnnotations(ctx, emitter, "overview", tripPlanID, violations)

	issues := make([]string, 0, len(violations))
	for _, violation := range violations {
		issues = append(issues, fmt.Sprintf("%s 命中 %s", violation.Label, violation.MatchedKeyword))
	}

	review := graph.ReviewInput{
		Level:     "trip",
		Dimension: "geo_scope",
		Score:     30,
		Passed:    false,
		Summary:   "目的地范围审查未通过，规划包含明显无关城市。",
		CriticalIssues: []string{
			"规划越界: " + strings.Join(issues, "；"),
		},
		ConstraintViolations: []graph.ConstraintViolation{
			{
				Dimension: "目的地范围",
				Rule:      "规划地点必须围绕用户指定目的地范围展开",
				Actual:    strings.Join(issues, "；"),
				Threshold: constraint.ScopeText,
				Severity:  "critical",
			},
		},
		Suggestions: []string{"请重新生成宏观阶段，删除不属于用户目的地范围的城市。"},
	}
	_, _ = graphClient.WriteReviewResult(ctx, tripPlanID, review)
	workflowtrace.EmitReviewAnnotation(ctx, emitter, "overview", tripPlanID, "范围审查", "范围审查", "scope", review)
	return &domaingeo.TravelGeoScopeError{Violations: violations}
}

func getStr(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
