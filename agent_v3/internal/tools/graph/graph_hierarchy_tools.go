package graphtools

import (
	"context"
	"fmt"

	"agent_v3/internal/graph"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

// --- merge_children ---

type MergeChildrenInput struct {
	ParentNodeID string `json:"parent_node_id" jsonschema:"required,description=父节点ID"`
}

type MergeChildrenOutput struct {
	Success bool `json:"success"`
}

func newMergeChildrenTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in MergeChildrenInput) (MergeChildrenOutput, error) {
			if client == nil || !client.IsEnabled() {
				return MergeChildrenOutput{Success: false}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			// Merge: set parent status back to outlined and collect child summaries
			if err := client.UpdateNode(ctx, in.ParentNodeID, map[string]any{
				"status": graph.StatusOutlined,
			}); err != nil {
				return MergeChildrenOutput{Success: false}, err
			}
			return MergeChildrenOutput{Success: true}, nil
		},
		function.WithName("merge_children"),
		function.WithDescription("[图数据库层级管理] 合并子节点摘要回父节点，父节点重置为outlined。用于回滚/重规划。"),
	)
}

// --- rebalance_phase ---

type RebalancePhaseInput struct {
	PhaseID              string `json:"phase_id" jsonschema:"required,description=Phase节点ID"`
	NewMonthDistribution string `json:"new_month_distribution" jsonschema:"required,description=新的月分布JSON"`
}

type RebalancePhaseOutput struct {
	Success bool `json:"success"`
}

func newRebalancePhaseTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in RebalancePhaseInput) (RebalancePhaseOutput, error) {
			if client == nil || !client.IsEnabled() {
				return RebalancePhaseOutput{Success: false}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			if err := client.UpdateNode(ctx, in.PhaseID, map[string]any{
				"status": graph.StatusOutlined,
			}); err != nil {
				return RebalancePhaseOutput{Success: false}, err
			}
			return RebalancePhaseOutput{Success: true}, nil
		},
		function.WithName("rebalance_phase"),
		function.WithDescription("[图数据库层级管理] 重新分配某阶段内各月的天数/区域。Phase重置为outlined，触发新一轮Month→Week拆分。"),
	)
}

// --- recalculate_week_count ---

type RecalculateWeekCountInput struct {
	MonthID  string `json:"month_id" jsonschema:"required,description=Month节点ID"`
	DayCount int    `json:"day_count" jsonschema:"description=该月实际天数"`
}

type RecalculateWeekCountOutput struct {
	WeekCount int  `json:"week_count"`
	Success   bool `json:"success"`
}

func newRecalculateWeekCountTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in RecalculateWeekCountInput) (RecalculateWeekCountOutput, error) {
			if client == nil || !client.IsEnabled() {
				return RecalculateWeekCountOutput{Success: false}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			days := in.DayCount
			if days <= 0 {
				days = 30
			}
			weekCount := days / 7
			if days%7 > 0 {
				weekCount++
			}
			if err := client.UpdateNode(ctx, in.MonthID, map[string]any{
				"weekCount": weekCount,
			}); err != nil {
				return RecalculateWeekCountOutput{Success: false}, err
			}
			return RecalculateWeekCountOutput{WeekCount: weekCount, Success: true}, nil
		},
		function.WithName("recalculate_week_count"),
		function.WithDescription("[图数据库层级管理] 修正Month的weekCount。L2 Month级审查时若周数不匹配，调用此工具修正。"),
	)
}
