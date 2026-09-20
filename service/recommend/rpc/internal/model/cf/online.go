package cf

import (
	"context"
	"sync"
	"time"

	"sea/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件实现实时 CF 与增量更新（Task 7.4）。
//
// 核心能力：
//   - OnlineCF 实现 domain.Hook，监听点击事件实时更新共现矩阵
//   - 流式增量更新共现矩阵（UpdateCoOccurrence，线程安全）
//   - 定时触发全量/增量训练（MaybeRetrain，1h 全量 / 5min 增量）
//   - 实时查询相似文章（GetSimilarArticles）
//   - CFFeedbackHook 写入共现矩阵 + 图谱 CO_OCCURRED_WITH 边
//     （通过 GraphEdgeWriter interface 抽象，可注入 domain.GraphQuerier 实现）
//
// 二开扩展点：
//   - 调整 fullRetrainInterval / incrementalInterval 改变训练节奏
//   - 实现 GraphEdgeWriter interface 注入自研图谱后端
//   - 替换 userRecentArticles 维护策略（如接入 Redis 会话存储）
// ============================================================================

// 默认训练间隔。
const (
	// defaultFullRetrainInterval 默认全量重训间隔（1 小时）。
	defaultFullRetrainInterval = time.Hour
	// defaultIncrementalInterval 默认增量训练间隔（5 分钟）。
	defaultIncrementalInterval = 5 * time.Minute
	// maxUserRecentArticles 每用户保留的近期点击文章上限（用于流式共现计算）。
	maxUserRecentArticles = 50
)

// GraphEdgeWriter 图谱 CO_OCCURRED_WITH 边写入抽象。
// 二开：实现该 interface 注入 domain.GraphQuerier 或自定义图谱客户端，
// 将文章共现关系写入图谱供 GraphRecaller 多跳召回使用。
type GraphEdgeWriter interface {
	// AddCOOccurredEdge 写入文章 A 与文章 B 的 CO_OCCURRED_WITH 边。
	// 实现应做幂等处理（重复写入只更新 count 属性）。
	AddCOOccurredEdge(ctx context.Context, articleA, articleB string) error
}

// OnlineCF 实时协同过滤，实现 domain.Hook 监听点击事件流式更新共现矩阵，
// 并按时间间隔触发全量/增量训练。
//
// 工作流程：
//  1. 点击事件 → OnEvent → 更新用户近期文章列表 → 两两共现 +1 → 写图谱边
//  2. MaybeRetrain → 检查间隔 → 全量重训（matrix.Retrain + mf.Retrain）
//     或增量训练（mf.IncrementalTrain）
//  3. GetSimilarArticles → 实时查共现矩阵返回相似文章
type OnlineCF struct {
	matrix              *CoOccurrenceMatrix
	mf                  *MF
	fullRetrainInterval time.Duration
	incrementalInterval time.Duration

	mu              sync.RWMutex
	lastFullRetrain time.Time
	lastIncremental time.Time

	// userRecentArticles 用户近期点击文章列表（userID -> []articleID），
	// 用于点击事件时与已有文章计算两两共现。
	userRecentMu       sync.RWMutex
	userRecentArticles map[string][]string

	// graph 图谱边写入器（可选，nil 时跳过图谱边写入）。
	graph GraphEdgeWriter
}

// NewOnlineCF 创建实时 CF，默认 1h 全量 / 5min 增量。
// matrix 文章共现矩阵；mf 矩阵分解模型（用于全量/增量训练触发）。
func NewOnlineCF(matrix *CoOccurrenceMatrix, mf *MF) *OnlineCF {
	return &OnlineCF{
		matrix:              matrix,
		mf:                  mf,
		fullRetrainInterval: defaultFullRetrainInterval,
		incrementalInterval: defaultIncrementalInterval,
		userRecentArticles:  make(map[string][]string),
	}
}

// SetGraphEdgeWriter 注入图谱边写入器（可选）。
// 二开：注入 domain.GraphQuerier 适配器，将共现关系写入 Neo4j 图谱。
func (o *OnlineCF) SetGraphEdgeWriter(w GraphEdgeWriter) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.graph = w
}

// SetRetrainIntervals 调整全量/增量训练间隔（二开点）。
// full 全量重训间隔；incremental 增量训练间隔。
func (o *OnlineCF) SetRetrainIntervals(full, incremental time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.fullRetrainInterval = full
	o.incrementalInterval = incremental
}

// Name 返回 Hook 名称，实现 domain.Hook.Name。
func (o *OnlineCF) Name() string { return "online_cf" }

// OnEvent 处理行为事件，实现 domain.Hook.OnEvent。
// 仅处理 click 事件：更新用户近期文章列表 → 与已有文章两两共现更新 → 写图谱边。
// 错误隔离：图谱边写入失败不影响共现矩阵更新。
func (o *OnlineCF) OnEvent(ctx context.Context, e domain.BehaviorEvent) error {
	if e.EventType != domain.EventClick {
		return nil
	}
	if e.UserID == "" || e.ArticleID == "" {
		return nil
	}

	// 取用户已有近期文章，与新文章两两共现更新。
	existing := o.getUserRecentArticles(e.UserID)
	for _, prev := range existing {
		if prev == e.ArticleID {
			continue
		}
		// 流式更新共现矩阵。
		o.UpdateCoOccurrence(prev, e.ArticleID)
		// 写图谱 CO_OCCURRED_WITH 边（best-effort，失败不阻断）。
		if w := o.getGraphWriter(); w != nil {
			_ = w.AddCOOccurredEdge(ctx, prev, e.ArticleID)
		}
	}

	// 追加新文章到用户近期列表。
	o.appendUserRecentArticle(e.UserID, e.ArticleID)
	return nil
}

// UpdateCoOccurrence 流式更新共现矩阵（线程安全）。
// articleA/articleB 两篇共现文章；委托给 CoOccurrenceMatrix.Increment。
func (o *OnlineCF) UpdateCoOccurrence(articleA, articleB string) {
	if o.matrix == nil {
		return
	}
	o.matrix.Increment(articleA, articleB)
}

// MaybeRetrain 检查时间间隔，触发全量或增量训练。
// interactions 训练数据（用户-文章交互记录）。
//   - 距上次全量训练 ≥ fullRetrainInterval → 全量重训（matrix + mf）
//   - 否则距上次增量训练 ≥ incrementalInterval → 增量训练（mf）
//   - 均未到间隔 → 跳过
func (o *OnlineCF) MaybeRetrain(ctx context.Context, interactions []Interaction) error {
	now := time.Now()
	o.mu.RLock()
	fullInterval := o.fullRetrainInterval
	incrInterval := o.incrementalInterval
	lastFull := o.lastFullRetrain
	lastIncr := o.lastIncremental
	o.mu.RUnlock()

	// 全量重训判断。
	if now.Sub(lastFull) >= fullInterval {
		if o.matrix != nil {
			o.matrix.Retrain(interactions)
		}
		if o.mf != nil {
			if err := o.mf.Retrain(interactions); err != nil {
				return err
			}
		}
		o.mu.Lock()
		o.lastFullRetrain = now
		o.lastIncremental = now
		o.mu.Unlock()
		return nil
	}

	// 增量训练判断。
	if now.Sub(lastIncr) >= incrInterval {
		if o.mf != nil {
			if err := o.mf.IncrementalTrain(interactions); err != nil {
				return err
			}
		}
		o.mu.Lock()
		o.lastIncremental = now
		o.mu.Unlock()
		return nil
	}

	return nil
}

// GetSimilarArticles 实时查询相似文章。
// articleID 目标文章；topN 返回数量上限。
// 返回 SimilarItem 列表（按相似度倒序）。
func (o *OnlineCF) GetSimilarArticles(articleID string, topN int) []SimilarItem {
	if o.matrix == nil {
		return nil
	}
	return o.matrix.GetSimilar(articleID, topN)
}

// ForceFullRetrainNow 立即触发一次全量重训（重置时间戳）。
// 二开：用于管理后台手动触发全量重训或测试中重置状态。
func (o *OnlineCF) ForceFullRetrainNow() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lastFullRetrain = time.Time{}
}

// getUserRecentArticles 获取用户近期点击文章列表（线程安全）。
func (o *OnlineCF) getUserRecentArticles(userID string) []string {
	o.userRecentMu.RLock()
	defer o.userRecentMu.RUnlock()
	arts := o.userRecentArticles[userID]
	out := make([]string, len(arts))
	copy(out, arts)
	return out
}

// appendUserRecentArticle 追加文章到用户近期列表并截断到上限（线程安全）。
func (o *OnlineCF) appendUserRecentArticle(userID, articleID string) {
	o.userRecentMu.Lock()
	defer o.userRecentMu.Unlock()
	arts := o.userRecentArticles[userID]
	arts = append(arts, articleID)
	if len(arts) > maxUserRecentArticles {
		arts = arts[len(arts)-maxUserRecentArticles:]
	}
	o.userRecentArticles[userID] = arts
}

// getGraphWriter 获取图谱边写入器（线程安全）。
func (o *OnlineCF) getGraphWriter() GraphEdgeWriter {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.graph
}

// 编译期断言：*OnlineCF 实现 domain.Hook interface。
var _ domain.Hook = (*OnlineCF)(nil)
