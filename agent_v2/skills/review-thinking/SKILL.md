---
name: review-thinking
description: 旅游规划思考质量审查。审查协调者 Agent 的 thinking_result 是否覆盖必要要素、是否有偷懒推理、是否暴露隐藏推理链路。
---

# 思考质量审查

## 目标

审查旅游规划协调者 Agent 的 thinking_result 质量，确保其覆盖必要推理要素、无偷懒推理、不暴露内部实现细节。

## 审查输入

你会收到协调者 Agent 的工作草稿，重点关注以下字段：
- thinking_result：思考结果摘要
- planning_process：规划过程摘要（用于对比检查重复）

## thinking_result 必须覆盖的要素

| 要素 | 检查内容 | 缺失特征 |
|---|---|---|
| 需求识别 | 是否正确提取了目的地、天数、交通偏好、人群、预算等 | thinking_result 中未提及用户的关键需求字段 |
| 信息缺口判断 | 是否识别了哪些信息不足、需要追问 | 未分析信息充足性就直接进入采集/规划 |
| 工具/成员调用依据 | 是否说明了为什么要调 zhihu_guide_material 或 amap-agent | 调用工具但未说明调用理由 |
| 路线取舍 | 是否说明了为什么选择这些地点、为什么排除其他地点 | 只列了选择结果，未说明排除理由 |

## 偷懒推理特征（critical/major）

以下任何一条即为偷懒推理，必须列入 critical_issues 或 issues：

- 跳过信息缺口分析，直接假设默认值（用户未说"按默认"时） → critical
- 省略验证步骤，声称"已验证"但 route_validation 中无对应记录 → critical
- thinking_result 内容过于笼统，没有针对具体目的地/用户需求的分析 → major
- 照搬模板句式，未结合实际问题填充内容 → major

## thinking_result 反模式

- 暴露逐字隐藏推理链路或内部实现细节（违规，必须只输出面向用户的摘要）
- 输出无关试探、自我对话或犹豫不决的推理过程
- 与 planning_process 内容高度重复（两者应有区分：thinking_result 侧重推理，planning_process 侧重执行）

## 相关反模式

| 编号 | 违规行为 | 严重程度 |
|---|---|---|
| V-31 | thinking_result 缺少需求识别 | major |
| V-32 | thinking_result 缺少信息缺口判断 | major |
| V-33 | thinking_result 缺少工具调用依据 | major |
| V-34 | thinking_result 缺少路线取舍 | major |
| V-35 | 暴露隐藏推理链路 | major |
| V-36 | thinking_result 与 planning_process 高度重复 | major |

## 评分规则

从 100 分开始扣分：

| 扣分项 | 扣分 | 具体说明 |
|---|---|---|
| 缺少需求识别 | -25 | thinking_result 未提及用户的目的地/天数/交通偏好等关键字段 |
| 缺少信息缺口判断 | -25 | 未分析"我还缺什么信息"就直接进入采集/规划 |
| 缺少工具调用依据 | -25 | 调用了 zhihu_guide_material 或 amap-agent 未说明为什么调用 |
| 缺少路线取舍 | -25 | 只列选择结果，未说明"为什么选 A 不选 B" |
| 偷懒推理 | -30 | 跳过缺口分析直接假设默认值；声称"已验证"但 route_validation 无对应记录 |
| 暴露隐藏推理链路 | -15 | thinking_result 中出现内部实现细节或逐字推理 |
| thinking_result 与 planning_process 高度重复 | -15 | 两者内容几乎相同，无推理/执行的区分 |

**通过阈值：65 分**。score >= 65 且无 critical_issues 为通过。

## 输出格式

必须输出单个 JSON object：

```json
{
  "dimension": "thinking_quality",
  "score": 80,
  "passed": true,
  "critical_issues": [],
  "elements_covered": ["需求识别", "信息缺口判断", "工具调用依据"],
  "elements_missing": ["路线取舍"],
  "laziness_signs": [],
  "issues": ["未说明为什么排除候选地点 X"],
  "suggestions": ["补充路线取舍理由"],
  "summary": "思考质量良好，缺少路线取舍说明。"
}
```
