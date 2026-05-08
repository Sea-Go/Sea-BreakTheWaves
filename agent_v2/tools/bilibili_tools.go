package tools

import (
	"context"

	"agent_v2/config"

	"trpc.group/trpc-go/trpc-agent-go/tool"
)

type BilibiliToolSet struct {
	runtime *bilibiliRuntime
	tools   []tool.Tool
}

func NewDefaultBilibiliToolSet() *BilibiliToolSet {
	return NewBilibiliToolSet(config.Cfg.Bilibili)
}

func NewBilibiliToolSet(cfg config.BilibiliConfig) *BilibiliToolSet {
	runtime := newBilibiliRuntime(cfg)
	return &BilibiliToolSet{
		runtime: runtime,
		tools:   newBilibiliTools(runtime),
	}
}

func (s *BilibiliToolSet) Tools(context.Context) []tool.Tool {
	return append([]tool.Tool(nil), s.tools...)
}

func (s *BilibiliToolSet) Close() error {
	return nil
}

func (s *BilibiliToolSet) Name() string {
	return "bilibili"
}

func NewDefaultBilibiliTools() []tool.Tool {
	return NewBilibiliTools(config.Cfg.Bilibili)
}

func NewBilibiliTools(cfg config.BilibiliConfig) []tool.Tool {
	return newBilibiliTools(newBilibiliRuntime(cfg))
}

func newBilibiliTools(runtime *bilibiliRuntime) []tool.Tool {
	return []tool.Tool{
		newBilibiliGuideMaterialTool(runtime),
	}
}
