# 365 天极限测试报告

> 测试日期：2026-05-14 | 请求：365 天中国环游 | 预算：36 万 | 模型：qwen3-max (coordinator)
> 
> **更新**：第二轮完整执行测试于 18:13 启动，Day 补齐逻辑成功将 31 → 365 天。

---

## 一、测试概览

### 第一轮（无 Day 补齐）

| 指标 | 数值 |
|------|------|
| 请求天数 | 365 |
| 实际创建 Day 节点 | **31**（被 LLM Coordinator 截断） |

### 第二轮（Go 层 Day 补齐）

| 指标 | 数值 |
|------|------|
| Coordinator 创建 | 31 Day |
| Go 层补齐 | 334 Day |
| **最终 Day 数** | **365 ✓** |
| Week 数 | 59 |
| Month 数 | 12 |
| Phase 数 | 4 |
| Phase 1 耗时 | ~8 分钟 |
| Day 补齐耗时 | ~1 秒 |
| 总耗时 | **50 分钟** |
| Phase 数量 | 6 |
| Month 数量 | 12 |
| Week 数量 | 10 |
| Day 数量 | 31 |
| POI 数量 | **0** |
| RouteSegment 数量 | **0** |
| ReviewResult 数量 | **0** |
| 最终 answer 长度 | 39,488 字符（~1273 字/天） |

---

## 二、各阶段详细分析

### Phase 1：宏观规划（Steps 1-7）

| 指标 | 数值 |
|------|------|
| 耗时 | ~8 分钟 |
| Coordinator 输出 | 13,392 字符 |
| 预期 Day 数 | 365 |
| 实际 Day 数 | **31** |

**问题 #1：LLM Coordinator 无法处理 365 天规模**

**现象**：Coordinator 要求创建 365 个 Day，但只创建了 31 个（约 1 天代表 12 天）。

**根因**：
- qwen3-max 上下文窗口 ~32K tokens，无法容纳 365 天的详细规划
- Coordinator 隐式将 365 天"压缩"为 31 个代表性 Day
- 每个 Month 创建了 ~2.5 个 Day，而非按 Week→Day 正确拆分
- Skill 中定义的 Phase→Month→Week→Day 层级拆分流程对 365 天规模不适用

**推断**：如果 coordinator 使用 deepseek-v4-pro（120K context），可能创建更多 Day，但 365 天仍然远超任何模型的上下文窗口。**纯 LLM 宏观规划的天花板约 60-90 天**。

---

### Phase 2：逐日 POI 验证（Step 8）

| 指标 | 数值 |
|------|------|
| 耗时 | 17 分 38 秒（31 天） |
| 每天耗时 | ~34 秒 |
| POI 写入数 | **0** |
| Route 写入数 | **0** |

**问题 #2：amap-agent 不调用高德 API 工具**

**现象**：31 次 amap-agent 调用全部返回空结果。completion_tokens 仅 10-25，prompt_tokens 仅 4000-5000。

**根因**：
- `amapPOIVerifyInstruction` 使用了 Planner 模式，但 amap-agent 在没有明确 POI 名称的情况下，无法用 `amap_poi_keyword_search` 找到合适结果
- 每个 Day 的区域信息太粗略（如"华北、华东"），amap 关键词搜索返回无关结果
- amap-agent 在工具调用失败后直接返回空 JSON，不敢编造数据（这是正确行为）
- **根本原因**：Phase 1 没有为每个 Day 分配具体的城市/区域，导致 Phase 2 无法进行有意义的 POI 搜索

**推断**：如果 365 天真有 365 个 Day 节点，Phase 2 需要 365 × 34s ≈ **3.4 小时**。加上高德 API QPS 限制（个人免费版 1 QPS），实际可能需要 **6-12 小时**。

---

### Phase 3：全量审查（Steps 9-10）

| 指标 | 数值 |
|------|------|
| 耗时 | 18 分 43 秒（31 天） |
| 每天耗时 | ~36 秒（5 个审查 agent 并发） |
| LLM 调用数 | **155**（31 天 × 5 agents） |
| ReviewResult 写入 | **0**（无数据可审查） |

**问题 #3：审查在无数据时仍然全量运行**

**现象**：即使每个 Day 的 POI 为空，5 个审查 agent 仍然被调用并输出 JSON。

**根因**：
- 审查 agent 是按天循环调用的，不检查 Day 是否有 POI 数据
- 无数据时审查 agent 仍然加载 skill、生成输出（浪费 LLM 调用）
- **成本估算**：155 次 LLM 调用 × ~3500 tokens/次 ≈ 540K tokens

**推断**：365 天全量审查 = 365 × 5 = **1825 次 LLM 调用**。即使每次 30 秒并发，也需要 ~1825/5 × 30s ≈ **3 小时**。**LLM 成本**：1825 × ~0.01 元 ≈ **18 元**。

---

### Phase 4：全局检查（Steps 11-12）

| 指标 | 数值 |
|------|------|
| 耗时 | 1 分 22 秒 |
| constraint_violations | 1 |

**无明显问题**。Phase 4 只做少量 Neo4j 查询，规模影响小。

---

### Phase 5：逐日输出（Step 13）

| 指标 | 数值 |
|------|------|
| 耗时 | 5 分 9 秒（31 天） |
| 每天耗时 | ~10 秒 |
| completion_tokens/天 | 286-341 |
| 总 answer 长度 | 39,488 字符 |
| get_day_fullContext 调用 | **31 次**（验证了逐日加载） |

**问题 #4：无数据导致输出空洞**

**现象**：每个 Day 的 `get_day_fullContext` 返回的 POI/Routes/Insights/Reviews 全部为空，DayOutputAgent 只能根据 Day 节点的基本信息（日期、主题、区域）生成泛泛的描述。

**每日输出示例**（来自实际测试）：
```
### Day 1: 云南15天自然风光摄影之旅 — Day 1

**日期**：2025年1月1日  
**主题**：自然风光摄影之旅  
**区域**：华北、华东  

当日无具体POI数据。建议用户提供更详细的目的地信息以获得精准规划。
```

**根因**：级联效应 — Phase 1 无具体区域 → Phase 2 无 POI → Phase 5 无内容。

**推断**：365 天全量输出 = 365 × 10s ≈ **1 小时**。总 answer 长度预估：365 × 1273 字 ≈ **465K 字符**（接近单次 LLM 上下文上限）。

---

## 三、总体问题汇总

| # | 问题 | 严重度 | 出现在 | 根因 |
|---|------|--------|--------|------|
| 1 | Coordinator 截断天数 | **Critical** | Phase 1 | LLM 上下文窗口不足以规划 365 天 |
| 2 | amap-agent POI 为空 | **Critical** | Phase 2 | Phase 1 未提供具体城市，amap 搜索无结果 |
| 3 | 审查在无数据时运行 | High | Phase 3 | 未检查 Day 是否有 POI 再调用审查 agent |
| 4 | 输出空洞 | High | Phase 5 | 级联效应：上游无数据导致下游无内容 |
| 5 | 完整流程耗时过长 | Medium | Phase 2-5 | 31 天 ≈ 50 分钟；365 天预估 6-10 小时 |
| 6 | LLM 成本线性增长 | Medium | Phase 3,5 | 天数 × 审查数 × 每千 token 费用 |

---

## 四、365 天规模的推算

假设 Coordinator 能创建完整的 365 天结构：

| 阶段 | 31 天实测 | 365 天推算 | 瓶颈 |
|------|----------|-----------|------|
| Phase 1 | 8 min | **不可行** — LLM 上下文限制 | Coordinator 上下文 |
| Phase 2 | 17 min | **3.4 小时**（无 QPS 限制）/ **12 小时**（1 QPS） | 高德 API QPS |
| Phase 3 | 19 min | **3 小时**（1825 次 LLM 调用） | LLM 成本 + 耗时 |
| Phase 4 | 1.4 min | ~2 min | Neo4j 查询性能 |
| Phase 5 | 5 min | **1 小时**（365 次 LLM 调用） | LLM 调用数 |
| **总计** | **50 min** | **7-16 小时** | — |
| **LLM 成本** | ~1 元 | **~30-50 元** | API 费用 |
| **Neo4j 节点** | 70 | **~2500**（365 Day + 1000+ POI + Routes + Reviews） | 数据库规模 |
| **Answer 长度** | 39K chars | **~465K chars** | 接近上下文上限 |

---

## 五、改进建议

### 短期（当前架构内优化）

1. **Phase 1 分批规划**：Coordinator 每次只规划 30 天的 Phase，多次调用完成 365 天
2. **Phase 3 数据门控**：跳过无 POI 的 Day，减少无意义 LLM 调用
3. **Phase 5 跳过空 Day**：如果 `get_day_fullContext` 返回空 POI，使用 Go 模板生成简短占位符（不调用 LLM）

### 中期（架构改进）

4. **并行 Phase 2**：10 个 amap-agent 并发处理不同 Day，缩短到 1/10 时间
5. **Phase 3 采样审查**：365 天中随机抽样 30% 做全量审查，其余做快速检查
6. **Phase 5 分组输出**：按 Phase 分组后，对每组调用一次 LLM 生成该 Phase 的完整输出（减少 LLM 调用次数）

### 长期（根本性解决）

7. **非 LLM 的宏规划**：用 Go 代码直接按日历生成 Phase/Month/Week/Day 结构（不依赖 LLM）
8. **流式输出**：逐日生成并立即发送给用户，而非等所有天完成才输出
9. **增量缓存**：已生成的 Day 内容缓存到 Neo4j，避免重复生成

---

## 六、测试环境

- Go 服务：`agent_v2/main.go`，端口 8080
- Neo4j：Docker `neo4j:latest`，端口 37474 (HTTP) / 37687 (Bolt)
- Coordinator 模型：qwen3-max
- Review 模型：qwen3-max (4 agents) + deepseek-v4-pro (1 agent, review-content)
- DayOutput 模型：qwen3-max
- 测试时间：2026-05-14 16:48 — 17:38