// ============================================================================
// 该文件实现搜索过滤 Filter，对命中按元数据维度过滤。
//
// 职责：
//   - 标签过滤：include（filter.Tags）/ exclude（Filter 配置的 ExcludeTags）
//   - 作者过滤：filter.Authors
//   - IP 频道过滤：filter.Channel
//   - 时间范围过滤：filter.TimeRange（支持 "7d"/"30d"/"24h"/"latest"/"today"/"week"/"month"）
//   - 质量阈值过滤：filter.QualityThreshold
//
// 说明：
//   - ArticleMetadata 与 MetadataRepo 定义在本文件（不修改 domain）
//   - 元数据加载失败采用 fail-closed（该命中被过滤），保证过滤语义严格
//
// 二开扩展点：
//   - 实现 MetadataRepo interface 接入自研元数据存储
//   - WithExcludeTags 注入全局排除标签
// ============================================================================

package search

import (
	"context"
	"strconv"
	"strings"
	"time"

	"sea/internal/domain"
)

// ArticleMetadata 文章元数据（过滤所需维度）。
// 定义在本文件，不修改 domain。
type ArticleMetadata struct {
	// Tags 文章标签。
	Tags []string
	// AuthorID 作者 ID。
	AuthorID string
	// Channel 所属 IP 频道。
	Channel string
	// CreateTime 创建时间（Unix 秒）。
	CreateTime int64
	// QualityScore 质量分（0-1）。
	QualityScore float64
}

// MetadataRepo 文章元数据加载契约。
type MetadataRepo interface {
	// LoadMetadata 加载指定文章的元数据。
	LoadMetadata(ctx context.Context, articleID string) (ArticleMetadata, error)
}

// Filter 搜索过滤器。
type Filter struct {
	repo        MetadataRepo
	excludeTags []string
	now         func() time.Time
}

// FilterOption Filter 构造选项。
type FilterOption func(*Filter)

// WithExcludeTags 注入全局排除标签（命中任一即过滤）。
// 注：domain.SearchFilter 仅含 include 标签（Tags），exclude 通过 Filter 配置补充。
func WithExcludeTags(tags []string) FilterOption {
	return func(f *Filter) {
		if tags != nil {
			f.excludeTags = append([]string(nil), tags...)
		}
	}
}

// NewFilter 创建搜索过滤器。
// repo 为元数据加载源；opts 为构造选项。
func NewFilter(repo MetadataRepo, opts ...FilterOption) *Filter {
	f := &Filter{
		repo: repo,
		now:  time.Now,
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

// Apply 对命中应用过滤，返回满足条件的命中子集。
//
// 签名严格遵循 spec：Apply(hits []SearchHit, filter SearchFilter) []SearchHit。
// 内部使用 context.Background() 调用 MetadataRepo（spec 未提供 ctx）。
//
// 过滤维度：标签 include/exclude / 作者 / 频道 / 时间范围 / 质量阈值。
// 元数据加载失败 → 该命中被过滤（fail-closed）。
// 空 ArticleID 的命中无法加载元数据，按 fail-closed 过滤。
func (f *Filter) Apply(hits []domain.SearchHit, filter domain.SearchFilter) []domain.SearchHit {
	ctx := context.Background()
	cutoff, hasTime := f.parseTimeRange(filter.TimeRange)

	out := make([]domain.SearchHit, 0, len(hits))
	for _, hit := range hits {
		if hit.ArticleID == "" {
			continue
		}
		meta, err := f.repo.LoadMetadata(ctx, hit.ArticleID)
		if err != nil {
			continue // fail-closed
		}
		if !f.match(meta, filter, cutoff, hasTime) {
			continue
		}
		out = append(out, hit)
	}
	return out
}

// match 判断单条元数据是否满足过滤条件。
func (f *Filter) match(meta ArticleMetadata, filter domain.SearchFilter, cutoff int64, hasTime bool) bool {
	// 标签 include：命中任一 include 标签
	if len(filter.Tags) > 0 && !overlap(meta.Tags, filter.Tags) {
		return false
	}
	// 标签 exclude：命中任一 exclude 标签即排除
	if len(f.excludeTags) > 0 && overlap(meta.Tags, f.excludeTags) {
		return false
	}
	// 作者：AuthorID 必须在 filter.Authors 中
	if len(filter.Authors) > 0 && !containsStr(filter.Authors, meta.AuthorID) {
		return false
	}
	// 频道：必须精确匹配
	if filter.Channel != "" && meta.Channel != filter.Channel {
		return false
	}
	// 时间范围：CreateTime >= cutoff
	if hasTime && meta.CreateTime < cutoff {
		return false
	}
	// 质量阈值：QualityScore >= threshold（threshold>0 时生效）
	if filter.QualityThreshold > 0 && meta.QualityScore < filter.QualityThreshold {
		return false
	}
	return true
}

// parseTimeRange 解析时间范围字符串，返回截止 Unix 秒与是否生效。
// 支持："latest"/"today" → 24h，"week"/"7d" → 7d，"month"/"30d" → 30d，
// 以及 "Nh"/"Nd" 形式（如 "24h"/"3d"）。空串不生效。
func (f *Filter) parseTimeRange(s string) (cutoff int64, ok bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, false
	}
	now := f.now()
	switch s {
	case "latest", "today":
		return now.Add(-24 * time.Hour).Unix(), true
	case "week", "7d":
		return now.Add(-7 * 24 * time.Hour).Unix(), true
	case "month", "30d":
		return now.Add(-30 * 24 * time.Hour).Unix(), true
	}
	// "Nh" / "Nd"
	if d, parsed := parseSuffixDuration(s); parsed {
		return now.Add(-d).Unix(), true
	}
	return 0, false
}

// parseSuffixDuration 解析 "Nh"/"Nd" 形式为时长。
func parseSuffixDuration(s string) (time.Duration, bool) {
	if len(s) < 2 {
		return 0, false
	}
	unit := s[len(s)-1]
	numStr := s[:len(s)-1]
	n, err := strconv.Atoi(numStr)
	if err != nil || n <= 0 {
		return 0, false
	}
	switch unit {
	case 'h':
		return time.Duration(n) * time.Hour, true
	case 'd':
		return time.Duration(n) * 24 * time.Hour, true
	default:
		return 0, false
	}
}

// overlap 判断 a 与 b 是否有交集。
func overlap(a, b []string) bool {
	set := make(map[string]struct{}, len(a))
	for _, x := range a {
		set[x] = struct{}{}
	}
	for _, y := range b {
		if _, ok := set[y]; ok {
			return true
		}
	}
	return false
}

// containsStr 判断 s 是否在列表中。
func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
