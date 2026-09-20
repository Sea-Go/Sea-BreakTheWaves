---
name: example-activity
category: channel
description: 活动期间推荐二开示例
tools:
  - activity_recommend
  - activity_pool_refill
  - activity_rule_recall
inputs:
  - name: user_id
    type: string
    required: true
    description: 目标用户 ID
  - name: activity_id
    type: string
    required: true
    description: 活动 ID
  - name: intent
    type: string
    required: false
    default: activity
    description: 用户意图（activity/browse）
  - name: topk
    type: int
    required: false
    default: 20
    description: 返回候选数
outputs:
  - name: candidates
    type: list
    description: 活动期间推荐候选列表
  - name: activity_meta
    type: map
    description: 活动元信息（名称/时段/权重）
---

# example-activity

> 本 Skill 是**二开示例模板**，演示业务方如何在不改推荐主链路的前提下，为"活动期间推荐"这一垂直场景接入自定义召回 / 排序 / 池策略。业务方可照抄本目录结构，替换为自己的业务逻辑。

## 用途
在运营活动期间（如双 11、周年庆、垂类活动周），把用户路由到独立"活动频道"，按活动专属召回池 + 活动规则 + 活动权重做推荐，活动结束后下线该频道而不影响主推荐链路。典型场景：
- 活动期间主推活动相关文章 / 商品；
- 活动池与常规池隔离，避免活动内容污染日常推荐；
- 活动结束自动回退到默认频道。

本 Skill 串接：`channel.route`（路由到活动频道）→ `channel.ensure_pool`（确保活动池就绪）→ `activity_rule_recall`（活动规则召回）→ `activity_pool_refill`（活动池补充）→ `activity_recommend`（活动期推荐主入口）。

## 调用方式
Agent 通过 tool_call 调用，传入 `user_id` / `activity_id`。底层流程：
1. 调 `channel.route`（intent=activity）把请求路由到活动频道；
2. 调 `channel.ensure_pool` 确保活动频道池水位达标；
3. 调 `activity_rule_recall` 按活动规则（活动标签 / 活动作者 / 活动位文章）召回候选；
4. 候选不足时调 `activity_pool_refill` 补充；
5. 调 `activity_recommend` 把候选按活动权重（活动期 boost）排序，返回 `candidates` + `activity_meta`。

## 二开步骤说明

业务方二开完整流程分四步：**放置技能目录 → 实现工具 → 注册工具 → 绑定到 Skill**。

### 步骤 1：放置技能目录

在 `recommendation/skills/` 下创建业务方自己的技能目录，命名建议 `<业务前缀>-<场景>`，例如 `example-activity/`。目录内至少包含：

```
recommendation/skills/example-activity/
├── SKILL.md              # 本文件：Skill 元信息 + 用途 + 二开说明
└── example_activity.tool.go   # 工具实现（go 文件，可选多个）
```

约定：
- 目录名与 front matter 的 `name` 字段保持一致（`example-activity`）；
- `SKILL.md` 是 Skill 的唯一描述入口，Agent 通过它理解技能用途与入参；
- `.tool.go` 文件实现 `skillsys.Tool` 接口，是 Skill 声明的 `tools` 的具体落地。

### 步骤 2：实现工具

在 `example_activity.tool.go` 中实现 `skillsys.Tool` 接口（见 `skillsys/tool.go`）：

```go
package example_activity

import (
    "context"
    "encoding/json"
)

// ActivityRecommendTool 实现 activity_recommend 工具
type ActivityRecommendTool struct {
    // 注入业务依赖：活动配置中心、活动召回池、活动权重表等
}

func NewActivityRecommendTool() *ActivityRecommendTool {
    return &ActivityRecommendTool{}
}

func (t *ActivityRecommendTool) Name() string { return "activity_recommend" }
func (t *ActivityRecommendTool) Description() string {
    return "活动期间推荐主入口：按活动权重排序候选并返回。"
}
func (t *ActivityRecommendTool) Parameters() map[string]any {
    return map[string]any{
        "type": "object",
        "properties": map[string]any{
            "user_id":     map[string]any{"type": "string"},
            "activity_id": map[string]any{"type": "string"},
            "topk":        map[string]any{"type": "int"},
        },
        "required": []string{"user_id", "activity_id"},
    }
}

func (t *ActivityRecommendTool) Invoke(ctx context.Context, argsRaw json.RawMessage) (any, error) {
    // 1. 解析参数
    // 2. 调用业务逻辑（活动权重排序、活动池读取等）
    // 3. 返回结果（会被 Registry.MarshalResult 序列化为 JSON）
    return map[string]any{"candidates": []any{}, "activity_meta": map[string]any{}}, nil
}
```

参考已有实现：`skills/pool_manage/pool_manage.tool.go`、`skills/milvus_search/milvus_search.tool.go`。

### 步骤 3：注册工具

在 `skillsys/RegisterSkills.go` 的 `RegisterSkills` 函数中追加业务方工具注册：

```go
import (
    // ... 已有 import
    "sea/skills/example_activity"
)

func RegisterSkills(
    reg *Registry,
    // ... 已有依赖参数
) {
    // ... 已有注册逻辑

    // 活动期间推荐（二开示例）
    reg.Register(example_activity.NewActivityRecommendTool())
    reg.Register(example_activity.NewActivityPoolRefillTool())
    reg.Register(example_activity.NewActivityRuleRecallTool())
}
```

约定：
- 工具 `Name()` 必须全局唯一（OpenAI tool calling 用名匹配）；
- 注册顺序不影响功能，但建议同类工具相邻注册，便于维护；
- 业务方依赖（如活动配置中心）通过 `RegisterSkills` 的参数注入，避免在工具内全局单例。

### 步骤 4：绑定到 Skill

工具注册后，在 `SKILL.md` 的 front matter `tools` 列表中声明该 Skill 依赖的工具名（与工具 `Name()` 返回值一致）：

```yaml
tools:
  - activity_recommend
  - activity_pool_refill
  - activity_rule_recall
```

Agent 加载 `SKILL.md` 时会读取 `tools` 列表，在调用该 Skill 时只会暴露声明的工具给模型，保证工具集最小化、避免误调用。

## 业务方二开流程总结

1. **明确场景**：确定二开场景（如活动推荐 / 垂类频道 / 私域内容），判断是新建频道还是复用现有频道。
2. **建目录**：在 `recommendation/skills/<业务前缀>-<场景>/` 下建 `SKILL.md` + `.tool.go`。
3. **写 SKILL.md**：填 front matter（name / category / description / tools / inputs / outputs）+ body（用途 / 调用方式 / 二开扩展点）。
4. **实现工具**：在 `.tool.go` 中实现 `skillsys.Tool` 接口，注入业务依赖。
5. **注册工具**：在 `skillsys/RegisterSkills.go` 中 `reg.Register(...)`。
6. **绑定 Skill**：在 `SKILL.md` 的 `tools` 列表声明工具名。
7. **联调验证**：通过 `ChatTest/chat_cli.go` 或 `agent/reco_agent_test.go` 验证 Skill 能被 Agent 正确调用。
8. **观测上线**：通过 `obs.trace` / `obs.metric` 监控新 Skill 的调用频次、延迟、成本，确认无异常后全量。

## 二开扩展点
- 替换活动召回规则（实现自定义 `RuleRecaller`，按活动标签 / 活动作者 / 活动位文章召回）
- 接入活动权重动态下发（活动配置中心实时调 boost 系数）
- 活动池预热（活动开始前定时 `channel.ensure_pool` 把活动内容灌满）
- 活动结束自动下线（活动结束后调 `channel.register` 把活动频道置为 inactive，路由自动回退默认频道）
- 活动效果评估（活动结束后跑 `eval.run` 限定 channel=activity，对比活动期 vs 非活动期指标）
