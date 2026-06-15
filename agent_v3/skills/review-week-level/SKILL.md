---
name: review-week-level
description: Week 周度约束审查。检查休息日底线、转移日上限、高强度天数上限、POI 密度。模仿人体极限约束。
---

# Week 级约束审查（周度约束——人体极限）

## 目标

审查一周内的旅行节奏是否在人体可承受范围内。这不是质量评审，而是**人体极限约束检查**——连续不休息会累垮，连续赶路会崩溃。

## 审查输入

你从图数据库或 coordinator 上下文中获取 Week 节点及其所有 Day 的摘要。

## 审查维度

### 1. 休息日底线
- 规则：Week.restDayCount ≥ 1（每 7 天至少 1 天纯休息，不含转移日）
- 违规判定：restDayCount = 0 → critical
- severity: critical

### 2. 转移日上限
- 规则：Week.transferDayCount ≤ 2（每周最多 2 天纯赶路/交通转移）
- 违规判定：超出 → 标记转移日列表
- severity: critical

### 3. 高强度上限
- 规则：Week.highIntensityDayCount ≤ 4（每周最多 4 天高强度，高强度定义为 Day.intensity = "high" 或 POI 数 ≥ 5）
- 违规判定：超出 → 标记高强度日列表
- severity: major

### 4. POI 密度
- 规则：Week 内每天平均 POI 数 ≤ 5，且每天主停留点（isMainStop=true）≤ 3
- 违规判定：超出日均 5 个 → 警告；超出主停留点 3 个 → major
- severity: 主停留点违规为 major，密度违规为 minor

## 输出格式

```json
{
  "dimension": "week_constraint",
  "score": 70,
  "passed": true,
  "critical_issues": [],
  "constraint_violations": [
    {
      "dimension": "POI 密度",
      "rule": "每天主停留点 ≤ 3",
      "actual": "Day 3 有 5 个主停留点",
      "threshold": "3",
      "severity": "major"
    }
  ],
  "suggestions": [],
  "summary": "休息日底线和转移日上限通过，但 Day 3 主停留点过多。"
}
```

passed = true 当且仅当 critical_issues 为空。