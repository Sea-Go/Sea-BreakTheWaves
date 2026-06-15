package amap

import (
	"context"
	"errors"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

type AmapPOIAroundSearchInput struct {
	Location   string `json:"location" jsonschema:"description=中心点坐标，经度,纬度，例如 116.481488,39.990464"`
	Keywords   string `json:"keywords,omitempty" jsonschema:"description=搜索关键词，例如 餐厅、咖啡、亲子、地铁站"`
	Types      string `json:"types,omitempty" jsonschema:"description=POI 类型编码或类型名称，多个用 | 分隔"`
	City       string `json:"city,omitempty" jsonschema:"description=城市名、citycode 或 adcode"`
	Radius     int    `json:"radius,omitempty" jsonschema:"description=查询半径，单位米，默认 3000"`
	SortRule   string `json:"sortrule,omitempty" jsonschema:"description=排序规则，distance 或 weight，默认 distance"`
	Offset     int    `json:"offset,omitempty" jsonschema:"description=每页记录数，默认 20，建议不超过 25"`
	Page       int    `json:"page,omitempty" jsonschema:"description=当前页码，默认 1"`
	Extensions string `json:"extensions,omitempty" jsonschema:"description=返回结果控制，base 或 all，默认 base"`
}

func newAmapPOIAroundSearchTool(runtime *amapRuntime) tool.Tool {
	return function.NewFunctionTool(runtime.POIAroundSearch,
		function.WithName("amap_poi_around_search"),
		function.WithDescription("高德周边搜索。用于根据酒店、景点或用户坐标查询附近餐厅、咖啡、亲子设施、地铁站等 POI。"),
	)
}

func (r *amapRuntime) POIAroundSearch(ctx context.Context, in AmapPOIAroundSearchInput) (AmapResponse, error) {
	if strings.TrimSpace(in.Location) == "" {
		return AmapResponse{OK: false, Endpoint: "/place/around"}, errors.New("location 不能为空")
	}
	q := newValues()
	putString(q, "location", in.Location)
	putString(q, "keywords", in.Keywords)
	putString(q, "types", in.Types)
	putString(q, "city", in.City)
	putInt(q, "radius", in.Radius)
	putString(q, "sortrule", in.SortRule)
	putInt(q, "offset", in.Offset)
	putInt(q, "page", in.Page)
	putString(q, "extensions", in.Extensions)
	return r.get(ctx, "/place/around", q, true)
}
