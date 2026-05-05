package tools

import (
	"context"
	"errors"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

type AmapPOIKeywordSearchInput struct {
	Keywords   string `json:"keywords,omitempty" jsonschema:"description=搜索关键词，例如 景点、餐厅、酒店、博物馆；keywords 和 types 至少填写一个"`
	Types      string `json:"types,omitempty" jsonschema:"description=POI 类型编码或类型名称，多个用 | 分隔；keywords 和 types 至少填写一个"`
	City       string `json:"city,omitempty" jsonschema:"description=城市名、中文、中文全拼、citycode 或 adcode"`
	CityLimit  bool   `json:"citylimit,omitempty" jsonschema:"description=是否仅返回指定城市数据"`
	Children   int    `json:"children,omitempty" jsonschema:"description=是否按照层级展示子 POI，0 不展示，1 展示"`
	Offset     int    `json:"offset,omitempty" jsonschema:"description=每页记录数，默认 20，建议不超过 25"`
	Page       int    `json:"page,omitempty" jsonschema:"description=当前页码，默认 1"`
	Extensions string `json:"extensions,omitempty" jsonschema:"description=返回结果控制，base 或 all，默认 base"`
}

func newAmapPOIKeywordSearchTool(runtime *amapRuntime) tool.Tool {
	return function.NewFunctionTool(runtime.POIKeywordSearch,
		function.WithName("amap_poi_keyword_search"),
		function.WithDescription("高德关键字搜索。用于按关键词或类型搜索旅游候选 POI，例如景点、餐厅、酒店、商圈、博物馆。"),
	)
}

func (r *amapRuntime) POIKeywordSearch(ctx context.Context, in AmapPOIKeywordSearchInput) (AmapResponse, error) {
	if strings.TrimSpace(in.Keywords) == "" && strings.TrimSpace(in.Types) == "" {
		return AmapResponse{OK: false, Endpoint: "/place/text"}, errors.New("keywords 和 types 至少填写一个")
	}
	q := newValues()
	putString(q, "keywords", in.Keywords)
	putString(q, "types", in.Types)
	putString(q, "city", in.City)
	putBool(q, "citylimit", in.CityLimit)
	putInt(q, "children", in.Children)
	putInt(q, "offset", in.Offset)
	putInt(q, "page", in.Page)
	putString(q, "extensions", in.Extensions)
	return r.get(ctx, "/place/text", q, true)
}
