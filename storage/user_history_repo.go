package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	types "sea/type"

	"sea/config"
	"sea/infra"

	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

// UserHistoryItem 表示一条用户推荐历史记录。
// - Embed 不做持久化返回（只用于 Add 时写入 Milvus）。
// - Similarity 仅在相似检索时返回。
type UserHistoryItem = types.UserHistoryItem

// UserHistoryRepo：
// - PG 负责“事实记录”（曝光/点击/偏好 + 时间戳），便于审计与时间窗口统计；
// - Milvus 负责向量相似检索（去重/偏好分析）。
//
// 设计目标：避免同时维护 pgvector 与 Milvus。
type UserHistoryRepo struct {
	db         *sql.DB
	cli        *milvusclient.Client
	collection string
	dim        int
}

// NewUserHistoryRepo 返回默认配置的 UserHistoryRepo。
// - 若 Milvus 未初始化（cli=nil），相似检索会退化为空结果，但 Add/ListRecent 仍可用。
func NewUserHistoryRepo(db *sql.DB) *UserHistoryRepo {
	cli := infra.Milvus()
	dim := config.Cfg.Milvus.Collections.Dim
	return NewUserHistoryRepoWithMilvus(db, cli, "user_rec_history", dim)
}

func NewUserHistoryRepoWithMilvus(db *sql.DB, cli *milvusclient.Client, collection string, dim int) *UserHistoryRepo {
	if strings.TrimSpace(collection) == "" {
		collection = "user_rec_history"
	}
	if dim <= 0 {
		dim = 2048
	}
	return &UserHistoryRepo{db: db, cli: cli, collection: collection, dim: dim}
}

func (r *UserHistoryRepo) makeHistoryID(userID, articleID string, ts time.Time) string {
	// history_id 只用于主键/关联 Milvus；这里用「user|unixNano|article」。
	return fmt.Sprintf("%s|%d|%s", userID, ts.UnixNano(), articleID)
}

// Add 写入一条历史记录。
// - 始终写 PG
// - 若 Embed 非空且 Milvus 可用，则同时写入 Milvus 向量集合
func (r *UserHistoryRepo) Add(ctx context.Context, it UserHistoryItem) error {
	if it.TS.IsZero() {
		it.TS = time.Now()
	}
	if strings.TrimSpace(it.UserID) == "" || strings.TrimSpace(it.ArticleID) == "" {
		return fmt.Errorf("user_id / article_id 不能为空")
	}
	if strings.TrimSpace(it.HistoryID) == "" {
		it.HistoryID = r.makeHistoryID(it.UserID, it.ArticleID, it.TS)
	}

	// 1) PG
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO user_rec_history(history_id, user_id, article_id, clicked, preference, ts)
		VALUES($1,$2,$3,$4,$5,$6)
	`, it.HistoryID, it.UserID, it.ArticleID, it.Clicked, it.Preference, it.TS)
	if err != nil {
		return err
	}

	// 2) Milvus（向量为空则跳过）
	if r.cli == nil || len(it.Embed) == 0 {
		return nil
	}

	ids := []string{it.HistoryID}
	vecs := [][]float32{it.Embed}

	// ✅ 关键修复：id 用 VarChar Column
	idCol := column.NewColumnVarChar("id", ids)
	vecCol := column.NewColumnFloatVector("vector", r.dim, vecs)
	userCol := column.NewColumnVarChar("user_id", []string{it.UserID})
	articleCol := column.NewColumnVarChar("article_id", []string{it.ArticleID})
	clickedCol := column.NewColumnBool("clicked", []bool{it.Clicked})
	prefCol := column.NewColumnFloat("preference", []float32{it.Preference})
	tsCol := column.NewColumnInt64("ts_unix", []int64{it.TS.Unix()})

	opt := milvusclient.NewColumnBasedInsertOption(
		r.collection,
		idCol, vecCol, userCol, articleCol, clickedCol, prefCol, tsCol,
	)
	_, err = r.cli.Insert(ctx, opt)
	return err
}

// ListRecent 返回用户最近 N 条历史记录（按时间倒序）。
func (r *UserHistoryRepo) ListRecent(ctx context.Context, userID string, limit int) ([]UserHistoryItem, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT history_id, user_id, article_id, clicked, preference, ts
		FROM user_rec_history
		WHERE user_id=$1
		ORDER BY ts DESC
		LIMIT $2
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []UserHistoryItem
	for rows.Next() {
		var it UserHistoryItem
		if err := rows.Scan(&it.HistoryID, &it.UserID, &it.ArticleID, &it.Clicked, &it.Preference, &it.TS); err != nil {
			return nil, err
		}
		res = append(res, it)
	}
	return res, rows.Err()
}

// SearchSimilar 在 Milvus 中对用户历史做相似检索（去重/偏好分析）。
// - 若 Milvus 不可用，则返回空
func (r *UserHistoryRepo) SearchSimilar(ctx context.Context, userID string, query []float32, topK int) ([]UserHistoryItem, error) {
	if r.cli == nil {
		return nil, nil
	}
	if topK <= 0 {
		topK = 10
	}
	if len(query) == 0 {
		return nil, nil
	}

	filter := fmt.Sprintf(`user_id == "%s"`, escFilter(userID))
	opt := milvusclient.NewSearchOption(
		r.collection,
		topK,
		[]entity.Vector{entity.FloatVector(query)},
	).WithANNSField("vector").WithFilter(filter)

	rs, err := r.cli.Search(ctx, opt)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, nil
	}

	set := rs[0]
	ids := make([]string, 0, set.ResultCount)
	scoreByID := map[string]float32{}
	for i := 0; i < set.ResultCount; i++ {
		id, _ := set.IDs.GetAsString(i)
		ids = append(ids, id)
		scoreByID[id] = set.Scores[i]
	}
	if len(ids) == 0 {
		return nil, nil
	}

	itemsByID, err := r.getByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	res := make([]UserHistoryItem, 0, len(ids))
	for _, id := range ids {
		if it, ok := itemsByID[id]; ok {
			it.Similarity = scoreByID[id]
			res = append(res, it)
		}
	}
	return res, nil
}

func (r *UserHistoryRepo) getByIDs(ctx context.Context, ids []string) (map[string]UserHistoryItem, error) {
	if len(ids) == 0 {
		return map[string]UserHistoryItem{}, nil
	}

	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
		args = append(args, id)
	}

	q := "SELECT history_id, user_id, article_id, clicked, preference, ts FROM user_rec_history WHERE history_id IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := map[string]UserHistoryItem{}
	for rows.Next() {
		var it UserHistoryItem
		if err := rows.Scan(&it.HistoryID, &it.UserID, &it.ArticleID, &it.Clicked, &it.Preference, &it.TS); err != nil {
			return nil, err
		}
		res[it.HistoryID] = it
	}
	return res, rows.Err()
}

func escFilter(s string) string {
	// Milvus filter 字符串用双引号包裹；最小化转义，防止表达式注入
	return strings.ReplaceAll(s, `"`, `\\"`)
}
