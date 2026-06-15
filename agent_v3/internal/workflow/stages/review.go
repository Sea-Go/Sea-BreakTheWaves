package stages

import (
	"context"
	"fmt"
	"strings"

	domaintravel "agent_v3/internal/domain/travel"
	"agent_v3/internal/graph"
	workflowreview "agent_v3/internal/review"
	workflowruntime "agent_v3/internal/workflow/runtime"
)

func (a *graphWorkflowAgent) runPhase3(ctx context.Context, tripPlanID string, trace *TraceEmitter, requirements ...workflowruntime.TravelRequirementSnapshot) error {
	overview, err := a.graphClient.GetTripOverview(ctx, tripPlanID)
	if err != nil {
		return fmt.Errorf("get trip overview: %w", err)
	}

	dayIDs := extractDayIDs(overview)
	for i, dayID := range dayIDs {
		if trace != nil {
			trace.Emit(ctx, PublicPlanningEvent{
				Type:           EventMapAnnotationAdded,
				Level:          "day",
				NodeID:         dayID,
				Status:         "active",
				PublicAction:   "启动当天审核",
				ThoughtSummary: fmt.Sprintf("正在审核第 %d/%d 天的地点、路线和内容质量。", i+1, len(dayIDs)),
				Annotation: &PublicMapAnnotation{
					ID:      stablePlanningAnnotationID("review-day-progress", dayID, fmt.Sprint(i+1)),
					Kind:    "review",
					Source:  "review",
					Title:   fmt.Sprintf("第 %d 天审核中", i+1),
					Summary: "审核 Agent 正在检查地点密度、路线合理性和内容质量。",
					Status:  "active",
					Anchor:  PublicMapAnnotationAnchor{Type: "scope", NodeID: dayID, Label: fmt.Sprintf("Day %d", i+1)},
				},
			})
		}

		subgraph, err := a.graphClient.GetDaySubgraph(ctx, dayID)
		if err != nil || subgraph == nil {
			continue
		}

		for _, poi := range subgraph.POIs {
			poiReview := workflowreview.RunPOIReview(ctx, subgraph, poi)
			if poiReview != nil {
				_, _ = a.graphClient.WriteReviewResult(ctx, poi.ID, *poiReview)
				emitReviewAnnotation(ctx, trace, "day", poi.ID, poi.Name, "POI 审核", "point", *poiReview)
			}
		}

		dayReviewResults := workflowreview.RunDayContentReviews(ctx, a.reviewAgents, subgraph)
		for _, r := range dayReviewResults {
			_, _ = a.graphClient.WriteReviewResult(ctx, dayID, r.Review)
			emitReviewAnnotation(ctx, trace, "day", dayID, subgraph.Day.Date, r.AgentName, "scope", r.Review)
		}
	}

	weekIDs := extractWeekIDs(overview)
	for _, weekID := range weekIDs {
		weekReview := workflowreview.RunWeekReview(ctx, weekID)
		if weekReview != nil {
			_, _ = a.graphClient.WriteReviewResult(ctx, weekID, *weekReview)
			emitReviewAnnotation(ctx, trace, "week", weekID, weekID, "周级审核", "scope", *weekReview)
		}
	}

	if err := workflowreview.ReviewGeoScope(ctx, a.graphClient, tripPlanID, overview, trace, requirements...); err != nil {
		return err
	}
	return a.reviewAnchorCoverage(ctx, tripPlanID, overview, trace, requirements...)
}

type anchorCoverageFinding struct {
	Anchor   domaintravel.DestinationAnchorSnapshot
	Covered  bool
	Critical bool
	Matches  []string
	Reason   string
}

func (a *graphWorkflowAgent) reviewAnchorCoverage(ctx context.Context, tripPlanID string, overview *graph.TripOverview, trace *TraceEmitter, requirements ...workflowruntime.TravelRequirementSnapshot) error {
	anchors := deriveAnchorsForGraphSplitting(overview, requirements...)
	if len(anchors) == 0 {
		return nil
	}
	findings, err := a.evaluateAnchorCoverage(ctx, overview, anchors)
	if err != nil {
		return err
	}

	var criticalMissing []string
	for _, finding := range findings {
		emitAnchorCoverageAnnotation(ctx, trace, tripPlanID, finding)
		if finding.Critical && !finding.Covered {
			criticalMissing = append(criticalMissing, finding.Anchor.Name)
		}
	}
	if len(criticalMissing) == 0 {
		review := graph.ReviewInput{
			Level:       "trip",
			Dimension:   "anchor_coverage",
			Score:       92,
			Passed:      true,
			Summary:     "核心目的地与自然风光锚点覆盖审核通过。",
			Suggestions: []string{"继续在最终方案中解释各锚点的景观窗口、体力和天气风险。"},
		}
		_, _ = a.graphClient.WriteReviewResult(ctx, tripPlanID, review)
		emitReviewAnnotation(ctx, trace, "overview", tripPlanID, "锚点覆盖", "锚点覆盖审核", "scope", review)
		return nil
	}

	review := graph.ReviewInput{
		Level:     "trip",
		Dimension: "anchor_coverage",
		Score:     45,
		Passed:    false,
		Summary:   "核心景点覆盖不足，不能直接生成最终方案。",
		CriticalIssues: []string{
			"缺失核心锚点: " + strings.Join(criticalMissing, "；"),
			"自然风光需求不能用城市到达、酒店或餐饮替代。",
		},
		Suggestions: []string{
			"请延长天数、舍弃部分目的地，或接受更高强度转移日后重新规划。",
			"也可以保留多地但明确哪些核心景点作为备选或放弃。",
		},
	}
	_, _ = a.graphClient.WriteReviewResult(ctx, tripPlanID, review)
	emitReviewAnnotation(ctx, trace, "overview", tripPlanID, "锚点覆盖", "锚点覆盖审核", "scope", review)
	return fmt.Errorf("核心景点覆盖不足: %s", strings.Join(criticalMissing, "；"))
}

func (a *graphWorkflowAgent) evaluateAnchorCoverage(ctx context.Context, overview *graph.TripOverview, anchors []domaintravel.DestinationAnchorSnapshot) ([]anchorCoverageFinding, error) {
	if overview == nil {
		return nil, nil
	}
	coverageTextParts := []string{}
	for _, day := range overview.Days {
		dayID := getStr(day, "id")
		if dayID == "" {
			continue
		}
		subgraph, err := a.graphClient.GetDaySubgraph(ctx, dayID)
		if err != nil || subgraph == nil {
			continue
		}
		for _, poi := range subgraph.POIs {
			if isLogisticsPOIType(poi.Type) || isGenericAdministrativePOI(graph.POIInput{Name: poi.Name, Type: poi.Type, Address: poi.Address}) {
				continue
			}
			coverageTextParts = append(coverageTextParts, poi.Name, poi.Type, poi.Address, poi.District, poi.City, poi.Description, poi.Notes)
		}
	}
	coverageText := strings.Join(coverageTextParts, " ")
	coveredHighPriorityByDestination := map[string]bool{}
	topPriorityByDestination := map[string]int{}
	for _, anchor := range anchors {
		if anchor.Kind != "destination" && anchor.Priority > topPriorityByDestination[anchor.Destination] {
			topPriorityByDestination[anchor.Destination] = anchor.Priority
		}
		if anchor.Kind != "destination" && anchor.Priority >= 90 && anchorCoveredByText(anchor, coverageText) {
			coveredHighPriorityByDestination[anchor.Destination] = true
		}
	}

	findings := make([]anchorCoverageFinding, 0, len(anchors))
	for _, anchor := range anchors {
		covered := anchorCoveredByText(anchor, coverageText)
		critical := false
		if anchor.Origin == anchorOriginUserExplicit && anchor.MustCover {
			critical = !covered && !coveredHighPriorityByDestination[anchor.Destination]
		}
		if anchor.Origin == anchorOriginSystemInferred && anchor.Priority >= 90 && anchor.Priority == topPriorityByDestination[anchor.Destination] {
			critical = true
		}
		reason := "已在日程、POI 或路线说明中覆盖。"
		if !covered {
			reason = "未在已验证 POI 或日程说明中找到，需要给出放弃原因或重新规划。"
		}
		findings = append(findings, anchorCoverageFinding{
			Anchor:   anchor,
			Covered:  covered,
			Critical: critical,
			Matches:  anchorMatchLabels(anchor, coverageText),
			Reason:   reason,
		})
	}
	return findings, nil
}

func anchorCoveredByText(anchor domaintravel.DestinationAnchorSnapshot, text string) bool {
	if strings.TrimSpace(anchor.Name) == "" {
		return false
	}
	names := []string{anchor.Name}
	if strings.HasSuffix(anchor.Name, "峰") {
		names = append(names, strings.TrimSuffix(anchor.Name, "峰"))
	}
	if strings.HasSuffix(anchor.Name, "国家公园") {
		names = append(names, strings.TrimSuffix(anchor.Name, "国家公园"))
	}
	for _, name := range names {
		if name != "" && strings.Contains(text, name) {
			return true
		}
	}
	return anchor.Kind == "destination" && anchor.Destination != "" && strings.Contains(text, anchor.Destination)
}

func anchorMatchLabels(anchor domaintravel.DestinationAnchorSnapshot, text string) []string {
	if anchorCoveredByText(anchor, text) {
		return []string{"命中: " + anchor.Name}
	}
	return nil
}

func emitAnchorCoverageAnnotation(ctx context.Context, emitter *TraceEmitter, tripPlanID string, finding anchorCoverageFinding) {
	if emitter == nil {
		return
	}
	status := "selected"
	if !finding.Covered {
		status = "review"
	}
	if finding.Critical && !finding.Covered {
		status = "rejected"
	}
	title := "锚点已覆盖"
	if !finding.Covered {
		title = "锚点待补齐"
	}
	summary := fmt.Sprintf("%s: %s", finding.Anchor.Name, finding.Reason)
	tags := []string{"锚点覆盖", defaultIfEmpty(finding.Anchor.Destination, "目的地")}
	if finding.Anchor.Kind != "" {
		tags = append(tags, finding.Anchor.Kind)
	}
	if finding.Critical {
		tags = append(tags, "关键")
	}
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapAnnotationAdded,
		Level:          "overview",
		NodeID:         tripPlanID,
		Status:         status,
		PublicAction:   "展示锚点覆盖审核",
		ThoughtSummary: "正在核对用户明确目的地和自然风光核心锚点是否被真实日程覆盖。",
		RecordedFacts:  []string{summary},
		Annotation: &PublicMapAnnotation{
			ID:       stablePlanningAnnotationID("anchor-coverage", tripPlanID, finding.Anchor.Destination, finding.Anchor.Name),
			Kind:     "anchor_coverage",
			Source:   "review",
			Title:    title,
			Summary:  truncateGuideText(summary, maxGuideAnnotationSummary),
			Status:   status,
			Tags:     tags,
			Reasons:  []string{defaultIfEmpty(finding.Anchor.Reason, "核心锚点覆盖检查")},
			Evidence: append([]string(nil), finding.Matches...),
			Anchor: PublicMapAnnotationAnchor{
				Type:   "scope",
				NodeID: tripPlanID,
				Label:  defaultIfEmpty(finding.Anchor.Name, "锚点覆盖"),
			},
		},
	})
}
