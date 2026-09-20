// Package tool 将推荐系统能力封装为 trpc-agent-go 的 function tool。
//
// 包职责:
//
// 该包把召回/排序/rerank/质量/搜索/图谱/画像/频道/事件等能力包装为
// trpc-agent-go 的 function tool（tool.Tool / tool/function），供多 Agent
// 在 ReAct 循环中驱动调用。工具与 Skill 清单（internal/skill）一一对应，
// 工具结果物化到 tool result，不污染 Prompt Cache 前缀。
//
// 核心 interface:
//   - Tool: 工具契约（由 trpc-agent-go tool.Tool 适配，本包提供构造器）
//   - ToolRegistry: 工具注册表，按名称注册与获取
//
// 二开扩展点:
//   - 注册新 tool: 实现 tool.Tool 并通过 ToolRegistry.Register 注入
//   - 替换 tool: 通过依赖注入替换默认 tool 实现（如自研 rerank tool）
//   - 工具与 Skill 绑定: 新增 tool 时同步在 internal/skill 声明对应 Skill
package tool
