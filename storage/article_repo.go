package storage

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"sea/chunk"
)

// ArticleRepo 负责文章元数据与 chunk 内容的存取。
type ArticleRepo struct {
	db *sql.DB
}

func NewArticleRepo(db *sql.DB) *ArticleRepo {
	return &ArticleRepo{db: db}
}

func (r *ArticleRepo) UpsertArticle(ctx context.Context, a chunk.Article) error {
	typeTags := strings.Join(a.TypeTags, ",")
	tags := strings.Join(a.Tags, ",")

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO articles(article_id, title, cover, type_tags, tags, score, created_at)
		VALUES($1,$2,$3,$4,$5,$6, now())
		ON CONFLICT(article_id) DO UPDATE SET
			title=EXCLUDED.title,
			cover=EXCLUDED.cover,
			type_tags=EXCLUDED.type_tags,
			tags=EXCLUDED.tags,
			score=EXCLUDED.score
	`, a.ArticleID, a.Title, a.Cover, typeTags, tags, float32(a.Score))
	return err
}

func (r *ArticleRepo) UpsertChunks(ctx context.Context, chunks []chunk.Chunk) error {
	// 简单起见：逐条 upsert（demo 可用）；生产建议批量 COPY 或 batch insert
	for _, c := range chunks {
		_, err := r.db.ExecContext(ctx, `
			INSERT INTO article_chunks(chunk_id, article_id, h2, content, created_at)
			VALUES($1,$2,$3,$4, now())
			ON CONFLICT(chunk_id) DO UPDATE SET
				h2=EXCLUDED.h2,
				content=EXCLUDED.content
		`, c.ChunkID, c.ArticleID, c.H2, c.Content)
		if err != nil {
			return err
		}
	}
	return nil
}

// GetChunksByIDs 根据 chunk_id 批量取回内容（用于生成/引用/验证）。
func (r *ArticleRepo) GetChunksByIDs(ctx context.Context, ids []string) ([]chunk.Chunk, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	// 动态拼 IN（为 demo 简化；生产建议用 pgx + ANY($1)）
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		placeholders = append(placeholders, "$"+itoa(i+1))
		args = append(args, id)
	}

	q := "SELECT chunk_id, article_id, h2, content, created_at FROM article_chunks WHERE chunk_id IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []chunk.Chunk
	for rows.Next() {
		var c chunk.Chunk
		var created time.Time
		if err := rows.Scan(&c.ChunkID, &c.ArticleID, &c.H2, &c.Content, &created); err != nil {
			return nil, err
		}
		c.Tokens = chunk.ApproxTokenCount(c.Content)
		res = append(res, c)
	}
	return res, rows.Err()
}

// GetArticleScores 批量获取文章分数（用于 remark 加权排序）。
func (r *ArticleRepo) GetArticleScores(ctx context.Context, ids []string) (map[string]float32, error) {
	if len(ids) == 0 {
		return map[string]float32{}, nil
	}

	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		placeholders = append(placeholders, "$"+itoa(i+1))
		args = append(args, id)
	}
	q := "SELECT article_id, score FROM articles WHERE article_id IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	m := map[string]float32{}
	for rows.Next() {
		var id string
		var score float32
		if err := rows.Scan(&id, &score); err != nil {
			return nil, err
		}
		m[id] = score
	}
	return m, rows.Err()
}

type ArticleMeta struct {
	ArticleID string
	Title     string
	Cover     string
	TypeTags  string
	Tags      string
	Score     float32
}

func (r *ArticleRepo) GetArticlesByIDs(ctx context.Context, ids []string) ([]ArticleMeta, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		placeholders = append(placeholders, "$"+itoa(i+1))
		args = append(args, id)
	}

	q := "SELECT article_id, title, cover, type_tags, tags, score FROM articles WHERE article_id IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []ArticleMeta
	for rows.Next() {
		var a ArticleMeta
		if err := rows.Scan(&a.ArticleID, &a.Title, &a.Cover, &a.TypeTags, &a.Tags, &a.Score); err != nil {
			return nil, err
		}
		res = append(res, a)
	}
	return res, rows.Err()
}
