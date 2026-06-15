package graphtools

import (
	"context"
	"fmt"

	"agent_v3/internal/graph"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

// --- check_weather_feasibility ---

type CheckWeatherFeasibilityInput struct {
	POIID string `json:"poi_id" jsonschema:"required,description=POI节点ID"`
	Month int    `json:"month" jsonschema:"required,description=月份 1-12"`
}

type CheckWeatherFeasibilityOutput struct {
	Feasible        bool   `json:"feasible"`
	RiskLevel       string `json:"risk_level"`
	Reason          string `json:"reason"`
	SuggestedBackup string `json:"suggested_backup"`
}

func newCheckWeatherFeasibilityTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in CheckWeatherFeasibilityInput) (CheckWeatherFeasibilityOutput, error) {
			if client == nil || !client.IsEnabled() {
				return CheckWeatherFeasibilityOutput{}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			// Load day subgraph to get POI region
			sg, err := client.GetDaySubgraph(ctx, in.POIID)
			if err != nil || sg == nil {
				// Fallback: query weather context with a generic approach
				return CheckWeatherFeasibilityOutput{
					Feasible: true, RiskLevel: "unknown",
					Reason: "无法获取POI区域信息，默认可行",
				}, nil
			}
			// Use the POI city/district as region for weather lookup
			region := "unknown"
			for _, poi := range sg.POIs {
				if poi.City != "" {
					region = poi.City
				} else if poi.District != "" {
					region = poi.District
				}
				break
			}
			if region == "unknown" {
				return CheckWeatherFeasibilityOutput{
					Feasible: true, RiskLevel: "unknown",
					Reason: "POI无区域信息，默认可行",
				}, nil
			}

			wc, err := client.GetWeatherContext(ctx, region, in.Month)
			if err != nil || wc == nil {
				return CheckWeatherFeasibilityOutput{
					Feasible: true, RiskLevel: "unknown",
					Reason: fmt.Sprintf("区域 %s 无气候数据，默认可行", region),
				}, nil
			}

			// Check constraints
			for _, c := range wc.Constraints {
				if c.Severity == "critical" {
					return CheckWeatherFeasibilityOutput{
						Feasible: false, RiskLevel: "critical",
						Reason:          fmt.Sprintf("天气约束: %s - %s", c.ConstraintType, c.Description),
						SuggestedBackup: "建议选择室内或低风险替代POI",
					}, nil
				}
			}

			// Check extreme weather risk
			for _, cd := range wc.ClimateData {
				if cd.ExtremeWeatherRisk == "high" {
					return CheckWeatherFeasibilityOutput{
						Feasible: false, RiskLevel: "high",
						Reason: fmt.Sprintf("%s %d月极端天气风险高", region, in.Month),
					}, nil
				}
			}

			return CheckWeatherFeasibilityOutput{
				Feasible: true, RiskLevel: "low",
				Reason: fmt.Sprintf("%s %d月天气条件良好", region, in.Month),
			}, nil
		},
		function.WithName("check_weather_feasibility"),
		function.WithDescription("[图数据库天气] 检查某POI在某月的气候可行性。返回风险等级和建议。"),
	)
}

// --- suggest_seasonal_alternatives ---

type SuggestSeasonalAlternativesInput struct {
	POIID string `json:"poi_id" jsonschema:"required,description=被替代的POI ID"`
	Month int    `json:"month" jsonschema:"required,description=月份 1-12"`
}

type SuggestSeasonalAlternativesOutput struct {
	Alternatives string `json:"alternatives"`
}

func newSuggestSeasonalAlternativesTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in SuggestSeasonalAlternativesInput) (SuggestSeasonalAlternativesOutput, error) {
			if client == nil || !client.IsEnabled() {
				return SuggestSeasonalAlternativesOutput{}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			// Return structured guidance - the coordinator uses this to find alternatives
			return SuggestSeasonalAlternativesOutput{
				Alternatives: fmt.Sprintf(
					`{"poi_id":"%s","month":%d,"instruction":"请通过 get_weather_context 查询同区域其他POI的气候可行性，优先选择同类型且无天气约束的POI"}`,
					in.POIID, in.Month,
				),
			}, nil
		},
		function.WithName("suggest_seasonal_alternatives"),
		function.WithDescription("[图数据库天气] 当POI在某月不可行时，推荐同类型但气候合适的替代POI。"),
	)
}

// --- get_seasonal_route_risk ---

type GetSeasonalRouteRiskInput struct {
	Region string `json:"region" jsonschema:"required,description=区域"`
	Month  int    `json:"month" jsonschema:"required,description=月份 1-12"`
}

type GetSeasonalRouteRiskOutput struct {
	OverallRisk string `json:"overall_risk"`
	Risks       string `json:"risks"`
	Advice      string `json:"advice"`
}

func newGetSeasonalRouteRiskTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in GetSeasonalRouteRiskInput) (GetSeasonalRouteRiskOutput, error) {
			if client == nil || !client.IsEnabled() {
				return GetSeasonalRouteRiskOutput{}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			wc, err := client.GetWeatherContext(ctx, in.Region, in.Month)
			if err != nil || wc == nil {
				return GetSeasonalRouteRiskOutput{
					OverallRisk: "unknown",
					Risks:       "无气候数据",
					Advice:      "建议手动查询该区域天气信息",
				}, nil
			}

			var riskLevels []string
			for _, c := range wc.Constraints {
				riskLevels = append(riskLevels, fmt.Sprintf("%s(%s): %s", c.ConstraintType, c.Severity, c.Description))
			}
			for _, cd := range wc.ClimateData {
				if cd.ExtremeWeatherRisk != "" && cd.ExtremeWeatherRisk != "none" {
					riskLevels = append(riskLevels, fmt.Sprintf("极端天气风险: %s", cd.ExtremeWeatherRisk))
				}
			}

			overallRisk := "low"
			advice := "路线可行"
			if len(riskLevels) > 0 {
				hasCritical := false
				for _, c := range wc.Constraints {
					if c.Severity == "critical" {
						hasCritical = true
						break
					}
				}
				if hasCritical {
					overallRisk = "high"
					advice = "存在严重天气约束，建议调整路线或日期"
				} else {
					overallRisk = "medium"
					advice = "存在天气风险，建议准备备选方案"
				}
			}

			return GetSeasonalRouteRiskOutput{
				OverallRisk: overallRisk,
				Risks:       fmt.Sprintf("%v", riskLevels),
				Advice:      advice,
			}, nil
		},
		function.WithName("get_seasonal_route_risk"),
		function.WithDescription("[图数据库天气] 返回某区域某时间段的路线风险画像：封山、台风、暴雨等。"),
	)
}

// init registers the graph weather tools with a helper that works with the function package.
func init() {
	// Ensure graph package types are imported and available
	_ = graph.Client{}
}
