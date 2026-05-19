# agent_v2 — 全国 365 天自驾旅行规划 Agent

基于 Go + Neo4j 图数据库的 AI Agent 旅行规划系统。多阶段工作流（需求准入 → 需求合并 → 宏观规划 → 图拆分 → 逐日展开 → 审查），将用户的模糊旅行需求转化为结构化的逐日行程。

## 架构概览

```
用户输入 → Orchestrator 阶段推进 →
  requirement_intake (需求准入 + 追问)
  → requirement_merge (需求合并)
  → macro_planning (TripPlan + Phase 创建)
  → graph_splitting (Phase→Month→Week→Day 拆分)
  → day_expansion (POI 验证 + 逐日详细输出)
  → review (L0-L5 六级审查)
```

核心设计：
- **TripPlanID 由 Go 层预生成**，传给 LLM 使用，最后从 Neo4j 确认，不依赖 LLM 文本提取
- **轻量级宏观规划 Agent**：仅 4 工具 + 1 子 Agent（Dili360 地理百科），而非早期的 24 工具 + 12 子 Agent
- **Neo4j 图数据库**：TripPlan → Phase → Month → Week → Day 层级存储，MERGE 幂等写入
- **Phase 完整性校验**：Go 层校验 seq 连续性、dayCount 总和、日期填充等

## 成功样例：全国 365 天自驾

### 输入

```
用户: 全国365天的旅游，预算20万，一人行，自驾游，更喜好自然风光
Agent: 在开始规划前，我需要先确认几项关键信息：
       1. 你计划从哪个城市出发？
       2. 每日节奏希望轻松、均衡，还是尽量多打卡？
       3. 计划什么时候开始？

用户: 从北京出发，6月1日开始，节奏均衡
```

### 工作流执行

| 阶段 | 耗时 | 结果 |
|------|------|------|
| `requirement_intake` | ~9s | 判断信息不足，生成 3 个追问（出发城市、节奏、日期） |
| `requirement_merge` | ~7s | 信息充足，进入宏观规划 |
| `macro_planning` | ~52s | 创建 TripPlan + 7 个 Phase，写入 Neo4j |
| Phase 校验 | <1s | 7 phases, totalDayCount=365 ✅ |

### Neo4j 存储结果

**TripPlan 根节点：**
```
id:          ca4eba4d-a64e-4980-b12d-59372347c287
name:        全国自然风光自驾之旅
totalDays:   365
budget:      200,000 元
startDate:   2024-06-01
transport:   自驾
style:       均衡 / 自然风光
```

**7 个 Phase：**

| seq | 名称 | 区域 | 天数 | 日期 |
|-----|------|------|------|------|
| 1 | 华北初夏自然探索 | 华北 | 45 | 06/01 - 07/15 |
| 2 | 西北盛夏风光之旅 | 西北 | 47 | 07/16 - 08/31 |
| 3 | 西南雨季山水行 | 西南 | 45 | 09/01 - 10/15 |
| 4 | 华南秋日滨海游 | 华南 | 46 | 10/16 - 11/30 |
| 5 | 华东深秋文化自然 | 华东 | 46 | 12/01 - 01/15 |
| 6 | 东北冰雪奇缘 | 东北 | 45 | 01/16 - 03/01 |
| 7 | 华北春日返程 | 华北 | 91 | 03/02 - 05/31 |

`45 + 47 + 45 + 46 + 46 + 45 + 91 = 365` ✅

### 逐日输出示例（Day 39）

```markdown
## 西南雨季山水漫游
**区域**: 西南 | **天数**: 45 天 | **时间**: 2024-09-01 ~ 2024-10-15

### Day 39: 2024-10-09 — 西南雨季山水漫游

**天气概况**: 气温18–24°C，多云转小雨，湿度较高。

**当日行程表**:
| 时间 | 活动 | 地点 | 说明 |
|------|------|------|------|
| 08:00–10:30 | 抵达并游览黄果树瀑布 | 安顺市镇宁县 | 雨季水量充沛，水帘洞可穿行 |
| 11:00–12:30 | 探访天星桥景区 | 黄果树景区内 | 石林、溶洞与溪流交织 |
| 13:00–14:00 | 午餐 | 黄果树镇农家乐 | 酸汤鱼与腊肉炒蕨菜 |
| 14:30–17:00 | 游览陡坡塘瀑布 | 黄果树景区东侧 | 《西游记》取景地 |
| 18:00 | 入住酒店 | 安顺市区 | 休整准备次日行程 |

**推荐景点**:
1. 黄果树大瀑布 — 亚洲最大瀑布之一
2. 天星桥下半段 — 水上石林和天然盆景

**餐饮推荐**: 酸汤鱼、布依族五色糯米饭
**交通方式**: 自驾从贵阳出发约130公里，经沪昆高速(G60)，车程约1.5小时
```

## 项目结构

```
agent_v2/
├── main.go                  # 入口：AG-UI SSE 服务 (127.0.0.1:8088/agui)
├── config.yaml              # LLM API 配置
├── agent/                   # Agent 定义
│   ├── orchestrator.go      # 阶段推进编排器
│   ├── intake_agent.go      # 需求准入 Agent（追问缺失信息）
│   ├── macro_planning_agent.go  # 宏观规划 Agent（4 工具 + Dili360）
│   ├── review_agents.go     # L0-L5 六级审查 Agent
│   ├── workflow_runner.go   # 工作流执行器
│   ├── workflow_checks.go   # Phase 完整性校验
│   ├── model_router.go      # 模型路由（按复杂度分级）
│   └── AmapAgent.go         # 高德地图 Agent
├── graph/                   # Neo4j 图操作
│   ├── operations.go        # 写操作（CREATE/MERGE）
│   └── queries.go           # 读操作（MATCH 查询）
├── tools/                   # 工具集
│   ├── graph_tools.go       # 图读写工具（create_trip_plan, split_parent_node 等）
│   ├── graph_write_tools.go # 图写入工具（write_climate_data 等）
│   └── amap_*.go            # 高德地图 API 工具（POI 搜索/路线/天气等）
├── skills/                  # Skill 定义（Markdown 格式）
│   ├── travel-requirement-intake/  # 需求准入 Skill
│   ├── travel-requirement-merge/   # 需求合并 Skill
│   ├── slow-travel-planner/        # 慢旅行规划 Skill
│   └── review-*/                   # 各层级审查 Skill
├── workflow/                # 工作流定义
├── config/                  # 配置加载
├── cmd/                     # 命令行工具
│   ├── run_travel_agent/    # 运行旅行 Agent
│   ├── amap_live_check/     # 高德 API 连通性测试
│   ├── zhihu_live_check/    # 知乎 API 连通性测试
│   └── bilibili_live_check/ # B站 API 连通性测试
└── doc/                     # 文档与测试输出
    ├── 365-day-test-report.md          # E2E 测试报告（AG-UI 事件流 + Neo4j 数据）
    ├── 365天全国自驾旅行规划.md          # 宏观规划输出（6 Phase 路线图 + 预算）
    └── 365-agent-final-output.txt      # 逐日详细行程（SSE 流式输出）
```

## 快速开始

```bash
# 1. 启动 Neo4j（Docker）
docker run -d --name neo4j -p 7474:7474 -p 7687:7687 \
  -e NEO4J_AUTH=neo4j/password neo4j:5

# 2. 配置 LLM API
cp config.example.yaml config.yaml
# 编辑 config.yaml，填入 API key

# 3. 启动服务
cd agent_v2 && go run .

# 4. 测试
curl -X POST http://127.0.0.1:8088/agui \
  -H "Content-Type: application/json" \
  -d '{"threadId":"test","runId":"run-1","messages":[{"role":"user","content":"全国365天的旅游，预算20万，一人行，自驾游，更喜好自然风光"}]}'
```

## 工具 Agent

| Agent | 用途 |
|-------|------|
| AmapAgent | 高德地图 POI 搜索、路线规划、天气查询 |
| Dili360Agent | 中国国家地理百科，提供景点深度介绍 |
| ZhihuSearchAgent | 知乎游记攻略搜索 |
| BilibiliSearchAgent | B站旅行视频素材搜索 |

## 审查层级

| 层级 | 范围 | 状态 |
|------|------|------|
| L0 | TripPlan 整体 | ✅ 已实现 |
| L1 | Phase 阶段 | ✅ 已实现 |
| L2 | Month 月度 | ⏳ 待实现 |
| L3 | Week 周度 | ⏳ 待实现 |
| L4 | Day 逐日 | ⏳ 待实现 |
| L5 | POI 点位 | ⏳ 待实现 |