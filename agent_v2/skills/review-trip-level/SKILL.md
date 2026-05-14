---
name: review-trip-level
description: TripPlan 年度约束审查。检查预算总控、全年节奏可持续性、必去地点覆盖、风险集中度。
---

# TripPlan 级约束审查（年度约束）

## 目标

审查全年旅行计划的顶层约束是否满足。你不是检查"内容好不好"，而是检查"是否违反不可逾越的年度限制"。

## 审查输入

你从图数据库或 coordinator 上下文中获取 TripPlan 节点及所有 Phase 的摘要数据。

## 审查维度

### 1. 预算总控
- 规则：所有 Phase 的 estimatedBudget 之和 ≤ TripPlan.budgetTotal
- 违规判定：超出 → 标记超预算比例和具体超出的 Phase
- severity: critical

### 2. 全年节奏可持续性
- 规则：全年连续高强度天数（日均 POI ≥ 5 或 intensity = "high" 的天数）不超过 TripPlan.maxConsecutiveHighIntensityDays
- 违规判定：超出 → 标记超限路段（哪几天到哪几天连续高强度）
- severity: critical

### 3. 必去覆盖
- 规则：TripPlan.mustVisit 中的每个地点至少出现在一个 Phase 的 region 或描述中
- 违规判定：缺失 → 列出未覆盖的必去项
- severity: critical

### 4. 风险集中度
- 规则：高风险天气区域（extremeWeatherRisk = "high" 的 ClimateData 关联区域）的连续天数不超过 7 天
- 违规判定：超出 → 标记风险集中段
- severity: major

## 输出格式

```json
{
  "dimension": "trip_constraint",
  "score": 85,
  "passed": true,
  "critical_issues": [],
  "constraint_violations": [],
  "suggestions": [],
  "summary": "年度约束审查通过。"
}
```

constraint_violations 中每个元素格式：
```json
{
  "dimension": "预算总控",
  "rule": "所有 Phase 预算之和 ≤ budgetTotal",
  "actual": "Phase 预算之和 120000",
  "threshold": "budgetTotal 100000",
  "severity": "critical"
}
```

passed = true 当且仅当 critical_issues 为空（无 critical severity 违规）。