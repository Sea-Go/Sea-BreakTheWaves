package agent

import (
	"context"
	"fmt"
	"sort"

	"agent_v2/graph"

	"trpc.group/trpc-go/trpc-agent-go/log"
)

// checkAfterMacroPlanning validates the completeness of macro planning results.
// If expectedTotalDays > 0, it also verifies Phase dayCount sum matches.
func (a *graphWorkflowAgent) checkAfterMacroPlanning(ctx context.Context, tripPlanID string, expectedTotalDays int) error {
	// 1. TripPlan must exist
	tp, err := a.graphClient.FindTripPlanByID(ctx, tripPlanID)
	if err != nil {
		return fmt.Errorf("TripPlan %s 查询失败: %w", tripPlanID, err)
	}
	if tp == nil {
		return fmt.Errorf("TripPlan %s 不存在", tripPlanID)
	}

	// 2. Get full overview
	overview, err := a.graphClient.GetTripOverview(ctx, tripPlanID)
	if err != nil {
		return fmt.Errorf("获取 TripOverview 失败: %w", err)
	}

	phases := overview.Phases
	if len(phases) < 3 {
		return fmt.Errorf("Phase 数量不足: 需要 >= 3，实际 %d", len(phases))
	}
	if len(phases) > 8 {
		return fmt.Errorf("Phase 数量过多: 需要 <= 8，实际 %d", len(phases))
	}

	// 3. Sort phases by seq before validation
	sort.Slice(phases, func(i, j int) bool {
		return getFloat(phases[i], "seq") < getFloat(phases[j], "seq")
	})

	// 4. Validate Phase seq continuity and required fields
	totalDayCount := 0
	for i, p := range phases {
		seq := int(getFloat(p, "seq"))
		if seq != i+1 {
			return fmt.Errorf("Phase[%d] seq 不连续: 期望 %d，实际 %d", i, i+1, seq)
		}
		if getStr(p, "startDate") == "" {
			return fmt.Errorf("Phase[%d] (%s) 缺少 startDate", i, getStr(p, "name"))
		}
		if getStr(p, "region") == "" {
			return fmt.Errorf("Phase[%d] (%s) 缺少 region", i, getStr(p, "name"))
		}
		dayCount := int(getFloat(p, "dayCount"))
		if dayCount <= 0 {
			return fmt.Errorf("Phase[%d] (%s) dayCount 无效: %d", i, getStr(p, "name"), dayCount)
		}
		totalDayCount += dayCount
	}

	// 5. If expectedTotalDays is provided, verify sum
	if expectedTotalDays > 0 && totalDayCount != expectedTotalDays {
		return fmt.Errorf("Phase dayCount 之和不匹配: 期望 %d，实际 %d", expectedTotalDays, totalDayCount)
	}

	// 6. Macro planning must NOT have Month/Week/Day nodes
	for _, childType := range []string{graph.NodeTypeMonth, graph.NodeTypeWeek, graph.NodeTypeDay} {
		count, err := a.graphClient.CountChildrenByType(ctx, tripPlanID, childType)
		if err != nil {
			log.Warnf("[workflow-checks] CountChildrenByType(%s, %s) error: %v", tripPlanID, childType, err)
			continue
		}
		if count > 0 {
			return fmt.Errorf("宏观规划阶段不应存在 %s 节点，实际有 %d 个", childType, count)
		}
	}

	log.Infof("[workflow-checks] macro planning validation passed: %d phases, totalDayCount=%d",
		len(phases), totalDayCount)
	return nil
}
