package tools

import (
	"context"
	"errors"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

type AmapInputTipsInput struct {
	Keywords  string `json:"keywords" jsonschema:"description=用户正在输入的关键词，例如 外滩、迪士尼、成都熊猫"`
	Type      string `json:"type,omitempty" jsonschema:"description=POI 类型编码或类型名称，多个用 | 分隔"`
	Location  string `json:"location,omitempty" jsonschema:"description=当前位置坐标，经度,纬度，用于提升提示相关性"`
	City      string `json:"city,omitempty" jsonschema:"description=城市名、citycode 或 adcode"`
	CityLimit bool   `json:"citylimit,omitempty" jsonschema:"description=是否仅返回指定城市数据"`
	Datatype  string `json:"datatype,omitempty" jsonschema:"description=返回数据类型，all 或 poi，默认 all"`
}

func newAmapInputTipsTool(runtime *amapRuntime) tool.Tool {
	return function.NewFunctionTool(runtime.InputTips,
		function.WithName("amap_input_tips"),
		function.WithDescription("高德输入提示。用于用户输入地名时做自动补全、纠错并获取候选 POI ID。"),
	)
}

func (r *amapRuntime) InputTips(ctx context.Context, in AmapInputTipsInput) (AmapResponse, error) {
	if strings.TrimSpace(in.Keywords) == "" {
		return AmapResponse{OK: false, Endpoint: "/assistant/inputtips"}, errors.New("keywords 不能为空")
	}
	q := newValues()
	putString(q, "keywords", in.Keywords)
	putString(q, "type", in.Type)
	putString(q, "location", in.Location)
	putString(q, "city", in.City)
	putBool(q, "citylimit", in.CityLimit)
	putString(q, "datatype", in.Datatype)
	return r.get(ctx, "/assistant/inputtips", q, true)
}
