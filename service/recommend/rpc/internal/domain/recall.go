package domain

// ============================================================================
// 该文件定义召回相关的辅助类型。
// Recaller interface 定义于 interfaces.go；RecallRequest/RecallResult/Candidate
// 等核心类型定义于 types.go（字段已完整，无需补充）。
// 本文件仅补充召回来源标识类型，供各 Recaller 实现统一引用，避免硬编码字符串
// 导致的拼写不一致问题。
// ============================================================================

// RecallSource 召回来源标识，用于 RecallResult.Source 与 Candidate.Source。
// 各 Recaller 实现应使用本类型常量赋值，确保来源标识全局一致。
type RecallSource string

const (
	// RecallSourceRule 规则召回（热点/最新/编辑精选）。
	RecallSourceRule RecallSource = "rule"
	// RecallSourceContent 内容召回（向量 + BM25 融合）。
	RecallSourceContent RecallSource = "content"
	// RecallSourceCF 协同过滤召回（User-CF/Item-CF/MF）。
	RecallSourceCF RecallSource = "cf"
	// RecallSourceGraph 图谱召回（Neo4j 多跳）。
	RecallSourceGraph RecallSource = "graph"
	// RecallSourceChannel 频道召回（频道独立池）。
	RecallSourceChannel RecallSource = "channel"
	// RecallSourceHybrid 混合召回（多源融合）。
	RecallSourceHybrid RecallSource = "hybrid"
)
