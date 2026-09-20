// Package domain 定义推荐系统的核心领域模型与全部中央契约 interface。
//
// 包职责:
//
// 该包是双路径推荐架构（fast/slow/hybrid）的领域中枢，统一抽象用户画像、
// 时间画像、行为事件、推荐请求/响应、知识图谱节点/边、文章质量、频道、
// 搜索协议、Skill 清单等核心类型，并集中声明 14 个核心 interface，供
// recall/rank/rerank/graph/search/profile/channel/skill/agent 等模块实现与二开。
// 所有 interface 定义见 interfaces.go；所有支撑类型定义见 types.go。
//
// 核心 interface（定义于 interfaces.go）:
//   - Recaller: 召回 interface，传统推荐快路径核心契约
//   - Ranker: 排序 interface，传统推荐快路径核心契约
//   - Reranker: 重排 interface，自研/外部 rerank 统一契约
//   - QualityJudger: 文章质量评判 interface，6 维评分 + Best-of-N
//   - GraphQuerier: 知识图谱查询 interface，Cypher/召回/实体链接/图片搜索
//   - Searcher: 搜索 interface，语义混合检索统一契约
//   - ProfileManager: 用户画像管理 interface，三层画像 + 时间画像
//   - ChannelManager: IP 频道管理 interface，频道注册/独立池/路由
//   - FeedbackConsumer: 反馈消费 interface，行为事件闭环
//   - SkillRegistry: Skill 注册体系 interface，50+ Skill 声明式二开
//   - AgentFactory: Agent 工厂 interface，12 个 Agent 统一构建
//   - Orchestrator: 编排 interface，双路径路由与融合
//   - EventEmitter: 事件发射 interface，行为事件统一出口
//   - HookRegistry: Hook 注册 interface，二开扩展点核心入口
//
// 二开扩展点:
//   - 实现 interface: 业务方实现任一 interface 并通过对应 Register 注入即可扩展
//   - 替换实现: 通过依赖注入替换默认实现（如自研 Reranker 替换外部 rerank）
//   - 新增类型: 通过 Skill 清单声明新能力，无需修改本包
package domain
