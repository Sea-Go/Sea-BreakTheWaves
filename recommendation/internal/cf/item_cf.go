package cf

import (
	"math"
	"sort"
	"sync"

	"sea/internal/domain"
)

// ============================================================================
// Item-CF：基于物品的协同过滤（Task 7.2）。
// 流程：构建文章共现矩阵 -> 计算 Jaccard/余弦相似度 -> 基于已看文章找相似
// 文章召回。
//
// CoOccurrenceMatrix 同时提供 Task 7.4（online.go）所需的实时增量接口
// （Increment/Retrain/GetSimilar），故加 mu 互斥锁支撑并发流式更新。
//
// 二开点：
//   - 相似度度量：通过 NewItemCF 的 metric 参数切换 "jaccard"/"cosine"，
//     或扩展 ComputeSimilarity 支持自定义度量
//   - 共现归一化：可扩展 CoOccurrenceMatrix 加入时间衰减/位置加权
// ============================================================================

// ItemCF Item-based 协同过滤。
type ItemCF struct {
	// minCoOccurrence 最小共现次数，低于该值视为相似度为 0（去噪）。
	minCoOccurrence int
	// similarityMetric 相似度度量："jaccard" 或 "cosine"。
	similarityMetric string
}

// NewItemCF 创建 Item-CF。metric 取值 "jaccard" 或 "cosine"。
func NewItemCF(minCoOccurrence int, metric string) *ItemCF {
	return &ItemCF{
		minCoOccurrence:  minCoOccurrence,
		similarityMetric: metric,
	}
}

// CoOccurrenceMatrix 文章共现矩阵（对称）。
// counts[a][b] 表示文章 a 与 b 被同一用户交互过的共现次数。
// mu 保护并发流式更新（Task 7.4 online.go 通过 Increment 实时写入）。
type CoOccurrenceMatrix struct {
	counts map[string]map[string]int
	mu     sync.RWMutex
}

// NewCoOccurrenceMatrix 创建共现矩阵。
func NewCoOccurrenceMatrix() *CoOccurrenceMatrix {
	return &CoOccurrenceMatrix{
		counts: make(map[string]map[string]int),
	}
}

// AddCoOccurrence 增加一次共现（双向对称，自身不计共现）。
func (m *CoOccurrenceMatrix) AddCoOccurrence(articleA, articleB string) {
	if articleA == articleB {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.counts[articleA] == nil {
		m.counts[articleA] = make(map[string]int)
	}
	if m.counts[articleB] == nil {
		m.counts[articleB] = make(map[string]int)
	}
	m.counts[articleA][articleB]++
	m.counts[articleB][articleA]++
}

// GetCoOccurrence 获取两篇文章的共现次数。
func (m *CoOccurrenceMatrix) GetCoOccurrence(articleA, articleB string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if row, ok := m.counts[articleA]; ok {
		return row[articleB]
	}
	return 0
}

// TotalCoOccurrence 获取某篇文章与所有其他文章的共现次数总和（用于归一化）。
func (m *CoOccurrenceMatrix) TotalCoOccurrence(article string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sum := 0
	if row, ok := m.counts[article]; ok {
		for _, c := range row {
			sum += c
		}
	}
	return sum
}

// CoOccurredArticles 返回与指定文章共现过的所有文章 ID。
func (m *CoOccurrenceMatrix) CoOccurredArticles(article string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	row, ok := m.counts[article]
	if !ok {
		return nil
	}
	arts := make([]string, 0, len(row))
	for other := range row {
		arts = append(arts, other)
	}
	return arts
}

// Increment 流式增量更新共现计数（Task 7.4 online.go 调用，线程安全）。
// 语义等价于 AddCoOccurrence，保留以兼容 online.go 的实时 CF 接口。
func (m *CoOccurrenceMatrix) Increment(articleA, articleB string) {
	m.AddCoOccurrence(articleA, articleB)
}

// Retrain 全量重训共现矩阵：从交互记录重建共现统计（Task 7.4 online.go 调用）。
// 同一用户交互过的文章两两共现 +1（按用户聚合）。
func (m *CoOccurrenceMatrix) Retrain(interactions []Interaction) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts = make(map[string]map[string]int)
	userArticles := make(map[string][]string)
	for _, it := range interactions {
		if it.UserID == "" || it.ArticleID == "" {
			continue
		}
		userArticles[it.UserID] = append(userArticles[it.UserID], it.ArticleID)
	}
	for _, arts := range userArticles {
		for i := 0; i < len(arts); i++ {
			for j := i + 1; j < len(arts); j++ {
				a, b := arts[i], arts[j]
				if a == b {
					continue
				}
				if m.counts[a] == nil {
					m.counts[a] = make(map[string]int)
				}
				if m.counts[b] == nil {
					m.counts[b] = make(map[string]int)
				}
				m.counts[a][b]++
				m.counts[b][a]++
			}
		}
	}
}

// GetSimilar 返回与指定文章共现次数最多的 topN 篇文章（Task 7.4 online.go 调用）。
// 按共现次数（Similarity）倒序排列；topN <= 0 时不截断。
func (m *CoOccurrenceMatrix) GetSimilar(articleID string, topN int) []SimilarItem {
	m.mu.RLock()
	row, ok := m.counts[articleID]
	if !ok {
		m.mu.RUnlock()
		return nil
	}
	items := make([]SimilarItem, 0, len(row))
	for id, count := range row {
		items = append(items, SimilarItem{ArticleID: id, Similarity: float64(count)})
	}
	m.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		return items[i].Similarity > items[j].Similarity
	})
	if topN > 0 && len(items) > topN {
		items = items[:topN]
	}
	return items
}

// ComputeSimilarity 计算两篇文章的相似度（Jaccard 或余弦）。
// 二开点：可扩展支持皮尔逊/自定义度量。
func (icf *ItemCF) ComputeSimilarity(matrix *CoOccurrenceMatrix, articleA, articleB string) float64 {
	co := matrix.GetCoOccurrence(articleA, articleB)
	if co < icf.minCoOccurrence {
		return 0
	}
	countA := matrix.TotalCoOccurrence(articleA)
	countB := matrix.TotalCoOccurrence(articleB)
	switch icf.similarityMetric {
	case "jaccard":
		denom := countA + countB - co
		if denom <= 0 {
			return 0
		}
		return float64(co) / float64(denom)
	case "cosine", "":
		if countA == 0 || countB == 0 {
			return 0
		}
		return float64(co) / math.Sqrt(float64(countA)*float64(countB))
	default:
		return 0
	}
}

// SimilarItem 相似文章。
type SimilarItem struct {
	// ArticleID 文章 ID。
	ArticleID string
	// Similarity 相似度。
	Similarity float64
}

// FindSimilarArticles 查找与指定文章最相似的 Top-N 文章。
// topN <= 0 时返回所有相似文章。
func (icf *ItemCF) FindSimilarArticles(articleID string, matrix *CoOccurrenceMatrix, topN int) []SimilarItem {
	candidates := matrix.CoOccurredArticles(articleID)
	items := make([]SimilarItem, 0, len(candidates))
	for _, other := range candidates {
		sim := icf.ComputeSimilarity(matrix, articleID, other)
		if sim > 0 {
			items = append(items, SimilarItem{ArticleID: other, Similarity: sim})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Similarity > items[j].Similarity
	})
	if topN > 0 && len(items) > topN {
		items = items[:topN]
	}
	return items
}

// Recommend 基于用户已看文章推荐相似文章。
// 分数聚合：对每个已看文章找相似文章，累加相似度（已看文章过滤掉）。
func (icf *ItemCF) Recommend(articleIDs []string, matrix *CoOccurrenceMatrix, topK int) []domain.Candidate {
	seen := make(map[string]bool)
	for _, aid := range articleIDs {
		seen[aid] = true
	}
	scores := make(map[string]float64)
	for _, aid := range articleIDs {
		for _, sim := range icf.FindSimilarArticles(aid, matrix, 0) {
			if seen[sim.ArticleID] {
				continue
			}
			scores[sim.ArticleID] += sim.Similarity
		}
	}
	cands := make([]domain.Candidate, 0, len(scores))
	for aid, s := range scores {
		cands = append(cands, domain.Candidate{
			ArticleID: aid,
			Score:     s,
			Source:    "cf",
			Scores:    map[string]float64{"cf_score": s},
		})
	}
	sort.Slice(cands, func(i, j int) bool {
		return cands[i].Score > cands[j].Score
	})
	if topK > 0 && len(cands) > topK {
		cands = cands[:topK]
	}
	return cands
}
