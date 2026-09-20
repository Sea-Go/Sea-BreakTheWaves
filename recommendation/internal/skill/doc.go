// Package skill 实现 Skill 注册体系（50+ Skills 声明式二开）。
//
// 包职责:
//
// 该包实现 SkillRegistry interface（定义于 domain 包），遵循 Anthropic Agent
// Skills 规范：每个 Skill 由 SKILL.md（描述 + 触发条件 + 使用说明）+ 工具 +
// 资源组成。加载器扫描技能目录自动加载。所有能力以 Skill 清单声明，分 12 类
// 共 50+ Skill：召回（6）/排序（5）/质量（5）/搜索（8）/图谱（6）/画像（5）/
// 频道（5）/事件（3）/解释（2）/评估（4）/工具（6）/观测（4）。技能内容物化
// 到 tool result（不污染 Prompt Cache 前缀）。
//
// 核心 interface:
//   - SkillRegistry: Skill 注册 interface（定义于 domain 包）
//     Register/Get/List/Invoke
//   - Skill: Skill 描述契约（定义于 domain 包）
//   - Loader: Skill 加载器 interface，扫描目录加载 SKILL.md + 工具 + 资源
//
// 二开扩展点:
//   - 注册新 Skill: 业务方放置技能目录（SKILL.md + 工具 + 资源），框架自动加载
//   - 替换 Skill: 通过 SkillRegistry.Register 注入自定义实现
//   - 工具与 Skill 绑定: 新增 Skill 时绑定对应工具（internal/tool）
package skill
