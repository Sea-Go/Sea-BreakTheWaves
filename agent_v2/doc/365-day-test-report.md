# 365 天全国自驾旅行规划 — 完整测试报告

> 测试时间: 2026-05-18 16:46 ~ 16:48
> 测试端口: 127.0.0.1:8088
> 用户输入: "全国365天的旅游，预算20万，一人行，自驾游，更喜好自然风光"

---

## 一、AG-UI 事件流（原始输出）

### 第 1 轮: 需求提交

请求:
```json
{"threadId":"doc-test-365","runId":"run-1","messages":[{"role":"user","content":"全国365天的旅游，预算20万，一人行，自驾游，更喜好自然风光"}]}
```

SSE 响应:
```
id: RUN_STARTED_1779094019476
data: {"type":"RUN_STARTED","timestamp":1779094019476,"threadId":"doc-test-365","runId":"run-1"}

id: TEXT_MESSAGE_START_1779094028696
data: {"type":"TEXT_MESSAGE_START","timestamp":1779094028696,"messageId":"wf-msg-1779094028696825000","role":""}

id: TEXT_MESSAGE_CONTENT_1779094028696
data: {"type":"TEXT_MESSAGE_CONTENT","timestamp":1779094028696,"messageId":"wf-msg-1779094028696825000","delta":"在开始规划前，我需要先确认几项关键信息：\n\n1. 你计划从哪个城市出发？\n2. 每日节奏希望轻松、均衡，还是尽量多打卡？\n3. 计划什么时候开始？如果还没确定，我可以默认从下个月开始。\n\n"}

id: TEXT_MESSAGE_END_1779094028697
data: {"type":"TEXT_MESSAGE_END","timestamp":1779094028697,"messageId":"wf-msg-1779094028696825000"}

id: RUN_FINISHED_1779094028697
data: {"type":"RUN_FINISHED","timestamp":1779094028697,"threadId":"doc-test-365","runId":"run-1"}
```

**用户可见输出:**
> 在开始规划前，我需要先确认几项关键信息：
>
> 1. 你计划从哪个城市出发？
> 2. 每日节奏希望轻松、均衡，还是尽量多打卡？
> 3. 计划什么时候开始？如果还没确定，我可以默认从下个月开始。

---

### 第 2 轮: 补充信息 → 需求合并 → 宏观规划

请求:
```json
{"threadId":"doc-test-365","runId":"run-2","messages":[{"role":"user","content":"从北京出发，6月1日开始，节奏均衡"}]}
```

SSE 响应:
```
id: RUN_STARTED_1779094028711
data: {"type":"RUN_STARTED","timestamp":1779094028711,"threadId":"doc-test-365","runId":"run-2"}

id: TEXT_MESSAGE_START_1779094087706
data: {"type":"TEXT_MESSAGE_START","timestamp":1779094087706,"messageId":"wf-msg-1779094087706658000","role":""}

id: TEXT_MESSAGE_CONTENT_1779094087706
data: {"type":"TEXT_MESSAGE_CONTENT","timestamp":1779094087706,"messageId":"wf-msg-1779094087706658000","delta":"宏观规划完成 — 已创建 7 个 Phase。接下来进入图拆分阶段。\n"}

id: TEXT_MESSAGE_END_1779094087706
data: {"type":"TEXT_MESSAGE_END","timestamp":1779094087706,"messageId":"wf-msg-1779094087706658000"}

id: TEXT_MESSAGE_START_1779094087706
data: {"type":"TEXT_MESSAGE_START","timestamp":1779094087706,"messageId":"wf-msg-1779094087706734000","role":""}

id: TEXT_MESSAGE_CONTENT_1779094087706
data: {"type":"TEXT_MESSAGE_CONTENT","timestamp":1779094087706,"messageId":"wf-msg-1779094087706734000","delta":"graph_splitting 阶段（待后续 PR 实现）\n"}

id: TEXT_MESSAGE_END_1779094087706
data: {"type":"TEXT_MESSAGE_END","timestamp":1779094087706,"messageId":"wf-msg-1779094087706734000"}

id: RUN_FINISHED_1779094087706
data: {"type":"RUN_FINISHED","timestamp":1779094087706,"threadId":"doc-test-365","runId":"run-2"}
```

**用户可见输出:**
> 宏观规划完成 — 已创建 7 个 Phase。接下来进入图拆分阶段。
>
> graph_splitting 阶段（待后续 PR 实现）

---

## 二、服务器日志（完整）

```
16:46:21 INFO  agent app starting
16:46:21 INFO  [travel-planning-agent] Neo4j 可用，使用混合图工作流 Agent
16:46:21 INFO  Amap AG-UI server listening on http://127.0.0.1:8088/agui

── 第1轮: 需求提交 ──

16:46:59 INFO  [orchestrator] handle: userID=user sessionID=doc-test-365 stage=requirement_intake msgLen=83
16:46:59 INFO  [orchestrator] intake: start userID=user sessionID=doc-test-365
16:46:59 INFO  [model-router] agent=intake-agent level=MEDIUM model=qwen3-max remaining_tpm=5000000/5000000
16:47:08 INFO  [model-usage] agent=intake-agent model=qwen3-max prompt_tokens=10128 completion_tokens=256 total_tokens=10384
16:47:08 INFO  [orchestrator] intake: decision ready=false missingP0=[start_city] missingP1=[pace start_date] askedRounds=0
16:47:08 INFO  [orchestrator] intake: not ready → askedRounds=1 questions=3

── 第2轮: 需求合并 ──

16:47:08 INFO  [orchestrator] handle: userID=user sessionID=doc-test-365 stage=awaiting_user_info msgLen=47
16:47:08 INFO  [orchestrator] merge: start userID=user sessionID=doc-test-365 askedRounds=1
16:47:08 INFO  [model-router] agent=intake-agent level=MEDIUM model=qwen3-max remaining_tpm=4989616/5000000
16:47:15 INFO  [model-usage] agent=intake-agent model=qwen3-max prompt_tokens=10031 completion_tokens=198 total_tokens=10229
16:47:15 INFO  [orchestrator] merge: decision ready=true missingP0=[] missingP1=[] askedRounds=1 maxRounds=2
16:47:15 INFO  [orchestrator] merge: ready → macro_planning

── 宏观规划（轻量 Agent，4 工具 + Dili360 子 Agent）──

16:47:15 INFO  [model-router] agent=macro-planning-agent level=MEDIUM model=qwen3-max remaining_tpm=4979387/5000000
16:47:31 INFO  [model-router] agent=macro-planning-agent level=MEDIUM model=qwen3-max remaining_tpm=5000000/5000000
16:47:58 INFO  [model-router] agent=macro-planning-agent level=MEDIUM model=qwen3-max remaining_tpm=5000000/5000000
16:48:06 INFO  [model-router] agent=macro-planning-agent level=MEDIUM model=qwen3-max remaining_tpm=5000000/5000000
16:48:07 INFO  [model-usage] agent=macro-planning-agent model=qwen3-max prompt_tokens=5470 completion_tokens=36 total_tokens=5506

── Neo4j 验证 + Phase 校验 ──

16:48:07 INFO  [workflow-runner] macro_planning: TripPlan ca4eba4d-a64e-4980-b12d-59372347c287 confirmed in Neo4j (user=user, session=doc-test-365)
16:48:07 INFO  [workflow-checks] macro planning validation passed: 7 phases, totalDayCount=365
16:48:07 INFO  [workflow-runner] macro_planning: tripPlanID=ca4eba4d-a64e-4980-b12d-59372347c287 phases=7 session=doc-test-365

── 阶段推进 ──

16:48:07 INFO  [model-router] agent=summary level=LOW model=qwen3.6-plus remaining_tpm=4992520/5000000
16:48:28 INFO  [model-usage] agent=summary model=qwen3.6-plus prompt_tokens=5534 completion_tokens=1031 total_tokens=6565
```

**关键日志解读:**

| 时间 | 事件 | 说明 |
|------|------|------|
| 16:47:08 | `intake: decision ready=false` | 需求准入 Agent 判断信息不足，生成 3 个追问 |
| 16:47:15 | `merge: decision ready=true` | 需求合并 Agent 判断信息充足，进入宏观规划 |
| 16:48:07 | `TripPlan confirmed in Neo4j` | **Go 层从 Neo4j 确认 TripPlan 存在**（非 LLM 文本提取） |
| 16:48:07 | `validation passed: 7 phases, totalDayCount=365` | Phase 完整性校验通过 |

---

## 三、Neo4j 图数据库存储结果

### TripPlan 根节点

```
tp.id:          ca4eba4d-a64e-4980-b12d-59372347c287
tp.name:        全国自然风光自驾之旅
tp.totalDays:   365
tp.startDate:   2024-06-01
tp.endDate:     2025-05-31
tp.budgetTotal: 200000.0
tp.travelStyle: 均衡
tp.transportMode: 自驾
tp.interests:   ["自然风光"]
tp.status:      decomposed
```

### Phase 子节点（7 个）

| seq | 名称 | 区域 | 天数 | 开始日期 | 结束日期 |
|-----|------|------|------|---------|---------|
| 1 | 华北初夏自然探索 | 华北 | 45 | 2024-06-01 | 2024-07-15 |
| 2 | 西北盛夏风光之旅 | 西北 | 47 | 2024-07-16 | 2024-08-31 |
| 3 | 西南雨季山水行 | 西南 | 45 | 2024-09-01 | 2024-10-15 |
| 4 | 华南秋日滨海游 | 华南 | 46 | 2024-10-16 | 2024-11-30 |
| 5 | 华东深秋文化自然 | 华东 | 46 | 2024-12-01 | 2025-01-15 |
| 6 | 东北冰雪奇缘 | 东北 | 45 | 2025-01-16 | 2025-03-01 |
| 7 | 华北春日返程 | 华北 | 91 | 2025-03-02 | 2025-05-31 |

**dayCount 校验:** 45 + 47 + 45 + 46 + 46 + 45 + 91 = **365** ✅

### ClimateData 气候数据

| Phase | 区域 | 月份 | 最高温 | 最低温 | 降水 | 极端天气风险 |
|-------|------|------|--------|--------|------|------------|
| 1 | 华北 | 6月 | 32°C | 20°C | 80mm | 低 |
| 2 | 西北 | 6月 | 28°C | 15°C | 20mm | 低 |
| 2 | 西北 | 7月 | 30°C | 18°C | 20mm | 低 |
| 3 | 西南 | 6月 | 26°C | 18°C | 180mm | 中 |
| 3 | 西南 | 8月 | 28°C | 19°C | 150mm | 中 |
| 4 | 华南 | 6月 | 33°C | 25°C | 250mm | 高 |
| 4 | 华南 | 10月 | 28°C | 20°C | 60mm | 低 |
| 5 | 华东 | 6月 | 29°C | 22°C | 200mm | 中 |
| 5 | 华东 | 11月 | 20°C | 10°C | 50mm | 低 |
| 6 | 东北 | 6月 | 25°C | 15°C | 100mm | 低 |
| 6 | 东北 | 12月 | -5°C | -15°C | 10mm | 高 |
| 7 | 华北 | 6月 | 32°C | 20°C | 80mm | 低 |

### 子节点统计

| 节点类型 | 数量 | 说明 |
|---------|------|------|
| Phase | 7 | ✅ 宏观规划产物 |
| Month | 0 | ⏳ 待 Phase 2 拆分 |
| Week | 0 | ⏳ 待 Phase 2 拆分 |
| Day | 0 | ⏳ 待 Phase 2 拆分 |

---

## 四、架构验证要点

### 1. TripPlanID 所有权回归 Go 层

```
Go 层预生成 expectedTripPlanID → 传给 LLM → LLM 调用 create_trip_plan → Go 层查 Neo4j 确认
```

日志证据:
```
[workflow-runner] macro_planning: TripPlan ca4eba4d... confirmed in Neo4j (user=user, session=doc-test-365)
```

不再依赖 LLM 文本输出中的 trip_plan_id。

### 2. 轻量级宏观规划 Agent

```
旧: newTravelPlanningTeam() — 24 工具 + 12 子 Agent
新: newMacroPlanningAgent() — 4 工具 + 1 子 Agent (Dili360)
```

工具清单: `create_trip_plan`, `split_parent_node`, `get_weather_context`, `write_climate_data`

### 3. 需求准入 → 需求合并 → 宏观规划 三阶段流

```
requirement_intake → awaiting_user_info → requirement_merge → macro_planning → graph_splitting
```

Orchestrator 控制 stage 推进，每个 stage 使用不同的 Agent。

### 4. Phase 完整性校验

校验项全部通过:
- Phase 数量: 7（范围 3-8）✅
- seq 连续: 1,2,3,4,5,6,7 ✅
- startDate/endDate: 全部填充 ✅
- region: 全部填充 ✅
- dayCount > 0: 全部 ✅
- dayCount 之和 = 365: ✅
- 无 Month/Week/Day 节点: ✅

### 5. MERGE 幂等性

Cypher 使用 `MERGE (tp:TripPlan {id: $id})` 而非 `CREATE`，重试不会产生重复节点。

---

## 五、待实现阶段

| 阶段 | 状态 | 说明 |
|------|------|------|
| `requirement_intake` | ✅ 已实现 | 需求准入 + 追问 |
| `requirement_merge` | ✅ 已实现 | 需求合并 |
| `macro_planning` | ✅ 已实现 | TripPlan + Phase 创建 |
| `graph_splitting` | ⏳ 待实现 | Phase→Month→Week→Day 拆分 |
| `day_expansion` | ⏳ 待实现 | POI 验证 + 路线写入 |
| `review` | ⏳ 待实现 | L0-L5 六级审查 |
| `final_output` | ⏳ 待实现 | 逐日详细输出 |

---

*本报告由 agent_v2 E2E 测试自动生成，包含 AG-UI 事件流、服务器日志、Neo4j 图数据三部分原始输出。*
