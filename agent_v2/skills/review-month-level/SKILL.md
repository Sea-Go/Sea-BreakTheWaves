---
name: review-month-level
description: Month 月度约束审查。检查周数正确性、天气窗口、月度预算、区域覆盖。
---

# Month 级约束审查（月度约束）

## 目标

审查单个月份的约束是否满足。重点关注：Month.weekCount 是否正确、该月的天气窗口是否充足、预算是否在月度限额内、区域覆盖是否完整。

## 审查输入

你从图数据库或 coordinator 上下文中获取 Month 节点、其 Weeks 摘要、关联的 ClimateData 和 WeatherConstraint。

## 审查维度

### 1. 周数正确性
- 规则：Month.weekCount = 该月实际天数 ÷ 7（取整，有余数则 +1）。例：31 天 = 5 周，28 天 = 4 周
- 违规判定：不匹配 → 标记期望值 vs 实际值，调用 recalculate_week_count 修正
- severity: critical

### 2. 天气窗口
- 规则：Month 内每周至少 4 天的 WeatherConstraint.severity ≠ "critical"
- 违规判定：不足 → 标记高危周及原因
- severity: major

### 3. 月度预算
- 规则：Month 内所有 Day 的 POI 费用 + 路线费用之和 ≤ Month.monthlyBudget
- 违规判定：超出 → 标记超支金额和比例
- severity: critical

### 4. 区域覆盖
- 规则：Month 下 Weeks 的 primaryLocation 覆盖了该区域的至少 2 个核心城市/地区
- 违规判定：缺失 → 列出遗漏的该区域核心城市
- severity: minor

## 输出格式

```json
{
  "dimension": "month_constraint",
  "score": 75,
  "passed": true,
  "critical_issues": [],
  "constraint_violations": [],
  "suggestions": [],
  "summary": "月度约束审查通过。"
}
```

passed = true 当且仅当 critical_issues 为空。