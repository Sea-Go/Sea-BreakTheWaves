// ============================================================================
// 该文件使用 stdlib stub 测试 SuggesterImpl，覆盖：
//   - Trie 插入与前缀检索（含 topK 截断、score 排序、空串）
//   - 三路融合：前缀（Trie）/ 热搜（按前缀过滤）/ 个性化（历史近因打分）
//   - 去重：按 Text 保留最高分
//   - 缓存：sync.Map + TTL 命中与过期
//   - 边界：空前缀、nil hot/history、topK<=0 回退、错误来源跳过
//   - 编译期断言 *SuggesterImpl 实现 Suggester
// ============================================================================

package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"sea/internal/domain"
)

// 编译期断言：*SuggesterImpl 实现 Suggester interface。
var _ Suggester = (*SuggesterImpl)(nil)

// ----------------------------------------------------------------------------
// Stubs
// ----------------------------------------------------------------------------

// stubHotRepo 模拟 HotQueryRepo。
type stubHotRepo struct {
	queries []domain.SearchSuggestion
	err     error
}

func (s *stubHotRepo) TopK(ctx context.Context, topK int) ([]domain.SearchSuggestion, error) {
	if s.err != nil {
		return nil, s.err
	}
	if topK <= 0 || topK >= len(s.queries) {
		return s.queries, nil
	}
	return s.queries[:topK], nil
}

// stubHistoryRepo 模拟 HistoryRepo。
type stubHistoryRepo struct {
	queries []string
	err     error
}

func (s *stubHistoryRepo) RecentQueries(ctx context.Context, userKey domain.UserKey, topK int) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	if topK <= 0 || topK >= len(s.queries) {
		return s.queries, nil
	}
	return s.queries[:topK], nil
}

// ----------------------------------------------------------------------------
// Trie 单元测试
// ----------------------------------------------------------------------------

// TestTrie_InsertAndPrefixSearch 验证 Trie 插入与前缀检索基本逻辑。
func TestTrie_InsertAndPrefixSearch(t *testing.T) {
	trie := NewTrie()
	trie.Insert("golang", 0.9)
	trie.Insert("gopher", 0.7)
	trie.Insert("python", 0.8)
	trie.Insert("go", 1.0)

	got := trie.PrefixSearch("go", 0)
	if len(got) != 3 {
		t.Fatalf("PrefixSearch(\"go\") 返回 %d 条, 期望 3（go/golang/gopher）", len(got))
	}
	// 验证按 score 降序：go(1.0) > golang(0.9) > gopher(0.7)
	if got[0].Text != "go" || got[0].Score != 1.0 {
		t.Errorf("首个 = %+v, 期望 go/1.0", got[0])
	}
	if got[1].Text != "golang" || got[1].Score != 0.9 {
		t.Errorf("第二个 = %+v, 期望 golang/0.9", got[1])
	}
	if got[2].Text != "gopher" || got[2].Score != 0.7 {
		t.Errorf("第三个 = %+v, 期望 gopher/0.7", got[2])
	}
	// Source 应被设置为 "prefix"
	for _, sg := range got {
		if sg.Source != "prefix" {
			t.Errorf("Source = %q, 期望 prefix（条目 %q）", sg.Source, sg.Text)
		}
	}
}

// TestTrie_PrefixSearch_TopK 验证 topK 截断。
func TestTrie_PrefixSearch_TopK(t *testing.T) {
	trie := NewTrie()
	trie.Insert("go1", 0.5)
	trie.Insert("go2", 0.6)
	trie.Insert("go3", 0.7)
	trie.Insert("go4", 0.8)

	got := trie.PrefixSearch("go", 2)
	if len(got) != 2 {
		t.Fatalf("PrefixSearch topK=2 返回 %d 条, 期望 2", len(got))
	}
	// 应保留 score 最高的两条
	if got[0].Text != "go4" || got[1].Text != "go3" {
		t.Errorf("topK=2 截断后 = [%s, %s], 期望 [go4, go3]", got[0].Text, got[1].Text)
	}
}

// TestTrie_PrefixSearch_NoMatch 验证无匹配返回 nil。
func TestTrie_PrefixSearch_NoMatch(t *testing.T) {
	trie := NewTrie()
	trie.Insert("golang", 0.9)
	if got := trie.PrefixSearch("rust", 0); got != nil {
		t.Errorf("PrefixSearch(\"rust\") = %v, 期望 nil", got)
	}
}

// TestTrie_Insert_Empty 验证空串插入被忽略。
func TestTrie_Insert_Empty(t *testing.T) {
	trie := NewTrie()
	trie.Insert("", 1.0)
	if got := trie.PrefixSearch("", 0); len(got) != 0 {
		t.Errorf("空串插入后 PrefixSearch(\"\") = %v, 期望空", got)
	}
}

// TestTrie_Insert_DuplicateKeepMax 验证重复插入取最大 score。
func TestTrie_Insert_DuplicateKeepMax(t *testing.T) {
	trie := NewTrie()
	trie.Insert("go", 0.3)
	trie.Insert("go", 0.9)
	trie.Insert("go", 0.5) // 不应覆盖 0.9
	got := trie.PrefixSearch("go", 0)
	if len(got) != 1 {
		t.Fatalf("返回 %d 条, 期望 1", len(got))
	}
	if got[0].Score != 0.9 {
		t.Errorf("重复插入后 score = %v, 期望 0.9（取 max）", got[0].Score)
	}
}

// TestTrie_PrefixSearch_EmptyPrefix 验证空前缀返回所有词条。
func TestTrie_PrefixSearch_EmptyPrefix(t *testing.T) {
	trie := NewTrie()
	trie.Insert("a", 0.3)
	trie.Insert("b", 0.5)
	trie.Insert("c", 0.4)
	got := trie.PrefixSearch("", 0)
	if len(got) != 3 {
		t.Errorf("空前缀返回 %d 条, 期望 3（全部）", len(got))
	}
}

// ----------------------------------------------------------------------------
// SuggesterImpl 单元测试
// ----------------------------------------------------------------------------

// TestSuggesterImpl_Name 验证构造与基础字段（通过 Suggest 间接验证可调用）。
func TestSuggesterImpl_Construct(t *testing.T) {
	s := NewSuggester(nil, nil)
	if s == nil {
		t.Fatal("NewSuggester 返回 nil")
	}
	if s.ttl != 60*time.Second {
		t.Errorf("默认 ttl = %v, 期望 60s", s.ttl)
	}
	if s.trie == nil {
		t.Error("trie 未初始化")
	}
}

// TestSuggesterImpl_Suggest_PrefixOnly 验证仅前缀来源（hot/history 均为 nil）。
func TestSuggesterImpl_Suggest_PrefixOnly(t *testing.T) {
	s := NewSuggester(nil, nil, WithSeedQueries([]string{"golang", "gopher", "go"}))
	got, err := s.Suggest(context.Background(), "go", domain.UserKey{UserID: "u1"}, 10)
	if err != nil {
		t.Fatalf("Suggest 返回错误: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("返回 %d 条, 期望 3（go/golang/gopher）", len(got))
	}
	// 全部来源应为 prefix
	for _, sg := range got {
		if sg.Source != "prefix" {
			t.Errorf("Source = %q, 期望 prefix（条目 %q）", sg.Source, sg.Text)
		}
	}
}

// TestSuggesterImpl_Suggest_HotFiltered 验证热搜按前缀过滤。
func TestSuggesterImpl_Suggest_HotFiltered(t *testing.T) {
	hot := &stubHotRepo{queries: []domain.SearchSuggestion{
		{Text: "golang tutorial", Score: 0.95},
		{Text: "python web", Score: 0.9},
		{Text: "golang tips", Score: 0.85},
	}}
	s := NewSuggester(hot, nil, WithSeedQueries(nil))
	got, err := s.Suggest(context.Background(), "golang", domain.UserKey{UserID: "u1"}, 10)
	if err != nil {
		t.Fatalf("Suggest 返回错误: %v", err)
	}
	// 仅 golang tutorial / golang tips 命中前缀
	if len(got) != 2 {
		t.Fatalf("返回 %d 条, 期望 2（仅 golang 前缀）", len(got))
	}
	for _, sg := range got {
		if sg.Source != "hot" {
			t.Errorf("Source = %q, 期望 hot（条目 %q）", sg.Source, sg.Text)
		}
	}
}

// TestSuggesterImpl_Suggest_PersonalizeScoring 验证个性化近因打分。
func TestSuggesterImpl_Suggest_PersonalizeScoring(t *testing.T) {
	hist := &stubHistoryRepo{queries: []string{"golang1", "golang2", "golang3"}}
	s := NewSuggester(nil, hist, WithSeedQueries(nil))
	got, err := s.Suggest(context.Background(), "golang", domain.UserKey{UserID: "u1"}, 10)
	if err != nil {
		t.Fatalf("Suggest 返回错误: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("返回 %d 条, 期望 3", len(got))
	}
	// golang1 (i=0) -> 1.0, golang2 (i=1) -> 0.5, golang3 (i=2) -> 0.333...
	if got[0].Text != "golang1" || !floatEq(got[0].Score, 1.0) {
		t.Errorf("首个 = %+v, 期望 golang1/1.0", got[0])
	}
	if got[1].Text != "golang2" || !floatEq(got[1].Score, 0.5) {
		t.Errorf("第二个 = %+v, 期望 golang2/0.5", got[1])
	}
	if got[2].Text != "golang3" || !floatEq(got[2].Score, 1.0/3.0) {
		t.Errorf("第三个 = %+v, 期望 golang3/0.333", got[2])
	}
	for _, sg := range got {
		if sg.Source != "personalize" {
			t.Errorf("Source = %q, 期望 personalize（条目 %q）", sg.Source, sg.Text)
		}
	}
}

// TestSuggesterImpl_Suggest_ThreeWayMerge 验证三路融合 + 去重 + topK 截断。
func TestSuggesterImpl_Suggest_ThreeWayMerge(t *testing.T) {
	hot := &stubHotRepo{queries: []domain.SearchSuggestion{
		{Text: "golang", Score: 0.95}, // 与 prefix 重复，应保留 hot（score 更高）
		{Text: "golang hot", Score: 0.7},
	}}
	hist := &stubHistoryRepo{queries: []string{"golang hist"}}
	s := NewSuggester(hot, hist, WithSeedQueries([]string{"golang", "gopher"}))

	got, err := s.Suggest(context.Background(), "go", domain.UserKey{UserID: "u1"}, 10)
	if err != nil {
		t.Fatalf("Suggest 返回错误: %v", err)
	}
	// 去重后应 4 条：golang(0.95 hot) / gopher(prefix 1.0) / golang hot(0.7) / golang hist(personalize 1.0)
	if len(got) != 4 {
		t.Fatalf("返回 %d 条, 期望 4（去重后）: %+v", len(got), got)
	}
	// 验证降序
	for i := 1; i < len(got); i++ {
		if got[i].Score > got[i-1].Score {
			t.Errorf("未按 score 降序: [%d]=%v > [%d]=%v", i-1, got[i-1].Score, i, got[i].Score)
		}
	}
	// 验证 golang 来源为 hot（0.95 > prefix 的 1.0？不，prefix=1.0 > hot=0.95）
	// 实际：golang 在 prefix 中 score=1.0，在 hot 中 score=0.95，去重保留 1.0（prefix）
	var golangSg *domain.SearchSuggestion
	for i := range got {
		if got[i].Text == "golang" {
			golangSg = &got[i]
			break
		}
	}
	if golangSg == nil {
		t.Fatal("未找到 golang 条目")
	}
	if golangSg.Score != 1.0 {
		t.Errorf("golang score = %v, 期望 1.0（prefix > hot）", golangSg.Score)
	}
	if golangSg.Source != "prefix" {
		t.Errorf("golang Source = %q, 期望 prefix（score 1.0）", golangSg.Source)
	}
}

// TestSuggesterImpl_Suggest_TopKTruncation 验证 topK 截断。
func TestSuggesterImpl_Suggest_TopKTruncation(t *testing.T) {
	hot := &stubHotRepo{queries: []domain.SearchSuggestion{
		{Text: "go1", Score: 0.5},
		{Text: "go2", Score: 0.6},
		{Text: "go3", Score: 0.7},
		{Text: "go4", Score: 0.8},
	}}
	s := NewSuggester(hot, nil)
	got, err := s.Suggest(context.Background(), "go", domain.UserKey{UserID: "u1"}, 2)
	if err != nil {
		t.Fatalf("Suggest 返回错误: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("返回 %d 条, 期望 2（topK 截断）", len(got))
	}
	// 应保留 score 最高的两条
	if got[0].Text != "go4" || got[1].Text != "go3" {
		t.Errorf("topK=2 截断后 = [%s, %s], 期望 [go4, go3]", got[0].Text, got[1].Text)
	}
}

// TestSuggesterImpl_Suggest_TopKZeroFallback 验证 topK<=0 时回退到 10。
func TestSuggesterImpl_Suggest_TopKZeroFallback(t *testing.T) {
	hot := &stubHotRepo{queries: []domain.SearchSuggestion{
		{Text: "go1", Score: 0.5},
		{Text: "go2", Score: 0.6},
	}}
	s := NewSuggester(hot, nil)
	got, err := s.Suggest(context.Background(), "go", domain.UserKey{UserID: "u1"}, 0)
	if err != nil {
		t.Fatalf("Suggest 返回错误: %v", err)
	}
	// topK=0 时不截断（topK>0 才截断），返回全部 2 条
	if len(got) != 2 {
		t.Errorf("topK=0 返回 %d 条, 期望 2（不截断）", len(got))
	}
}

// TestSuggesterImpl_Suggest_EmptyPrefix 验证空前缀返回所有来源。
func TestSuggesterImpl_Suggest_EmptyPrefix(t *testing.T) {
	hot := &stubHotRepo{queries: []domain.SearchSuggestion{
		{Text: "hot1", Score: 0.9},
		{Text: "hot2", Score: 0.8},
	}}
	hist := &stubHistoryRepo{queries: []string{"hist1", "hist2"}}
	s := NewSuggester(hot, hist, WithSeedQueries([]string{"seed1", "seed2"}))

	got, err := s.Suggest(context.Background(), "", domain.UserKey{UserID: "u1"}, 10)
	if err != nil {
		t.Fatalf("Suggest 返回错误: %v", err)
	}
	// 前缀 2 + 热搜 2 + 个性化 2 = 6
	if len(got) != 6 {
		t.Fatalf("空前缀返回 %d 条, 期望 6（全部来源）: %+v", len(got), got)
	}
}

// TestSuggesterImpl_Suggest_NilRepos 验证 hot/history 均为 nil 时不崩溃。
func TestSuggesterImpl_Suggest_NilRepos(t *testing.T) {
	s := NewSuggester(nil, nil)
	got, err := s.Suggest(context.Background(), "any", domain.UserKey{UserID: "u1"}, 10)
	if err != nil {
		t.Fatalf("Suggest 返回错误: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("nil repos 返回 %d 条, 期望 0", len(got))
	}
}

// TestSuggesterImpl_Suggest_HotErrorSkipped 验证 hot 出错仅跳过该来源。
func TestSuggesterImpl_Suggest_HotErrorSkipped(t *testing.T) {
	hot := &stubHotRepo{err: errors.New("hot backend down")}
	hist := &stubHistoryRepo{queries: []string{"golang"}}
	s := NewSuggester(hot, hist, WithSeedQueries([]string{"golang"}))

	got, err := s.Suggest(context.Background(), "go", domain.UserKey{UserID: "u1"}, 10)
	if err != nil {
		t.Fatalf("Suggest 不应返回错误: %v", err)
	}
	// prefix(golang) + personalize(golang) 去重后 = 1 条
	if len(got) != 1 {
		t.Errorf("hot 出错后返回 %d 条, 期望 1（仅 prefix+personalize）: %+v", len(got), got)
	}
}

// TestSuggesterImpl_Suggest_HistoryErrorSkipped 验证 history 出错仅跳过该来源。
func TestSuggesterImpl_Suggest_HistoryErrorSkipped(t *testing.T) {
	hot := &stubHotRepo{queries: []domain.SearchSuggestion{{Text: "golang", Score: 0.9}}}
	hist := &stubHistoryRepo{err: errors.New("history backend down")}
	s := NewSuggester(hot, hist, WithSeedQueries([]string{"golang"}))

	got, err := s.Suggest(context.Background(), "go", domain.UserKey{UserID: "u1"}, 10)
	if err != nil {
		t.Fatalf("Suggest 不应返回错误: %v", err)
	}
	// prefix(golang 1.0) + hot(golang 0.9) 去重后 = 1 条
	if len(got) != 1 {
		t.Errorf("history 出错后返回 %d 条, 期望 1: %+v", len(got), got)
	}
}

// TestSuggesterImpl_Suggest_CacheHit 验证缓存命中。
func TestSuggesterImpl_Suggest_CacheHit(t *testing.T) {
	hot := &stubHotRepo{queries: []domain.SearchSuggestion{
		{Text: "golang", Score: 0.9},
	}}
	s := NewSuggester(hot, nil)

	// 第一次调用
	got1, err := s.Suggest(context.Background(), "go", domain.UserKey{UserID: "u1"}, 10)
	if err != nil {
		t.Fatalf("首次 Suggest 错误: %v", err)
	}
	// 修改 hot 数据（模拟底层变化），缓存应仍返回首次结果
	hot.queries[0].Score = 0.1
	got2, err := s.Suggest(context.Background(), "go", domain.UserKey{UserID: "u1"}, 10)
	if err != nil {
		t.Fatalf("二次 Suggest 错误: %v", err)
	}
	if len(got1) != len(got2) || got1[0].Score != got2[0].Score {
		t.Errorf("缓存未命中: 首次=%+v 二次=%+v", got1, got2)
	}
	if got2[0].Score != 0.9 {
		t.Errorf("缓存值 = %v, 期望 0.9（首次结果）", got2[0].Score)
	}
}

// TestSuggesterImpl_Suggest_CacheExpiry 验证缓存过期后重新采集。
func TestSuggesterImpl_Suggest_CacheExpiry(t *testing.T) {
	hot := &stubHotRepo{queries: []domain.SearchSuggestion{
		{Text: "golang", Score: 0.9},
	}}
	// 用极短 TTL
	s := NewSuggester(hot, nil, WithTTL(20*time.Millisecond))

	// 第一次调用
	got1, err := s.Suggest(context.Background(), "go", domain.UserKey{UserID: "u1"}, 10)
	if err != nil {
		t.Fatalf("首次 Suggest 错误: %v", err)
	}
	if got1[0].Score != 0.9 {
		t.Fatalf("首次 score = %v, 期望 0.9", got1[0].Score)
	}

	// 修改 hot 数据
	hot.queries[0].Score = 0.5
	// 等待缓存过期
	time.Sleep(30 * time.Millisecond)

	got2, err := s.Suggest(context.Background(), "go", domain.UserKey{UserID: "u1"}, 10)
	if err != nil {
		t.Fatalf("二次 Suggest 错误: %v", err)
	}
	if got2[0].Score != 0.5 {
		t.Errorf("过期后 score = %v, 期望 0.5（重新采集）", got2[0].Score)
	}
}

// TestSuggesterImpl_Suggest_CacheKeyIsolation 验证不同 userKey/topK 缓存隔离。
func TestSuggesterImpl_Suggest_CacheKeyIsolation(t *testing.T) {
	hot := &stubHotRepo{queries: []domain.SearchSuggestion{
		{Text: "golang", Score: 0.9},
	}}
	s := NewSuggester(hot, nil)

	// u1 + topK=10
	got1, _ := s.Suggest(context.Background(), "go", domain.UserKey{UserID: "u1"}, 10)
	// u2 + topK=10（不同 userKey，应独立采集）
	hot.queries[0] = domain.SearchSuggestion{Text: "golang", Score: 0.3}
	got2, _ := s.Suggest(context.Background(), "go", domain.UserKey{UserID: "u2"}, 10)
	if got1[0].Score != 0.9 {
		t.Errorf("u1 score = %v, 期望 0.9", got1[0].Score)
	}
	if got2[0].Score != 0.3 {
		t.Errorf("u2 score = %v, 期望 0.3（独立采集）", got2[0].Score)
	}
}

// TestDedupSuggestions 验证去重逻辑（按 Text 保留最高分）。
func TestDedupSuggestions(t *testing.T) {
	in := []domain.SearchSuggestion{
		{Text: "go", Score: 0.5, Source: "prefix"},
		{Text: "go", Score: 0.9, Source: "hot"}, // 重复，分数更高
		{Text: "go", Score: 0.7, Source: "prefix"},
		{Text: "golang", Score: 0.8, Source: "prefix"},
		{Text: "", Score: 1.0, Source: "prefix"}, // 空 Text 跳过
	}
	out := dedupSuggestions(in)
	if len(out) != 2 {
		t.Fatalf("去重后 = %d 条, 期望 2", len(out))
	}
	// 找到 go 与 golang
	scoreMap := map[string]float64{}
	for _, sg := range out {
		scoreMap[sg.Text] = sg.Score
	}
	if scoreMap["go"] != 0.9 {
		t.Errorf("go score = %v, 期望 0.9（取 max）", scoreMap["go"])
	}
	if scoreMap["golang"] != 0.8 {
		t.Errorf("golang score = %v, 期望 0.8", scoreMap["golang"])
	}
}

// TestDedupSuggestions_Empty 验证空输入返回 nil。
func TestDedupSuggestions_Empty(t *testing.T) {
	if out := dedupSuggestions(nil); out != nil {
		t.Errorf("空输入 = %v, 期望 nil", out)
	}
	if out := dedupSuggestions([]domain.SearchSuggestion{}); out != nil {
		t.Errorf("空切片 = %v, 期望 nil", out)
	}
}

// TestHasPrefixFold 验证大小写不敏感前缀匹配。
func TestHasPrefixFold(t *testing.T) {
	cases := []struct {
		s, prefix string
		want      bool
	}{
		{"Golang Tutorial", "go", true},
		{"golang", "GOLANG", true},
		{"Python", "go", false},
		{"", "go", false},
		{"go", "", true},
		{"Gopher", "gop", true},
	}
	for _, c := range cases {
		if got := hasPrefixFold(c.s, c.prefix); got != c.want {
			t.Errorf("hasPrefixFold(%q, %q) = %v, 期望 %v", c.s, c.prefix, got, c.want)
		}
	}
}

// TestCacheKey 验证缓存键生成。
func TestCacheKey(t *testing.T) {
	k1 := cacheKey("go", domain.UserKey{UserID: "u1", Channel: "c1"}, 10)
	k2 := cacheKey("go", domain.UserKey{UserID: "u1", Channel: "c1"}, 10)
	k3 := cacheKey("go", domain.UserKey{UserID: "u2", Channel: "c1"}, 10)
	k4 := cacheKey("go", domain.UserKey{UserID: "u1", Channel: "c1"}, 20)
	if k1 != k2 {
		t.Errorf("相同参数键不一致: %q vs %q", k1, k2)
	}
	if k1 == k3 {
		t.Errorf("不同 UserID 键相同: %q", k1)
	}
	if k1 == k4 {
		t.Errorf("不同 topK 键相同: %q", k1)
	}
}

// TestWithTTL_ZeroOrNegative 验证 TTL<=0 时保持默认值。
func TestWithTTL_ZeroOrNegative(t *testing.T) {
	s := NewSuggester(nil, nil, WithTTL(0))
	if s.ttl != 60*time.Second {
		t.Errorf("TTL=0 后 ttl = %v, 期望保持默认 60s", s.ttl)
	}
	s = NewSuggester(nil, nil, WithTTL(-1 * time.Second))
	if s.ttl != 60*time.Second {
		t.Errorf("TTL<0 后 ttl = %v, 期望保持默认 60s", s.ttl)
	}
}

// TestWithSeedQueries 验证种子词库注入。
func TestWithSeedQueries(t *testing.T) {
	s := NewSuggester(nil, nil, WithSeedQueries([]string{"go", "golang", "gopher"}))
	got, err := s.Suggest(context.Background(), "go", domain.UserKey{UserID: "u1"}, 10)
	if err != nil {
		t.Fatalf("Suggest 错误: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("种子词库返回 %d 条, 期望 3", len(got))
	}
}
