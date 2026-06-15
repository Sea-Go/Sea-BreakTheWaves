package amap

import (
	"context"
	"errors"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

type AmapWalkingRouteInput struct {
	Origin      string `json:"origin" jsonschema:"description=起点坐标，经度,纬度"`
	Destination string `json:"destination" jsonschema:"description=终点坐标，经度,纬度"`
}

func newAmapWalkingRouteTool(runtime *amapRuntime) tool.Tool {
	return function.NewFunctionTool(runtime.WalkingRoute,
		function.WithName("amap_route_walking"),
		function.WithDescription("高德步行路径规划。适合景区内、商圈内、地铁站到景点等短距离步行方案。"),
	)
}

func (r *amapRuntime) WalkingRoute(ctx context.Context, in AmapWalkingRouteInput) (AmapResponse, error) {
	if strings.TrimSpace(in.Origin) == "" || strings.TrimSpace(in.Destination) == "" {
		return AmapResponse{OK: false, Endpoint: "/direction/walking"}, errors.New("origin 和 destination 不能为空")
	}
	q := newValues()
	putString(q, "origin", in.Origin)
	putString(q, "destination", in.Destination)
	return r.get(ctx, "/direction/walking", q, true)
}
