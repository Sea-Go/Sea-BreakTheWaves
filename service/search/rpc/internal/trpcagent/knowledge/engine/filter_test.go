package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/domain"
)

// ============================================================================
// 该文件测试 Filter，覆盖：
//   - 标签 include/exclude / 作者 / 频道 / 时间范围 / 质量阈值
//   - 元数据加载失败 fail-closed / 空 ArticleID / 无过滤条件全保留
//   - parseSuffixDuration / parseTimeRange
// ============================================================================

// 编译期断言：*metaRepo 实现 MetadataRepo interface。
var _ MetadataRepo = (*metaRepo)(nil)

// metaRepo 模拟 MetadataRepo。
type metaRepo struct {
	data  map[string]ArticleMetadata
	errOn string
}

func (m *metaRepo) LoadMetadata(ctx context.Context, articleID string) (ArticleMetadata, error) {
	_ = ctx
	if m.errOn == articleID {
		return ArticleMetadata{}, errors.New("metadata load fail")
	}
	return m.data[articleID], nil
}

// hitIDs 提取命中 ID 列表（调试辅助）。
func hitIDs(hits []domain.SearchHit) []string {
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.ArticleID)
	}
	return ids
}

// TestFilter_Apply_TagsInclude 验证标签 include 过滤。
func TestFilter_Apply_TagsInclude(t *testing.T) {
	repo := &metaRepo{
		data: map[string]ArticleMetadata{
			"a1": {Tags: []string{"tech"}},
			"a2": {Tags: []string{"finance"}},
		},
	}
	f := NewFilter(repo)
	hits := []domain.SearchHit{{ArticleID: "a1"}, {ArticleID: "a2"}}
	out := f.Apply(hits, domain.SearchFilter{Tags: []string{"tech"}})
	if len(out) != 1 || out[0].ArticleID != "a1" {
		t.Errorf("Tags include 过滤结果 = %v, 期望 [a1]", hitIDs(out))
	}
}

// TestFilter_Apply_TagsExclude 验证标签 exclude 过滤（WithExcludeTags）。
func TestFilter_Apply_TagsExclude(t *testing.T) {
	repo := &metaRepo{
		data: map[string]ArticleMetadata{
			"a1": {Tags: []string{"tech"}},
			"a2": {Tags: []string{"spam"}},
		},
	}
	f := NewFilter(repo, WithExcludeTags([]string{"spam"}))
	hits := []domain.SearchHit{{ArticleID: "a1"}, {ArticleID: "a2"}}
	out := f.Apply(hits, domain.SearchFilter{})
	if len(out) != 1 || out[0].ArticleID != "a1" {
		t.Errorf("Tags exclude 过滤结果 = %v, 期望 [a1]", hitIDs(out))
	}
}

// TestFilter_Apply_Authors 验证作者过滤。
func TestFilter_Apply_Authors(t *testing.T) {
	repo := &metaRepo{
		data: map[string]ArticleMetadata{
			"a1": {AuthorID: "u1"},
			"a2": {AuthorID: "u2"},
		},
	}
	f := NewFilter(repo)
	hits := []domain.SearchHit{{ArticleID: "a1"}, {ArticleID: "a2"}}
	out := f.Apply(hits, domain.SearchFilter{Authors: []string{"u1"}})
	if len(out) != 1 || out[0].ArticleID != "a1" {
		t.Errorf("Authors 过滤结果 = %v, 期望 [a1]", hitIDs(out))
	}
}

// TestFilter_Apply_Channel 验证频道过滤。
func TestFilter_Apply_Channel(t *testing.T) {
	repo := &metaRepo{
		data: map[string]ArticleMetadata{
			"a1": {Channel: "ai"},
			"a2": {Channel: "finance"},
		},
	}
	f := NewFilter(repo)
	hits := []domain.SearchHit{{ArticleID: "a1"}, {ArticleID: "a2"}}
	out := f.Apply(hits, domain.SearchFilter{Channel: "ai"})
	if len(out) != 1 || out[0].ArticleID != "a1" {
		t.Errorf("Channel 过滤结果 = %v, 期望 [a1]", hitIDs(out))
	}
}

// TestFilter_Apply_TimeRange 验证时间范围过滤。
func TestFilter_Apply_TimeRange(t *testing.T) {
	fixedNow := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	// "7d" → cutoff = 6月10日 10:00
	repo := &metaRepo{
		data: map[string]ArticleMetadata{
			"a1": {CreateTime: time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC).Unix()}, // 6/12 在 cutoff 后
			"a2": {CreateTime: time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC).Unix()},  // 6/8 在 cutoff 前
		},
	}
	f := NewFilter(repo)
	f.now = func() time.Time { return fixedNow }
	hits := []domain.SearchHit{{ArticleID: "a1"}, {ArticleID: "a2"}}
	out := f.Apply(hits, domain.SearchFilter{TimeRange: "7d"})
	if len(out) != 1 || out[0].ArticleID != "a1" {
		t.Errorf("TimeRange 过滤结果 = %v, 期望 [a1]", hitIDs(out))
	}
}

// TestFilter_Apply_QualityThreshold 验证质量阈值过滤。
func TestFilter_Apply_QualityThreshold(t *testing.T) {
	repo := &metaRepo{
		data: map[string]ArticleMetadata{
			"a1": {QualityScore: 0.9},
			"a2": {QualityScore: 0.3},
		},
	}
	f := NewFilter(repo)
	hits := []domain.SearchHit{{ArticleID: "a1"}, {ArticleID: "a2"}}
	out := f.Apply(hits, domain.SearchFilter{QualityThreshold: 0.5})
	if len(out) != 1 || out[0].ArticleID != "a1" {
		t.Errorf("QualityThreshold 过滤结果 = %v, 期望 [a1]", hitIDs(out))
	}
}

// TestFilter_Apply_NoFilter 验证空过滤条件保留全部。
func TestFilter_Apply_NoFilter(t *testing.T) {
	repo := &metaRepo{
		data: map[string]ArticleMetadata{
			"a1": {Tags: []string{"tech"}},
			"a2": {Tags: []string{"finance"}},
		},
	}
	f := NewFilter(repo)
	hits := []domain.SearchHit{{ArticleID: "a1"}, {ArticleID: "a2"}}
	out := f.Apply(hits, domain.SearchFilter{})
	if len(out) != 2 {
		t.Errorf("无过滤条件应保留全部, got %d", len(out))
	}
}

// TestFilter_Apply_MetadataError_FailClosed 验证元数据加载失败 fail-closed。
func TestFilter_Apply_MetadataError_FailClosed(t *testing.T) {
	repo := &metaRepo{
		data: map[string]ArticleMetadata{
			"a1": {Tags: []string{"tech"}},
		},
		errOn: "a2",
	}
	f := NewFilter(repo)
	hits := []domain.SearchHit{{ArticleID: "a1"}, {ArticleID: "a2"}}
	out := f.Apply(hits, domain.SearchFilter{})
	if len(out) != 1 || out[0].ArticleID != "a1" {
		t.Errorf("fail-closed 应过滤 a2, 结果 = %v", hitIDs(out))
	}
}

// TestFilter_Apply_EmptyArticleID 验证空 ArticleID 被过滤。
func TestFilter_Apply_EmptyArticleID(t *testing.T) {
	repo := &metaRepo{data: map[string]ArticleMetadata{}}
	f := NewFilter(repo)
	hits := []domain.SearchHit{{ArticleID: ""}, {ArticleID: ""}}
	out := f.Apply(hits, domain.SearchFilter{})
	if len(out) != 0 {
		t.Errorf("空 ArticleID 应被过滤, got %d", len(out))
	}
}

// TestFilter_ParseTimeRange 验证时间范围解析。
func TestFilter_ParseTimeRange(t *testing.T) {
	fixedNow := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	f := NewFilter(&metaRepo{data: map[string]ArticleMetadata{}})
	f.now = func() time.Time { return fixedNow }
	cases := []struct {
		range_  string
		wantDur time.Duration
	}{
		{"latest", 24 * time.Hour},
		{"today", 24 * time.Hour},
		{"week", 7 * 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"month", 30 * 24 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
		{"24h", 24 * time.Hour},
		{"3d", 3 * 24 * time.Hour},
	}
	for _, c := range cases {
		cutoff, ok := f.parseTimeRange(c.range_)
		if !ok {
			t.Errorf("TimeRange %q 应生效", c.range_)
			continue
		}
		wantCutoff := fixedNow.Add(-c.wantDur).Unix()
		if cutoff != wantCutoff {
			t.Errorf("TimeRange %q cutoff = %d, 期望 %d", c.range_, cutoff, wantCutoff)
		}
	}
	// 空串不生效
	if _, ok := f.parseTimeRange(""); ok {
		t.Errorf("空 TimeRange 不应生效")
	}
}

// TestFilter_ParseTimeRange_Invalid 验证非法时间范围不生效。
func TestFilter_ParseTimeRange_Invalid(t *testing.T) {
	f := NewFilter(&metaRepo{data: map[string]ArticleMetadata{}})
	if _, ok := f.parseTimeRange("foobar"); ok {
		t.Errorf("非法 TimeRange 不应生效")
	}
}

// TestParseSuffixDuration 验证后缀时长解析。
func TestParseSuffixDuration(t *testing.T) {
	cases := []struct {
		s    string
		ok   bool
		want time.Duration
	}{
		{"24h", true, 24 * time.Hour},
		{"3d", true, 3 * 24 * time.Hour},
		{"0h", false, 0}, // 非正数
		{"abc", false, 0},
		{"3x", false, 0}, // 未知单位
	}
	for _, c := range cases {
		got, ok := parseSuffixDuration(c.s)
		if ok != c.ok {
			t.Errorf("parseSuffixDuration(%q) ok = %v, 期望 %v", c.s, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("parseSuffixDuration(%q) = %v, 期望 %v", c.s, got, c.want)
		}
	}
}

// TestOverlap 验证 overlap 辅助函数。
func TestOverlap(t *testing.T) {
	if !overlap([]string{"a", "b"}, []string{"b", "c"}) {
		t.Errorf("有交集应返回 true")
	}
	if overlap([]string{"a"}, []string{"b"}) {
		t.Errorf("无交集应返回 false")
	}
}

// TestContainsStr 验证 containsStr 辅助函数。
func TestContainsStr(t *testing.T) {
	if !containsStr([]string{"a", "b"}, "a") {
		t.Errorf("应包含 a")
	}
	if containsStr([]string{"a", "b"}, "c") {
		t.Errorf("不应包含 c")
	}
}
