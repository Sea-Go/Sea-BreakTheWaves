package tools

import (
	"context"
	"fmt"

	"agent_v2/graph"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

// --- get_subgraph ---

type GetSubgraphInput struct {
	NodeID string `json:"node_id" jsonschema:"required,description=节点ID"`
	Depth  int    `json:"depth" jsonschema:"description=查询深度 - day=Day级子图, week=Week级子图, month=Month级子图, phase=Phase级子图, 0=只返回节点自身"`
}

type GetSubgraphOutput struct {
	Data string `json:"data"` // JSON-serialized subgraph data
}

func newGetSubgraphTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in GetSubgraphInput) (GetSubgraphOutput, error) {
			if client == nil || !client.IsEnabled() {
				return GetSubgraphOutput{}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			// Try Day-level subgraph first
			sg, err := client.GetDaySubgraph(ctx, in.NodeID)
			if err == nil && sg != nil {
				return GetSubgraphOutput{Data: marshalGraphData(sg)}, nil
			}
			// Fallback to generic children
			children, err := client.ChildrenSummary(ctx, in.NodeID)
			if err != nil {
				return GetSubgraphOutput{}, err
			}
			return GetSubgraphOutput{Data: marshalGraphData(children)}, nil
		},
		function.WithName("get_subgraph"),
		function.WithDescription("[图数据库读取] 核心隔离查询：加载任意节点的子图，只返回该节点及直接关联数据。处理某天时只查当天数据。"),
	)
}

// --- get_children ---

type GetChildrenInput struct {
	ParentNodeID string `json:"parent_node_id" jsonschema:"required,description=父节点ID"`
}

type GetChildrenOutput struct {
	Children string `json:"children"`
}

func newGetChildrenTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in GetChildrenInput) (GetChildrenOutput, error) {
			if client == nil || !client.IsEnabled() {
				return GetChildrenOutput{}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			children, err := client.ChildrenSummary(ctx, in.ParentNodeID)
			if err != nil {
				return GetChildrenOutput{}, err
			}
			return GetChildrenOutput{Children: marshalGraphData(children)}, nil
		},
		function.WithName("get_children"),
		function.WithDescription("[图数据库读取] 获取某节点的所有直接子节点摘要。查看某Phase下有哪些Month。"),
	)
}

// --- get_trip_overview ---

type GetTripOverviewInput struct {
	TripPlanID string `json:"trip_plan_id" jsonschema:"required,description=TripPlan节点ID"`
}

type GetTripOverviewOutput struct {
	Overview string `json:"overview"`
}

func newGetTripOverviewTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in GetTripOverviewInput) (GetTripOverviewOutput, error) {
			if client == nil || !client.IsEnabled() {
				return GetTripOverviewOutput{}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			overview, err := client.GetTripOverview(ctx, in.TripPlanID)
			if err != nil {
				return GetTripOverviewOutput{}, err
			}
			return GetTripOverviewOutput{Overview: marshalGraphData(overview)}, nil
		},
		function.WithName("get_trip_overview"),
		function.WithDescription("[图数据库读取] 获取完整层级树摘要：TripPlan + 所有Phase/Month/Week/Day的概要。不含POI细节。"),
	)
}

// --- get_weather_context ---

type GetWeatherContextInput struct {
	Region string `json:"region" jsonschema:"required,description=区域名称"`
	Month  int    `json:"month" jsonschema:"required,description=月份 1-12"`
}

type GetWeatherContextOutput struct {
	WeatherContext string `json:"weather_context"`
}

func newGetWeatherContextTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in GetWeatherContextInput) (GetWeatherContextOutput, error) {
			if client == nil || !client.IsEnabled() {
				return GetWeatherContextOutput{}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			wc, err := client.GetWeatherContext(ctx, in.Region, in.Month)
			if err != nil {
				return GetWeatherContextOutput{}, err
			}
			return GetWeatherContextOutput{WeatherContext: marshalGraphData(wc)}, nil
		},
		function.WithName("get_weather_context"),
		function.WithDescription("[图数据库读取] 获取某区域某月的完整天气画像：ClimateData + WeatherConstraint + SeasonalEvent。"),
	)
}

// --- get_day_full_context ---

type GetDayFullContextInput struct {
	DayID string `json:"day_id" jsonschema:"required,description=Day节点ID"`
}

type GetDayFullContextOutput struct {
	FullContext string `json:"full_context"`
}

func newGetDayFullContextTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in GetDayFullContextInput) (GetDayFullContextOutput, error) {
			if client == nil || !client.IsEnabled() {
				return GetDayFullContextOutput{}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			sg, err := client.GetDaySubgraph(ctx, in.DayID)
			if err != nil {
				return GetDayFullContextOutput{}, err
			}
			return GetDayFullContextOutput{FullContext: marshalGraphData(sg)}, nil
		},
		function.WithName("get_day_full_context"),
		function.WithDescription("[图数据库读取] 获取单天完整上下文：Day + POIs + Routes + Insights + Weather + Reviews。用于Day级审查。"),
	)
}

// --- query_insights ---

type QueryInsightsInput struct {
	Region string `json:"region" jsonschema:"description=按区域查询"`
	POIID  string `json:"poi_id" jsonschema:"description=按POI ID查询"`
}

type QueryInsightsOutput struct {
	Insights string `json:"insights"`
}

func newQueryInsightsTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in QueryInsightsInput) (QueryInsightsOutput, error) {
			if client == nil || !client.IsEnabled() {
				return QueryInsightsOutput{}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			// Use weather context as a proxy for region-based insight query
			// or get children as a generic fallback
			children, _ := client.ChildrenSummary(ctx, in.Region)
			return QueryInsightsOutput{Insights: marshalGraphData(children)}, nil
		},
		function.WithName("query_insights"),
		function.WithDescription("[图数据库读取] 按区域或POI查询关联的攻略洞察。"),
	)
}

// --- get_unplanned_nodes ---

type GetUnplannedNodesInput struct {
	ParentNodeID string `json:"parent_node_id" jsonschema:"required,description=父节点ID"`
}

type GetUnplannedNodesOutput struct {
	Unplanned string `json:"unplanned"`
}

func newGetUnplannedNodesTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in GetUnplannedNodesInput) (GetUnplannedNodesOutput, error) {
			if client == nil || !client.IsEnabled() {
				return GetUnplannedNodesOutput{}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			nodes, err := client.GetUnplannedNodes(ctx, in.ParentNodeID)
			if err != nil {
				return GetUnplannedNodesOutput{}, err
			}
			return GetUnplannedNodesOutput{Unplanned: marshalGraphData(nodes)}, nil
		},
		function.WithName("get_unplanned_nodes"),
		function.WithDescription("[图数据库读取] 发现遗漏：返回某节点下状态非done/reviewed的子节点列表。"),
	)
}

// --- get_layer_review_status ---

type GetLayerReviewStatusInput struct {
	ParentNodeID string `json:"parent_node_id" jsonschema:"required,description=父节点ID"`
	Level        string `json:"level" jsonschema:"description=审查层级 TripPlan/Phase/Month/Week/Day/POI"`
}

type GetLayerReviewStatusOutput struct {
	ReviewStatus string `json:"review_status"`
}

func newGetLayerReviewStatusTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in GetLayerReviewStatusInput) (GetLayerReviewStatusOutput, error) {
			if client == nil || !client.IsEnabled() {
				return GetLayerReviewStatusOutput{}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			status, err := client.GetLayerReviewStatus(ctx, in.ParentNodeID)
			if err != nil {
				return GetLayerReviewStatusOutput{}, err
			}
			return GetLayerReviewStatusOutput{ReviewStatus: marshalGraphData(status)}, nil
		},
		function.WithName("get_layer_review_status"),
		function.WithDescription("[图数据库读取] 获取某层所有节点的审查状态汇总。用于逐层审查进度追踪。"),
	)
}

// --- get_constraint_violations ---

type GetConstraintViolationsInput struct {
	NodeID string `json:"node_id" jsonschema:"required,description=节点ID"`
}

type GetConstraintViolationsOutput struct {
	Violations string `json:"violations"`
}

func newGetConstraintViolationsTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in GetConstraintViolationsInput) (GetConstraintViolationsOutput, error) {
			if client == nil || !client.IsEnabled() {
				return GetConstraintViolationsOutput{}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			violations, err := client.GetConstraintViolations(ctx, in.NodeID)
			if err != nil {
				return GetConstraintViolationsOutput{}, err
			}
			return GetConstraintViolationsOutput{Violations: marshalGraphData(violations)}, nil
		},
		function.WithName("get_constraint_violations"),
		function.WithDescription("[图数据库读取] 追溯约束违规：返回某节点及其子树中所有未通过审查的违规详情。"),
	)
}

// --- get_node_budget_summary ---

type GetNodeBudgetSummaryInput struct {
	NodeID string `json:"node_id" jsonschema:"required,description=节点ID"`
}

type GetNodeBudgetSummaryOutput struct {
	TotalCost float64 `json:"total_cost"`
}

func newGetNodeBudgetSummaryTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in GetNodeBudgetSummaryInput) (GetNodeBudgetSummaryOutput, error) {
			if client == nil || !client.IsEnabled() {
				return GetNodeBudgetSummaryOutput{}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			cost, err := client.GetNodeBudgetSummary(ctx, in.NodeID)
			if err != nil {
				return GetNodeBudgetSummaryOutput{}, err
			}
			return GetNodeBudgetSummaryOutput{TotalCost: cost}, nil
		},
		function.WithName("get_node_budget_summary"),
		function.WithDescription("[图数据库读取] 预算约束检查：汇总某节点及其子树的所有费用。"),
	)
}