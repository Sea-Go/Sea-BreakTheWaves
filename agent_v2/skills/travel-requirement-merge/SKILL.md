---
name: travel-requirement-merge
description: 合并用户追问回复到旅行需求快照。Use when the user replies to follow-up questions about their travel requirements; parse natural language answers, merge into existing TravelRequirementSnapshot, and output SkillResult JSON.
---

# 旅行需求合并

## 目标

读取已有 TravelRequirementSnapshot、上一轮缺失字段、上一轮追问和用户新回复，输出一个增量 SkillResult JSON。只做需求合并、缺失判断、追问或默认补齐，不生成旅行方案，不调用地图、攻略、天气或图写入工具。

## 合法字段

`result.requirement` 只能包含 TravelRequirementSnapshot 字段：

- `destination_scope`
- `total_days`
- `start_date`
- `end_date`
- `start_city`
- `end_city`
- `budget_total`
- `budget_monthly`
- `transport_mode`
- `travel_style`
- `pace`
- `high_altitude_acceptance`
- `daily_driving_preference`
- `accommodation_style`
- `food_preference`
- `must_visit`
- `avoid_places`
- `special_constraints`
- `destination_anchors`

预算缺失在 `missing_fields` 中可写 `"budget"`，但补齐时必须写入 `budget_total` 或 `budget_monthly`。

## 合并规则

- 只输出本轮用户明确新增、修改或默认补齐的字段。
- 已有非空字段如果用户没有明确修改，不要重复输出，也不要追问。
- 不要用空字符串、空数组或 null 覆盖已有非空字段。
- 对短时城市游、街区探索、漫步、city walk 等语义，由 agent 根据已有快照和用户新回复自主判断合适的 `transport_mode`、`travel_style` 和 `special_constraints`；不要套用长线或自驾旅行默认。
- 结合上一轮追问理解短答；例如用户按顺序回答多个问题时，要把答案映射到对应缺失字段。
- 如果短答表达“无所谓、都行、随便、按你推荐”，不要把这些词当作城市、预算或风格；应在 `result.default_intent` 标记默认意图，并只补齐允许默认的字段。

## 默认意图

`result.default_intent` 必须输出：

- `"none"`：没有默认意图
- `"explicit_default"`：明确要求按默认、别问了、你决定、直接规划
- `"implicit_default"`：模糊表示都行、随便、无所谓

默认意图只允许补齐 P1/P2。P0 缺失时必须继续追问 P0，不能默认。

## 缺失分级

P0 必填，缺失不能进入规划，也不能默认：

- `destination_scope`
- `total_days`
- `start_city`

P1 可追问或默认补齐：

- `start_date`
- `budget`，由 `budget_total` 或 `budget_monthly` 满足
- `transport_mode`
- `travel_style`
- `pace`
- `high_altitude_acceptance`，当目的地或锚点涉及高原、高海拔、雪山、川西、藏东南等风险时需要确认
- `daily_driving_preference`，当交通方式是自驾且天数较长或跨多目的地时需要确认

P2 可选或默认：

- `accommodation_style`
- `food_preference`

## 追问规则

- `follow_up_questions` 只询问当前仍缺失字段。
- 不要追问已有非空字段，尤其不要重复追问第一轮已经给出的 `start_city`。
- 如果 P0 缺失，优先追问 P0。
- 如果 P0 已完整、用户表达默认意图，可以通过默认补齐任务补齐 P1/P2 并进入规划。

## 默认补齐策略

默认补齐任务只能补 P1/P2。默认值必须由 agent 根据快照里的目的地、天数、交通方式和风格生成，不能套用固定的长线或自驾旅行模板。默认值必须写入合法字段；不要把默认值写入 `missing_fields` 之外的无关字段。

## SkillResult 输出协议

只输出单个 JSON object，不要 markdown，不要解释。

必需字段：

- `skill_name`: `"travel-requirement-merge"`，追问生成任务可用 `"travel-requirement-question-generation"`，默认补齐任务可用 `"travel-requirement-default-completion"`
- `stage`: `"requirement_merge"` 或当前任务对应阶段
- `status`: `"need_user_input"` / `"ready"`
- `requirement_ready`: boolean
- `missing_fields`: 仍缺失字段数组
- `filled_fields`: 本轮填充字段数组
- `follow_up_questions`: 追问问题数组
- `result.requirement`: 本轮增量字段
- `result.default_intent`: `"none"` / `"explicit_default"` / `"implicit_default"`
- `next_stage`: `"awaiting_user_info"` / `"macro_planning"`
- `stop_workflow`: 需要用户回复时 true，可以进入规划时 false
- `output`: 展示给用户的自然语言文本

## 示例

已有快照中 `start_city = "北京"`，用户新回复：“预算2万，节奏轻松”

输出重点：

- 只输出 `budget_total` 和 `pace`
- 不追问 `start_city`
- 不在 `result.requirement` 中重复输出 `start_city`

已有快照 P0 完整，用户回复：“其他按默认来”

输出重点：

- `result.default_intent = "explicit_default"`
- 默认补齐 P1/P2
- 如果补齐后没有缺失，`status = "ready"` 且 `next_stage = "macro_planning"`

已有快照缺 `start_city`，用户回复：“其他都行”

输出重点：

- `result.default_intent = "implicit_default"`
- `missing_fields` 必须包含 `start_city`
- 继续追问出发城市，不能默认
