package stages

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	domaintravel "agent_v3/internal/domain/travel"
	"agent_v3/internal/graph"
	workflowruntime "agent_v3/internal/workflow/runtime"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"trpc.group/trpc-go/trpc-agent-go/log"
)

func (a *graphWorkflowAgent) runGraphSplitting(ctx context.Context, tripPlanID string, requirements ...workflowruntime.TravelRequirementSnapshot) error {
	overview, err := a.graphClient.GetTripOverview(ctx, tripPlanID)
	if err != nil {
		return fmt.Errorf("get trip overview: %w", err)
	}
	tripAnchors := deriveAnchorsForGraphSplitting(overview, requirements...)

	globalDayIdx := 1
	for _, p := range overview.Phases {
		phaseID := getStr(p, "id")
		phaseName := getStr(p, "name")
		phaseRegion := getStr(p, "region")
		phaseTheme := getStr(p, "theme")
		phaseStart := getStr(p, "startDate")
		phaseEnd := getStr(p, "endDate")
		phaseAnchors := anchorsForPhase(tripAnchors, phaseName, phaseRegion, phaseTheme, len(overview.Phases) <= 1)
		phaseDayOffset := 0

		if phaseStart == "" || phaseEnd == "" {
			log.Warnf("[graph-splitting] Phase %s missing dates, skipping", phaseName)
			continue
		}

		startT, err := time.Parse("2006-01-02", phaseStart)
		if err != nil {
			return fmt.Errorf("parse phase start date %s: %w", phaseStart, err)
		}
		endT, err := time.Parse("2006-01-02", phaseEnd)
		if err != nil {
			return fmt.Errorf("parse phase end date %s: %w", phaseEnd, err)
		}

		log.Infof("[graph-splitting] Phase %s: %s ~ %s (%s)", phaseName, phaseStart, phaseEnd, phaseRegion)

		// Split phase into months by calendar boundary
		months := splitByMonth(startT, endT, phaseRegion)
		for mi, m := range months {
			monthID := fmt.Sprintf("month-%s-%d", phaseID[:8], mi+1)
			// Create Month node
			_, err := a.graphClient.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
				_, err := tx.Run(ctx,
					`MATCH (p:Phase {id: $phaseID})
					 MERGE (m:Month {id: $id})
					 ON CREATE SET m.name = $name, m.seq = $seq, m.startDate = $startDate,
						m.endDate = $endDate, m.region = $region, m.status = 'outlined'
					 MERGE (p)-[:HAS_MONTH]->(m)
					 RETURN m.id`,
					map[string]any{
						"phaseID": phaseID, "id": monthID,
						"name": m.name, "seq": mi + 1,
						"startDate": m.start.Format("2006-01-02"),
						"endDate":   m.end.Format("2006-01-02"),
						"region":    phaseRegion,
					})
				return nil, err
			})
			if err != nil {
				log.Errorf("[graph-splitting] create month %s: %v", monthID, err)
				continue
			}

			// Split month into weeks
			weeks := splitByWeek(m.start, m.end, phaseRegion)
			for wi, w := range weeks {
				weekID := fmt.Sprintf("week-%s-%d-%d", phaseID[:8], mi+1, wi+1)
				_, err := a.graphClient.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
					_, err := tx.Run(ctx,
						`MATCH (m:Month {id: $monthID})
						 CREATE (m)-[:HAS_WEEK]->(w:Week {
							id: $id, name: $name, seq: $seq, startDate: $startDate, endDate: $endDate,
							primaryLocation: $region, status: 'outlined'
						 }) RETURN w.id`,
						map[string]any{
							"monthID": monthID, "id": weekID,
							"name": w.name, "seq": wi + 1,
							"startDate": w.start.Format("2006-01-02"),
							"endDate":   w.end.Format("2006-01-02"),
							"region":    phaseRegion,
						})
					return nil, err
				})
				if err != nil {
					log.Errorf("[graph-splitting] create week %s: %v", weekID, err)
					continue
				}

				// Split week into days
				days := splitByDay(w.start, w.end, globalDayIdx)
				for _, d := range days {
					dayID := fmt.Sprintf("day-%s-%d", phaseID[:8], d.dayIndex)
					globalDayIdx++
					phaseDayOffset++
					dayPlan := anchoredDayPlanForPhase(phaseAnchors, phaseDayOffset, phaseName, phaseRegion)
					_, err := a.graphClient.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
						_, err := tx.Run(ctx,
							`MATCH (w:Week {id: $weekID})
							 CREATE (w)-[:HAS_DAY]->(d:Day {
								id: $id, date: $date, dayIndex: $dayIndex, theme: $theme,
								intensity: $intensity, primaryArea: $primaryArea,
								routeOverview: $routeOverview, thinkingNotes: $thinkingNotes,
								status: 'outlined'
							 }) RETURN d.id`,
							map[string]any{
								"weekID": weekID, "id": dayID,
								"date": d.date, "dayIndex": d.dayIndex,
								"theme": dayPlan.Theme, "intensity": "均衡",
								"primaryArea":   dayPlan.PrimaryArea,
								"routeOverview": dayPlan.RouteOverview,
								"thinkingNotes": dayPlan.ThinkingNotes,
							})
						return nil, err
					})
					if err != nil {
						log.Errorf("[graph-splitting] create day %s: %v", dayID, err)
					}
				}
			}
		}
	}

	// Verify
	overview2, err := a.graphClient.GetTripOverview(ctx, tripPlanID)
	if err == nil {
		log.Infof("[graph-splitting] done: %d phases, %d months, %d weeks, %d days",
			len(overview2.Phases), len(overview2.Months), len(overview2.Weeks), len(overview2.Days))
	}
	return nil
}

type anchoredDayPlan struct {
	Theme         string
	PrimaryArea   string
	RouteOverview string
	ThinkingNotes string
}

func deriveAnchorsFromTripOverview(overview *graph.TripOverview) []domaintravel.DestinationAnchorSnapshot {
	if overview == nil {
		return nil
	}
	snap := workflowruntime.TravelRequirementSnapshot{
		DestinationScope: strings.Join(append([]string{
			overview.TripPlan.Name,
			overview.TripPlan.RawRequirements,
		}, overview.TripPlan.MustVisit...), " "),
		TotalDays:     overview.TripPlan.TotalDays,
		TransportMode: overview.TripPlan.TransportMode,
		TravelStyle:   append([]string{overview.TripPlan.TravelStyle}, overview.TripPlan.Interests...),
		MustVisit:     append([]string(nil), overview.TripPlan.MustVisit...),
	}
	enrichRequirementWithDeterministicFields(&snap, snap.DestinationScope)
	return snap.DestinationAnchors
}

func deriveAnchorsForGraphSplitting(overview *graph.TripOverview, requirements ...workflowruntime.TravelRequirementSnapshot) []domaintravel.DestinationAnchorSnapshot {
	if len(requirements) > 0 {
		req := requirements[0]
		if len(req.DestinationAnchors) == 0 {
			enrichRequirementWithDeterministicFields(&req, strings.Join([]string{
				req.DestinationScope,
				strings.Join(req.MustVisit, " "),
			}, " "))
		}
		if len(req.DestinationAnchors) > 0 {
			return req.DestinationAnchors
		}
	}
	return deriveAnchorsFromTripOverview(overview)
}

func anchorsForPhase(anchors []domaintravel.DestinationAnchorSnapshot, phaseName, phaseRegion, phaseTheme string, allowAll bool) []domaintravel.DestinationAnchorSnapshot {
	text := strings.Join([]string{phaseName, phaseRegion, phaseTheme}, " ")
	var matched []domaintravel.DestinationAnchorSnapshot
	for _, anchor := range anchors {
		if anchor.Kind == "destination" {
			continue
		}
		if phaseTextMatchesAnchorDestination(text, anchor.Destination) || strings.Contains(text, anchor.Name) {
			matched = append(matched, anchor)
		}
	}
	if len(matched) == 0 && allowAll {
		for _, anchor := range anchors {
			if anchor.Kind != "destination" {
				matched = append(matched, anchor)
			}
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].Priority > matched[j].Priority
	})
	return dedupeDestinationAnchors(matched)
}

func phaseTextMatchesAnchorDestination(text, destination string) bool {
	if destination != "" && strings.Contains(text, destination) {
		return true
	}
	switch destination {
	case "香格里拉":
		return containsAny(text, []string{"迪庆", "滇西北", "梅里", "德钦"})
	case "稻城亚丁":
		return containsAny(text, []string{"亚丁", "稻城", "川西", "甘孜"})
	case "林芝":
		return containsAny(text, []string{"林芝", "藏东南", "西藏东南", "鲁朗", "巴松措", "南迦巴瓦"})
	default:
		return false
	}
}

func anchoredDayPlanForPhase(anchors []domaintravel.DestinationAnchorSnapshot, phaseDayOffset int, phaseName, phaseRegion string) anchoredDayPlan {
	fallbackArea := firstNonEmptyString(phaseRegion, phaseName)
	if len(anchors) == 0 {
		return anchoredDayPlan{
			Theme:         phaseName,
			PrimaryArea:   fallbackArea,
			RouteOverview: fmt.Sprintf("围绕%s展开，当天地点需要通过地图搜索复核。", fallbackArea),
			ThinkingNotes: "未匹配到内置自然锚点，按阶段区域生成日级搜索。",
		}
	}
	if phaseDayOffset <= 0 {
		phaseDayOffset = 1
	}
	anchor := anchors[(phaseDayOffset-1)%len(anchors)]
	return anchoredDayPlan{
		Theme:         fmt.Sprintf("%s自然风光：%s", defaultIfEmpty(anchor.Destination, fallbackArea), anchor.Name),
		PrimaryArea:   anchor.Name,
		RouteOverview: fmt.Sprintf("围绕%s展开自然风光体验；它是%s的核心候选锚点，后续只用真实 POI 坐标上图。", anchor.Name, defaultIfEmpty(anchor.Destination, fallbackArea)),
		ThinkingNotes: strings.TrimSpace(strings.Join([]string{
			"anchor=" + anchor.Name,
			"destination=" + anchor.Destination,
			"reason=" + anchor.Reason,
		}, "；")),
	}
}

// --- Month/Week/Day splitting helpers ---

type monthSpan struct {
	name  string
	start time.Time
	end   time.Time
}

func splitByMonth(start, end time.Time, region string) []monthSpan {
	var months []monthSpan
	cur := start
	for cur.Before(end) || cur.Equal(end) {
		// End of current month
		monthEnd := time.Date(cur.Year(), cur.Month()+1, 0, 0, 0, 0, 0, cur.Location())
		if monthEnd.After(end) {
			monthEnd = end
		}
		months = append(months, monthSpan{
			name:  fmt.Sprintf("%s %d年%d月", region, cur.Year(), cur.Month()),
			start: cur,
			end:   monthEnd,
		})
		cur = monthEnd.AddDate(0, 0, 1)
	}
	return months
}

type weekSpan struct {
	name  string
	start time.Time
	end   time.Time
}

func splitByWeek(start, end time.Time, region string) []weekSpan {
	var weeks []weekSpan
	cur := start
	seq := 1
	for cur.Before(end) || cur.Equal(end) {
		weekEnd := cur.AddDate(0, 0, 6)
		if weekEnd.After(end) {
			weekEnd = end
		}
		weeks = append(weeks, weekSpan{
			name:  fmt.Sprintf("第%d周", seq),
			start: cur,
			end:   weekEnd,
		})
		cur = weekEnd.AddDate(0, 0, 1)
		seq++
	}
	return weeks
}

type daySpan struct {
	date     string
	dayIndex int
}

func splitByDay(start, end time.Time, startIdx int) []daySpan {
	var days []daySpan
	cur := start
	idx := startIdx
	for cur.Before(end) || cur.Equal(end) {
		days = append(days, daySpan{
			date:     cur.Format("2006-01-02"),
			dayIndex: idx,
		})
		cur = cur.AddDate(0, 0, 1)
		idx++
	}
	return days
}

// --- Final Output: week-based LLM generation ---
