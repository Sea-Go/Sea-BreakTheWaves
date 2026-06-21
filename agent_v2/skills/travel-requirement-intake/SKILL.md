---
name: travel-requirement-intake
description: 旅游规划需求准入分析。Use when the user asks for a travel plan, itinerary, route, POI recommendation, city walk, nearby travel, or executable tourism advice; extract structured requirement fields, identify missing information, and output SkillResult JSON.
---

# 旅游规划需求准入分析

## 目标

从完整历史、用户最新消息和当前 TravelRequirementSnapshot 中理解旅行需求，输出一个 SkillResult JSON。只做需求理解、缺失判断和追问，不生成旅行方案，不调用地图、攻略、天气或图写入工具。

## TravelRequirementSnapshot 合法字段

只能在 `result.requirement` 中输出这些字段：

| 字段名 | 说明 |
| --- | --- |
| `destination_scope` | 目的地范围、城市、省份、国家或区域 |
| `total_days` | 总天数，number |
| `start_date` | 出发日期，YYYY-MM-DD |
| `end_date` | 结束日期，YYYY-MM-DD |
| `start_city` | 出发城市 |
| `end_city` | 终点城市 |
| `budget_total` | 总预算 |
| `budget_monthly` | 月预算 |
| `transport_mode` | 交通方式 |
| `travel_style` | 旅行风格数组 |
| `pace` | 行程节奏 |
| `high_altitude_acceptance` | 高海拔接受度 |
| `daily_driving_preference` | 日均驾驶强度偏好 |
| `accommodation_style` | 住宿偏好 |
| `food_preference` | 饮食偏好数组 |
| `must_visit` | 必去地点数组 |
| `avoid_places` | 不想去的地点数组 |
| `special_constraints` | 特殊限制数组 |
| `destination_anchors` | 目的地锚点数组，仅在用户明确提供或已有快照中存在时输出 |

不要输出空字符串、空数组或 null 来覆盖已有非空字段。

## 缺失分级

P0 必填，缺失不能进入规划，也不能默认：

- `destination_scope`
- `total_days`
- `start_city`

P1 可追问，也可在用户表达默认意图后由默认补齐任务补齐：

- `start_date`
- `budget`，由 `budget_total` 或 `budget_monthly` 满足
- `transport_mode`
- `travel_style`
- `pace`
- `high_altitude_acceptance`，当目的地或锚点涉及高原、高海拔、雪山、川西、藏东南等风险时需要确认
- `daily_driving_preference`，当交通方式是自驾且天数较长或跨多目的地时需要确认

P2 可选，也可默认：

- `accommodation_style`
- `food_preference`

## 理解与追问规则

- 结合完整历史和当前快照理解用户意图；如果第一轮已经给过某字段，后续不要重复追问该字段。
- 只抽取用户明确表达或从上下文可稳定推出的字段。
- 对短时城市游、街区探索、漫步、city walk 等语义，由 agent 根据完整上下文自主判断合适的 `transport_mode`、`travel_style` 和 `special_constraints`；不要套用长线或自驾旅行默认。
- 如果已有快照某字段非空，而本轮用户没有明确修改，不要在 `result.requirement` 中重复输出该字段。
- 如果用户表达“按默认、别问了、你决定、都行、随便、无所谓”等默认意图，在 `result.default_intent` 输出 `explicit_default` 或 `implicit_default`；否则输出 `none`。
- 即使用户有默认意图，P0 缺失时也必须继续追问 P0，不能进入规划。
- `follow_up_questions` 只询问当前仍缺失的字段，不要询问已有非空字段。
- 如果 P0 完整但 P1/P2 缺失且用户没有默认意图，先追问一次关键信息。

## 默认补齐策略

默认补齐任务只能补 P1/P2，不得补 P0。默认值必须由 agent 根据目的地、天数、交通方式和用户偏好生成，不能套用固定的长线或自驾旅行模板。默认值必须写入合法 snapshot 字段。

## SkillResult 输出协议

只输出单个 JSON object，不要 markdown，不要解释。

必需字段：

- `skill_name`: `"travel-requirement-intake"`，追问生成任务可用 `"travel-requirement-question-generation"`，默认补齐任务可用 `"travel-requirement-default-completion"`
- `stage`: `"requirement_intake"` 或当前任务对应阶段
- `status`: `"need_user_input"` / `"ready"`
- `requirement_ready`: boolean
- `missing_fields`: 仍缺失字段数组；预算缺口可用 `"budget"`
- `follow_up_questions`: 追问问题数组
- `result.requirement`: 本轮抽取或默认补齐的字段
- `result.default_intent`: `"none"` / `"explicit_default"` / `"implicit_default"`
- `next_stage`: `"awaiting_user_info"` / `"macro_planning"`
- `stop_workflow`: 需要用户回复时 true，可以进入规划时 false
- `output`: 展示给用户的自然语言文本

## 示例

用户：“从北京出发，7天去云南，自驾，预算2万，喜欢自然风光”

输出重点：

- `result.requirement.start_city = "北京"`
- `result.requirement.total_days = 7`
- `result.requirement.destination_scope = "云南"`
- `result.requirement.transport_mode = "自驾"`
- 不追问 `start_city`

用户：“我要一个全国365天的旅游 plan，按默认来”，但没有出发城市

输出重点：

- `result.default_intent = "explicit_default"`
- `missing_fields` 必须包含 `start_city`
- `status = "need_user_input"`
- 不得把 `start_city` 默认成任意城市
