package agent

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"agent_v2/config"
	"agent_v2/graph"
	"agent_v2/tools"

	"trpc.group/trpc-go/trpc-agent-go/log"
)

const (
	maxGuideTopicsPerRun       = 24
	maxGuideAnnotationBatch    = 50
	maxGuideAnnotationSummary  = 280
	maxGuideAnnotationReasons  = 5
	maxGuideAnnotationEvidence = 5
)

type guideEvidenceTopic struct {
	Topic       string
	Level       string
	NodeID      string
	AnchorLabel string
	Region      string
	Reason      string
}

func emitGuideEvidenceForTrip(ctx context.Context, emitter *TraceEmitter, graphClient *graph.Client, tripPlanID string, overview *graph.TripOverview) {
	if emitter == nil || overview == nil {
		return
	}
	topics := buildGuideEvidenceTopics(overview)
	if len(topics) == 0 {
		emitter.Emit(ctx, PublicPlanningEvent{
			Type:           EventMapAnnotationAdded,
			Level:          "overview",
			PublicAction:   "准备攻略证据",
			ThoughtSummary: "当前大规划还没有形成可搜索的区域主题，暂时只保留规划结构。",
			Annotation: &PublicMapAnnotation{
				ID:      stablePlanningAnnotationID("guide-empty", tripPlanID, time.Now().Format(time.RFC3339Nano)),
				Kind:    "thought",
				Source:  "planning",
				Title:   "攻略证据待补充",
				Summary: "需要先形成区域或日程主题，才能采集可用的攻略素材。",
				Status:  "review",
				Anchor:  PublicMapAnnotationAnchor{Type: "scope", Label: "攻略证据"},
			},
		})
		return
	}

	if len(topics) > maxGuideTopicsPerRun {
		emitter.Emit(ctx, PublicPlanningEvent{
			Type:           EventMapAnnotationAdded,
			Level:          "overview",
			PublicAction:   "聚合攻略主题",
			ThoughtSummary: fmt.Sprintf("检测到 %d 个可搜索主题，本轮先按区域和日程主题去重展示前 %d 个，避免地图一次性涌入过多素材。", len(topics), maxGuideTopicsPerRun),
			Annotation: &PublicMapAnnotation{
				ID:      stablePlanningAnnotationID("guide-topic-limit", tripPlanID, fmt.Sprint(len(topics))),
				Kind:    "thought",
				Source:  "planning",
				Title:   "攻略主题已聚合",
				Summary: fmt.Sprintf("已从 %d 个主题聚合为 %d 个搜索任务，后续展开具体阶段时继续补齐。", len(topics), maxGuideTopicsPerRun),
				Status:  "active",
				Anchor:  PublicMapAnnotationAnchor{Type: "scope", Label: "攻略证据"},
			},
		})
		topics = topics[:maxGuideTopicsPerRun]
	}

	for _, topic := range topics {
		emitGuideSearchStarted(ctx, emitter, tripPlanID, topic)
		run, err := tools.CollectZhihuGuideMaterial(ctx, config.Cfg.Zhihu, topic.Topic)
		if err != nil {
			emitGuideSearchError(ctx, emitter, tripPlanID, topic, err)
			continue
		}
		emitGuideRunAnnotations(ctx, emitter, graphClient, tripPlanID, topic, run)
	}
}

func buildGuideEvidenceTopics(overview *graph.TripOverview) []guideEvidenceTopic {
	seen := map[string]bool{}
	topics := make([]guideEvidenceTopic, 0)

	addTopic := func(topic guideEvidenceTopic) {
		topic.Topic = compactGuideTopic(topic.Topic)
		if topic.Topic == "" || seen[topic.Topic] {
			return
		}
		if topic.Level == "" {
			topic.Level = "overview"
		}
		if topic.AnchorLabel == "" {
			topic.AnchorLabel = topic.Topic
		}
		seen[topic.Topic] = true
		topics = append(topics, topic)
	}

	for _, phase := range overview.Phases {
		region := firstNonEmptyString(getStr(phase, "region"), getStr(phase, "name"))
		if region == "" {
			continue
		}
		phaseID := getStr(phase, "id")
		topicParts := []string{region}
		if season := getStr(phase, "season"); season != "" {
			topicParts = append(topicParts, season)
		}
		if theme := getStr(phase, "theme"); theme != "" {
			topicParts = append(topicParts, theme)
		}
		if overview.TripPlan.TransportMode != "" {
			topicParts = append(topicParts, overview.TripPlan.TransportMode)
		}
		if overview.TripPlan.TravelStyle != "" {
			topicParts = append(topicParts, overview.TripPlan.TravelStyle)
		}
		topicParts = append(topicParts, "旅行攻略")
		addTopic(guideEvidenceTopic{
			Topic:       strings.Join(topicParts, " "),
			Level:       "phase",
			NodeID:      phaseID,
			AnchorLabel: firstNonEmptyString(region, getStr(phase, "name")),
			Region:      region,
			Reason:      "阶段区域攻略素材",
		})
	}

	for _, anchor := range deriveAnchorsFromTripOverview(overview) {
		if anchor.Kind == "destination" {
			continue
		}
		topic := anchor.Query
		if topic == "" {
			topic = strings.TrimSpace(strings.Join([]string{anchor.Destination, anchor.Name, overview.TripPlan.TransportMode, "攻略"}, " "))
		}
		addTopic(guideEvidenceTopic{
			Topic:       topic,
			Level:       "phase",
			NodeID:      overview.TripPlan.ID,
			AnchorLabel: anchor.Name,
			Region:      firstNonEmptyString(anchor.Destination, anchor.Name),
			Reason:      "核心自然锚点攻略素材",
		})
	}

	for _, day := range overview.Days {
		region := firstNonEmptyString(getStr(day, "phaseRegion"), getStr(day, "primaryArea"), getStr(day, "primaryLocation"))
		theme := getStr(day, "theme")
		if region == "" && theme == "" {
			continue
		}
		topicParts := []string{}
		if region != "" {
			topicParts = append(topicParts, region)
		}
		if theme != "" {
			topicParts = append(topicParts, theme)
		}
		if overview.TripPlan.TransportMode != "" {
			topicParts = append(topicParts, overview.TripPlan.TransportMode)
		}
		topicParts = append(topicParts, "旅行攻略")
		addTopic(guideEvidenceTopic{
			Topic:       strings.Join(topicParts, " "),
			Level:       "day",
			NodeID:      getStr(day, "id"),
			AnchorLabel: firstNonEmptyString(region, theme, getStr(day, "date")),
			Region:      region,
			Reason:      "日程主题攻略素材",
		})
	}

	if len(topics) == 0 {
		tripTopic := strings.TrimSpace(strings.Join(append([]string{overview.TripPlan.Name, overview.TripPlan.TravelStyle, overview.TripPlan.TransportMode}, overview.TripPlan.Interests...), " "))
		if tripTopic != "" {
			addTopic(guideEvidenceTopic{
				Topic:       tripTopic + " 旅行攻略",
				Level:       "overview",
				NodeID:      overview.TripPlan.ID,
				AnchorLabel: firstNonEmptyString(overview.TripPlan.Name, "整段行程"),
				Region:      overview.TripPlan.Name,
				Reason:      "整体行程攻略素材",
			})
		}
	}

	return topics
}

func emitGuideSearchStarted(ctx context.Context, emitter *TraceEmitter, tripPlanID string, topic guideEvidenceTopic) {
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapAnnotationAdded,
		Level:          defaultIfEmpty(topic.Level, "overview"),
		NodeID:         topic.NodeID,
		Status:         "active",
		PublicAction:   "采集知乎攻略素材",
		ThoughtSummary: "正在把知乎经验作为主观体验信号，不把它当作地理事实；没有精确地点时会挂在当前地图范围。",
		RecordedFacts:  []string{"搜索主题：" + topic.Topic, "展示位置：" + defaultIfEmpty(topic.AnchorLabel, "当前地图范围")},
		Annotation: &PublicMapAnnotation{
			ID:      stablePlanningAnnotationID("zhihu-search-start", tripPlanID, topic.Topic),
			Kind:    "thought",
			Source:  "zhihu",
			Title:   "开始采集知乎素材",
			Summary: fmt.Sprintf("正在搜索「%s」，用于补充避坑、体验、交通和节奏判断。", topic.Topic),
			Status:  "active",
			Tags:    []string{"知乎", "攻略素材"},
			Reasons: []string{defaultIfEmpty(topic.Reason, "补充用户体验证据")},
			Anchor: PublicMapAnnotationAnchor{
				Type:   "scope",
				NodeID: topic.NodeID,
				Label:  defaultIfEmpty(topic.AnchorLabel, "当前地图范围"),
			},
		},
	})
}

func emitGuideSearchError(ctx context.Context, emitter *TraceEmitter, tripPlanID string, topic guideEvidenceTopic, err error) {
	summary := "知乎素材暂时不可用，地图会继续展示已确认的规划结构。"
	if err != nil {
		summary = truncateGuideText("知乎素材暂时不可用："+err.Error(), maxGuideAnnotationSummary)
	}
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapAnnotationAdded,
		Level:          defaultIfEmpty(topic.Level, "overview"),
		NodeID:         topic.NodeID,
		Status:         "review",
		PublicAction:   "记录素材采集失败",
		ThoughtSummary: "这一步不会阻断路线规划；失败原因只作为公开状态记录展示在地图证据层。",
		RecordedFacts:  []string{"搜索主题：" + topic.Topic},
		Annotation: &PublicMapAnnotation{
			ID:      stablePlanningAnnotationID("zhihu-search-error", tripPlanID, topic.Topic, summary),
			Kind:    "zhihu_source",
			Source:  "zhihu",
			Title:   "知乎素材暂不可用",
			Summary: summary,
			Status:  "review",
			Tags:    []string{"知乎", "待重试"},
			Reasons: []string{"搜索或解析失败"},
			Anchor: PublicMapAnnotationAnchor{
				Type:   "scope",
				NodeID: topic.NodeID,
				Label:  defaultIfEmpty(topic.AnchorLabel, "当前地图范围"),
			},
		},
	})
}

func emitGuideRunAnnotations(ctx context.Context, emitter *TraceEmitter, graphClient *graph.Client, tripPlanID string, topic guideEvidenceTopic, run tools.ZhihuGuideRun) {
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

func annotationFromZhihuCandidate(tripPlanID string, topic guideEvidenceTopic, candidate tools.ZhihuGuideCandidate, selectedURLs map[string]bool) *PublicMapAnnotation {
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

func normalizeGuideCandidateStatus(candidate tools.ZhihuGuideCandidate, selectedURLs map[string]bool) string {
	if selectedURLs[normalizeURLKey(candidate.URL)] || selectedURLs[candidate.URLHash] {
		return "selected"
	}
	switch strings.TrimSpace(candidate.Status) {
	case tools.ZhihuGuideStatusRejected:
		return "rejected"
	case tools.ZhihuGuideStatusAccepted, tools.ZhihuGuideStatusReview:
		return "review"
	case tools.ZhihuGuideStatusSelected:
		return "selected"
	default:
		return "raw"
	}
}

func selectedZhihuURLs(run tools.ZhihuGuideRun) map[string]bool {
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

func guideCandidateTags(candidate tools.ZhihuGuideCandidate, status string) []string {
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

func guideCandidateEvidence(candidate tools.ZhihuGuideCandidate) []string {
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

func queryPlanEvidence(items []tools.ZhihuGuideQuery) []string {
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

func writeGuideInsightFromAnnotation(ctx context.Context, graphClient *graph.Client, tripPlanID string, topic guideEvidenceTopic, annotation *PublicMapAnnotation) {
	if graphClient == nil || !graphClient.IsEnabled() || strings.TrimSpace(tripPlanID) == "" || annotation == nil {
		return
	}
	_, err := graphClient.WriteGuideInsight(ctx, tripPlanID, graph.GuideInsightInput{
		ID:             annotation.ID,
		Source:         annotation.Source,
		SourceTitle:    annotation.Title,
		SourceURL:      annotation.URL,
		AuthorName:     annotation.AuthorName,
		ContentSummary: annotation.Summary,
		Keywords:       append([]string(nil), annotation.Tags...),
		Sentiment:      "neutral",
		Status:         annotation.Status,
		Score:          annotation.Score,
		Reasons:        append([]string(nil), annotation.Reasons...),
		MatchedRegion:  topic.Region,
	})
	if err != nil {
		log.Warnf("[planning-guide] write guide insight failed: %v", err)
	}
}

func compactGuideTopic(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func truncateGuideText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "..."
}

func stablePlanningAnnotationID(parts ...string) string {
	return "ann-" + hashGuideText(strings.Join(parts, "|"))[:16]
}

func hashGuideText(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func normalizeURLKey(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}

func limitStrings(values []string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func uniqueLimitedStrings(values []string, limit int) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}
