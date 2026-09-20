// ============================================================================
// 该文件实现个性化搜索 PersonalizerImpl，对召回命中按用户兴趣 re-rank。
//
// 职责：
//   - 注入长期 / 短期 / 周期兴趣，按 tag 命中度加权 re-rank
//   - 命中长期兴趣 +longW（默认 0.1），短期 +shortW（默认 0.2），周期槽位 +periodicW（默认 0.15）
//   - 提供 PersonalizeForUser 便捷入口（通过 ProfileRepo 加载画像后调用 Personalize）
//
// 二开扩展点：
//   - 实现 ProfileRepo interface 自定义画像加载
//   - 实现 ArticleTagger interface 提供文章标签（tag 命中所需，默认 noop 不加权）
//   - WithWeights 调整三层兴趣权重；WithArticleTagger 注入标签源
// ============================================================================

package search

import (
	"context"
	"sort"
	"strconv"
	"time"

	"sea/internal/domain"
)

// ProfileRepo 用户画像加载契约。
type ProfileRepo interface {
	// Load 加载用户画像与时间画像。
	Load(ctx context.Context, key domain.UserKey) (domain.UserProfile, domain.TemporalProfile, error)
}

// ArticleTagger 文章标签源契约，用于按 tag 命中度加权。
// SearchHit 仅含 ArticleID，tag 命中需通过该 interface 获取文章标签。
type ArticleTagger interface {
	// Tags 返回指定文章的标签列表。
	Tags(ctx context.Context, articleID string) ([]string, error)
}

// Personalizer 个性化搜索 interface。
type Personalizer interface {
	// Personalize 对命中按画像 re-rank。
	Personalize(ctx context.Context, hits []domain.SearchHit, profile domain.UserProfile, temporal domain.TemporalProfile) ([]domain.SearchHit, error)
}

// noopTagger 默认空标签源，tagger 未注入时不加权。
type noopTagger struct{}

func (noopTagger) Tags(context.Context, string) ([]string, error) { return nil, nil }

// PersonalizerImpl 个性化搜索实现。
type PersonalizerImpl struct {
	repo       ProfileRepo
	tagger     ArticleTagger
	longW      float64
	shortW     float64
	periodicW  float64
	now        func() time.Time
}

// PersonalizerOption PersonalizerImpl 构造选项。
type PersonalizerOption func(*PersonalizerImpl)

// WithWeights 设置三层兴趣加权系数（long/short/periodic）。
// 默认 long=0.1 / short=0.2 / periodic=0.15。
func WithWeights(long, short, periodic float64) PersonalizerOption {
	return func(p *PersonalizerImpl) {
		if long >= 0 {
			p.longW = long
		}
		if short >= 0 {
			p.shortW = short
		}
		if periodic >= 0 {
			p.periodicW = periodic
		}
	}
}

// WithArticleTagger 注入文章标签源（tag 命中度加权所需）。
func WithArticleTagger(t ArticleTagger) PersonalizerOption {
	return func(p *PersonalizerImpl) {
		if t != nil {
			p.tagger = t
		}
	}
}

// NewPersonalizer 创建个性化搜索器。
// repo 用于 PersonalizeForUser 加载画像；opts 为构造选项。
func NewPersonalizer(repo ProfileRepo, opts ...PersonalizerOption) *PersonalizerImpl {
	p := &PersonalizerImpl{
		repo:      repo,
		tagger:    noopTagger{},
		longW:     0.1,
		shortW:    0.2,
		periodicW: 0.15,
		now:       time.Now,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Personalize 对命中按画像 re-rank，实现 Personalizer.Personalize。
//
// 流程：
//  1. 构建兴趣加权表 boosts[tag]：长期 +longW，短期 +shortW，周期槽位 +periodicW；
//     静态画像 Tags/Interests 也按 longW 计入（注册兴趣视为长期）。
//  2. 对每条命中，通过 ArticleTagger 获取标签，累加 boosts 中对应权重到 Score。
//  3. 按 Score 降序稳定排序（保留原始相对顺序）。
//
// tagger 返回错误时按空标签处理（不加权），不阻断整体。
func (p *PersonalizerImpl) Personalize(ctx context.Context, hits []domain.SearchHit, profile domain.UserProfile, temporal domain.TemporalProfile) ([]domain.SearchHit, error) {
	boosts := p.buildBoosts(profile, temporal)

	out := make([]domain.SearchHit, len(hits))
	copy(out, hits)
	for i := range out {
		if out[i].ArticleID == "" {
			continue
		}
		tags, _ := p.tagger.Tags(ctx, out[i].ArticleID)
		boost := 0.0
		for _, tag := range tags {
			boost += boosts[tag]
		}
		out[i].Score += boost
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out, nil
}

// PersonalizeForUser 通过 ProfileRepo 加载画像后调用 Personalize 的便捷入口。
func (p *PersonalizerImpl) PersonalizeForUser(ctx context.Context, key domain.UserKey, hits []domain.SearchHit) ([]domain.SearchHit, error) {
	profile, temporal, err := p.repo.Load(ctx, key)
	if err != nil {
		return nil, err
	}
	return p.Personalize(ctx, hits, profile, temporal)
}

// buildBoosts 构建标签加权表。
func (p *PersonalizerImpl) buildBoosts(profile domain.UserProfile, temporal domain.TemporalProfile) map[string]float64 {
	boosts := make(map[string]float64, 16)
	add := func(tag string, w float64) {
		if tag == "" || w == 0 {
			return
		}
		boosts[tag] += w
	}
	// 长期兴趣
	for _, it := range temporal.LongTerm {
		add(it.Tag, p.longW)
	}
	// 短期兴趣
	for _, it := range temporal.ShortTerm {
		add(it.Tag, p.shortW)
	}
	// 会话兴趣也按短期加权（会话兴趣衰减快，强度近似短期）
	for _, it := range temporal.Session {
		add(it.Tag, p.shortW)
	}
	// 周期槽位（当前时间）
	slot := currentSlot(p.now())
	for _, it := range temporal.Periodic[slot] {
		add(it.Tag, p.periodicW)
	}
	// 静态画像注册标签 / 兴趣视为长期
	if profile.Static != nil {
		for _, tag := range profile.Static.Tags {
			add(tag, p.longW)
		}
		for _, tag := range profile.Static.Interests {
			add(tag, p.longW)
		}
	}
	// 动态画像主题视为短期
	if profile.Dynamic != nil {
		for _, topic := range profile.Dynamic.Topics {
			add(topic, p.shortW)
		}
	}
	return boosts
}

// currentSlot 计算当前周期槽位（"weekday-HH" / "weekend-HH"）。
// 与 TemporalProfile.Periodic map 的键格式对齐。
func currentSlot(now time.Time) string {
	h := now.Hour()
	prefix := "weekday"
	if isWeekend(now.Weekday()) {
		prefix = "weekend"
	}
	return prefix + "-" + strconv.Itoa(h)
}

// isWeekend 判断是否周末（周六/周日）。
func isWeekend(w time.Weekday) bool {
	return w == time.Saturday || w == time.Sunday
}
