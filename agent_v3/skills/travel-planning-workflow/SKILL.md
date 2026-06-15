---
name: travel-planning-workflow
description: 图数据库模式下的四阶段十三步旅行规划工作流。This skill should be loaded when the coordinator operates in Neo4j graph mode and needs the complete step-by-step workflow for multi-day trip planning.
---

# 图数据库模式：四阶段十三步工作流

## 运行前提

需求准入已由 Go 层 Orchestrator 完成。上下文中已包含完整的 TravelRequirementSnapshot。
本 skill 只在 orchestrator 确认 requirement_ready=true 后被加载。
禁止加载 travel-requirement-intake。禁止向用户追问。

## 阶段一：宏观规划 + L0 TripPlan / L1 Phase 级审查

### Step 1: 基于需求快照创建 TripPlan
- 输入：Orchestrator 提供的结构化 TravelRequirementSnapshot
- 调用 create_trip_plan 创建根节点（绑定 userId/sessionId/requestId，设定 budgetTotal、maxConsecutiveHighIntensityDays 等）
- 规划 3-8 个 Phase（region, season, theme, dayCount, start/end anchor）
- 禁止重新解析用户原始意图
- 禁止加载 travel-requirement-intake
- 禁止向用户追问

### Step 2: 气候驱动的 Phase 拆分
- 对候选区域调用 get_weather_context 获取各月气候
- 调用 split_parent_node(TripPlan → Phase) 拆分
- 写入 SeasonalEvent + ClimateData
- 委托 dili360-agent 获取各地理区域背景

### Step 3: Phase → Month 拆分（计算周数）
- 调用 split_parent_node(Phase → Months)
- 显式计算每个 Month 的 weekCount（该月天数÷7 取整，有余数+1）
- 为每个 Month 写入 monthlyBudget

### Step 4: L1 Phase 级审查 + L0 TripPlan 级审查
- 调用 review-phase-agent：检查气候合适性 + 地理递进 + 预算分配
- 调用 review-trip-agent：检查预算总控 + 节奏可持续 + 必去覆盖
- 不通过 → 调用 merge_children 或 rebalance_phase 调整 → 重审

## 阶段二：中观规划 + L2 Month 级审查

### Step 5: 攻略素材采集
- 按 Phase 调用 zhihu_guide_material 和 bilibili_guide_material
- 调用 write_guide_insight 写入图（素材不入上下文）

### Step 6: Month → Week 拆分
- 调用 split_parent_node(Month → Week)
- 设定每周主题、primaryLocation
- 初步分配 restDayCount

### Step 7: L2 Month 级审查 + Week → Day 拆分
- 调用 review-month-agent：检查周数正确性 + 天气窗口 + 月度预算 + 区域覆盖
- 不通过 → 调用 recalculate_week_count 修正
- 调用 split_parent_node(Week → Days)
- 每天设定粗略主题和 intensity

## 阶段三：微观规划 + L5 POI / L4 Day / L3 Week 级审查

### Step 8: 逐天地理事实验证
对当前 Week 内的每一天：
- 调用 get_subgraph(dayID) — 只加载当天子图（context isolation）
- 委托 amap-agent 验证 POI（地理编码、周边搜索、路线查询）
- 调用 upsert_poi_to_day 即时写入验证结果
- 调用 write_route 写入路线数据
- 调用 check_weather_feasibility 检查天气可行性
- 标记 isRainyDayBackup 或调用 suggest_seasonal_alternatives
- 调用 update_node(dayID, status="verified")

### Step 9: L5 POI 级审查 + L4 Day 级审查
对当天的每个 POI：
- 调用 review-poi-agent：检查地理验证 + 天气备份 + 费用合理性
对当天：
- 调用 get_day_full_context(dayID) 加载完整子图
- 委托 review-workflow-agent、review-thinking-agent、review-content-agent、review-output-agent、review-laziness-agent 并发审查
- 调用 write_review_result 写入审查结果
- 不通过 → 修正 → 重审

### Step 10: L3 Week 级审查
- 调用 get_subgraph(weekID) 加载周内所有 Day 摘要
- 调用 review-week-agent：检查休息日底线 ≥ 1 + 转移日 ≤ 2 + 高强度日 ≤ 4 + POI 密度
- 不通过 → 调整 Day 分配 → 重审

## 阶段四：全局汇总 + 全层级终审

### Step 11: 逐月逐阶段汇总
- 调用 get_trip_overview 获取全局视图
- 调用 get_constraint_violations 追溯所有违规是否已修复
- 检查跨层级一致性

### Step 12: 天气风险终审
- 调用 get_seasonal_route_risk 获取全年风险画像
- 确认所有高风险日有备选方案

### Step 13: 逐日增量输出（核心：不一次性生成全部内容）

**关键原则**：不对着 30 天的数据一次性生成大段文字，那样会因上下文限制导致每段只能写一两句。改为逐日从图数据库拉取完整数据，逐段生成详细内容。

**逐日循环协议（必须严格遵循）**：

1. **加载 answer 格式**：调用 `skill_load("travel-answer-format")` 获取最终输出格式规范。

2. **获取全局结构**：调用 `get_trip_overview(tripPlanID)`，获取完整层级树（Phase → Month → Week → Day），确认共有多少天、每个 Phase 下有哪些 Day。

3. **逐 Phase、逐日生成（核心协议）**：

   对每个 Phase，执行以下子步骤：

   **3a. Phase 头**：输出 Phase 标题（如"第一阶段：云南春日探索（3月-4月，共12天）"），1-2 句话概括该 Phase 的气候特点和旅行主题。

   **3b. 逐日循环**（对 Phase 内的每一天，严格按顺序处理）：
   ```
   FOR EACH day IN phase:
     ① 调用 get_day_full_context(dayID) — 只加载这一天
     ② 立即基于返回数据生成这一天完整详细文本（见下方"每日输出模板"）
     ③ 将当天文本追加到 answer 累积区
     ④ 再处理下一天
   ```

   **关键约束**：
   - **禁止预加载所有天**：不要一次性并行调用所有 Day 的 `get_day_full_context`。一次只处理一天。
   - **每天独立生成**：每调完一次 `get_day_full_context`，马上生成当天文本并追加，然后再调用下一天的 `get_day_full_context`。
   - 这确保了每一天都有充分的上下文空间来生成详细内容。

   **3c. Phase 汇总**：该 Phase 所有天处理完毕后，输出 Phase 级别汇总：
   - 总天数、预算使用、审查通过状态
   - 该 Phase 的关键亮点和注意事项

4. **每日输出模板**（每天文本必须包含以下全部要素，不可遗漏）：

   ```markdown
   ### Day {dayIndex}: {date} — {theme}（强度: {intensity}）

   **天气概况**：{从 get_day_full_context 返回的 climate 中提取：温度范围、降水概率、日出日落时间、极端天气风险}

   **当日行程**：

   | 顺序 | 时间 | POI | 类型 | 停留 | 说明 |
   |------|------|-----|------|------|------|
   | {visitOrder} | {startTime}-{endTime} | {name} | {type} | {duration}分钟 | {notes} |

   **POI 详情**（对每个 POI 展开描述）：
   - **{POI名称}**（{类型}）
     - 地址：{address}，{district}，{city}
     - 坐标：({lat}, {lng}) — 高德已确认
     - 推荐理由：{从攻略洞察中提取的主观推荐原因}
     - 预计游览时间：{duration}分钟
     - 费用预估：{estimatedCost}元
     - 攻略信号：{从 insights 中提取的知乎/B站建议，标注"攻略/主观信号"}
     - 交通备注：{从 routes 中提取的距离和交通方式}

   **路线衔接**（POI 之间的交通）：
   - {fromPOI} → {toPOI}: {transportMode}, {distanceMeters}米, 约{durationMin}分钟
     {如为高德路线，标注"高德已确认"}

   **天气注意事项**：
   - {从 weather constraints 中提取的当日注意事项}
   - {如 POI 有 isRainyDayBackup，列出备选方案}

   **海拔提醒**（如适用）：
   - {高原/高海拔地区标注海拔高度和适应建议}

   **本日小结**：{当日行程节奏评估、亮点、注意事项总结}
   ```

5. **全局汇总**（所有 Phase 处理完毕后）：
   - 补充 `weather_notes` 数组：全年各区域的天气注意事项汇总
   - 补充 `seasonal_events` 数组：季节事件提醒
   - 补充 `constraint_review_summary`：六级审查通过状态摘要
   - 将完整的 answer 字段嵌入最终 JSON 输出格式

6. **最终 JSON 输出**：按 coordinator instruction 中定义的 JSON schema 输出，其中 `answer` 字段包含上述逐日生成的全部内容。