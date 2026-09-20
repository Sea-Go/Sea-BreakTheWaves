package skillsys

import (
	"sort"

	"github.com/openai/openai-go/v3"
)

// OpenAITools 把 Registry 中的 Tool 转成 OpenAI tool definitions。
func (r *Registry) OpenAITools() []openai.ChatCompletionToolUnionParam {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names) // 让工具顺序稳定，便于调试

	res := make([]openai.ChatCompletionToolUnionParam, 0, len(names))
	for _, name := range names {
		t := r.tools[name]
		res = append(res, openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			// Name 是必填字段（string），不要用 openai.String 包装。
			Name:        t.Name(),
			Description: openai.String(t.Description()),
			Parameters:  openai.FunctionParameters(t.Parameters()),
		}))
	}
	return res
}
