package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"sea/config"
	"sea/infra"

	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

// MemoryChunkRepo 管理 user_memory_chunks（用于“tokenize 后的长期/周期记忆”分块）。
//
// 设计说明：
//   - PG 仅保存 chunk 的原文/审计字段（便于调试与可追溯）。
//   - 向量索引统一由 Milvus 负责（避免同时维护 pgvector 与 Milvus）。
//   - 为避免依赖 Milvus 删除/Upsert API（不同版本差异较大），Milvus 的主键里包含一个 version_unix：
//     每次替换 chunks 都会产生一个新的 version；检索时按 version 过滤即可“只看到最新一版”。
type MemoryChunkRepo struct {
	db            *sql.DB
	cli           *milvusclient.Client
	collection    string
	dim           int
	maxContentLen int
}

// NewMemoryChunkRepo 返回“默认配置”的记忆分块 Repo。
// - PG 负责落原文/审计字段；
// - Milvus 负责向量索引（如未初始化 Milvus，则 repo 会自动退化为仅写 PG）。
func NewMemoryChunkRepo(db *sql.DB) *MemoryChunkRepo {
	cli := infra.Milvus()
	dim := config.Cfg.Milvus.Collections.Dim
	return NewMemoryChunkRepoWithMilvus(db, cli, "user_memory_chunks", dim)
}

// NewMemoryChunkRepoWithMilvus 允许显式注入 Milvus client/collection/dim（便于测试/多集合）。
func NewMemoryChunkRepoWithMilvus(db *sql.DB, cli *milvusclient.Client, collection string, dim int) *MemoryChunkRepo {
	if strings.TrimSpace(collection) == "" {
		collection = "user_memory_chunks"
	}
	if dim <= 0 {
		dim = 2048
	}
	return &MemoryChunkRepo{db: db, cli: cli, collection: collection, dim: dim, maxContentLen: 8192}
}

func (r *MemoryChunkRepo) chunkID(userID string, memType MemoryType, periodBucket string, chunkIndex int, versionUnix int64) string {
	// id 里包含 version，避免旧向量残留影响检索
	return fmt.Sprintf("%s|%s|%s|%d|%d", userID, string(memType), periodBucket, chunkIndex, versionUnix)
}

// ReplaceChunks 用新的 chunks 覆盖某个 memory（先删后插）。
// updatedAt 用于与 user_memory 的更新时间对齐，作为 Milvus version 过滤条件。
func (r *MemoryChunkRepo) ReplaceChunks(ctx context.Context, userID string, memType MemoryType, periodBucket string, updatedAt time.Time, chunks []string, vectors [][]float32) error {
	// 1) PG：先删旧，再写新
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM user_memory_chunks
		WHERE user_id=$1 AND memory_type=$2 AND period_bucket=$3
	`, userID, string(memType), periodBucket)
	if err != nil {
		return err
	}

	for i, c := range chunks {
		_, err := r.db.ExecContext(ctx, `
			INSERT INTO user_memory_chunks(user_id, memory_type, period_bucket, chunk_index, content, updated_at)
			VALUES($1,$2,$3,$4,$5,$6)
		`, userID, string(memType), periodBucket, i, c, updatedAt)
		if err != nil {
			return err
		}
	}

	// 2) Milvus：写入最新 version 的向量（旧 version 不删除，检索时按 version 过滤）
	if r.cli == nil {
		// 允许仅落 PG（例如本地未启 Milvus 时）；上层检索自然会退化为空
		return nil
	}

	versionUnix := updatedAt.Unix()
	ids := make([]string, 0, len(chunks))
	vecs := make([][]float32, 0, len(chunks))
	idxs := make([]int64, 0, len(chunks))
	contents := make([]string, 0, len(chunks))

	for i, c := range chunks {
		// 向量为空则跳过（不参与召回）
		if i >= len(vectors) || len(vectors[i]) == 0 {
			continue
		}
		ids = append(ids, r.chunkID(userID, memType, periodBucket, i, versionUnix))
		vecs = append(vecs, vectors[i])
		idxs = append(idxs, int64(i))
		if len(c) > r.maxContentLen {
			c = c[:r.maxContentLen]
		}
		contents = append(contents, c)
	}

	if len(ids) == 0 {
		return nil
	}

	// ✅ 关键修复：id 用 VarChar Column
	idCol := column.NewColumnVarChar("id", ids)
	vecCol := column.NewColumnFloatVector("vector", r.dim, vecs)
	userCol := column.NewColumnVarChar("user_id", repeatStr(userID, len(ids)))
	memTypeCol := column.NewColumnVarChar("memory_type", repeatStr(string(memType), len(ids)))
	periodCol := column.NewColumnVarChar("period_bucket", repeatStr(periodBucket, len(ids)))
	chunkIdxCol := column.NewColumnInt64("chunk_index", idxs)
	versionCol := column.NewColumnInt64("version_unix", repeatInt64(versionUnix, len(ids)))
	contentCol := column.NewColumnVarChar("content", contents)

	opt := milvusclient.NewColumnBasedInsertOption(
		r.collection,
		idCol, vecCol, userCol, memTypeCol, periodCol, chunkIdxCol, versionCol, contentCol,
	)
	_, err = r.cli.Insert(ctx, opt)
	return err
}

// SearchMemoryChunks 在 Milvus 中做相似检索，返回最相关的记忆片段（用于生成意图）。
// updatedAt 必须与 ReplaceChunks 使用的 updatedAt 一致，以便按 version 过滤到“最新一版”。
func (r *MemoryChunkRepo) SearchMemoryChunks(ctx context.Context, userID string, memType MemoryType, periodBucket string, updatedAt time.Time, query []float32, topK int) ([]string, error) {
	if r.cli == nil {
		return nil, nil
	}
	if topK <= 0 {
		topK = 5
	}
	if len(query) == 0 {
		return nil, nil
	}

	versionUnix := updatedAt.Unix()
	filter := fmt.Sprintf(`user_id == "%s" && memory_type == "%s" && period_bucket == "%s" && version_unix == %d`,
		esc(userID), esc(string(memType)), esc(periodBucket), versionUnix,
	)

	opt := milvusclient.NewSearchOption(
		r.collection,
		topK,
		[]entity.Vector{entity.FloatVector(query)},
	).WithANNSField("vector").WithFilter(filter).WithOutputFields("content", "chunk_index")

	rs, err := r.cli.Search(ctx, opt)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, nil
	}

	set := rs[0]
	contentCol := set.GetColumn("content")
	chunkIdxCol := set.GetColumn("chunk_index")
	ordered := make([]string, set.ResultCount)
	missingIdxByPos := make(map[int]int, set.ResultCount)

	for i := 0; i < set.ResultCount; i++ {
		content := ""
		if contentCol != nil {
			if v, err := contentCol.Get(i); err == nil {
				switch vv := v.(type) {
				case string:
					content = strings.TrimSpace(vv)
				case []byte:
					content = strings.TrimSpace(string(vv))
				}
			}
		}
		if content != "" {
			ordered[i] = content
			continue
		}

		if chunkIdxCol != nil {
			if v, err := chunkIdxCol.Get(i); err == nil {
				switch vv := v.(type) {
				case int64:
					missingIdxByPos[i] = int(vv)
					continue
				case int32:
					missingIdxByPos[i] = int(vv)
					continue
				case int:
					missingIdxByPos[i] = vv
					continue
				}
			}
		}

		id, _ := set.IDs.GetAsString(i)
		parts := strings.Split(id, "|")
		if len(parts) < 5 {
			continue
		}
		var ci int
		_, _ = fmt.Sscanf(parts[3], "%d", &ci)
		missingIdxByPos[i] = ci
	}

	if len(missingIdxByPos) > 0 {
		missingIdxs := make([]int, 0, len(missingIdxByPos))
		for _, idx := range missingIdxByPos {
			missingIdxs = append(missingIdxs, idx)
		}
		contentByIdx, err := r.getChunkContentsByIndexes(ctx, userID, memType, periodBucket, missingIdxs)
		if err != nil {
			return nil, err
		}
		for pos, ci := range missingIdxByPos {
			if c, ok := contentByIdx[ci]; ok && strings.TrimSpace(c) != "" {
				ordered[pos] = c
			}
		}
	}
	res := make([]string, 0, len(ordered))
	for _, c := range ordered {
		if strings.TrimSpace(c) != "" {
			res = append(res, c)
		}
	}
	return res, nil
}

func (r *MemoryChunkRepo) getChunkContentsByIndexes(ctx context.Context, userID string, memType MemoryType, periodBucket string, idxs []int) (map[int]string, error) {
	// 构造 IN (...) 占位符
	args := make([]any, 0, 3+len(idxs))
	args = append(args, userID, string(memType), periodBucket)

	pl := make([]string, 0, len(idxs))
	for i, v := range idxs {
		args = append(args, v)
		pl = append(pl, fmt.Sprintf("$%d", 4+i))
	}

	q := fmt.Sprintf(`
		SELECT chunk_index, content
		FROM user_memory_chunks
		WHERE user_id=$1 AND memory_type=$2 AND period_bucket=$3
		AND chunk_index IN (%s)
	`, strings.Join(pl, ","))

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := map[int]string{}
	for rows.Next() {
		var idx int
		var c string
		if err := rows.Scan(&idx, &c); err != nil {
			return nil, err
		}
		res[idx] = c
	}
	return res, rows.Err()
}

func esc(s string) string {
	// Milvus filter 字符串用双引号包裹；这里最小化转义，防止表达式注入
	return strings.ReplaceAll(s, `"`, `\\"`)
}

func repeatStr(v string, n int) []string {
	res := make([]string, n)
	for i := range res {
		res[i] = v
	}
	return res
}

func repeatInt64(v int64, n int) []int64 {
	res := make([]int64, n)
	for i := range res {
		res[i] = v
	}
	return res
}
