package skillsys

import (
	"sort"

	"github.com/openai/openai-go/v3"
	"trpc.group/trpc-go/trpc-agent-go/tool"
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
		decl := t.Declaration()
		res = append(res, openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name:        decl.Name,
			Description: openai.String(decl.Description),
			Parameters:  openai.FunctionParameters(schemaToMap(decl.InputSchema)),
		}))
	}
	return res
}

// schemaToMap 将 tool.Schema 转换为 map[string]any（OpenAI 需要的格式）。
func schemaToMap(s *tool.Schema) map[string]any {
	if s == nil {
		return nil
	}
	m := map[string]any{
		"type": s.Type,
	}
	if s.Description != "" {
		m["description"] = s.Description
	}
	if len(s.Required) > 0 {
		m["required"] = s.Required
	}
	if len(s.Properties) > 0 {
		props := make(map[string]any, len(s.Properties))
		for k, v := range s.Properties {
			props[k] = schemaToMap(v)
		}
		m["properties"] = props
	}
	if s.Items != nil {
		m["items"] = schemaToMap(s.Items)
	}
	if s.AdditionalProperties != nil {
		m["additionalProperties"] = s.AdditionalProperties
	}
	if s.Default != nil {
		m["default"] = s.Default
	}
	if len(s.Enum) > 0 {
		m["enum"] = s.Enum
	}
	return m
}
