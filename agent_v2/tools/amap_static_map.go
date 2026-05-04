package tools

import (
	"context"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

type AmapStaticMapInput struct {
	Location string `json:"location,omitempty" jsonschema:"description=地图中心点坐标，经度,纬度"`
	Zoom     int    `json:"zoom,omitempty" jsonschema:"description=缩放级别，通常 1-17"`
	Size     string `json:"size,omitempty" jsonschema:"description=图片尺寸，宽*高，例如 750*500"`
	Scale    int    `json:"scale,omitempty" jsonschema:"description=普通图 1，高清图 2"`
	Markers  string `json:"markers,omitempty" jsonschema:"description=标注规则，按高德 staticmap markers 参数格式传入"`
	Labels   string `json:"labels,omitempty" jsonschema:"description=标签规则，按高德 staticmap labels 参数格式传入"`
	Paths    string `json:"paths,omitempty" jsonschema:"description=路线或区域规则，按高德 staticmap paths 参数格式传入"`
	Traffic  bool   `json:"traffic,omitempty" jsonschema:"description=是否叠加实时路况"`
	Validate bool   `json:"validate,omitempty" jsonschema:"description=是否实际请求静态图 URL 验证状态码和内容类型；默认 false 只返回脱敏 URL"`
}

func newAmapStaticMapTool(runtime *amapRuntime) tool.Tool {
	return function.NewFunctionTool(runtime.StaticMap,
		function.WithName("amap_static_map"),
		function.WithDescription("高德静态地图。用于生成行程路线或景点分布图的脱敏 URL，可选择实际请求验证图片状态。"),
	)
}

func (r *amapRuntime) StaticMap(ctx context.Context, in AmapStaticMapInput) (AmapStaticMapResult, error) {
	return r.staticMap(ctx, in)
}
