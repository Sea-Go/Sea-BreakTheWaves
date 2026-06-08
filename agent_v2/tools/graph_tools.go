package tools

import (
	"context"

	"agent_v2/config"
	"agent_v2/graph"

	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// GraphToolSet wraps the Neo4j client and exposes graph interaction tools.
type GraphToolSet struct {
	client *graph.Client
	tools  []tool.Tool
}

// NewDefaultGraphToolSet creates a GraphToolSet from the global client.
func NewDefaultGraphToolSet() *GraphToolSet {
	client := graph.GetClient()
	if client == nil || !client.IsEnabled() {
		return &GraphToolSet{client: nil, tools: nil}
	}
	return &GraphToolSet{
		client: client,
		tools:  newGraphTools(client),
	}
}

// NewGraphToolSet creates a GraphToolSet from specific config.
func NewGraphToolSet(cfg config.Neo4jConfig) *GraphToolSet {
	client, err := graph.NewClient(cfg)
	if err != nil {
		return &GraphToolSet{client: nil, tools: nil}
	}
	return &GraphToolSet{
		client: client,
		tools:  newGraphTools(client),
	}
}

func (s *GraphToolSet) Tools(_ context.Context) []tool.Tool {
	if s.tools == nil {
		return nil
	}
	return append([]tool.Tool(nil), s.tools...)
}

func (s *GraphToolSet) Close() error {
	if s.client != nil {
		return s.client.Close(context.Background())
	}
	return nil
}

func (s *GraphToolSet) Name() string {
	return "graph"
}

// IsEnabled reports whether the graph database is available.
func (s *GraphToolSet) IsEnabled() bool {
	return s.client != nil && s.client.IsEnabled()
}

// NewDefaultGraphTools returns graph tools as a flat slice (for direct coordinator use).
func NewDefaultGraphTools() []tool.Tool {
	client := graph.GetClient()
	if client == nil || !client.IsEnabled() {
		return nil
	}
	return newGraphTools(client)
}

// newGraphTools builds the full tool list.
func newGraphTools(client *graph.Client) []tool.Tool {
	if client == nil || !client.IsEnabled() {
		return nil
	}
	return []tool.Tool{
		// Write tools (8)
		newCreateTripPlanTool(client),
		newSplitParentNodeTool(client),
		newUpsertPOIToDayTool(client),
		newWriteRouteTool(client),
		newWriteGuideInsightTool(client),
		newWriteReviewResultTool(client),
		newUpdateNodeTool(client),
		newWriteClimateDataTool(client),

		// Read tools (10)
		newGetSubgraphTool(client),
		newGetChildrenTool(client),
		newGetTripOverviewTool(client),
		newGetWeatherContextTool(client),
		newGetDayFullContextTool(client),
		newQueryInsightsTool(client),
		newGetUnplannedNodesTool(client),
		newGetLayerReviewStatusTool(client),
		newGetConstraintViolationsTool(client),
		newGetNodeBudgetSummaryTool(client),

		// Hierarchy tools (3)
		newMergeChildrenTool(client),
		newRebalancePhaseTool(client),
		newRecalculateWeekCountTool(client),

		// Weather tools (3)
		newCheckWeatherFeasibilityTool(client),
		newSuggestSeasonalAlternativesTool(client),
		newGetSeasonalRouteRiskTool(client),
	}
}

// NewMacroPlanningGraphTools returns only the 4 tools needed for macro planning.
func NewMacroPlanningGraphTools() []tool.Tool {
	client := graph.GetClient()
	if client == nil || !client.IsEnabled() {
		return nil
	}
	return []tool.Tool{
		newCreateTripPlanTool(client),
		newSplitParentNodeTool(client),
		newGetWeatherContextTool(client),
		newWriteClimateDataTool(client),
	}
}