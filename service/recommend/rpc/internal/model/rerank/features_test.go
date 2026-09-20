package rerank

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// TestFeatureExtractor_ExtractAll 验证 6 类特征提取的完整性与关键值。
func TestFeatureExtractor_ExtractAll(t *testing.T) {
	fe := NewFeatureExtractor()
	c := domain.Candidate{
		ArticleID: "art-1",
		Score:     0.8,
		Scores: map[string]float64{
			"click_rate":           0.5,
			"like_rate":            0.3,
			"favorite_rate":        0.2,
			"completion_rate":      0.6,
			"dislike_rate":         0.05,
			"quality_authority":    0.7,
			"quality_depth":        0.6,
			"quality_freshness":    0.8,
			"quality_completeness": 0.5,
			"quality_readability":  0.9,
			"quality_citation":     0.4,
		},
		Extra: map[string]any{
			"title":        "深度学习在推荐系统中的应用",
			"summary":      "本文探讨深度学习模型如何提升推荐效果。",
			"tags":         []string{"AI", "深度学习", "推荐系统"},
			"channel":      "tech",
			"published_at": time.Now().Add(-48 * time.Hour),
			"selected_ids": []string{"art-2", "art-3"},
		},
	}
	profile := domain.UserProfile{
		Key: domain.UserKey{UserID: "u1", Channel: "tech"},
		Static: &domain.StaticProfile{
			Interests: []string{"AI", "推荐系统"},
			Tags:      []string{"tech"},
		},
		Dynamic: &domain.DynamicProfile{
			Topics: []string{"深度学习"},
		},
		Behavior: &domain.BehaviorProfile{
			LikedTags:    map[string]int{"AI": 5, "深度学习": 3},
			DislikedTags: map[string]int{"娱乐": 2},
		},
	}
	temporal := domain.TemporalProfile{
		DecayState: map[string]domain.DecayState{
			"AI":   {Tag: "AI", Score: 0.7},
			"深度学习": {Tag: "深度学习", Score: 0.5},
		},
		PeriodicPattern: domain.NewPeriodicPattern(),
	}
	temporal.PeriodicPattern.Add(currentPeriodSlot(time.Now()), domain.InterestEntry{
		Tag: "AI", Weight: 0.8,
	})

	feats := fe.Extract(context.Background(), c, profile, temporal)

	// 验证 6 类特征均非空
	checks := []struct {
		name string
		fv   FeatureVector
	}{
		{"Text", feats.Text},
		{"Behavior", feats.Behavior},
		{"Time", feats.Time},
		{"Quality", feats.Quality},
		{"Profile", feats.Profile},
		{"Diversity", feats.Diversity},
	}
	for _, ck := range checks {
		if len(ck.fv) == 0 {
			t.Errorf("%s 特征为空", ck.name)
		}
	}

	// 文本特征
	titleLen := float64(len("深度学习在推荐系统中的应用"))
	if feats.Text["title_length"] != titleLen {
		t.Errorf("title_length 期望 %v, 实际 %v", titleLen, feats.Text["title_length"])
	}
	if feats.Text["tag_count"] != 3 {
		t.Errorf("tag_count 期望 3, 实际 %v", feats.Text["tag_count"])
	}

	// 行为特征
	if feats.Behavior["click_rate"] != 0.5 {
		t.Errorf("click_rate 期望 0.5, 实际 %v", feats.Behavior["click_rate"])
	}
	if feats.Behavior["behavior_overall"] <= 0 {
		t.Errorf("behavior_overall 应为正, 实际 %v", feats.Behavior["behavior_overall"])
	}

	// 时间特征
	if feats.Time["freshness"] <= 0 || feats.Time["freshness"] > 1 {
		t.Errorf("freshness 应在 (0,1], 实际 %v", feats.Time["freshness"])
	}
	if math.Abs(feats.Time["periodic_match"]-1.0) > 1e-9 {
		t.Errorf("periodic_match 期望 1.0 (AI 在当前槽位), 实际 %v", feats.Time["periodic_match"])
	}
	if math.Abs(feats.Time["decay_score"]-0.6) > 1e-9 {
		t.Errorf("decay_score 期望 0.6, 实际 %v", feats.Time["decay_score"])
	}

	// 质量特征
	expectedQ := (0.7 + 0.6 + 0.8 + 0.5 + 0.9 + 0.4) / 6.0
	if math.Abs(feats.Quality["quality_overall"]-expectedQ) > 1e-9 {
		t.Errorf("quality_overall 期望 %v, 实际 %v", expectedQ, feats.Quality["quality_overall"])
	}

	// 画像特征
	if feats.Profile["channel_match"] != 1.0 {
		t.Errorf("channel_match 期望 1.0, 实际 %v", feats.Profile["channel_match"])
	}
	if feats.Profile["interest_match"] <= 0 {
		t.Errorf("interest_match 应为正, 实际 %v", feats.Profile["interest_match"])
	}
	if feats.Profile["liked_tag_match"] <= 0 {
		t.Errorf("liked_tag_match 应为正, 实际 %v", feats.Profile["liked_tag_match"])
	}
	if feats.Profile["disliked_tag_penalty"] != 0 {
		t.Errorf("disliked_tag_penalty 期望 0, 实际 %v", feats.Profile["disliked_tag_penalty"])
	}

	// 多样性特征
	if feats.Diversity["selected_count"] != 2 {
		t.Errorf("selected_count 期望 2, 实际 %v", feats.Diversity["selected_count"])
	}
	if feats.Diversity["tag_diversity"] < 0 || feats.Diversity["tag_diversity"] > 1 {
		t.Errorf("tag_diversity 应在 [0,1], 实际 %v", feats.Diversity["tag_diversity"])
	}
}

// TestFeatureExtractor_EmptyCandidate 验证空候选不 panic 且各特征 map 已初始化。
func TestFeatureExtractor_EmptyCandidate(t *testing.T) {
	fe := NewFeatureExtractor()
	c := domain.Candidate{ArticleID: "empty"}
	profile := domain.UserProfile{Key: domain.UserKey{UserID: "u"}}
	temporal := domain.TemporalProfile{PeriodicPattern: domain.NewPeriodicPattern()}

	feats := fe.Extract(context.Background(), c, profile, temporal)
	if feats.Text == nil || feats.Behavior == nil || feats.Time == nil ||
		feats.Quality == nil || feats.Profile == nil || feats.Diversity == nil {
		t.Fatalf("空候选特征 map 不应为 nil")
	}
	if feats.Text["title_length"] != 0 {
		t.Errorf("空标题长度期望 0, 实际 %v", feats.Text["title_length"])
	}
	if feats.Time["freshness"] != 0.5 {
		t.Errorf("无发布时间 freshness 期望 0.5, 实际 %v", feats.Time["freshness"])
	}
}

// TestFeatureExtractor_FlattenAndKeys 验证展平特征与键序对齐。
func TestFeatureExtractor_FlattenAndKeys(t *testing.T) {
	fe := NewFeatureExtractor()
	c := domain.Candidate{ArticleID: "art-1", Extra: map[string]any{"title": "AI", "tags": []string{"AI"}}}
	profile := domain.UserProfile{Key: domain.UserKey{UserID: "u"}}
	temporal := domain.TemporalProfile{PeriodicPattern: domain.NewPeriodicPattern()}

	feats := fe.Extract(context.Background(), c, profile, temporal)
	flat := flattenFeatures(feats)
	keys := featureKeys()

	// 每个键序对应值应可取到（缺失为 0）
	slice := featureVectorToSlice(flat, keys)
	if len(slice) != len(keys) {
		t.Fatalf("特征向量长度期望 %d, 实际 %d", len(keys), len(slice))
	}
	// 展平 map 至少包含键序中所有键
	for _, k := range keys {
		if _, ok := flat[k]; !ok {
			t.Errorf("展平特征缺少键 %s", k)
		}
	}
}
