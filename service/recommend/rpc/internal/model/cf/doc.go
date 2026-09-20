// Package cf 实现协同过滤：User-CF/Item-CF/ALS MF/实时增量/冷启动。
//
// 包职责:
//
// 该包提供 CFService 实现 Recaller interface（定义于 domain 包）接入快路径
// 与慢路径召回源。包含 User-CF（用户行为序列向量化 + 余弦相似度 + Top-N
// 相似用户 + 推荐未看文章）、Item-CF（文章共现矩阵 + Jaccard/余弦 + 相似
// 文章）、ALS MF（64/128 维隐因子模型 + 点积预测）、实时增量（流式更新
// 共现矩阵 + 1h 全量/5min 增量 ALS）、冷启动（新用户画像召回 + 热门兜底；
// 新文章内容向量找相似文章的 CF 分数迁移）。CFFeedbackHook 写入共现矩阵
// 与图谱 CO_OCCURRED_WITH 边，触发增量 ALS。
//
// 核心 interface:
//   - CFService: 协同过滤服务，实现 Recaller interface
//   - SimilarityCalculator: 相似度计算 interface（余弦/Jaccard/点积）
//   - MFModel: 矩阵分解模型 interface（训练/预测/增量更新）
//
// 二开扩展点:
//   - 新增 CF 算法: 实现 CFService/Recaller interface 注册新算法
//   - 自定义相似度: 实现 SimilarityCalculator interface 注入业务相似度
//   - 冷启动策略: 实现冷启动 interface 自定义新用户/新文章推荐逻辑
package cf
