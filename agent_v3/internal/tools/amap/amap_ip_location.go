package amap

import (
	"context"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

type AmapIPLocationInput struct {
	IP string `json:"ip,omitempty" jsonschema:"description=待定位 IP；为空时高德会尝试使用请求来源 IP"`
}

func newAmapIPLocationTool(runtime *amapRuntime) tool.Tool {
	return function.NewFunctionTool(runtime.IPLocation,
		function.WithName("amap_ip_location"),
		function.WithDescription("高德 IP 定位。用于用户未填写城市时粗略判断默认城市，不适合精确定位。"),
	)
}

func (r *amapRuntime) IPLocation(ctx context.Context, in AmapIPLocationInput) (AmapResponse, error) {
	q := newValues()
	putString(q, "ip", in.IP)
	return r.get(ctx, "/ip", q, true)
}
