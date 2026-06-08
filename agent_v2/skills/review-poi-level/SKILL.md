---
name: review-poi-level
description: POI 单点约束审查。检查地理验证状态、天气备份存在性、攻略印证、费用合理性。
---

# POI 级约束审查（单点约束）

## 目标

审查单个 POI 的元数据完整性：是否经过地理验证、是否有天气备份、是否有攻略来源背书、费用是否合理。

## 审查输入

你从图数据库或 coordinator 上下文中获取 POI 节点及其关联的 WeatherConstraint 和 GuideInsight。

## 审查维度

### 1. 地理验证
- 规则：POI.verifiedBy 非空（值为 "amap_geocode"、"amap_poi_detail"、"amap_poi_keyword_search" 等）
- 违规判定：未验证 → critical
- severity: critical

### 2. 天气备份
- 规则：若 POI 所在区域月份的 WeatherConstraint.severity ≥ "major"，则 POI.isRainyDayBackup = true 或存在 Day 的 HAS_BACKUP 边指向另一个同区域 POI
- 违规判定：无备份 → critical
- severity: critical

### 3. 攻略印证（soft）
- 规则：POI 至少被 1 条 GuideInsight 引用（INSIGHT_FOR_POI 边），或 verifiedBy 来自 amap-agent
- 违规判定：来源缺失 → 警告但不阻塞
- severity: minor

### 4. 费用合理性
- 规则：POI.estimatedCost 非空且 > 0（不考虑免费景点）
- 违规判定：费用缺失 → 警告（soft）
- severity: minor

## 输出格式

```json
{
  "dimension": "poi_constraint",
  "score": 90,
  "passed": true,
  "critical_issues": [],
  "constraint_violations": [],
  "suggestions": [],
  "summary": "POI 约束审查通过。"
}
```

passed = true 当且仅当 critical_issues 为空（无地理未验证、无天气备份缺失）。