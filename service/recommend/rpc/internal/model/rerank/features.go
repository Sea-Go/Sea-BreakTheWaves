package rerank

import (
	"context"
	"math"
	"time"

	"sea/service/recommend/rpc/internal/domain"
)

// ============================================================================
// features.go 实现自研重排的特征工程，提取 6 类特征供 LambdaMART/Cross-encoder
// 等模型消费。特征来源：候选 Extra（title/summary/tags/published_at/...）、
// 候选 Scores（行为率/质量分）、用户画像（兴趣/频道/点赞标签）、时间画像
// （周期槽位/衰减状态）。所有特征均容错缺失字段（默认 0），确保离线可编译。
// ============================================================================

// FeatureVector 特征向量，键为特征名，值为特征值（float64）。
type FeatureVector map[string]float64

// Features 6 类特征集合，覆盖文本/行为/时间/质量/画像/多样性。
// 注：每类特征以 FeatureVector 表达（spec 中递归类型在 Go 不合法，故用 FeatureVector）。
type Features struct {
	Text      FeatureVector // 文本特征：标题长度/关键词匹配/摘要相关性
	Behavior  FeatureVector // 行为特征：点击率/点赞率/收藏率/完成率
	Time      FeatureVector // 时间特征：时效性/周期匹配/衰减
	Quality   FeatureVector // 质量特征：6 维质量分数（Authority/Depth/Freshness/Completeness/Readability/Citation）
	Profile   FeatureVector // 画像特征：兴趣匹配度/频道匹配/标签匹配
	Diversity FeatureVector // 多样性特征：与已选文章的相似度（多样性惩罚）
}

// FeatureExtractor 特征提取器，从候选文章/用户画像/时间画像提取 6 类特征。
type FeatureExtractor struct{}

// NewFeatureExtractor 创建特征提取器。
func NewFeatureExtractor() *FeatureExtractor {
	return &FeatureExtractor{}
}

// Extract 提取全部 6 类特征。
// ctx 上下文；candidate 候选文章；profile 用户画像；temporal 时间画像。
// 返回 Features（6 类特征集合）。
func (fe *FeatureExtractor) Extract(ctx context.Context, candidate domain.Candidate, profile domain.UserProfile, temporal domain.TemporalProfile) Features {
	_ = ctx
	return Features{
		Text:      fe.extractTextFeatures(candidate),
		Behavior:  fe.extractBehaviorFeatures(candidate),
		Time:      fe.extractTimeFeatures(candidate, temporal),
		Quality:   fe.extractQualityFeatures(candidate),
		Profile:   fe.extractProfileFeatures(candidate, profile),
		Diversity: fe.extractDiversityFeatures(candidate),
	}
}

// extractTextFeatures 文本特征：标题长度/关键词匹配/摘要相关性。
func (fe *FeatureExtractor) extractTextFeatures(c domain.Candidate) FeatureVector {
	fv := FeatureVector{}
	title := stringField(c, "title")
	summary := stringField(c, "summary")
	tags := stringSliceField(c, "tags")
	fv["title_length"] = float64(len(title))
	fv["title_log_length"] = math.Log1p(float64(len(title)))
	fv["summary_length"] = float64(len(summary))
	fv["tag_count"] = float64(len(tags))
	// 标签密度（标签数 / 标题长度，归一化）
	fv["tag_density"] = 0.0
	if len(title) > 0 {
		fv["tag_density"] = float64(len(tags)) / float64(len(title))
	}
	// 标题-摘要长度比（摘要相关性近似）
	fv["title_summary_ratio"] = 0.0
	if len(summary) > 0 {
		fv["title_summary_ratio"] = float64(len(title)) / float64(len(summary))
	}
	return fv
}

// extractBehaviorFeatures 行为特征：点击率/点赞率/收藏率/完成率。
func (fe *FeatureExtractor) extractBehaviorFeatures(c domain.Candidate) FeatureVector {
	fv := FeatureVector{
		"click_rate":      scoreField(c, "click_rate"),
		"like_rate":       scoreField(c, "like_rate"),
		"favorite_rate":   scoreField(c, "favorite_rate"),
		"completion_rate": scoreField(c, "completion_rate"),
		"dislike_rate":    scoreField(c, "dislike_rate"),
	}
	// 综合行为分（加权融合，dislike 为负向）
	fv["behavior_overall"] = fv["click_rate"]*0.3 +
		fv["like_rate"]*0.25 +
		fv["favorite_rate"]*0.2 +
		fv["completion_rate"]*0.25 -
		fv["dislike_rate"]*0.1
	return fv
}

// extractTimeFeatures 时间特征：时效性/周期匹配/衰减。
func (fe *FeatureExtractor) extractTimeFeatures(c domain.Candidate, temporal domain.TemporalProfile) FeatureVector {
	fv := FeatureVector{}
	publishedAt := timeField(c, "published_at")
	if publishedAt.IsZero() {
		// 发布时间未知，给中性时效分
		fv["freshness"] = 0.5
		fv["age_hours"] = 0.0
	} else {
		ageHours := time.Since(publishedAt).Hours()
		if ageHours < 0 {
			ageHours = 0
		}
		fv["age_hours"] = ageHours
		// 时效性：7 天内高，指数衰减 R = exp(-age / 7d)
		fv["freshness"] = math.Exp(-ageHours / (24 * 7))
	}
	// 周期匹配：候选 tags 与当前时间槽位 PeriodicPattern 兴趣的加权重合度
	tags := stringSliceField(c, "tags")
	slot := currentPeriodSlot(time.Now())
	fv["periodic_match"] = periodicMatch(tags, temporal.PeriodicPattern, slot)
	// 衰减：候选 tags 在 DecayState 中的衰减后分数均值
	fv["decay_score"] = meanDecayScore(tags, temporal.DecayState)
	return fv
}

// extractQualityFeatures 质量特征：6 维质量分数。
func (fe *FeatureExtractor) extractQualityFeatures(c domain.Candidate) FeatureVector {
	fv := FeatureVector{
		"quality_authority":    scoreField(c, "quality_authority"),
		"quality_depth":        scoreField(c, "quality_depth"),
		"quality_freshness":    scoreField(c, "quality_freshness"),
		"quality_completeness": scoreField(c, "quality_completeness"),
		"quality_readability":  scoreField(c, "quality_readability"),
		"quality_citation":     scoreField(c, "quality_citation"),
	}
	// 综合质量分（等权平均）
	fv["quality_overall"] = (fv["quality_authority"] + fv["quality_depth"] +
		fv["quality_freshness"] + fv["quality_completeness"] +
		fv["quality_readability"] + fv["quality_citation"]) / 6.0
	return fv
}

// extractProfileFeatures 画像特征：兴趣匹配度/频道匹配/标签匹配。
func (fe *FeatureExtractor) extractProfileFeatures(c domain.Candidate, profile domain.UserProfile) FeatureVector {
	fv := FeatureVector{}
	tags := stringSliceField(c, "tags")
	channel := stringField(c, "channel")
	// 兴趣匹配度：候选 tags 与用户静态+动态兴趣的 Jaccard 重合度
	interests := userInterests(profile)
	fv["interest_match"] = jaccard(tags, interests)
	// 频道匹配：候选频道与用户当前频道是否一致
	fv["channel_match"] = 0.0
	if profile.Key.Channel != "" && profile.Key.Channel == channel {
		fv["channel_match"] = 1.0
	}
	// 标签匹配：候选 tags 与用户点赞标签的重合度（带频次加权）
	fv["liked_tag_match"] = likedTagMatch(tags, profile)
	// 不感兴趣标签惩罚
	fv["disliked_tag_penalty"] = dislikedTagMatch(tags, profile)
	return fv
}

// extractDiversityFeatures 多样性特征：与已选文章的相似度（多样性惩罚）。
// 已选文章 ID 列表由调用方写入 candidate.Extra["selected_ids"]。
func (fe *FeatureExtractor) extractDiversityFeatures(c domain.Candidate) FeatureVector {
	fv := FeatureVector{}
	selected := stringSliceField(c, "selected_ids")
	fv["selected_count"] = float64(len(selected))
	// 与已选文章的最大相似度（越高越不多样，惩罚越大）
	// 简化：用 ID 字符集 Jaccard 相似度近似；实际应基于 embedding 余弦相似度
	maxSim := 0.0
	for _, sid := range selected {
		if sid == c.ArticleID {
			continue
		}
		sim := idSimilarity(c.ArticleID, sid)
		if sim > maxSim {
			maxSim = sim
		}
	}
	fv["max_similarity"] = maxSim
	fv["diversity_penalty"] = maxSim
	fv["tag_diversity"] = 1.0 - maxSim
	// 标签重合度（与已选文章的近似重叠）
	fv["tag_overlap"] = maxSim * float64(len(stringSliceField(c, "tags")))
	return fv
}

// ---- 辅助函数：字段读取 ----

// stringField 从候选 Extra 中读取字符串字段。
func stringField(c domain.Candidate, key string) string {
	if c.Extra == nil {
		return ""
	}
	v, ok := c.Extra[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// stringSliceField 从候选 Extra 中读取字符串切片字段（兼容 []string 与 []any）。
func stringSliceField(c domain.Candidate, key string) []string {
	if c.Extra == nil {
		return nil
	}
	v, ok := c.Extra[key]
	if !ok {
		return nil
	}
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			if str, ok := e.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// scoreField 从候选 Scores 或 Extra 中读取 float64 特征值（缺失返回 0）。
func scoreField(c domain.Candidate, key string) float64 {
	if c.Scores != nil {
		if v, ok := c.Scores[key]; ok {
			return v
		}
	}
	if c.Extra != nil {
		if v, ok := c.Extra[key]; ok {
			switch n := v.(type) {
			case float64:
				return n
			case float32:
				return float64(n)
			case int:
				return float64(n)
			case int64:
				return float64(n)
			}
		}
	}
	return 0.0
}

// timeField 从候选 Extra 中读取时间字段。
func timeField(c domain.Candidate, key string) time.Time {
	if c.Extra == nil {
		return time.Time{}
	}
	v, ok := c.Extra[key]
	if !ok {
		return time.Time{}
	}
	switch t := v.(type) {
	case time.Time:
		return t
	case *time.Time:
		if t == nil {
			return time.Time{}
		}
		return *t
	}
	return time.Time{}
}

// ---- 辅助函数：时间画像计算 ----

// currentPeriodSlot 根据当前小时返回时间槽位键（如 "09-12"，对应 PeriodicPattern）。
func currentPeriodSlot(t time.Time) string {
	h := t.Hour()
	switch {
	case h >= 0 && h < 6:
		return "00-06"
	case h < 9:
		return "06-09"
	case h < 12:
		return "09-12"
	case h < 14:
		return "12-14"
	case h < 18:
		return "14-18"
	case h < 21:
		return "18-21"
	default:
		return "21-24"
	}
}

// periodicMatch 计算候选 tags 与当前槽位 PeriodicPattern 兴趣的加权重合度（0-1）。
func periodicMatch(tags []string, pattern domain.PeriodicPattern, slot string) float64 {
	entries := pattern.Get(slot)
	if len(entries) == 0 || len(tags) == 0 {
		return 0.0
	}
	tagSet := make(map[string]bool, len(tags))
	for _, t := range tags {
		tagSet[t] = true
	}
	total := 0.0
	matched := 0.0
	for _, e := range entries {
		w := e.Weight
		if w <= 0 {
			w = 1.0
		}
		total += w
		if tagSet[e.Tag] {
			matched += w
		}
	}
	if total == 0 {
		return 0.0
	}
	return matched / total
}

// meanDecayScore 计算候选 tags 在 DecayState 中的衰减后分数均值。
func meanDecayScore(tags []string, decay map[string]domain.DecayState) float64 {
	if len(tags) == 0 || len(decay) == 0 {
		return 0.0
	}
	sum := 0.0
	cnt := 0
	for _, t := range tags {
		if s, ok := decay[t]; ok {
			sum += s.Score
			cnt++
		}
	}
	if cnt == 0 {
		return 0.0
	}
	return sum / float64(cnt)
}

// ---- 辅助函数：画像匹配 ----

// userInterests 收集用户静态+动态兴趣标签（去重）。
func userInterests(profile domain.UserProfile) []string {
	set := make(map[string]bool)
	if profile.Static != nil {
		for _, t := range profile.Static.Interests {
			set[t] = true
		}
		for _, t := range profile.Static.Tags {
			set[t] = true
		}
	}
	if profile.Dynamic != nil {
		for _, t := range profile.Dynamic.Topics {
			set[t] = true
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	return out
}

// jaccard 计算两个字符串切片的 Jaccard 相似度。
func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}
	setA := make(map[string]bool, len(a))
	for _, x := range a {
		setA[x] = true
	}
	inter := 0
	for _, x := range b {
		if setA[x] {
			inter++
		}
	}
	if inter == 0 {
		return 0.0
	}
	union := len(a) + len(b) - inter
	return float64(inter) / float64(union)
}

// likedTagMatch 计算候选 tags 与用户点赞标签的加权重合度（按频次归一化）。
func likedTagMatch(tags []string, profile domain.UserProfile) float64 {
	if profile.Behavior == nil || len(profile.Behavior.LikedTags) == 0 || len(tags) == 0 {
		return 0.0
	}
	totalFreq := 0
	for _, freq := range profile.Behavior.LikedTags {
		totalFreq += freq
	}
	if totalFreq == 0 {
		return 0.0
	}
	matchedFreq := 0
	for _, t := range tags {
		if freq, ok := profile.Behavior.LikedTags[t]; ok {
			matchedFreq += freq
		}
	}
	return float64(matchedFreq) / float64(totalFreq)
}

// dislikedTagMatch 计算候选 tags 与用户不感兴趣标签的加权重合度（惩罚项）。
func dislikedTagMatch(tags []string, profile domain.UserProfile) float64 {
	if profile.Behavior == nil || len(profile.Behavior.DislikedTags) == 0 || len(tags) == 0 {
		return 0.0
	}
	totalFreq := 0
	for _, freq := range profile.Behavior.DislikedTags {
		totalFreq += freq
	}
	if totalFreq == 0 {
		return 0.0
	}
	matchedFreq := 0
	for _, t := range tags {
		if freq, ok := profile.Behavior.DislikedTags[t]; ok {
			matchedFreq += freq
		}
	}
	return float64(matchedFreq) / float64(totalFreq)
}

// idSimilarity 基于两个 ID 的字符集 Jaccard 相似度（0-1），用于多样性近似。
func idSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0.0
	}
	if a == b {
		return 1.0
	}
	setA := make(map[rune]bool)
	for _, r := range a {
		setA[r] = true
	}
	setB := make(map[rune]bool)
	for _, r := range b {
		setB[r] = true
	}
	inter := 0
	for r := range setA {
		if setB[r] {
			inter++
		}
	}
	if inter == 0 {
		return 0.0
	}
	union := len(setA) + len(setB) - inter
	return float64(inter) / float64(union)
}

// ---- 辅助函数：特征展平 ----

// flattenFeatures 将 6 类特征按 "<类>.<特征名>" 展平为单一 FeatureVector。
func flattenFeatures(f Features) FeatureVector {
	out := make(FeatureVector, 0)
	merge := func(prefix string, fv FeatureVector) {
		for k, v := range fv {
			out[prefix+"."+k] = v
		}
	}
	merge("text", f.Text)
	merge("behavior", f.Behavior)
	merge("time", f.Time)
	merge("quality", f.Quality)
	merge("profile", f.Profile)
	merge("diversity", f.Diversity)
	return out
}

// featureKeys 返回 6 类特征展平后的固定键序（用于 Model.Predict 输入对齐）。
func featureKeys() []string {
	return []string{
		"text.title_length", "text.title_log_length", "text.summary_length",
		"text.tag_count", "text.tag_density", "text.title_summary_ratio",
		"behavior.click_rate", "behavior.like_rate", "behavior.favorite_rate",
		"behavior.completion_rate", "behavior.dislike_rate", "behavior.behavior_overall",
		"time.freshness", "time.age_hours", "time.periodic_match", "time.decay_score",
		"quality.quality_authority", "quality.quality_depth", "quality.quality_freshness",
		"quality.quality_completeness", "quality.quality_readability",
		"quality.quality_citation", "quality.quality_overall",
		"profile.interest_match", "profile.channel_match",
		"profile.liked_tag_match", "profile.disliked_tag_penalty",
		"diversity.selected_count", "diversity.max_similarity",
		"diversity.diversity_penalty", "diversity.tag_diversity", "diversity.tag_overlap",
	}
}

// featureVectorToSlice 将 FeatureVector 按固定键序展开为 []float64（缺失键为 0）。
func featureVectorToSlice(fv FeatureVector, keys []string) []float64 {
	out := make([]float64, len(keys))
	for i, k := range keys {
		out[i] = fv[k]
	}
	return out
}
