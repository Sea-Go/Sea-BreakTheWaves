package amap

import (
	"context"

	"agent_v3/internal/config"

	"trpc.group/trpc-go/trpc-agent-go/tool"
)

type AmapToolSet struct {
	runtime *amapRuntime
	tools   []tool.Tool
}

func NewDefaultAmapToolSet() *AmapToolSet {
	return NewAmapToolSet(config.Cfg.Amap)
}

func NewAmapToolSet(cfg config.AmapConfig) *AmapToolSet {
	runtime := newAmapRuntime(cfg)
	return &AmapToolSet{
		runtime: runtime,
		tools:   newAmapTools(runtime),
	}
}

func (s *AmapToolSet) Tools(context.Context) []tool.Tool {
	return append([]tool.Tool(nil), s.tools...)
}

func (s *AmapToolSet) Close() error {
	return nil
}

func (s *AmapToolSet) Name() string {
	return "amap"
}

func NewDefaultAmapTools() []tool.Tool {
	return NewAmapTools(config.Cfg.Amap)
}

func NewAmapTools(cfg config.AmapConfig) []tool.Tool {
	return newAmapTools(newAmapRuntime(cfg))
}

func newAmapTools(runtime *amapRuntime) []tool.Tool {
	return []tool.Tool{
		newAmapPOIKeywordSearchTool(runtime),
		newAmapPOIAroundSearchTool(runtime),
		newAmapPOIDetailTool(runtime),
		newAmapInputTipsTool(runtime),
		newAmapGeocodeTool(runtime),
		newAmapRegeocodeTool(runtime),
		newAmapDistanceTool(runtime),
		newAmapWalkingRouteTool(runtime),
		newAmapTransitRouteTool(runtime),
		newAmapDrivingRouteTool(runtime),
		newAmapBicyclingRouteTool(runtime),
		newAmapDistrictSearchTool(runtime),
		newAmapIPLocationTool(runtime),
		newAmapStaticMapTool(runtime),
	}
}
