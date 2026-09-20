package domain

// ============================================================================
// 该文件定义排序（Rank）相关的辅助类型。
//
// Ranker interface 定义于 interfaces.go；RankContext / RankResult / Candidate
// 定义于 types.go（RankContext 已含 Profile/TemporalProfile/Channel/Intent/
// Candidates 字段，无需补充）。本文件补充排序辅助类型 FeatureMap，供
// internal/rank 包的排序器（WeightedRanker/LRRanker/GBDTRanker）使用。
//
// 二开扩展点：业务方可通过 Candidate.Scores 注入自定义特征，排序器按
// weights 加权计算；亦可直接使用 FeatureMap 封装特征并扩展方法。
// ============================================================================

// FeatureMap 特征映射，用于排序器特征工程。
//
// 键为特征名（如 "cf_score"/"graph_score"/"freshness"/"quality"），
// 值为特征值。与 Candidate.Scores 结构一致，提供安全访问方法。
//
// 二开扩展点：业务方可实现自定义特征提取逻辑，将多源分数（召回/CF/图谱/
// 质量分/新鲜度）汇入 FeatureMap，再交由排序器加权计算。
type FeatureMap map[string]float64

// Get 安全获取特征值。
// 若 FeatureMap 为 nil 或特征不存在，返回 0。
// 排序器在 Candidate.Scores 为 nil 时仍可正常工作（所有特征视为 0）。
func (f FeatureMap) Get(name string) float64 {
	if f == nil {
		return 0
	}
	return f[name]
}

// Set 设置特征值。若 FeatureMap 为 nil 会 panic，调用方应先初始化。
func (f FeatureMap) Set(name string, value float64) {
	f[name] = value
}

// Has 判断特征是否存在。
func (f FeatureMap) Has(name string) bool {
	if f == nil {
		return false
	}
	_, ok := f[name]
	return ok
}
