package amap

import (
	"context"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

type AmapDistrictSearchInput struct {
	Keywords    string `json:"keywords,omitempty" jsonschema:"description=行政区关键词，支持城市名、区县名或 adcode"`
	Subdistrict int    `json:"subdistrict,omitempty" jsonschema:"description=下级行政区级数，0 不返回下级，1 返回一级下级，默认 1"`
	Page        int    `json:"page,omitempty" jsonschema:"description=页码，默认 1"`
	Offset      int    `json:"offset,omitempty" jsonschema:"description=每页记录数，默认 20"`
	Extensions  string `json:"extensions,omitempty" jsonschema:"description=返回结果控制，base 或 all，默认 base"`
	Filter      string `json:"filter,omitempty" jsonschema:"description=行政区划过滤条件，例如 100000"`
}

func newAmapDistrictSearchTool(runtime *amapRuntime) tool.Tool {
	return function.NewFunctionTool(runtime.DistrictSearch,
		function.WithName("amap_district_search"),
		function.WithDescription("高德行政区域查询。用于获取城市、区县 adcode，限定搜索范围、查天气或做城市标准化。"),
	)
}

func (r *amapRuntime) DistrictSearch(ctx context.Context, in AmapDistrictSearchInput) (AmapResponse, error) {
	q := newValues()
	putString(q, "keywords", in.Keywords)
	putInt(q, "subdistrict", in.Subdistrict)
	putInt(q, "page", in.Page)
	putInt(q, "offset", in.Offset)
	putString(q, "extensions", in.Extensions)
	putString(q, "filter", in.Filter)
	return r.get(ctx, "/config/district", q, true)
}
