// Package obs 实现统一可观测性：OTel GenAI + Prometheus + 结构化日志。
//
// 包职责:
//
// 该包提供三类观测能力：OTel GenAI span（含 fast_path/slow_path/hybrid/
// graph_query/cypher 节点）、Prometheus metrics（推荐关键指标 + 成本 +
// CPU + CF + rerank + 画像 + 图谱 + 双路径分布）、结构化 JSON 日志（携带
// OTel trace_id，可按 trace_id/channel/user_id 检索）。删除旧 zlog/
// AgentTrace 双 ID 层，提供薄封装。通过 trpc-agent-go Callbacks 接入
// Agent 循环，发射对应 BehaviorEvent。
//
// 核心 interface:
//   - Tracer: 链路追踪封装，StartSpan/EndSpan
//   - MetricsCollector: 指标采集封装，Counter/Histogram/Gauge
//   - Logger: 结构化日志封装
//   - CostCallback: 成本回调，记录 token/费用/Prompt Cache 命中
//
// 二开扩展点:
//   - 新增 collector: 实现 MetricsCollector interface 暴露自定义指标
//   - 新增 Callback: 实现 trpc-agent-go Callbacks 接入 Agent 循环
//   - 日志目标: 通过 Logger 配置切换 ES/Filebeat/本地文件
package obs
