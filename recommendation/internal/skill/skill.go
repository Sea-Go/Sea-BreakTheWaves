// ============================================================================
// 该文件定义 internal/skill 包内部使用的 Skill 扩展类型。
//
// domain.Skill 是中央契约（定义于 domain 包，不可修改），仅含
// Name/Description/Tools/Resources 四个字段。为承载 SKILL.md 解析出的
// 分类信息（Category）与二开注入的执行函数（Execute），本包定义 Skill
// 类型嵌入 domain.Skill 并扩展两个字段。
//
// 二开扩展点：业务方可通过 Execute 字段注入自定义执行逻辑；
// 若不注入，Registry.Invoke 时将 Description 物化为 tool result
// （不污染 Prompt Cache 前缀）。
// ============================================================================

package skill

import (
	"context"

	"sea/internal/domain"
)

// Skill 扩展 domain.Skill，增加 Category 与 Execute 字段。
//
// 该类型用于 internal/skill 包内部：Loader 解析 SKILL.md 后构造该类型
// （含 Category），二开方可通过 Execute 注入执行逻辑。Registry 对外
// 暴露 domain.Skill（满足 domain.SkillRegistry interface 契约），
// 内部存储该扩展类型以保留 Category 与 Execute。
//
// 二开扩展点：
//   - 注册自定义 Skill：通过 Registry.RegisterSkill 注入含 Execute 的 Skill
//   - 替换 Skill：放置同名技能目录覆盖默认实现
type Skill struct {
	domain.Skill // 嵌入 domain.Skill（Name/Description/Tools/Resources）

	// Category Skill 分类（recall/rank/quality/search/graph/profile/
	// channel/event/explain/eval/tool/obs 共 12 类）。
	Category string

	// Execute Skill 执行函数，二开点。
	// 若为 nil，Registry.Invoke 将 Description 物化为 tool result 返回
	// （不污染 Prompt Cache 前缀）。
	Execute func(ctx context.Context, input domain.SkillInput) (domain.SkillOutput, error)
}

// SkillManifest Skill 清单，承载批量加载结果。
type SkillManifest struct {
	// Skills Skill 列表。
	Skills []Skill
}
