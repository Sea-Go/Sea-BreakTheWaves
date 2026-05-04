package tools

import (
	"context"
	"errors"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

type AmapDrivingRouteInput struct {
	Origin          string   `json:"origin" jsonschema:"description=起点坐标，经度,纬度"`
	Destination     string   `json:"destination" jsonschema:"description=终点坐标，经度,纬度"`
	OriginID        string   `json:"originid,omitempty" jsonschema:"description=起点 POI ID，可提升路线准确性"`
	DestinationID   string   `json:"destinationid,omitempty" jsonschema:"description=终点 POI ID，可提升路线准确性"`
	Strategy        int      `json:"strategy,omitempty" jsonschema:"description=驾车路线策略，默认 0"`
	Waypoints       []string `json:"waypoints,omitempty" jsonschema:"description=途经点坐标列表，每个值是 经度,纬度"`
	AvoidPolygons   []string `json:"avoidpolygons,omitempty" jsonschema:"description=避让区域，多边形坐标串列表"`
	AvoidRoad       string   `json:"avoidroad,omitempty" jsonschema:"description=避让道路名称"`
	Province        string   `json:"province,omitempty" jsonschema:"description=车牌省份简称，用于限行判断"`
	Number          string   `json:"number,omitempty" jsonschema:"description=除省份外的车牌号码，用于限行判断"`
	CarType         int      `json:"cartype,omitempty" jsonschema:"description=车辆类型，默认 0 普通燃油汽车"`
	Ferry           int      `json:"ferry,omitempty" jsonschema:"description=是否使用轮渡，0 可以，1 不可以"`
	RoadAggregation bool     `json:"roadaggregation,omitempty" jsonschema:"description=是否将连续相同名称路段聚合"`
	Extensions      string   `json:"extensions,omitempty" jsonschema:"description=返回结果控制，base 或 all，默认 base"`
}

func newAmapDrivingRouteTool(runtime *amapRuntime) tool.Tool {
	return function.NewFunctionTool(runtime.DrivingRoute,
		function.WithName("amap_route_driving"),
		function.WithDescription("高德驾车路径规划。适合自驾游、包车游、郊区景点串联和途经点路线。"),
	)
}

func (r *amapRuntime) DrivingRoute(ctx context.Context, in AmapDrivingRouteInput) (AmapResponse, error) {
	if strings.TrimSpace(in.Origin) == "" || strings.TrimSpace(in.Destination) == "" {
		return AmapResponse{OK: false, Endpoint: "/direction/driving"}, errors.New("origin 和 destination 不能为空")
	}
	q := newValues()
	putString(q, "origin", in.Origin)
	putString(q, "destination", in.Destination)
	putString(q, "originid", in.OriginID)
	putString(q, "destinationid", in.DestinationID)
	putInt(q, "strategy", in.Strategy)
	putJoined(q, "waypoints", in.Waypoints, ";")
	putJoined(q, "avoidpolygons", in.AvoidPolygons, "|")
	putString(q, "avoidroad", in.AvoidRoad)
	putString(q, "province", in.Province)
	putString(q, "number", in.Number)
	putInt(q, "cartype", in.CarType)
	putInt(q, "ferry", in.Ferry)
	if in.RoadAggregation {
		q.Set("roadaggregation", "true")
	}
	putString(q, "extensions", in.Extensions)
	return r.get(ctx, "/direction/driving", q, true)
}
