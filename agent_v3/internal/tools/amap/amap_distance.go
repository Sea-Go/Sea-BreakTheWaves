package amap

import (
	"context"
	"errors"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

type AmapDistanceInput struct {
	Origins     []string `json:"origins" jsonschema:"description=起点坐标列表，每个值是 经度,纬度，例如 116.481488,39.990464"`
	Destination string   `json:"destination" jsonschema:"description=终点坐标，经度,纬度"`
	Type        int      `json:"type,omitempty" jsonschema:"description=测量类型，0 直线距离，1 驾车导航距离，3 步行规划距离，默认 0"`
}

func newAmapDistanceTool(runtime *amapRuntime) tool.Tool {
	return function.NewFunctionTool(runtime.Distance,
		function.WithName("amap_distance"),
		function.WithDescription("高德距离测量。用于快速估算多个景点到目标点的直线、驾车或步行距离，辅助行程排序。"),
	)
}

func (r *amapRuntime) Distance(ctx context.Context, in AmapDistanceInput) (AmapResponse, error) {
	if len(in.Origins) == 0 {
		return AmapResponse{OK: false, Endpoint: "/distance"}, errors.New("origins 不能为空")
	}
	if strings.TrimSpace(in.Destination) == "" {
		return AmapResponse{OK: false, Endpoint: "/distance"}, errors.New("destination 不能为空")
	}
	q := newValues()
	putJoined(q, "origins", in.Origins, "|")
	putString(q, "destination", in.Destination)
	putInt(q, "type", in.Type)
	return r.get(ctx, "/distance", q, true)
}
