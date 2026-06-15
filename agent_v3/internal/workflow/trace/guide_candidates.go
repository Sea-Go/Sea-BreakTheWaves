package trace

import (
	"context"
	"fmt"
	"strings"

	"agent_v3/internal/graph"
	zhihutools "agent_v3/internal/tools/zhihu"
)

func emitGuideRunAnnotations(ctx context.Context, emitter *TraceEmitter, graphClient *graph.Client, tripPlanID string, topic guideEvidenceTopic, run zhihutools.ZhihuGuideRun) {
	selectedURLs := selectedZhihuURLs(run)
	summary := fmt.Sprintf("知乎素材：原始 %d 条，去重后 %d 条，精选 %d 条。", run.Stats.RawCount, run.Stats.DedupedCount, run.Stats.SelectedCount)
	if len(run.Errors) > 0 {
		summary += fmt.Sprintf(" 其中 %d 个查询返回错误，已保留可用结果。", len(run.Errors))
	}
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapAnnotationAdded,
		Level:          defaultIfEmpty(topic.Level, "overview"),
		NodeID:         topic.NodeID,
		Status:         "active",
		PublicAction:   "整理知乎攻略素材",
		ThoughtSummary: "已将知乎结果按相关性、互动质量、内容摘要和避坑价值筛选，作为地图证据层展示。",
		RecordedFacts:  []string{summary},
		Annotation: &PublicMapAnnotation{
			ID:       stablePlanningAnnotationID("zhihu-query-plan", tripPlanID, run.RunID, topic.Topic),
			Kind:     "thought",
			Source:   "zhihu",
			Title:    "知乎素材筛选概览",
			Summary:  summary,
			Status:   "active",
			Tags:     []string{"知乎", "筛选概览"},
			Reasons:  []string{"主题相关", "互动质量", "近期内容", "摘要可用"},
			Evidence: queryPlanEvidence(run.QueryPlan),
			Anchor: PublicMapAnnotationAnchor{
				Type:   "scope",
				NodeID: topic.NodeID,
				Label:  defaultIfEmpty(topic.AnchorLabel, "当前地图范围"),
			},
		},
	})

	events := make([]PublicPlanningEvent, 0, len(run.FilteredCandidates))
	for _, candidate := range run.FilteredCandidates {
		annotation := annotationFromZhihuCandidate(tripPlanID, topic, candidate, selectedURLs)
		if annotation == nil {
			continue
		}
		events = append(events, PublicPlanningEvent{
			Type:           EventMapAnnotationAdded,
			Level:          defaultIfEmpty(topic.Level, "overview"),
			NodeID:         topic.NodeID,
			Status:         annotation.Status,
			PublicAction:   "展示知乎攻略素材",
			ThoughtSummary: "这条素材用于解释体验和取舍，不用于替代地图坐标或路线事实。",
			Annotation:     annotation,
		})
		writeGuideInsightFromAnnotation(ctx, graphClient, tripPlanID, topic, annotation)
		if len(events) >= maxGuideAnnotationBatch {
			emitGuideAnnotationBatch(ctx, emitter, topic, events)
			events = events[:0]
		}
	}
	if len(events) > 0 {
		emitGuideAnnotationBatch(ctx, emitter, topic, events)
	}
}

func emitGuideAnnotationBatch(ctx context.Context, emitter *TraceEmitter, topic guideEvidenceTopic, events []PublicPlanningEvent) {
	if len(events) == 0 {
		return
	}
	batch := append([]PublicPlanningEvent(nil), events...)
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapBatch,
		Level:          defaultIfEmpty(topic.Level, "overview"),
		NodeID:         topic.NodeID,
		PublicAction:   "批量展示攻略素材",
		ThoughtSummary: fmt.Sprintf("已把 %d 条知乎素材加入地图证据层。", len(batch)),
		Events:         batch,
	})
}

func annotationFromZhihuCandidate(tripPlanID string, topic guideEvidenceTopic, candidate zhihutools.ZhihuGuideCandidate, selectedURLs map[string]bool) *PublicMapAnnotation {
	title := strings.TrimSpace(candidate.Title)
	if title == "" && strings.TrimSpace(candidate.Summary) == "" {
		return nil
	}
	status := normalizeGuideCandidateStatus(candidate, selectedURLs)
	urlKey := strings.TrimSpace(candidate.URL)
	if urlKey == "" {
		urlKey = title + candidate.Summary
	}
	tags := guideCandidateTags(candidate, status)
	reasons := limitStrings(candidate.Reasons, maxGuideAnnotationReasons)
	if len(reasons) == 0 {
		reasons = []string{"主题相关性待复核"}
	}

	return &PublicMapAnnotation{
		ID:         stablePlanningAnnotationID("zhihu", tripPlanID, topic.Topic, urlKey),
		Kind:       "zhihu_source",
		Source:     "zhihu",
		Title:      defaultIfEmpty(title, "知乎攻略素材"),
		Summary:    truncateGuideText(defaultIfEmpty(candidate.Summary, "这条素材没有结构化摘要，保留标题和筛选信号供复核。"), maxGuideAnnotationSummary),
		URL:        strings.TrimSpace(candidate.URL),
		AuthorName: strings.TrimSpace(candidate.AuthorName),
		Score:      candidate.Score,
		Status:     status,
		Tags:       tags,
		Reasons:    reasons,
		Evidence:   guideCandidateEvidence(candidate),
		Anchor: PublicMapAnnotationAnchor{
			Type:   "scope",
			NodeID: topic.NodeID,
			Label:  defaultIfEmpty(topic.AnchorLabel, "当前地图范围"),
		},
	}
}

func normalizeGuideCandidateStatus(candidate zhihutools.ZhihuGuideCandidate, selectedURLs map[string]bool) string {
	if selectedURLs[normalizeURLKey(candidate.URL)] || selectedURLs[candidate.URLHash] {
		return "selected"
	}
	switch strings.TrimSpace(candidate.Status) {
	case zhihutools.ZhihuGuideStatusRejected:
		return "rejected"
	case zhihutools.ZhihuGuideStatusAccepted, zhihutools.ZhihuGuideStatusReview:
		return "review"
	case zhihutools.ZhihuGuideStatusSelected:
		return "selected"
	default:
		return "raw"
	}
}

func selectedZhihuURLs(run zhihutools.ZhihuGuideRun) map[string]bool {
	out := map[string]bool{}
	for _, item := range run.SelectedForLLM.Items {
		key := normalizeURLKey(item.URL)
		if key != "" {
			out[key] = true
			out[hashGuideText(key)] = true
		}
	}
	return out
}

func guideCandidateTags(candidate zhihutools.ZhihuGuideCandidate, status string) []string {
	tags := []string{"知乎"}
	if status != "" {
		tags = append(tags, status)
	}
	if candidate.SourceIntent != "" {
		tags = append(tags, candidate.SourceIntent)
	}
	for _, source := range candidate.Sources {
		if source.Intent != "" {
			tags = append(tags, source.Intent)
		}
		if source.Scope != "" {
			tags = append(tags, source.Scope)
		}
	}
	return uniqueLimitedStrings(tags, 6)
}

func guideCandidateEvidence(candidate zhihutools.ZhihuGuideCandidate) []string {
	evidence := []string{}
	if candidate.VoteUpCount > 0 {
		evidence = append(evidence, fmt.Sprintf("赞同 %d", candidate.VoteUpCount))
	}
	if candidate.CommentCount > 0 {
		evidence = append(evidence, fmt.Sprintf("评论 %d", candidate.CommentCount))
	}
	if candidate.SourceQuery != "" {
		evidence = append(evidence, "命中搜索："+candidate.SourceQuery)
	}
	if candidate.SearchScope != "" {
		evidence = append(evidence, "来源范围："+candidate.SearchScope)
	}
	return limitStrings(evidence, maxGuideAnnotationEvidence)
}

func queryPlanEvidence(items []zhihutools.ZhihuGuideQuery) []string {
	evidence := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Query) == "" {
			continue
		}
		if item.Intent != "" {
			evidence = append(evidence, fmt.Sprintf("%s：%s", item.Intent, item.Query))
		} else {
			evidence = append(evidence, item.Query)
		}
	}
	return limitStrings(evidence, maxGuideAnnotationEvidence)
}
