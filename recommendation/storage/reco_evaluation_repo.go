package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	types "sea/type"
)

type RecoEvaluationRepo struct {
	db *sql.DB
}

func NewRecoEvaluationRepo(db *sql.DB) *RecoEvaluationRepo {
	if db == nil {
		return nil
	}
	return &RecoEvaluationRepo{db: db}
}

func (r *RecoEvaluationRepo) RecordRequest(ctx context.Context, item types.RecoRequestLog) error {
	if r == nil || r.db == nil {
		return sql.ErrConnDone
	}

	item.RecRequestID = strings.TrimSpace(item.RecRequestID)
	if item.RecRequestID == "" {
		return nil
	}
	if item.Surface == "" {
		item.Surface = "home_feed"
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO reco_request_logs(
			rec_request_id, user_id, session_id, surface, query, status,
			returned_count, candidate_count, created_at
		)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT(rec_request_id) DO UPDATE SET
			user_id = EXCLUDED.user_id,
			session_id = EXCLUDED.session_id,
			surface = EXCLUDED.surface,
			query = EXCLUDED.query,
			status = EXCLUDED.status,
			returned_count = EXCLUDED.returned_count,
			candidate_count = EXCLUDED.candidate_count,
			created_at = EXCLUDED.created_at
	`, item.RecRequestID, item.UserID, item.SessionID, item.Surface, item.Query, item.Status, item.ReturnedCount, item.CandidateCount, item.CreatedAt)
	return err
}

func (r *RecoEvaluationRepo) RecordEvents(ctx context.Context, events []types.RecoEventLog) (int, error) {
	if r == nil || r.db == nil {
		return 0, sql.ErrConnDone
	}

	accepted := 0
	for _, event := range events {
		event.RecRequestID = strings.TrimSpace(event.RecRequestID)
		event.ArticleID = strings.TrimSpace(event.ArticleID)
		event.EventType = normalizeRecoEventType(event.EventType)
		if event.EventType == "" || event.ArticleID == "" {
			continue
		}
		if event.Surface == "" {
			event.Surface = "home_feed"
		}
		if event.EventTS.IsZero() {
			event.EventTS = time.Now()
		}
		metadata, err := json.Marshal(event.Metadata)
		if err != nil || len(metadata) == 0 || string(metadata) == "null" {
			metadata = []byte("{}")
		}

		_, err = r.db.ExecContext(ctx, `
			INSERT INTO reco_event_logs(
				rec_request_id, user_id, session_id, surface, article_id, rank,
				event_type, event_ts, metadata
			)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb)
		`, event.RecRequestID, event.UserID, event.SessionID, event.Surface, event.ArticleID, event.Rank, event.EventType, event.EventTS, string(metadata))
		if err != nil {
			return accepted, err
		}
		accepted++
	}

	return accepted, nil
}

func (r *RecoEvaluationRepo) Summary(ctx context.Context, surface string, window time.Duration) (types.RecoEvaluationSummary, error) {
	if r == nil || r.db == nil {
		return types.RecoEvaluationSummary{}, sql.ErrConnDone
	}
	if window <= 0 {
		window = 24 * time.Hour
	}
	surface = strings.TrimSpace(surface)
	if surface == "" {
		surface = "dashboard_recommend"
	}

	since := time.Now().Add(-window)
	counts, err := r.loadCounts(ctx, surface, since)
	if err != nil {
		return types.RecoEvaluationSummary{}, err
	}

	coverage, err := r.coverage(ctx, surface, since)
	if err != nil {
		return types.RecoEvaluationSummary{}, err
	}
	diversity, err := r.diversity(ctx, surface, since)
	if err != nil {
		return types.RecoEvaluationSummary{}, err
	}
	personalization, err := r.personalization(ctx, surface, since)
	if err != nil {
		return types.RecoEvaluationSummary{}, err
	}
	ndcg, err := r.ndcg(ctx, surface, since)
	if err != nil {
		return types.RecoEvaluationSummary{}, err
	}

	hitRate := safeRatio(float64(counts.PositiveRequests), float64(counts.Requests))
	precision := safeRatio(float64(counts.PositiveItems), float64(counts.Impressions))
	recallApprox := safeRatio(float64(counts.PositiveItems), float64(maxInt64(counts.UniqueArticles, 1)))
	ctr := safeRatio(float64(counts.Clicks), float64(counts.Impressions))
	cvr := safeRatio(float64(counts.Conversions), float64(counts.Clicks))

	return types.RecoEvaluationSummary{
		Surface:         surface,
		Window:          formatWindow(window),
		WindowSeconds:   int64(window.Seconds()),
		GeneratedAt:     time.Now(),
		RequestCount:    counts.Requests,
		ImpressionCount: counts.Impressions,
		ClickCount:      counts.Clicks,
		ConversionCount: counts.Conversions,
		MetricValues: []types.RecoMetricValue{
			metric("hit_rate", "命中率", "推荐效果指标", hitRate, "ratio", "窗口内至少产生一次正反馈的推荐请求占比。", "online_implicit"),
			metric("recall", "召回率", "推荐效果指标", recallApprox, "ratio", "线上隐式近似值；严格召回率由离线评测集补充。", "online_implicit"),
			metric("precision", "准确率", "推荐效果指标", precision, "ratio", "推荐曝光内容中产生正反馈的内容占比。", "online_implicit"),
			metric("ranking_quality", "排序质量", "推荐效果指标", ndcg, "ratio", "基于点击/点赞/收藏/阅读完成权重计算的 NDCG@10。", "online_implicit"),
			metric("diversity", "多样性", "推荐列表质量指标", diversity, "ratio", "单次推荐列表内主题标签去重占比。", "online_implicit"),
			metric("coverage", "覆盖率", "推荐列表质量指标", coverage, "ratio", "窗口内被推荐过的内容占当前推荐内容库比例。", "online_implicit"),
			metric("personalization", "个性化程度", "个性化指标", personalization, "ratio", "不同用户推荐列表的平均 1-Jaccard 相似度。", "online_implicit"),
			metric("ctr", "点击率（CTR）", "用户行为指标", ctr, "ratio", "点击事件数 / 曝光事件数。", "online_behavior"),
			metric("cvr", "转化率（CVR）", "用户行为指标", cvr, "ratio", "点赞、收藏、完整阅读等转化事件数 / 点击事件数。", "online_behavior"),
		},
	}, nil
}

type recoCounts struct {
	Requests         int64
	Impressions      int64
	Clicks           int64
	Conversions      int64
	PositiveRequests int64
	PositiveItems    int64
	UniqueArticles   int64
}

func (r *RecoEvaluationRepo) loadCounts(ctx context.Context, surface string, since time.Time) (recoCounts, error) {
	var counts recoCounts
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM reco_request_logs
		WHERE surface = $1 AND created_at >= $2
	`, surface, since).Scan(&counts.Requests); err != nil {
		return counts, err
	}

	if err := r.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE event_type = 'impression'),
			COUNT(*) FILTER (WHERE event_type = 'click'),
			COUNT(*) FILTER (WHERE event_type IN ('like','favorite','read_complete')),
			COUNT(DISTINCT rec_request_id) FILTER (WHERE event_type IN ('click','like','favorite','read_complete')),
			COUNT(DISTINCT rec_request_id || ':' || article_id) FILTER (WHERE event_type IN ('click','like','favorite','read_complete')),
			COUNT(DISTINCT article_id) FILTER (WHERE event_type = 'impression')
		FROM reco_event_logs
		WHERE surface = $1 AND event_ts >= $2
	`, surface, since).Scan(
		&counts.Impressions,
		&counts.Clicks,
		&counts.Conversions,
		&counts.PositiveRequests,
		&counts.PositiveItems,
		&counts.UniqueArticles,
	); err != nil {
		return counts, err
	}

	return counts, nil
}

func (r *RecoEvaluationRepo) coverage(ctx context.Context, surface string, since time.Time) (float64, error) {
	var recommended, total int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT article_id)
		FROM reco_event_logs
		WHERE surface = $1 AND event_type = 'impression' AND event_ts >= $2
	`, surface, since).Scan(&recommended); err != nil {
		return 0, err
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM articles`).Scan(&total); err != nil {
		return 0, err
	}
	return safeRatio(float64(recommended), float64(total)), nil
}

func (r *RecoEvaluationRepo) diversity(ctx context.Context, surface string, since time.Time) (float64, error) {
	var value sql.NullFloat64
	err := r.db.QueryRowContext(ctx, `
		WITH list_stats AS (
			SELECT
				e.rec_request_id,
				COUNT(DISTINCT e.article_id)::float AS total_items,
				COUNT(DISTINCT NULLIF(COALESCE(a.type_tags, a.tags, ''), ''))::float AS topic_items
			FROM reco_event_logs e
			LEFT JOIN articles a ON a.article_id = e.article_id
			WHERE e.surface = $1
			  AND e.event_type = 'impression'
			  AND e.event_ts >= $2
			GROUP BY e.rec_request_id
		)
		SELECT AVG(CASE WHEN total_items <= 0 THEN 0 ELSE topic_items / total_items END)
		FROM list_stats
	`, surface, since).Scan(&value)
	if err != nil {
		return 0, err
	}
	if !value.Valid {
		return 0, nil
	}
	return clampRatio(value.Float64), nil
}

func (r *RecoEvaluationRepo) personalization(ctx context.Context, surface string, since time.Time) (float64, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT user_id, article_id
		FROM reco_event_logs
		WHERE surface = $1
		  AND event_type = 'impression'
		  AND event_ts >= $2
		  AND user_id <> ''
		ORDER BY user_id, event_ts DESC
		LIMIT 2000
	`, surface, since)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	userArticles := map[string]map[string]struct{}{}
	for rows.Next() {
		var userID, articleID string
		if err := rows.Scan(&userID, &articleID); err != nil {
			return 0, err
		}
		if userArticles[userID] == nil {
			userArticles[userID] = map[string]struct{}{}
		}
		userArticles[userID][articleID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(userArticles) < 2 {
		return 0, nil
	}

	users := make([]string, 0, len(userArticles))
	for userID := range userArticles {
		users = append(users, userID)
	}
	sort.Strings(users)
	if len(users) > 50 {
		users = users[:50]
	}

	var sum float64
	var pairs int
	for i := 0; i < len(users); i++ {
		for j := i + 1; j < len(users); j++ {
			sum += 1 - jaccard(userArticles[users[i]], userArticles[users[j]])
			pairs++
		}
	}
	return safeRatio(sum, float64(pairs)), nil
}

func (r *RecoEvaluationRepo) ndcg(ctx context.Context, surface string, since time.Time) (float64, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT rec_request_id, rank, event_type
		FROM reco_event_logs
		WHERE surface = $1
		  AND event_ts >= $2
		  AND rank > 0
		  AND event_type IN ('click','like','favorite','read_complete')
		ORDER BY rec_request_id, rank
	`, surface, since)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	byRequest := map[string]map[int]float64{}
	for rows.Next() {
		var reqID, eventType string
		var rank int
		if err := rows.Scan(&reqID, &rank, &eventType); err != nil {
			return 0, err
		}
		if byRequest[reqID] == nil {
			byRequest[reqID] = map[int]float64{}
		}
		byRequest[reqID][rank] += eventWeight(eventType)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(byRequest) == 0 {
		return 0, nil
	}

	var sum float64
	var counted int
	for _, weightsByRank := range byRequest {
		weights := make([]float64, 0, len(weightsByRank))
		var dcg float64
		for rank, weight := range weightsByRank {
			if rank <= 10 {
				dcg += weight / math.Log2(float64(rank)+1)
				weights = append(weights, weight)
			}
		}
		sort.Slice(weights, func(i, j int) bool { return weights[i] > weights[j] })
		var idcg float64
		for i, weight := range weights {
			idcg += weight / math.Log2(float64(i+2))
		}
		if idcg > 0 {
			sum += dcg / idcg
			counted++
		}
	}
	return safeRatio(sum, float64(counted)), nil
}

func metric(key, label, category string, value float64, unit, description, source string) types.RecoMetricValue {
	return types.RecoMetricValue{
		Key:         key,
		Label:       label,
		Category:    category,
		Value:       clampRatio(value),
		Unit:        unit,
		Description: description,
		Source:      source,
	}
}

func normalizeRecoEventType(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	switch value {
	case types.RecoEventImpression, types.RecoEventClick, types.RecoEventLike,
		types.RecoEventDislike, types.RecoEventFavorite, types.RecoEventReadComplete:
		return value
	default:
		return ""
	}
}

func eventWeight(eventType string) float64 {
	switch eventType {
	case types.RecoEventLike, types.RecoEventFavorite, types.RecoEventReadComplete:
		return 2
	case types.RecoEventClick:
		return 1
	default:
		return 0
	}
}

func safeRatio(numerator, denominator float64) float64 {
	if denominator <= 0 || math.IsNaN(numerator) || math.IsNaN(denominator) {
		return 0
	}
	return clampRatio(numerator / denominator)
}

func clampRatio(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func jaccard(left, right map[string]struct{}) float64 {
	if len(left) == 0 && len(right) == 0 {
		return 0
	}
	intersection := 0
	for value := range left {
		if _, ok := right[value]; ok {
			intersection++
		}
	}
	union := len(left) + len(right) - intersection
	return safeRatio(float64(intersection), float64(union))
}

func formatWindow(window time.Duration) string {
	if window == 24*time.Hour {
		return "24h"
	}
	if window%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", window/(24*time.Hour))
	}
	if window%time.Hour == 0 {
		return fmt.Sprintf("%dh", window/time.Hour)
	}
	return window.String()
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
