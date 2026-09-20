// Package search 实现搜索增强栈（含图谱知识与图片搜索）。
//
// 包职责:
//
// 该包实现 Searcher interface（定义于 domain 包），提供完整搜索增强栈：
// 语义混合检索（Milvus hybrid: dense + sparse + rerank field + BM25 + 元数据
// 过滤）、查询理解（intent/实体/时效，WithStructuredOutputJSONSchema）、
// 查询重写（同义/拼改/相关）、个性化搜索（注入长期/短期/周期兴趣）、
// 搜索过滤（标签/类型/IP 频道/作者/时间/质量阈值）、搜索补全（前缀 + 热搜
// + 个性化，缓存 < 100ms）、搜索历史与点击反馈、图谱知识搜索（实体识别后
// 查图谱获取关联文章/作者/IP，结果含 GraphKnowledge）、图片搜索（以图搜文：
// 图片节点 → 视觉相似 → 文章）。SearchAgent 作为慢路径搜索子图。
//
// 核心 interface:
//   - Searcher: 搜索 interface（定义于 domain 包），Search(ctx, SearchQuery)
//   - Registry: 搜索器注册表
//   - QueryUnderstander: 查询理解 interface
//   - QueryRewriter: 查询重写 interface
//
// 二开扩展点:
//   - 注册 Searcher: 实现 Searcher interface 并通过 Register 注入
//   - 自定义查询理解: 实现 QueryUnderstander interface 扩展 intent/实体识别
//   - 自定义重写策略: 实现 QueryRewriter interface 注入业务同义词
package search
