package skillsys

import (
	"context"
	"encoding/json"
)

// Tool 是“可被大模型调用”的最小单元。
// 约束：
// - Name 必须全局唯一（用于 OpenAI tool calling）。
// - Parameters 必须是 JSON Schema（OpenAI function 参数格式）。
// - Invoke 输入 argsRaw 为模型给出的 JSON 字符串（你需要自行解析）。
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Invoke(ctx context.Context, argsRaw json.RawMessage) (any, error)
}

// MarshalResult 工具输出统一序列化为 JSON，作为 tool message 的 content。
func MarshalResult(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
