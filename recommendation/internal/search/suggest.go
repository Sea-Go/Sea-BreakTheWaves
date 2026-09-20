// ============================================================================
// 该文件实现搜索补全 SuggesterImpl，三路融合 + 缓存。
//
// 三路来源：
//   - 前缀匹配（内置 Trie，由种子词库构建）
//   - 热搜（HotQueryRepo.TopK，按前缀过滤）
//   - 个性化（HistoryRepo.RecentQueries，按前缀过滤，按近因打分）
//
// 性能：
//   - sync.Map + TTL 缓存最终结果，重复请求 < 100ms
//   - Trie 与缓存均为内存操作
//
// 二开扩展点：
//   - 实现 HotQueryRepo / HistoryRepo interface 接入自研数据源
//   - WithSeedQueries 注入业务种子词库；WithTTL 调整缓存有效期
// ============================================================================

package search

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sea/internal/domain"
)

// HotQueryRepo 热搜查询契约。
type HotQueryRepo interface {
	// TopK 返回前 topK 热搜建议。
	TopK(ctx context.Context, topK int) ([]domain.SearchSuggestion, error)
}

// HistoryRepo 用户搜索历史契约。
// 注：本文件使用 HistoryRepo 名称，避免与 Task 10.6 的 HistoryStore 冲突。
type HistoryRepo interface {
	// RecentQueries 返回用户最近 topK 条搜索词（按时间倒序，最新在前）。
	RecentQueries(ctx context.Context, userKey domain.UserKey, topK int) ([]string, error)
}

// Suggester 搜索补全 interface。
type Suggester interface {
	// Suggest 返回补全建议。prefix 为前缀；userKey 用于个性化；topK 为返回数量。
	Suggest(ctx context.Context, prefix string, userKey domain.UserKey, topK int) ([]domain.SearchSuggestion, error)
}

// suggestCacheEntry 缓存条目。
type suggestCacheEntry struct {
	suggestions []domain.SearchSuggestion
	cachedAt    time.Time
}

// SuggesterImpl 搜索补全实现。
type SuggesterImpl struct {
	hot     HotQueryRepo
	history HistoryRepo
	trie    *Trie
	cache   sync.Map
	ttl     time.Duration
	now     func() time.Time
}

// SuggesterOption SuggesterImpl 构造选项。
type SuggesterOption func(*SuggesterImpl)

// WithSeedQueries 注入 Trie 种子词库（前缀匹配来源）。
func WithSeedQueries(qs []string) SuggesterOption {
	return func(s *SuggesterImpl) {
		for _, q := range qs {
			s.trie.Insert(q, 1.0)
		}
	}
}

// WithTTL 设置缓存有效期（默认 60s）。
func WithTTL(ttl time.Duration) SuggesterOption {
	return func(s *SuggesterImpl) {
		if ttl > 0 {
			s.ttl = ttl
		}
	}
}

// NewSuggester 创建搜索补全器。
// hot / history 均可为 nil（nil 的来源被跳过）；opts 为构造选项。
func NewSuggester(hot HotQueryRepo, history HistoryRepo, opts ...SuggesterOption) *SuggesterImpl {
	s := &SuggesterImpl{
		hot:     hot,
		history: history,
		trie:    NewTrie(),
		ttl:     60 * time.Second,
		now:     time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Suggest 返回补全建议，实现 Suggester.Suggest。
//
// 流程：
//  1. 命中缓存（sync.Map + TTL）直接返回；
//  2. 三路并行采集：前缀（Trie）/ 热搜（按前缀过滤）/ 个性化（历史按前缀过滤，近因打分）；
//  3. 按 Text 去重（保留最高分），按 Score 降序截断到 topK；
//  4. 写入缓存后返回。
//
// 任一来源失败仅跳过该来源，不阻断整体。
func (s *SuggesterImpl) Suggest(ctx context.Context, prefix string, userKey domain.UserKey, topK int) ([]domain.SearchSuggestion, error) {
	ck := cacheKey(prefix, userKey, topK)
	if v, ok := s.cache.Load(ck); ok {
		e := v.(suggestCacheEntry)
		if s.now().Sub(e.cachedAt) < s.ttl {
			return e.suggestions, nil
		}
	}

	fetchK := topK
	if fetchK <= 0 {
		fetchK = 10
	}

	var combined []domain.SearchSuggestion

	// 前缀匹配（Trie）
	for _, sg := range s.trie.PrefixSearch(prefix, fetchK*2) {
		combined = append(combined, sg)
	}

	// 热搜
	if s.hot != nil {
		hots, err := s.hot.TopK(ctx, fetchK*2)
		if err == nil {
			for _, h := range hots {
				if prefix == "" || hasPrefixFold(h.Text, prefix) {
					h.Source = "hot"
					combined = append(combined, h)
				}
			}
		}
	}

	// 个性化（历史）
	if s.history != nil {
		recents, err := s.history.RecentQueries(ctx, userKey, fetchK*2)
		if err == nil {
			for i, q := range recents {
				if q == "" {
					continue
				}
				if prefix == "" || hasPrefixFold(q, prefix) {
					// 近因打分：最新 = 1.0，依次衰减
					score := 1.0 / float64(i+1)
					combined = append(combined, domain.SearchSuggestion{Text: q, Score: score, Source: "personalize"})
				}
			}
		}
	}

	merged := dedupSuggestions(combined)
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Score > merged[j].Score
	})
	if topK > 0 && len(merged) > topK {
		merged = merged[:topK]
	}

	s.cache.Store(ck, suggestCacheEntry{suggestions: merged, cachedAt: s.now()})
	return merged, nil
}

// cacheKey 生成缓存键。
func cacheKey(prefix string, userKey domain.UserKey, topK int) string {
	return prefix + "|" + userKey.UserID + "|" + userKey.Channel + "|" + strconv.Itoa(topK)
}

// hasPrefixFold 大小写不敏感的前缀匹配。
func hasPrefixFold(s, prefix string) bool {
	return strings.HasPrefix(strings.ToLower(s), strings.ToLower(prefix))
}

// dedupSuggestions 按 Text 去重，保留最高分（来源随最高分）。
func dedupSuggestions(in []domain.SearchSuggestion) []domain.SearchSuggestion {
	if len(in) == 0 {
		return nil
	}
	best := make(map[string]domain.SearchSuggestion, len(in))
	order := make([]string, 0, len(in))
	for _, sg := range in {
		if sg.Text == "" {
			continue
		}
		if cur, ok := best[sg.Text]; !ok {
			best[sg.Text] = sg
			order = append(order, sg.Text)
		} else if sg.Score > cur.Score {
			best[sg.Text] = sg
		}
	}
	out := make([]domain.SearchSuggestion, 0, len(order))
	for _, k := range order {
		out = append(out, best[k])
	}
	return out
}

// ----------------------------------------------------------------------------
// Trie 内置前缀树实现
// ----------------------------------------------------------------------------

// trieNode Trie 节点。
type trieNode struct {
	children map[rune]*trieNode
	isEnd    bool
	text     string
	score    float64
}

// Trie 前缀树，支持插入与前缀检索。
type Trie struct {
	root *trieNode
}

// NewTrie 创建空 Trie。
func NewTrie() *Trie {
	return &Trie{root: &trieNode{children: make(map[rune]*trieNode)}}
}

// Insert 插入一条文本（多次插入取最大 score）。
func (t *Trie) Insert(text string, score float64) {
	if text == "" {
		return
	}
	node := t.root
	for _, r := range text {
		c, ok := node.children[r]
		if !ok {
			c = &trieNode{children: make(map[rune]*trieNode)}
			node.children[r] = c
		}
		node = c
	}
	node.isEnd = true
	node.text = text
	if score > node.score {
		node.score = score
	}
}

// PrefixSearch 返回所有以 prefix 开头的文本，按 score 降序取 topK（topK<=0 不截断）。
// 返回的 SearchSuggestion.Source 为 "prefix"。
func (t *Trie) PrefixSearch(prefix string, topK int) []domain.SearchSuggestion {
	node := t.root
	for _, r := range prefix {
		c, ok := node.children[r]
		if !ok {
			return nil
		}
		node = c
	}
	var results []domain.SearchSuggestion
	t.collect(node, &results)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}
	for i := range results {
		results[i].Source = "prefix"
	}
	return results
}

// collect 深度优先收集子树所有完整词条。
func (t *Trie) collect(node *trieNode, out *[]domain.SearchSuggestion) {
	if node == nil {
		return
	}
	if node.isEnd {
		*out = append(*out, domain.SearchSuggestion{Text: node.text, Score: node.score})
	}
	for _, c := range node.children {
		t.collect(c, out)
	}
}
