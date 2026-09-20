// Package graph 实现 Neo4j 知识图谱基础设施。
//
// 包职责:
//
// 该包实现 GraphQuerier interface（定义于 domain 包），建立大图谱（12 节点
// 类型：Article/Tag/TypeTag/Author/IP/Channel/Topic/Entity/Image/User/
// Behavior/QualityReport；多类关系：HAS_TAG/WROTE_BY/BELONGS_IP/LIKED/
// CO_OCCURRED_WITH/VISUALLY_SIMILAR 等）。提供 Schema 初始化、Client、
// Operations（upsert/link）、Cypher 生成与执行、实体链接、图片节点与视觉
// 相似。供图谱召回、LLM 图谱查询、搜索知识、图片关联使用。
//
// 核心 interface:
//   - GraphQuerier: 图谱查询 interface（定义于 domain 包）
//     QueryByCypher/RecallByGraph/EntityLink/ImageSearch
//   - Client: Neo4j 客户端（基于 neo4j-go-driver/v5）
//
// 二开扩展点:
//   - 新增 Cypher 模板: 在 cypher.go 增加预定义查询模板，参数化防注入
//   - 新增节点/关系类型: 在 schema.go 扩展 Schema，幂等初始化
//   - 替换实现: 实现 GraphQuerier interface 注入自定义图谱后端
package graph
