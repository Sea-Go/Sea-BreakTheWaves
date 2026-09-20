// Package cpu 实现 CPU 与速度优化。
//
// 包职责:
//
// 该包提供 CPU Optimizer：并行召回（GraphAgent 并行边 + errgroup + ants 池
// 2×CPU）、pprof（HTTP 端点 + 持续火焰图采样）、批处理（Milvus 批量查询 +
// 向量批量计算 + CF 批量相似度）、三级缓存（L1 LRU 内存 + L2 Redis + L3
// Prompt Cache）、异步 I/O（Kafka 事件异步消费 + Hook 异步 + 反馈异步写入）、
// automaxprocs（GOMAXPROCS 与配额匹配）、热路径零分配（逃逸分析 + sync.Pool）。
// 快路径天然低 CPU（无 LLM），双路径分流本身降 CPU。
//
// 核心 interface:
//   - Optimizer: CPU 优化器 interface，并行/批处理/缓存统一入口
//   - Pool: 协程池 interface（基于 ants）
//   - Cache: 三级缓存 interface（L1/L2/L3）
//
// 二开扩展点:
//   - 自定义池策略: 实现 Pool interface 替换默认 ants 池配置
//   - 自定义缓存: 实现 Cache interface 接入自定义缓存后端
//   - pprof 端点: 通过配置开启/关闭 pprof HTTP 端点
package cpu
