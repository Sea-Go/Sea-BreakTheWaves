// Package server 实现 HTTP/gRPC/Kafka/AG-UI 入口适配。
//
// 包职责:
//
// 该包装配推荐系统的对外入口：HTTP server（推荐/搜索/频道/事件/AG-UI 流式
// 端点）、gRPC server（供 Sea-RideTheWind 网关 RPC 调用）、Kafka consumer
// （异步行为事件消费）、AG-UI（Adapter/Translator/Runner/Service，实时推送
// 流式响应）。所有入口通过依赖注入装配 Orchestrator 与各 Agent，配置驱动
// 启用/关闭。前端只通过 Sea-RideTheWind 网关调用，不直连 reco。
//
// 核心 interface:
//   - Server: server 契约，Start/Stop
//   - Handler: 请求处理 interface（推荐/搜索/频道/事件）
//   - Streamer: AG-UI 流式响应 interface
//
// 二开扩展点:
//   - 替换 server 实现: 实现 Server interface 注入自定义 HTTP/gRPC 框架
//   - 自定义 Handler: 实现 Handler interface 扩展请求处理逻辑
//   - 流式协议: 实现 Streamer interface 自定义 AG-UI 流式协议
package server
