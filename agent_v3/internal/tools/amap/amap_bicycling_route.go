package amap

import (
	"context"
	"errors"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

type AmapBicyclingRouteInput struct {
	Origin      string `json:"origin" jsonschema:"description=起点坐标，经度,纬度"`
	Destination string `json:"destination" jsonschema:"description=终点坐标，经度,纬度"`
}

func newAmapBicyclingRouteTool(runtime *amapRuntime) tool.Tool {
	return function.NewFunctionTool(runtime.BicyclingRoute,
		function.WithName("amap_route_bicycling"),
		function.WithDescription("高德骑行路径规划。适合城市骑行、景区骑行、慢游路线。"),
	)
}

func (r *amapRuntime) BicyclingRoute(ctx context.Context, in AmapBicyclingRouteInput) (AmapResponse, error) {
	if strings.TrimSpace(in.Origin) == "" || strings.TrimSpace(in.Destination) == "" {
		return AmapResponse{OK: false, Endpoint: "/direction/bicycling"}, errors.New("origin 和 destination 不能为空")
	}
	q := newValues()
	putString(q, "origin", in.Origin)
	putString(q, "destination", in.Destination)
	return r.get(ctx, "/direction/bicycling", q, true)
}
