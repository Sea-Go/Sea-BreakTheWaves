package tools

import (
	"context"
	"errors"
	"strings"

	"agent_v2/config"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

type AmapGeocodeInput struct {
	Address string `json:"address" jsonschema:"description=结构化地址，例如 上海外滩、成都宽窄巷子"`
	City    string `json:"city,omitempty" jsonschema:"description=指定查询城市，可提高匹配精度"`
}

func newAmapGeocodeTool(runtime *amapRuntime) tool.Tool {
	return function.NewFunctionTool(runtime.Geocode,
		function.WithName("amap_geocode"),
		function.WithDescription("高德地理编码。用于把结构化地址或地名转换为经纬度坐标。"),
	)
}

func (r *amapRuntime) Geocode(ctx context.Context, in AmapGeocodeInput) (AmapResponse, error) {
	if strings.TrimSpace(in.Address) == "" {
		return AmapResponse{OK: false, Endpoint: "/geocode/geo"}, errors.New("address 不能为空")
	}
	q := newValues()
	putString(q, "address", in.Address)
	putString(q, "city", in.City)
	return r.get(ctx, "/geocode/geo", q, true)
}

func GeocodeAddress(ctx context.Context, cfg config.AmapConfig, in AmapGeocodeInput) (AmapResponse, error) {
	return newAmapRuntime(cfg).Geocode(ctx, in)
}
