---
name: travel-requirement-intake
description: 旅游规划需求准入分析。Use when the user asks for a travel plan, itinerary, route, POI recommendation, city walk, nearby travel, or executable tourism advice; extract structured requirement fields, identify missing information, and output SkillResult JSON.
---

# 旅游规划需求准入分析

## 目标

本 Skill 用于旅行规划的第一步：从用户消息中抽取结构化需求字段，判断哪些字段已知、哪些缺失，输出 SkillResult JSON。

本 Skill 只做字段抽取和需求分析，不做以下操作：
- 创建 TripPlan
- 调用地图工具（amap_*）
- 调用攻略工具（zhihu_*、bilibili_*）
- 调用天气工具（get_weather_context、check_weather_feasibility）
- 调用图写入工具（create_trip_plan、split_parent_node）
- 生成旅行方案

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

## 信息分级

### P0 必填字段（缺失不能进入正式规划）

1. `destination_scope`：目的地范围
2. `total_days`：总天数
3. `start_city`：出发城市

### P1 长周期必须字段（≥30 天旅行必须至少问一轮）

1. `start_date`：出发日期
2. `budget_total`：总预算
3. `transport_mode`：交通方式
4. `travel_style`：旅行风格
5. `pace`：节奏

### P2 可选字段（缺失时使用默认值）

- `accommodation_style`：住宿偏好（默认"经济舒适型"）
- `food_preference`：饮食偏好（默认["当地特色"]）
- `must_visit`：必去地点（默认[]）
- `avoid_places`：不想去的地点（默认[]）
- `special_constraints`：特殊限制（默认[]）

## 长周期旅行规则

当 `total_days` ≥ 30 时，即使 `destination_scope` 和 `total_days` 已明确，也必须询问 P1 字段（start_city, start_date, budget, transport_mode, travel_style, pace）。

## 用户表达"按默认"的处理

如果用户明确说"按默认"、"别问了"、"你决定"、"都行"、"随便"，则：
- P0 字段仍然必须从用户消息中提取（不能用默认值）
- P1 字段可以使用默认值，不再追问
- 输出 `status: "ready"` 而非 `status: "need_user_input"`

## SkillResult 输出协议

只输出单个 JSON object，不要 markdown，不要解释。

### 字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `skill_name` | string | 固定 `"travel-requirement-intake"` |
| `stage` | string | 固定 `"requirement_intake"` |
| `status` | string | `"need_user_input"`（需要用户补充）或 `"ready"`（信息充足） |
| `requirement_ready` | boolean | true=可以进入正式规划 |
| `missing_fields` | []string | 缺失字段名数组（使用上方字段名表中的 key） |
| `follow_up_questions` | []string | 追问问题数组 |
| `result.requirement` | object | 已抽取的结构化需求字段（只包含已识别的字段） |
| `next_stage` | string | `"awaiting_user_info"` 或 `"macro_planning"` |
| `stop_workflow` | boolean | true（需要用户回复时）或 false（可以进入规划时） |
| `output` | string | 展示给用户的文本 |

### 示例 1：需要用户补充信息

输入："我要一个全国365天的旅游plan"

输出：
```json
{
  "skill_name": "travel-requirement-intake",
  "stage": "requirement_intake",
  "status": "need_user_input",
  "requirement_ready": false,
  "missing_fields": ["start_city", "start_date", "budget_total", "transport_mode", "travel_style", "pace"],
  "follow_up_questions": [
    "你计划从哪个城市出发？",
    "这365天旅行大概从什么时候开始？",
    "总预算或每月预算大概是多少？",
    "主要交通方式是自驾、高铁火车、飞机，还是混合？",
    "你更偏自然风光、历史文化、美食城市、摄影打卡，还是慢旅行？",
    "每天节奏希望轻松、均衡，还是尽量多打卡？"
  ],
  "result": {
    "requirement": {
      "destination_scope": "全国",
      "total_days": 365
    }
  },
  "next_stage": "awaiting_user_info",
  "stop_workflow": true,
  "output": "在开始规划前，我需要先确认几项关键信息：\n\n1. 你计划从哪个城市出发？\n2. 这365天旅行大概从什么时候开始？\n3. 总预算或每月预算大概是多少？\n4. 主要交通方式是自驾、高铁火车、飞机，还是混合？\n5. 你更偏自然风光、历史文化、美食城市、摄影打卡，还是慢旅行？\n6. 每天节奏希望轻松、均衡，还是尽量多打卡？"
}
```

### 示例 2：信息充足，可以进入规划

输入："从北京出发，7天云南自驾游，喜欢自然风光，预算2万"

输出：
```json
{
  "skill_name": "travel-requirement-intake",
  "stage": "requirement_intake",
  "status": "ready",
  "requirement_ready": true,
  "missing_fields": [],
  "follow_up_questions": [],
  "result": {
    "requirement": {
      "destination_scope": "云南",
      "total_days": 7,
      "start_city": "北京",
      "budget_total": "2万",
      "transport_mode": "自驾",
      "travel_style": ["自然风光"],
      "pace": "均衡"
    }
  },
  "next_stage": "macro_planning",
  "stop_workflow": false,
  "output": "需求已确认，开始为您规划云南7天自驾自然风光之旅。"
}
```

### 示例 3：用户表达"按默认"

输入："我要一个全国365天的旅游plan，别问了按默认来"

输出：
```json
{
  "skill_name": "travel-requirement-intake",
  "stage": "requirement_intake",
  "status": "ready",
  "requirement_ready": true,
  "missing_fields": [],
  "follow_up_questions": [],
  "result": {
    "requirement": {
      "destination_scope": "全国",
      "total_days": 365,
      "start_city": "",
      "start_date": "",
      "budget_total": "中等",
      "transport_mode": "高铁/火车为主",
      "travel_style": ["自然风光", "历史文化"],
      "pace": "均衡"
    }
  },
  "next_stage": "macro_planning",
  "stop_workflow": false,
  "output": "已按默认设置开始规划：中等预算、高铁为主、自然风光+历史文化、均衡节奏。"
}
```
