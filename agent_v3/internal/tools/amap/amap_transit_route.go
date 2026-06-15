package amap

import (
	"context"
	"errors"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

type AmapTransitRouteInput struct {
	Origin      string `json:"origin" jsonschema:"description=起点坐标，经度,纬度"`
	Destination string `json:"destination" jsonschema:"description=终点坐标，经度,纬度"`
	City        string `json:"city" jsonschema:"description=起点所在城市，支持城市名、citycode 或 adcode"`
	CityD       string `json:"cityd,omitempty" jsonschema:"description=终点所在城市，跨城公交时填写"`
	Strategy    int    `json:"strategy,omitempty" jsonschema:"description=公交换乘策略，默认 0"`
	NightFlag   int    `json:"nightflag,omitempty" jsonschema:"description=是否计算夜班车，0 否，1 是"`
	Date        string `json:"date,omitempty" jsonschema:"description=出发日期，格式 YYYY-MM-DD"`
	Time        string `json:"time,omitempty" jsonschema:"description=出发时间，格式 HH:mm"`
	Extensions  string `json:"extensions,omitempty" jsonschema:"description=返回结果控制，base 或 all，默认 base"`
}

func newAmapTransitRouteTool(runtime *amapRuntime) tool.Tool {
	return function.NewFunctionTool(runtime.TransitRoute,
		function.WithName("amap_route_transit"),
		function.WithDescription("高德公交路径规划。适合城市自由行中的公交、地铁等公共交通换乘方案。"),
	)
}

func (r *amapRuntime) TransitRoute(ctx context.Context, in AmapTransitRouteInput) (AmapResponse, error) {
	if strings.TrimSpace(in.Origin) == "" || strings.TrimSpace(in.Destination) == "" {
		return AmapResponse{OK: false, Endpoint: "/direction/transit/integrated"}, errors.New("origin 和 destination 不能为空")
	}
	if strings.TrimSpace(in.City) == "" {
		return AmapResponse{OK: false, Endpoint: "/direction/transit/integrated"}, errors.New("city 不能为空")
	}
	q := newValues()
	putString(q, "origin", in.Origin)
	putString(q, "destination", in.Destination)
	putString(q, "city", in.City)
	putString(q, "cityd", in.CityD)
	putInt(q, "strategy", in.Strategy)
	putInt(q, "nightflag", in.NightFlag)
	putString(q, "date", in.Date)
	putString(q, "time", in.Time)
	putString(q, "extensions", in.Extensions)
	return r.get(ctx, "/direction/transit/integrated", q, true)
}
