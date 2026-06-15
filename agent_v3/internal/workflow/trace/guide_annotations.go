package trace

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agent_v3/internal/config"
	"agent_v3/internal/graph"
	zhihutools "agent_v3/internal/tools/zhihu"
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
		run, err := zhihutools.CollectZhihuGuideMaterial(ctx, config.Cfg.Zhihu, topic.Topic)
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
