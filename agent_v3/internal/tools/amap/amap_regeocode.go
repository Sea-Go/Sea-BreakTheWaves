package amap

import (
	"context"
	"errors"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

type AmapRegeoInput struct {
	Location   string `json:"location" jsonschema:"description=坐标，经度,纬度，例如 116.481488,39.990464"`
	PoiType    string `json:"poitype,omitempty" jsonschema:"description=逆地理附近 POI 类型，多个用 | 分隔"`
	Radius     int    `json:"radius,omitempty" jsonschema:"description=搜索半径，单位米，默认 1000"`
	Extensions string `json:"extensions,omitempty" jsonschema:"description=返回结果控制，base 或 all，默认 base"`
	RoadLevel  int    `json:"roadlevel,omitempty" jsonschema:"description=道路等级，0 显示所有道路，1 过滤非主干道路"`
	HomeOrCorp int    `json:"homeorcorp,omitempty" jsonschema:"description=返回附近 POI 优先策略，0 不干扰，1 居家，2 公司"`
}

func newAmapRegeocodeTool(runtime *amapRuntime) tool.Tool {
	return function.NewFunctionTool(runtime.Regeo,
		function.WithName("amap_regeocode"),
		function.WithDescription("高德逆地理编码。用于把经纬度转换为地址、行政区、附近 POI/AOI 等位置解释信息。"),
	)
}

func (r *amapRuntime) Regeo(ctx context.Context, in AmapRegeoInput) (AmapResponse, error) {
	if strings.TrimSpace(in.Location) == "" {
		return AmapResponse{OK: false, Endpoint: "/geocode/regeo"}, errors.New("location 不能为空")
	}
	q := newValues()
	putString(q, "location", in.Location)
	putString(q, "poitype", in.PoiType)
	putInt(q, "radius", in.Radius)
	putString(q, "extensions", in.Extensions)
	putInt(q, "roadlevel", in.RoadLevel)
	putInt(q, "homeorcorp", in.HomeOrCorp)
	return r.get(ctx, "/geocode/regeo", q, true)
}
