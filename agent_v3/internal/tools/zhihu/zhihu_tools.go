package zhihu

import (
	"context"

	"agent_v3/internal/config"

	"trpc.group/trpc-go/trpc-agent-go/tool"
)

type ZhihuToolSet struct {
	runtime *zhihuRuntime
	tools   []tool.Tool
}

func NewDefaultZhihuToolSet() *ZhihuToolSet {
	return NewZhihuToolSet(config.Cfg.Zhihu)
}

func NewZhihuToolSet(cfg config.ZhihuConfig) *ZhihuToolSet {
	runtime := newZhihuRuntime(cfg)
	return &ZhihuToolSet{
		runtime: runtime,
		tools:   newZhihuTools(runtime),
	}
}

func (s *ZhihuToolSet) Tools(context.Context) []tool.Tool {
	return append([]tool.Tool(nil), s.tools...)
}

func (s *ZhihuToolSet) Close() error {
	return nil
}

func (s *ZhihuToolSet) Name() string {
	return "zhihu"
}

func NewDefaultZhihuTools() []tool.Tool {
	return NewZhihuTools(config.Cfg.Zhihu)
}

func NewZhihuTools(cfg config.ZhihuConfig) []tool.Tool {
	return newZhihuTools(newZhihuRuntime(cfg))
}

func newZhihuTools(runtime *zhihuRuntime) []tool.Tool {
	return []tool.Tool{
		newZhihuGuideMaterialTool(runtime),
	}
}
