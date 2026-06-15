---
name: review-phase-level
description: Phase 季节约束审查。检查气候合适性、地理递进逻辑、预算分配、节庆覆盖。
---

# Phase 级约束审查（季节约束）

## 目标

审查单个 Phase（3-6个月的时间段）的约束是否满足。Phase 是季节+区域的组合，审查重点是：这个季节去这个区域是否合理。

## 审查输入

你从图数据库或 coordinator 上下文中获取 Phase 节点、其 Months 摘要、关联的 ClimateData、WeatherConstraint 和 SeasonalEvent。

## 审查维度

### 1. 气候合适性
- 规则：Phase 内所有区域的月份，在 ClimateData 中 avgHighTemp 在 [5°C, 38°C] 范围内，且 extremeWeatherRisk ≠ "high"
- 违规判定：违规 → 标记不适合的区域+月份，说明温度或天气风险
- severity: critical

### 2. 地理递进逻辑
- 规则：Phase 内 Months 按地理顺序排列，不出现超过 500km 的折返（即从一个城市出发，不应该到下一个月又回到上一个城市附近）
- 违规判定：折返 → 标记折返段的起始城市和目标城市，计算大致距离
- severity: major

### 3. 预算分配合理性
- 规则：Phase 的 estimatedBudget 占全年比例与 Phase.dayCount 占全年比例偏差 ≤ 20%
- 违规判定：偏差 > 20% → 标记偏差值
- severity: major

### 4. 节庆覆盖（soft）
- 规则：Phase 所在时间段内的 SeasonalEvent 至少有 1 个被引用在该 Phase 的 climateSummary 中
- 违规判定：缺失 → 提醒但不阻塞（soft constraint）
- severity: minor

## 输出格式

```json
{
  "dimension": "phase_constraint",
  "score": 80,
  "passed": true,
  "critical_issues": [],
  "constraint_violations": [],
  "suggestions": [],
  "summary": "Phase 季节约束审查通过。"
}
```

passed = true 当且仅当 critical_issues 为空。