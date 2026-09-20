package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"sea/service/search/rpc/internal/domain"
)

// ============================================================================
// 该文件测试 PersonalizerImpl，覆盖：
//   - 三层兴趣 tag 命中加权 re-rank（长期/短期/周期）
//   - 无 tagger 不加权 / 自定义权重 / 空命中
//   - PersonalizeForUser（repo 加载 + 错误传播）
//   - currentSlot / isWeekend / 编译期断言
// ============================================================================

// 编译期断言：*PersonalizerImpl 实现 Personalizer interface。
var _ Personalizer = (*PersonalizerImpl)(nil)

// stubProfileRepo 模拟 ProfileRepo。
type stubProfileRepo struct {
	profile  domain.UserProfile
	temporal domain.TemporalProfile
	err      error
}

func (s *stubProfileRepo) Load(ctx context.Context, key domain.UserKey) (domain.UserProfile, domain.TemporalProfile, error) {
	return s.profile, s.temporal, s.err
}

// stubArticleTagger 模拟 ArticleTagger。
type stubArticleTagger struct {
	tags map[string][]string
	err  error
}

func (s stubArticleTagger) Tags(ctx context.Context, articleID string) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.tags[articleID], nil
}

// TestPersonalizerImpl_TagBoost 验证三层兴趣加权 re-rank。
func TestPersonalizerImpl_TagBoost(t *testing.T) {
	fixedNow := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC) // Monday 10:00
	slot := currentSlot(fixedNow)
	if slot != "weekday-10" {
		t.Fatalf("测试前置：slot = %q, 期望 weekday-10", slot)
	}
	temporal := domain.TemporalProfile{
		LongTerm:  []domain.Interest{{Tag: "ai", Weight: 1}},
		ShortTerm: []domain.Interest{{Tag: "go", Weight: 1}},
		Periodic:  map[string][]domain.Interest{slot: {{Tag: "tech", Weight: 1}}},
	}
	tagger := stubArticleTagger{tags: map[string][]string{
		"a1": {"ai"},
		"a2": {"go"},
		"a3": {"tech"},
		"a4": {"none"},
	}}
	hits := []domain.SearchHit{
		{ArticleID: "a1", Score: 0.5, Source: "vector"},
		{ArticleID: "a2", Score: 0.5, Source: "vector"},
		{ArticleID: "a3", Score: 0.5, Source: "vector"},
		{ArticleID: "a4", Score: 0.5, Source: "vector"},
	}
	p := NewPersonalizer(nil, WithArticleTagger(tagger))
	p.now = func() time.Time { return fixedNow }

	out, err := p.Personalize(context.Background(), hits, domain.UserProfile{}, temporal)
	if err != nil {
		t.Fatalf("Personalize 返回错误: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("命中数 = %d, 期望 4", len(out))
	}
	// 期望分数：a2=0.7(+0.2) a3=0.65(+0.15) a1=0.6(+0.1) a4=0.5
	want := []struct {
		id    string
		score float64
	}{{"a2", 0.7}, {"a3", 0.65}, {"a1", 0.6}, {"a4", 0.5}}
	for i, w := range want {
		if out[i].ArticleID != w.id {
			t.Errorf("位置 %d = %q, 期望 %q", i, out[i].ArticleID, w.id)
		}
		if !floatEq(out[i].Score, w.score) {
			t.Errorf("%q score = %v, 期望 %v", w.id, out[i].Score, w.score)
		}
	}
}

// TestPersonalizerImpl_NoTagger 验证无 tagger 时不加权且稳定排序保留原顺序。
func TestPersonalizerImpl_NoTagger(t *testing.T) {
	hits := []domain.SearchHit{
		{ArticleID: "a1", Score: 0.5},
		{ArticleID: "a2", Score: 0.5},
		{ArticleID: "a3", Score: 0.5},
	}
	p := NewPersonalizer(nil) // 默认 noop tagger
	out, err := p.Personalize(context.Background(), hits, domain.UserProfile{}, domain.TemporalProfile{})
	if err != nil {
		t.Fatalf("Personalize 返回错误: %v", err)
	}
	for i := range out {
		if out[i].ArticleID != hits[i].ArticleID {
			t.Errorf("位置 %d = %q, 期望 %q（稳定排序）", i, out[i].ArticleID, hits[i].ArticleID)
		}
		if out[i].Score != 0.5 {
			t.Errorf("%q score = %v, 期望 0.5（不加权）", out[i].ArticleID, out[i].Score)
		}
	}
}

// TestPersonalizerImpl_EmptyHits 验证空命中。
func TestPersonalizerImpl_EmptyHits(t *testing.T) {
	p := NewPersonalizer(nil)
	out, err := p.Personalize(context.Background(), nil, domain.UserProfile{}, domain.TemporalProfile{})
	if err != nil {
		t.Fatalf("Personalize 返回错误: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("空命中应返回空, got %d", len(out))
	}
}

// TestPersonalizerImpl_Weights 验证自定义权重。
func TestPersonalizerImpl_Weights(t *testing.T) {
	fixedNow := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	temporal := domain.TemporalProfile{
		LongTerm:  []domain.Interest{{Tag: "ai"}},
		ShortTerm: []domain.Interest{{Tag: "go"}},
		Periodic:  map[string][]domain.Interest{currentSlot(fixedNow): {{Tag: "tech"}}},
	}
	tagger := stubArticleTagger{tags: map[string][]string{"a1": {"ai"}, "a2": {"go"}, "a3": {"tech"}}}
	hits := []domain.SearchHit{
		{ArticleID: "a1", Score: 0.5},
		{ArticleID: "a2", Score: 0.5},
		{ArticleID: "a3", Score: 0.5},
	}
	p := NewPersonalizer(nil, WithArticleTagger(tagger), WithWeights(0.5, 0.5, 0.5))
	p.now = func() time.Time { return fixedNow }
	out, err := p.Personalize(context.Background(), hits, domain.UserProfile{}, temporal)
	if err != nil {
		t.Fatalf("Personalize 返回错误: %v", err)
	}
	// 三层均 +0.5 → 各 1.0
	for _, h := range out {
		if !floatEq(h.Score, 1.0) {
			t.Errorf("%q score = %v, 期望 1.0", h.ArticleID, h.Score)
		}
	}
}

// TestPersonalizerImpl_StaticProfileBoost 验证静态画像标签按长期加权。
func TestPersonalizerImpl_StaticProfileBoost(t *testing.T) {
	profile := domain.UserProfile{
		Static: &domain.StaticProfile{Tags: []string{"finance"}},
	}
	tagger := stubArticleTagger{tags: map[string][]string{"a1": {"finance"}}}
	hits := []domain.SearchHit{{ArticleID: "a1", Score: 0.5}}
	p := NewPersonalizer(nil, WithArticleTagger(tagger))
	out, err := p.Personalize(context.Background(), hits, profile, domain.TemporalProfile{})
	if err != nil {
		t.Fatalf("Personalize 返回错误: %v", err)
	}
	if !floatEq(out[0].Score, 0.6) {
		t.Errorf("静态标签加权后 score = %v, 期望 0.6", out[0].Score)
	}
}

// TestPersonalizerImpl_PersonalizeForUser 验证通过 repo 加载画像。
func TestPersonalizerImpl_PersonalizeForUser(t *testing.T) {
	temporal := domain.TemporalProfile{LongTerm: []domain.Interest{{Tag: "ai"}}}
	repo := &stubProfileRepo{temporal: temporal}
	tagger := stubArticleTagger{tags: map[string][]string{"a1": {"ai"}}}
	p := NewPersonalizer(repo, WithArticleTagger(tagger))
	out, err := p.PersonalizeForUser(context.Background(), domain.UserKey{UserID: "u1"}, []domain.SearchHit{{ArticleID: "a1", Score: 0.5}})
	if err != nil {
		t.Fatalf("PersonalizeForUser 返回错误: %v", err)
	}
	if !floatEq(out[0].Score, 0.6) {
		t.Errorf("score = %v, 期望 0.6（长期 +0.1）", out[0].Score)
	}
}

// TestPersonalizerImpl_PersonalizeForUser_Error 验证 repo 错误传播。
func TestPersonalizerImpl_PersonalizeForUser_Error(t *testing.T) {
	repo := &stubProfileRepo{err: errors.New("profile load fail")}
	p := NewPersonalizer(repo)
	_, err := p.PersonalizeForUser(context.Background(), domain.UserKey{UserID: "u1"}, nil)
	if err == nil {
		t.Fatalf("repo 错误应传播")
	}
}

// TestCurrentSlot 验证周期槽位计算。
func TestCurrentSlot(t *testing.T) {
	monday := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)   // 2024-01-01 周一
	saturday := time.Date(2024, 1, 6, 20, 0, 0, 0, time.UTC) // 2024-01-06 周六
	if got := currentSlot(monday); got != "weekday-10" {
		t.Errorf("周一 10 点 slot = %q, 期望 weekday-10", got)
	}
	if got := currentSlot(saturday); got != "weekend-20" {
		t.Errorf("周六 20 点 slot = %q, 期望 weekend-20", got)
	}
}

// TestIsWeekend 验证周末判断。
func TestIsWeekend(t *testing.T) {
	if !isWeekend(time.Saturday) || !isWeekend(time.Sunday) {
		t.Errorf("周六/周日应为周末")
	}
	if isWeekend(time.Monday) || isWeekend(time.Friday) {
		t.Errorf("周一/周五不应为周末")
	}
}
