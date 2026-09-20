// Package repo 实现数据仓储层，interface 化聚合多数据源。
//
// 包职责:
//
// 该包定义仓储 interface 并提供实现，聚合 5 类数据源：Postgres（文章/
// 用户/行为）、Milvus（向量）、Redis（缓存/池）、Neo4j（图谱）、Kafka
// （事件流）。UserRepository interface 聚合多仓库，子错误通过 PartialErrors
// 聚合返回。所有仓储以 interface 暴露，便于 mock 与二开替换。
//
// 核心 interface:
//   - UserRepository: 用户仓储 interface，Load/UpdateDynamic/UpdateTemporal
//   - ArticleRepository: 文章仓储 interface
//   - PoolRepository: 候选池仓储 interface
//   - HistoryRepository: 用户历史仓储 interface
//
// 二开扩展点:
//   - 替换仓储实现: 实现 interface 并通过依赖注入替换默认实现
//   - 新增数据源: 实现对应仓储 interface 接入新数据源
//   - PartialErrors: 多源聚合时部分失败通过 PartialErrors 返回，不中断主流程
package repo
