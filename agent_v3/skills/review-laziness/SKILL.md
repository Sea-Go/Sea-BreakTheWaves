---
name: review-laziness
description: 旅游规划偷懒检测。检测协调者 Agent 是否存在跳步、模糊措辞、模板化填充、照搬热门清单等偷懒行为。
---

# 偷懒检测

## 目标

检测旅游规划协调者 Agent 是否存在偷懒行为，包括流程偷懒和内容偷懒。即使格式合规，偷懒行为也会严重影响回答的实际价值。

## 审查输入

你会收到协调者 Agent 的工作草稿，包含以下字段：
- thinking_result：思考结果摘要
- planning_process：规划过程摘要
- answer：面向用户的最终方案
- content_insights：攻略素材提炼
- route_validation：路线验证记录
- follow_up_questions：追问列表
- insufficient_information：信息是否充足

## 偷懒行为清单

| 编号 | 偷懒行为 | 检测方法 | 严重程度 |
|---|---|---|---|
| L-01 | 跳过追问直接给方案 | 首次请求且用户未说"别问"，但 `insufficient_information` 为 false 且无追问记录 | critical |
| L-02 | 模糊措辞替代具体数据 | answer 中出现"不远""很近""很快""大概X分钟"但无工具验证标注 | major |
| L-03 | 遗漏关键维度 | 缺少交通、预算、人群适配、拥挤风险、备选方案中的任何一项 | major |
| L-04 | 内容过短敷衍 | answer 长度明显过短（如 3 天行程 answer 不足 500 字） | major |
| L-05 | 声称信息不足但未列问题 | `insufficient_information` 为 true 但 `follow_up_questions` 为空 | critical |
| L-06 | 路线验证为空但有具体数据 | `route_validation` 为空数组但 answer 中有具体距离/时间 | critical |
| L-07 | 模板化填充 | 所有地点使用完全相同的句式结构，缺乏个性化分析 | minor |
| L-08 | 照搬热门清单 | 直接堆叠热门景点，无差异化、无本地体验、无替代方案 | major |
| L-09 | 跳过审查步骤 | 最终输出未经 review-agent 审查 | critical |
| L-10 | 先写行程再验证 | planning_process 中验证步骤在行程设计之后 | critical |

## 严重程度定义

- **critical**：必须修正，列入 `critical_issues`
- **major**：强烈建议修正，列入 `issues`
- **minor**：建议改进，列入 `issues`

## 检测方法详解

### L-01 跳过追问直接给方案
- 检查：用户首次请求 + 用户未说"别问" + `insufficient_information` 为 false + 无追问记录
- 如果 `follow_up_questions` 不为空或 `insufficient_information` 为 true，则不算偷懒

### L-02 模糊措辞替代具体数据
- 在 answer 中搜索：不远、很近、很快、大概、大约、一般、通常、应该、差不多、几分钟、一小会儿
- 这些词出现时，检查是否有工具验证标注（"已由高德验证""待实时确认"等）
- 无标注的模糊措辞 = 偷懒

### L-03 遗漏关键维度
- answer 中必须覆盖以下维度中的至少一项讨论：交通方式、预算估算、人群适配、拥挤风险/替代、备选方案
- 如果 answer 是多天行程但完全没有提及这些维度中的任何一个 = 偷懒

### L-04 内容过短敷衍
- 3 天及以上行程的 answer 不足 500 字 = 偷懒
- 1-2 天行程的 answer 不足 200 字 = 偷懒

### L-05 声称信息不足但未列问题
- `insufficient_information` 为 true 时，`follow_up_questions` 必须非空
- 否则 agent 在逃避输出

### L-06 路线验证为空但有具体数据
- `route_validation` 为空数组 + answer 中有具体数字（距离/时间/等待）= 数据来源不明 = critical

### L-07 模板化填充
- 检查多个地点的"为什么推荐"和"简单介绍"是否使用相同句式
- 如"XX是一个值得去的地方""XX以其XX闻名"重复出现

### L-08 照搬热门清单
- 所有推荐都是大众热门景点
- 无本地街区/市场/公园等差异化体验
- 无替代方案或更安静的替代推荐

### L-09 跳过审查步骤
- planning_process 中无审查步骤的描述
- 最终输出前未提交给 review-agent

### L-10 先写行程再验证
- planning_process 中路线验证步骤出现在行程设计之后
- 应该是先验证再设计

## 评分规则

从 100 分开始，按偷懒行为扣分：

| 严重程度 | 扣分 |
|---|---|
| critical | -40 |
| major | -25 |
| minor | -10 |

**通过阈值：65 分**。score >= 65 且无 critical_issues 为通过。

## 输出格式

必须输出单个 JSON object：

```json
{
  "dimension": "laziness_detection",
  "score": 80,
  "passed": true,
  "critical_issues": [],
  "laziness_behaviors_found": [
    {
      "id": "L-02",
      "behavior": "模糊措辞替代具体数据",
      "severity": "major",
      "evidence": "answer 中出现'不远'但无工具验证标注"
    }
  ],
  "issues": ["answer 中 1 处使用模糊措辞'不远'替代具体距离"],
  "suggestions": ["将'不远'替换为具体步行距离或标注'待实时确认'"],
  "summary": "检测到 1 处模糊措辞，无 critical 偷懒行为。"
}
```
