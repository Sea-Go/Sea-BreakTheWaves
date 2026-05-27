package skillsys

import (
	"encoding/json"

	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// Tool 是 trpc-agent-go 的 tool.CallableTool 别名，表示可被调用的工具。
// 所有工具改用 function.NewFunctionTool[I,O] 创建，编译期类型安全。
type Tool = tool.CallableTool

// MarshalResult 工具输出统一序列化为 JSON，作为 tool message 的 content。
func MarshalResult(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
