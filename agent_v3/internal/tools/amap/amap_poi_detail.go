package amap

import (
	"context"
	"errors"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

type AmapPOIDetailInput struct {
	ID         string `json:"id" jsonschema:"description=高德 POI ID"`
	Extensions string `json:"extensions,omitempty" jsonschema:"description=返回结果控制，base 或 all，默认 base"`
}

func newAmapPOIDetailTool(runtime *amapRuntime) tool.Tool {
	return function.NewFunctionTool(runtime.POIDetail,
		function.WithName("amap_poi_detail"),
		function.WithDescription("高德 POI ID 查询。用于根据搜索或输入提示得到的 POI ID 查询地点详情。"),
	)
}

func (r *amapRuntime) POIDetail(ctx context.Context, in AmapPOIDetailInput) (AmapResponse, error) {
	if strings.TrimSpace(in.ID) == "" {
		return AmapResponse{OK: false, Endpoint: "/place/detail"}, errors.New("id 不能为空")
	}
	q := newValues()
	putString(q, "id", in.ID)
	putString(q, "extensions", in.Extensions)
	return r.get(ctx, "/place/detail", q, true)
}
