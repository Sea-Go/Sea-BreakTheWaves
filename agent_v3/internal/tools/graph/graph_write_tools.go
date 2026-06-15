package graphtools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent_v3/internal/graph"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

// --- create_trip_plan ---

type CreateTripPlanInput struct {
	TripPlanID                      string   `json:"trip_plan_id" jsonschema:"required,description=系统预生成的 TripPlanID，必须原样使用"`
	Name                            string   `json:"name" jsonschema:"required,description=旅行名称"`
	StartDate                       string   `json:"start_date" jsonschema:"required,description=出发日期 YYYY-MM-DD"`
	EndDate                         string   `json:"end_date" jsonschema:"required,description=结束日期 YYYY-MM-DD"`
	TotalDays                       int      `json:"total_days" jsonschema:"required,description=总天数"`
	BudgetTotal                     float64  `json:"budget_total" jsonschema:"description=总预算"`
	TravelStyle                     string   `json:"travel_style" jsonschema:"description=旅行风格"`
	TransportMode                   string   `json:"transport_mode" jsonschema:"description=交通方式"`
	Interests                       []string `json:"interests" jsonschema:"description=兴趣偏好"`
	MustVisit                       []string `json:"must_visit" jsonschema:"description=必去地点"`
	Avoid                           []string `json:"avoid" jsonschema:"description=避雷地点"`
	RawRequirements                 string   `json:"raw_requirements" jsonschema:"description=用户原始需求"`
	MaxConsecutiveHighIntensityDays int      `json:"max_consecutive_high_intensity_days" jsonschema:"description=连续高强度天数上限"`
	UserID                          string   `json:"user_id" jsonschema:"required,description=用户ID"`
	SessionID                       string   `json:"session_id" jsonschema:"required,description=会话ID"`
	RequestID                       string   `json:"request_id" jsonschema:"required,description=请求ID"`
}

type CreateTripPlanOutput struct {
	TripPlanID string `json:"trip_plan_id"`
	Success    bool   `json:"success"`
}

func newCreateTripPlanTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in CreateTripPlanInput) (CreateTripPlanOutput, error) {
			if client == nil || !client.IsEnabled() {
				return CreateTripPlanOutput{Success: false},
					fmt.Errorf("Neo4j 图数据库不可用")
			}
			if strings.TrimSpace(in.TripPlanID) == "" {
				return CreateTripPlanOutput{Success: false}, fmt.Errorf("trip_plan_id is required")
			}
			if strings.TrimSpace(in.UserID) == "" {
				return CreateTripPlanOutput{Success: false}, fmt.Errorf("user_id is required")
			}
			if strings.TrimSpace(in.SessionID) == "" {
				return CreateTripPlanOutput{Success: false}, fmt.Errorf("session_id is required")
			}
			if strings.TrimSpace(in.RequestID) == "" {
				return CreateTripPlanOutput{Success: false}, fmt.Errorf("request_id is required")
			}
			tp := graph.TripPlanNode{
				ID:   in.TripPlanID,
				Name: in.Name, StartDate: in.StartDate, EndDate: in.EndDate,
				TotalDays: in.TotalDays, BudgetTotal: in.BudgetTotal,
				TravelStyle: in.TravelStyle, TransportMode: in.TransportMode,
				Interests: in.Interests, MustVisit: in.MustVisit, Avoid: in.Avoid,
				RawRequirements:                 in.RawRequirements,
				MaxConsecutiveHighIntensityDays: in.MaxConsecutiveHighIntensityDays,
				UserID:                          in.UserID, SessionID: in.SessionID, RequestID: in.RequestID,
			}
			id, err := client.CreateTripPlan(ctx, tp)
			if err != nil {
				return CreateTripPlanOutput{Success: false}, err
			}
			return CreateTripPlanOutput{TripPlanID: id, Success: true}, nil
		},
		function.WithName("create_trip_plan"),
		function.WithDescription("[图数据库写入] 创建 TripPlan 根节点，存储全年旅行计划的元数据。"),
	)
}

// --- split_parent_node ---

type SplitChildSpec struct {
	ID        string `json:"id" jsonschema:"description=子节点ID，留空自动生成"`
	Name      string `json:"name" jsonschema:"required,description=子节点名称"`
	Seq       int    `json:"seq" jsonschema:"required,description=序号"`
	StartDate string `json:"start_date" jsonschema:"description=开始日期"`
	EndDate   string `json:"end_date" jsonschema:"description=结束日期"`
	Region    string `json:"region" jsonschema:"description=所属区域"`
	DayCount  int    `json:"day_count" jsonschema:"description=该阶段包含的天数"`
}

type SplitParentNodeInput struct {
	ParentNodeID string           `json:"parent_node_id" jsonschema:"required,description=父节点ID"`
	ChildType    string           `json:"child_type" jsonschema:"required,description=子节点类型 Phase/Month/Week/Day"`
	Children     []SplitChildSpec `json:"children" jsonschema:"required,description=子节点列表"`
}

type SplitParentNodeOutput struct {
	ChildIDs []string `json:"child_ids"`
	Success  bool     `json:"success"`
}

func newSplitParentNodeTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in SplitParentNodeInput) (SplitParentNodeOutput, error) {
			if client == nil || !client.IsEnabled() {
				return SplitParentNodeOutput{Success: false}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			children := make([]graph.SplitChildInput, len(in.Children))
			for i, ch := range in.Children {
				children[i] = graph.SplitChildInput{
					ID: ch.ID, Name: ch.Name, Seq: ch.Seq,
					StartDate: ch.StartDate, EndDate: ch.EndDate, Region: ch.Region,
					DayCount: ch.DayCount,
				}
			}
			ids, err := client.SplitParentNode(ctx, in.ParentNodeID, in.ChildType, children)
			if err != nil {
				return SplitParentNodeOutput{Success: false}, err
			}
			return SplitParentNodeOutput{ChildIDs: ids, Success: true}, nil
		},
		function.WithName("split_parent_node"),
		function.WithDescription("[图数据库写入] 通用拆分工具：将父节点拆分为N个子节点，父节点标记decomposed。用于 TripPlan→Phase、Phase→Month、Month→Week、Week→Day。"),
	)
}

// --- upsert_poi_to_day ---

type UpsertPOIToDayInput struct {
	DayID            string  `json:"day_id" jsonschema:"required,description=所属Day节点ID"`
	POIID            string  `json:"poi_id" jsonschema:"description=POI ID，留空自动生成"`
	Name             string  `json:"name" jsonschema:"required,description=POI名称"`
	Type             string  `json:"type" jsonschema:"required,description=类型：景点/餐饮/住宿/交通枢纽/购物/其他"`
	Lat              float64 `json:"lat" jsonschema:"required,description=纬度"`
	Lng              float64 `json:"lng" jsonschema:"required,description=经度"`
	Address          string  `json:"address" jsonschema:"description=地址"`
	District         string  `json:"district" jsonschema:"description=所在区县"`
	City             string  `json:"city" jsonschema:"description=所在城市"`
	Description      string  `json:"description" jsonschema:"description=面向用户展示的地点介绍、推荐亮点或游玩说明"`
	AmapPOIID        string  `json:"amap_poi_id" jsonschema:"description=高德POI ID"`
	VisitOrder       int     `json:"visit_order" jsonschema:"required,description=当天访问顺序"`
	StartTime        string  `json:"start_time" jsonschema:"description=预计到达时间 HH:MM"`
	EndTime          string  `json:"end_time" jsonschema:"description=预计离开时间 HH:MM"`
	Duration         int     `json:"duration" jsonschema:"description=停留时长 分钟"`
	IsMainStop       bool    `json:"is_main_stop" jsonschema:"description=是否主停留点"`
	IsOptional       bool    `json:"is_optional" jsonschema:"description=是否可跳过"`
	IsRainyDayBackup bool    `json:"is_rainy_day_backup" jsonschema:"description=是否雨天备选"`
	Notes            string  `json:"notes" jsonschema:"description=备注"`
	VerifiedBy       string  `json:"verified_by" jsonschema:"description=验证来源"`
	EstimatedCost    float64 `json:"estimated_cost" jsonschema:"description=预估费用"`
}

type UpsertPOIToDayOutput struct {
	POIID   string `json:"poi_id"`
	Success bool   `json:"success"`
}

func newUpsertPOIToDayTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in UpsertPOIToDayInput) (UpsertPOIToDayOutput, error) {
			if client == nil || !client.IsEnabled() {
				return UpsertPOIToDayOutput{Success: false}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			poi := graph.POIInput{
				ID: in.POIID, Name: in.Name, Type: in.Type, Lat: in.Lat, Lng: in.Lng,
				Address: in.Address, District: in.District, City: in.City,
				Description: in.Description,
				AmapPOIID:   in.AmapPOIID, VisitOrder: in.VisitOrder,
				StartTime: in.StartTime, EndTime: in.EndTime, Duration: in.Duration,
				IsMainStop: in.IsMainStop, IsOptional: in.IsOptional,
				IsRainyDayBackup: in.IsRainyDayBackup, Notes: in.Notes,
				VerifiedBy: in.VerifiedBy, EstimatedCost: in.EstimatedCost,
			}
			id, err := client.UpsertPOIToDay(ctx, in.DayID, poi)
			if err != nil {
				return UpsertPOIToDayOutput{Success: false}, err
			}
			return UpsertPOIToDayOutput{POIID: id, Success: true}, nil
		},
		function.WithName("upsert_poi_to_day"),
		function.WithDescription("[图数据库写入] 创建或更新POI节点，并关联到指定Day节点。地理验证结果即时写入图。"),
	)
}

// --- write_route ---

type WriteRouteInput struct {
	FromPOIID      string  `json:"from_poi_id" jsonschema:"required,description=起点POI ID"`
	ToPOIID        string  `json:"to_poi_id" jsonschema:"required,description=终点POI ID"`
	TransportMode  string  `json:"transport_mode" jsonschema:"required,description=交通方式 walking/driving/transit/bicycling"`
	DistanceMeters float64 `json:"distance_meters" jsonschema:"required,description=距离 米"`
	DurationMin    float64 `json:"duration_min" jsonschema:"required,description=耗时 分钟"`
	Polyline       string  `json:"polyline" jsonschema:"description=路线轨迹，经纬度串"`
	EstimatedCost  float64 `json:"estimated_cost" jsonschema:"description=预估费用"`
	Notes          string  `json:"notes" jsonschema:"description=备注"`
}

type WriteRouteOutput struct {
	Success bool `json:"success"`
}

func newWriteRouteTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in WriteRouteInput) (WriteRouteOutput, error) {
			if client == nil || !client.IsEnabled() {
				return WriteRouteOutput{Success: false}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			route := graph.RouteInput{
				FromPOIID: in.FromPOIID, ToPOIID: in.ToPOIID,
				TransportMode: in.TransportMode, DistanceMeters: in.DistanceMeters,
				DurationMin: in.DurationMin, Polyline: in.Polyline,
				EstimatedCost: in.EstimatedCost, Notes: in.Notes,
			}
			if err := client.WriteRoute(ctx, route); err != nil {
				return WriteRouteOutput{Success: false}, err
			}
			return WriteRouteOutput{Success: true}, nil
		},
		function.WithName("write_route"),
		function.WithDescription("[图数据库写入] 在两个POI之间创建路线关系，包含交通方式、距离、耗时。"),
	)
}

// --- write_guide_insight ---

type WriteGuideInsightInput struct {
	TripPlanID     string   `json:"trip_plan_id" jsonschema:"required,description=所属TripPlan ID"`
	Source         string   `json:"source" jsonschema:"required,description=来源 zhihu/bilibili"`
	SourceTitle    string   `json:"source_title" jsonschema:"description=原文标题"`
	SourceURL      string   `json:"source_url" jsonschema:"description=原文链接"`
	AuthorName     string   `json:"author_name" jsonschema:"description=作者"`
	ContentSummary string   `json:"content_summary" jsonschema:"required,description=内容摘要"`
	Keywords       []string `json:"keywords" jsonschema:"description=关键词"`
	Sentiment      string   `json:"sentiment" jsonschema:"description=情感倾向 positive/negative/neutral"`
	Status         string   `json:"status" jsonschema:"description=筛选状态 selected/review/raw/rejected"`
	Score          float64  `json:"score" jsonschema:"description=素材评分"`
	Reasons        []string `json:"reasons" jsonschema:"description=筛选理由"`
	MatchedPOIs    []string `json:"matched_pois" jsonschema:"description=关联的POI ID列表"`
	MatchedRegion  string   `json:"matched_region" jsonschema:"description=关联的区域"`
}

type WriteGuideInsightOutput struct {
	InsightID string `json:"insight_id"`
	Success   bool   `json:"success"`
}

func newWriteGuideInsightTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in WriteGuideInsightInput) (WriteGuideInsightOutput, error) {
			if client == nil || !client.IsEnabled() {
				return WriteGuideInsightOutput{Success: false}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			insight := graph.GuideInsightInput{
				Source: in.Source, SourceTitle: in.SourceTitle, SourceURL: in.SourceURL,
				AuthorName: in.AuthorName, ContentSummary: in.ContentSummary,
				Keywords: in.Keywords, Sentiment: in.Sentiment, Status: in.Status,
				Score: in.Score, Reasons: in.Reasons,
				MatchedPOIs: in.MatchedPOIs, MatchedRegion: in.MatchedRegion,
			}
			id, err := client.WriteGuideInsight(ctx, in.TripPlanID, insight)
			if err != nil {
				return WriteGuideInsightOutput{Success: false}, err
			}
			return WriteGuideInsightOutput{InsightID: id, Success: true}, nil
		},
		function.WithName("write_guide_insight"),
		function.WithDescription("[图数据库写入] 将从知乎/B站提炼的攻略洞察写入图，关联到TripPlan。素材不入上下文。"),
	)
}

// --- write_review_result ---

type ConstraintViolationSpec struct {
	Dimension string `json:"dimension" jsonschema:"required,description=约束维度"`
	Rule      string `json:"rule" jsonschema:"required,description=约束规则"`
	Actual    string `json:"actual" jsonschema:"required,description=实际情况"`
	Threshold string `json:"threshold" jsonschema:"required,description=阈值"`
	Severity  string `json:"severity" jsonschema:"required,description=严重程度 critical/major/minor"`
}

type WriteReviewResultInput struct {
	TargetNodeID         string                    `json:"target_node_id" jsonschema:"required,description=被审查节点ID"`
	Level                string                    `json:"level" jsonschema:"required,description=审查层级 TripPlan/Phase/Month/Week/Day/POI"`
	Dimension            string                    `json:"dimension" jsonschema:"required,description=审查维度"`
	Score                int                       `json:"score" jsonschema:"required,description=评分"`
	Passed               bool                      `json:"passed" jsonschema:"required,description=是否通过"`
	CriticalIssues       []string                  `json:"critical_issues" jsonschema:"description=关键问题"`
	Issues               []string                  `json:"issues" jsonschema:"description=一般问题"`
	Suggestions          []string                  `json:"suggestions" jsonschema:"description=改进建议"`
	Summary              string                    `json:"summary" jsonschema:"description=审查摘要"`
	ConstraintViolations []ConstraintViolationSpec `json:"constraint_violations" jsonschema:"description=约束违规详情"`
}

type WriteReviewResultOutput struct {
	ReviewID string `json:"review_id"`
	Success  bool   `json:"success"`
}

func newWriteReviewResultTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in WriteReviewResultInput) (WriteReviewResultOutput, error) {
			if client == nil || !client.IsEnabled() {
				return WriteReviewResultOutput{Success: false}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			violations := make([]graph.ConstraintViolation, len(in.ConstraintViolations))
			for i, v := range in.ConstraintViolations {
				violations[i] = graph.ConstraintViolation{
					Dimension: v.Dimension, Rule: v.Rule,
					Actual: v.Actual, Threshold: v.Threshold, Severity: v.Severity,
				}
			}
			review := graph.ReviewInput{
				Level: in.Level, Dimension: in.Dimension, Score: in.Score,
				Passed: in.Passed, CriticalIssues: in.CriticalIssues,
				Issues: in.Issues, Suggestions: in.Suggestions, Summary: in.Summary,
				ConstraintViolations: violations,
			}
			id, err := client.WriteReviewResult(ctx, in.TargetNodeID, review)
			if err != nil {
				return WriteReviewResultOutput{Success: false}, err
			}
			return WriteReviewResultOutput{ReviewID: id, Success: true}, nil
		},
		function.WithName("write_review_result"),
		function.WithDescription("[图数据库写入] 将审查结果写入图，关联到被审查节点。支持所有层级。"),
	)
}

// --- update_node ---

type UpdateNodeInput struct {
	NodeID     string         `json:"node_id" jsonschema:"required,description=节点ID"`
	Properties map[string]any `json:"properties" jsonschema:"required,description=要更新的属性"`
}

type UpdateNodeOutput struct {
	Success bool `json:"success"`
}

func newUpdateNodeTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in UpdateNodeInput) (UpdateNodeOutput, error) {
			if client == nil || !client.IsEnabled() {
				return UpdateNodeOutput{Success: false}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			if err := client.UpdateNode(ctx, in.NodeID, in.Properties); err != nil {
				return UpdateNodeOutput{Success: false}, err
			}
			return UpdateNodeOutput{Success: true}, nil
		},
		function.WithName("update_node"),
		function.WithDescription("[图数据库写入] 更新任意节点的属性。用于修改状态、主题、备注等。"),
	)
}

// --- write_climate_data ---

type WriteClimateDataInput struct {
	Region             string  `json:"region" jsonschema:"required,description=区域名称"`
	Month              int     `json:"month" jsonschema:"required,description=月份 1-12"`
	AvgHighTemp        float64 `json:"avg_high_temp" jsonschema:"required,description=平均最高温 °C"`
	AvgLowTemp         float64 `json:"avg_low_temp" jsonschema:"required,description=平均最低温 °C"`
	Precipitation      float64 `json:"precipitation" jsonschema:"description=降水量 mm"`
	Humidity           float64 `json:"humidity" jsonschema:"description=湿度 %%"`
	RainyDays          int     `json:"rainy_days" jsonschema:"description=雨天数"`
	SunriseTime        string  `json:"sunrise_time" jsonschema:"description=日出时间 HH:MM"`
	SunsetTime         string  `json:"sunset_time" jsonschema:"description=日落时间 HH:MM"`
	ExtremeWeatherRisk string  `json:"extreme_weather_risk" jsonschema:"description=极端天气风险 none/low/medium/high"`
}

type WriteClimateDataOutput struct {
	Success bool `json:"success"`
}

func newWriteClimateDataTool(client *graph.Client) tool.Tool {
	return function.NewFunctionTool(
		func(ctx context.Context, in WriteClimateDataInput) (WriteClimateDataOutput, error) {
			if client == nil || !client.IsEnabled() {
				return WriteClimateDataOutput{Success: false}, fmt.Errorf("Neo4j 图数据库不可用")
			}
			cd := graph.ClimateDataNode{
				Region: in.Region, Month: in.Month,
				AvgHighTemp: in.AvgHighTemp, AvgLowTemp: in.AvgLowTemp,
				Precipitation: in.Precipitation, Humidity: in.Humidity,
				RainyDays: in.RainyDays, SunriseTime: in.SunriseTime,
				SunsetTime: in.SunsetTime, ExtremeWeatherRisk: in.ExtremeWeatherRisk,
			}
			if err := client.WriteClimateData(ctx, cd); err != nil {
				return WriteClimateDataOutput{Success: false}, err
			}
			return WriteClimateDataOutput{Success: true}, nil
		},
		function.WithName("write_climate_data"),
		function.WithDescription("[图数据库写入] 写入某区域某月的历史气候数据，供审查时作为天气约束源。"),
	)
}

// marshalGraphData is a helper for tools that return complex graph data as JSON strings.
func marshalGraphData(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
