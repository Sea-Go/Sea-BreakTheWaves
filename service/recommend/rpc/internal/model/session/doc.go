// Package session 封装 trpc-agent-go 的 Session/Memory/Summary 能力。
//
// 包职责:
//
// 该包封装 trpc-agent-go 的 Session（Memory/Summary/boundary/filterKey），
// 提供基于 Memory 模块的增量更新（写入 {user:topics}/{user:tier}/{user:tags}/
// {user:long_term}/{user:short_term}/{user:periodic}）、异步 Session Summary
// 压缩（SummaryBoundary + session_search/session_load 恢复）、ChannelFilterKey
// 频道切换隔离。默认异步 Summary 不阻塞主链路，仅长 ReAct loop 同步。
//
// 核心 interface:
//   - MemoryService: 记忆服务，UpdateUserState/Load/Save
//   - SummaryService: Summary 压缩服务，Compress/Load/Boundary
//   - FilterKey: 频道 filterKey 隔离策略
//
// 二开扩展点:
//   - 自定义 Summary 策略: 实现 SummaryService interface 注入压缩逻辑
//   - 自定义 Memory 后端: 实现 MemoryService interface 切换存储
//   - filterKey 策略: 实现 FilterKey interface 自定义频道隔离规则
package session
