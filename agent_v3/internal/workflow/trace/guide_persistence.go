package trace

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"strings"

	"agent_v3/internal/graph"

	"trpc.group/trpc-go/trpc-agent-go/log"
)

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
