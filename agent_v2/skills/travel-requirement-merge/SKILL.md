---
name: travel-requirement-merge
description: 合并用户追问回复到旅行需求快照。Use when the user replies to follow-up questions about their travel requirements; parse natural language answers, merge into existing TravelRequirementSnapshot, and output SkillResult JSON.
---

# 旅行需求合并

## 目标

本 Skill 用于用户回复追问后的第二步：将用户的自然语言回复解析为结构化字段，合并到现有的 TravelRequirementSnapshot 中。

本 Skill 只做字段解析和合并，不做以下操作：
- 创建 TripPlan
- 调用地图工具（amap_*）
- 调用攻略工具（zhihu_*、bilibili_*）
- 调用天气工具（get_weather_context、check_weather_feasibility）
- 调用图写入工具（create_trip_plan、split_parent_node）
- 生成旅行方案

## 输入

上下文中会包含：
1. 现有的 TravelRequirementSnapshot JSON（已抽取的字段）
2. 用户的新消息（回复追问的内容）

## 合并规则

### 只更新用户明确提到的字段

- 用户说"从北京出发" → 更新 `start_city: "北京"`
- 用户说"6月1日" → 更新 `start_date: "2026-06-01"`
- 用户说"10万" → 更新 `budget_total: "10万"`
- 用户说"高铁为主" → 更新 `transport_mode: "高铁"`
- 用户说"喜欢自然风光和美食" → 更新 `travel_style: ["自然风光", "美食"]`
- 用户说"慢一点" → 更新 `pace: "轻松"`

### 永远不用空值覆盖非空值

如果 `start_city` 已经是 "北京"，用户回复中没有提到出发城市，则保持 "北京" 不变。

### 自然语言解析示例

| 用户输入 | 解析结果 |
|---------|---------|
| "6月" | `start_date: "2026-06-01"`（年份取当前年或下一年） |
| "10万" | `budget_total: "10万"` |
| "高铁为主" | `transport_mode: "高铁"` |
| "自然风光和历史文化" | `travel_style: ["自然风光", "历史文化"]` |
| "慢一点" | `pace: "轻松"` |
| "轻松" | `pace: "轻松"` |
| "紧凑" | `pace: "紧凑"` |

### 用户表达"按默认"的处理

如果用户明确说"按默认"、"别问了"、"你决定"、"都行"、"随便"，则：
- 输出 `default_intent: "explicit_default"` 或 `"implicit_default"`
- P0 字段如果仍然缺失，继续追问
- P1 字段可以使用默认值

## 字段名（必须使用这些 key）

| 字段名 | 说明 | 示例 |
|--------|------|------|
| `destination_scope` | 目的地范围 | "全国"、"云南"、"大理" |
| `total_days` | 总天数 | 365, 7, 30 |
| `start_city` | 出发城市 | "北京"、"上海" |
| `start_date` | 出发日期 | "2026-06-01" |
| `budget_total` | 总预算 | "10万"、"不限" |
| `transport_mode` | 交通方式 | "自驾"、"高铁"、"飞机"、"混合" |
| `travel_style` | 旅行风格数组 | ["自然风光","历史文化","美食"] |
| `pace` | 节奏 | "轻松"、"均衡"、"紧凑" |

## SkillResult 输出协议

只输出单个 JSON object，不要 markdown，不要解释。

### 字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `skill_name` | string | 固定 `"travel-requirement-merge"` |
| `stage` | string | 固定 `"requirement_merge"` |
| `status` | string | `"ready"`（信息充足）或 `"need_user_input"`（仍需补充） |
| `requirement_ready` | boolean | true=可以进入正式规划 |
| `missing_fields` | []string | 仍然缺失的字段名数组 |
| `filled_fields` | []string | 本轮已填充的字段名数组 |
| `result.requirement` | object | 只包含本轮变更的字段（部分更新） |
| `result.default_intent` | string | `"none"` / `"explicit_default"` / `"implicit_default"` |
| `next_stage` | string | `"macro_planning"`（信息充足）或 `"awaiting_user_info"`（仍需补充） |
| `stop_workflow` | boolean | false（信息充足）或 true（仍需补充） |
| `output` | string | 展示给用户的文本 |

### 示例 1：用户补充完整信息

现有快照：
```json
{
  "destination_scope": "全国",
  "total_days": 365
}
```

用户回复："从哈尔滨出发，6月1日开始，预算10万，高铁为主，喜欢自然风光和历史文化，节奏慢一点"

输出：
```json
{
  "skill_name": "travel-requirement-merge",
  "stage": "requirement_merge",
  "status": "ready",
  "requirement_ready": true,
  "missing_fields": [],
  "filled_fields": ["start_city", "start_date", "budget_total", "transport_mode", "travel_style", "pace"],
  "result": {
    "requirement": {
      "start_city": "哈尔滨",
      "start_date": "2026-06-01",
      "budget_total": "10万",
      "transport_mode": "高铁",
      "travel_style": ["自然风光", "历史文化"],
      "pace": "轻松"
    },
    "default_intent": "none"
  },
  "next_stage": "macro_planning",
  "stop_workflow": false,
  "output": "需求已确认，开始为您规划从哈尔滨出发的全国365天自然风光+历史文化之旅。"
}
```

### 示例 2：用户只补充部分信息

现有快照：
```json
{
  "destination_scope": "全国",
  "total_days": 365
}
```

用户回复："从哈尔滨出发，6月开始"

输出：
```json
{
  "skill_name": "travel-requirement-merge",
  "stage": "requirement_merge",
  "status": "need_user_input",
  "requirement_ready": false,
  "missing_fields": ["budget_total", "transport_mode", "travel_style", "pace"],
  "filled_fields": ["start_city", "start_date"],
  "result": {
    "requirement": {
      "start_city": "哈尔滨",
      "start_date": "2026-06-01"
    },
    "default_intent": "none"
  },
  "next_stage": "awaiting_user_info",
  "stop_workflow": true,
  "output": "已记录出发城市和时间。还需要确认几项：\n\n1. 总预算大概是多少？\n2. 主要交通方式是自驾、高铁火车、飞机，还是混合？\n3. 你更偏自然风光、历史文化、美食城市、摄影打卡，还是慢旅行？\n4. 每天节奏希望轻松、均衡，还是尽量多打卡？"
}
```

### 示例 3：用户表达"按默认"

现有快照：
```json
{
  "destination_scope": "全国",
  "total_days": 365,
  "start_city": "哈尔滨"
}
```

用户回复："按默认来，别问了"

输出：
```json
{
  "skill_name": "travel-requirement-merge",
  "stage": "requirement_merge",
  "status": "ready",
  "requirement_ready": true,
  "missing_fields": [],
  "filled_fields": ["budget_total", "transport_mode", "travel_style", "pace"],
  "result": {
    "requirement": {
      "budget_total": "中等",
      "transport_mode": "高铁/火车为主",
      "travel_style": ["自然风光", "历史文化"],
      "pace": "均衡"
    },
    "default_intent": "explicit_default"
  },
  "next_stage": "macro_planning",
  "stop_workflow": false,
  "output": "已按默认设置开始规划：中等预算、高铁为主、自然风光+历史文化、均衡节奏。"
}
```
