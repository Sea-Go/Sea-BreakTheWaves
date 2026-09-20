// Command graph-backfill 将 Postgres 数据回填到 Neo4j 知识图谱。
//
// 回填内容：
//  1. 文章表（article）→ Article 节点 + Tag/Author/IP/Image 关系
//  2. 用户行为表（reco_event_logs）→ Behavior 节点 + User-Behavior 关系
//  3. 共现矩阵（同 session 内被一起点击的文章对）→ CO_OCCURRED_WITH 关系
//
// 支持增量回填（--since 按 updated_at/event_ts 过滤）。
//
// Postgres schema 说明（基于 storage/source_article_repo.go 与
// infra/postgres_init.go 实际表结构）：
//   - article 表（源库）：id, title, brief, cover_image_url, manual_type_tag,
//     secondary_tags(JSONB), author_id, status(2=已发布), deleted_at,
//     created_at, updated_at(假设存在；若实际缺失，用 COALESCE(updated_at, created_at) 兜底)。
//   - users 表（源库）：uid, username。
//   - reco_event_logs 表（本地库，infra/postgres_init.go 建表）：id(BIGSERIAL),
//     rec_request_id, user_id, session_id, article_id, event_type, event_ts,
//     rank。event_type 含 'click'/'like'/'dislike' 等；session_id 用于共现计算。
//
// 使用的 graph.Client 方法签名（定义于 internal/graph/operations.go 与 client.go）：
//   - NewClient(uri, username, password string) (*Client, error)
//   - (*Client).Close(ctx context.Context) error
//   - (*Client).EnsureSchema(ctx context.Context) error
//   - (*Client).UpsertArticle(ctx, ArticleInput) error
//   - (*Client).UpsertImage(ctx, ImageInput) error
//   - (*Client).LinkArticleTag(ctx, articleID, tag string) error
//   - (*Client).LinkArticleAuthor(ctx, articleID, authorID string) error
//   - (*Client).LinkArticleIP(ctx, articleID, ip string) error
//   - (*Client).LinkArticleImage(ctx, articleID, imageID string) error
//   - (*Client).RecordBehavior(ctx, BehaviorInput) error
//   - (*Client).LinkUserBehavior(ctx, userID, behaviorID string) error
//   - (*Client).LinkCoOccurrence(ctx, articleA, articleB string, weight float64) error
//
// 用法：
//
//	graph-backfill --pg-dsn "postgres://..." --neo4j-uri bolt://localhost:7687 \
//	  --neo4j-user neo4j --neo4j-password secret --batch-size 500
//
// 增量回填：
//
//	graph-backfill --since 2026-06-01T00:00:00Z ...
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/trpcagent/graph/legacy"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// cliFlags CLI 参数集合。
type cliFlags struct {
	pgDSN     string
	neo4jURI  string
	neo4jUser string
	neo4jPass string
	since     string
	batchSize int
	dryRun    bool
}

// parseFlags 解析命令行参数并做基本校验。
func parseFlags() (cliFlags, error) {
	f := cliFlags{}
	flag.StringVar(&f.pgDSN, "pg-dsn", os.Getenv("PG_DSN"), "Postgres DSN（默认从环境变量 PG_DSN 读取）")
	flag.StringVar(&f.neo4jURI, "neo4j-uri", "bolt://localhost:7687", "Neo4j Bolt URI")
	flag.StringVar(&f.neo4jUser, "neo4j-user", "", "Neo4j 用户名")
	flag.StringVar(&f.neo4jPass, "neo4j-password", "", "Neo4j 密码")
	flag.StringVar(&f.since, "since", "", "增量回填起始时间（RFC3339，按 updated_at/event_ts 过滤）")
	flag.IntVar(&f.batchSize, "batch-size", 500, "批量大小")
	flag.BoolVar(&f.dryRun, "dry-run", false, "只打印不写入")
	flag.Parse()

	if strings.TrimSpace(f.pgDSN) == "" {
		return f, fmt.Errorf("--pg-dsn 或环境变量 PG_DSN 未设置")
	}
	if f.batchSize <= 0 {
		return f, fmt.Errorf("--batch-size 必须为正数")
	}
	return f, nil
}

// parseSince 解析 RFC3339 时间；空串返回零值与 false。
func parseSince(s string) (time.Time, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("invalid --since (RFC3339): %w", err)
	}
	return t, true, nil
}

// backfillStats 回填统计。
type backfillStats struct {
	articles  int
	behaviors int
	cooccur   int
}

func main() {
	f, err := parseFlags()
	if err != nil {
		log.Fatalf("参数错误: %v", err)
	}
	since, hasSince, err := parseSince(f.since)
	if err != nil {
		log.Fatalf("参数错误: %v", err)
	}

	start := time.Now()
	ctx := context.Background()

	// 1. 连接 Postgres（database/sql + pgx/v5/stdlib，与 infra/postgres_init.go 一致）。
	db, err := sql.Open("pgx", f.pgDSN)
	if err != nil {
		log.Fatalf("打开 Postgres 失败: %v", err)
	}
	defer db.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := db.PingContext(pingCtx); err != nil {
		cancel()
		log.Fatalf("Postgres ping 失败: %v", err)
	}
	cancel()
	log.Printf("Postgres 已连接")

	// 2. 连接 Neo4j（graph.NewClient 内部构造 driver，不调用 VerifyConnectivity）。
	client, err := graph.NewClient(f.neo4jURI, f.neo4jUser, f.neo4jPass)
	if err != nil {
		log.Fatalf("连接 Neo4j 失败: %v", err)
	}
	defer client.Close(ctx)
	log.Printf("Neo4j 已连接")

	// 3. EnsureSchema（依赖 Task 2.2）。
	if err := client.EnsureSchema(ctx); err != nil {
		log.Fatalf("EnsureSchema 失败: %v", err)
	}
	if f.dryRun {
		log.Printf("DRY-RUN 模式：仅统计，不写入 Neo4j")
	}

	stats := backfillStats{}

	// 4. 分批回填文章。
	if n, err := backfillArticles(ctx, db, client, since, hasSince, f.batchSize, f.dryRun); err != nil {
		log.Fatalf("回填文章失败: %v", err)
	} else {
		stats.articles = n
		log.Printf("文章回填完成: %d 篇", n)
	}

	// 5. 分批回填行为。
	if n, err := backfillBehaviors(ctx, db, client, since, hasSince, f.batchSize, f.dryRun); err != nil {
		log.Fatalf("回填行为失败: %v", err)
	} else {
		stats.behaviors = n
		log.Printf("行为回填完成: %d 条", n)
	}

	// 6. 分批计算共现。
	if n, err := backfillCoOccurrence(ctx, db, client, since, hasSince, f.batchSize, f.dryRun); err != nil {
		log.Fatalf("回填共现失败: %v", err)
	} else {
		stats.cooccur = n
		log.Printf("共现回填完成: %d 对", n)
	}

	// 7. 打印统计：节点数、关系数、耗时。
	log.Printf("==== 回填统计 ====")
	log.Printf("文章节点(Article): %d", stats.articles)
	log.Printf("行为节点(Behavior): %d", stats.behaviors)
	log.Printf("共现关系(CO_OCCURRED_WITH): %d", stats.cooccur)
	log.Printf("总耗时: %s", time.Since(start))
}

// articleRow 文章行（来自源 article 表）。
type articleRow struct {
	id            string
	title         string
	brief         string
	coverURL      string
	manualTypeTag string
	secondaryTags []string
	authorID      string
	authorName    string
	createdAt     time.Time
}

// backfillArticles 分批查询文章并写入图谱（Article 节点 + Tag/Author/IP/Image 关系）。
// 增量时按 COALESCE(a.updated_at, a.created_at) > $since 过滤。
func backfillArticles(ctx context.Context, db *sql.DB, client *graph.Client,
	since time.Time, hasSince bool, batch int, dryRun bool) (int, error) {
	// 参数占位符：$1=batch, $2=offset, $3=since（仅增量时）。
	query := `
SELECT
	a.id,
	COALESCE(a.title, ''),
	COALESCE(a.brief, ''),
	COALESCE(a.cover_image_url, ''),
	COALESCE(a.manual_type_tag, ''),
	COALESCE(a.secondary_tags::text, '[]'),
	COALESCE(a.author_id, ''),
	COALESCE(u.username, ''),
	a.created_at
FROM article a
LEFT JOIN users u ON a.author_id = u.uid::text
WHERE a.status = 2
  AND a.deleted_at IS NULL`
	if hasSince {
		query += " AND COALESCE(a.updated_at, a.created_at) > $3"
	}
	query += " ORDER BY a.created_at, a.id LIMIT $1 OFFSET $2"

	count, offset := 0, 0
	for {
		var rows *sql.Rows
		var err error
		if hasSince {
			rows, err = db.QueryContext(ctx, query, batch, offset, since)
		} else {
			rows, err = db.QueryContext(ctx, query, batch, offset)
		}
		if err != nil {
			return count, fmt.Errorf("query articles: %w", err)
		}
		var batchRows []articleRow
		for rows.Next() {
			var r articleRow
			var tagsJSON string
			if err := rows.Scan(&r.id, &r.title, &r.brief, &r.coverURL,
				&r.manualTypeTag, &tagsJSON, &r.authorID, &r.authorName, &r.createdAt); err != nil {
				rows.Close()
				return count, fmt.Errorf("scan article: %w", err)
			}
			r.secondaryTags = parseTagsJSON(tagsJSON)
			batchRows = append(batchRows, r)
		}
		rows.Close()
		if len(batchRows) == 0 {
			break
		}

		for _, r := range batchRows {
			count++
			if dryRun {
				continue
			}
			// Article 节点：用 ArticleInput 结构体传入（operations.go 定义）。
			// manual_type_tag 同时作为 TypeTag（主类型）与 IPName（频道独立池）。
			if err := client.UpsertArticle(ctx, graph.ArticleInput{
				ID:         r.id,
				Title:      r.title,
				Content:    r.brief,
				AuthorID:   r.authorID,
				AuthorName: r.authorName,
				IPName:     r.manualTypeTag,
				TypeTag:    r.manualTypeTag,
				CreateTime: r.createdAt,
			}); err != nil {
				return count, fmt.Errorf("upsert article %s: %w", r.id, err)
			}
			// 关联次级标签 -> Tag 节点。
			for _, tag := range r.secondaryTags {
				if tag = strings.TrimSpace(tag); tag == "" {
					continue
				}
				if err := client.LinkArticleTag(ctx, r.id, tag); err != nil {
					return count, fmt.Errorf("link tag %q: %w", tag, err)
				}
			}
			// 关联作者 -> Author 节点（LinkArticleAuthor 仅需 articleID + authorID）。
			if r.authorID != "" {
				if err := client.LinkArticleAuthor(ctx, r.id, r.authorID); err != nil {
					return count, fmt.Errorf("link author: %w", err)
				}
			}
			// 关联 IP（manual_type_tag 作为 IP 名）。
			if r.manualTypeTag != "" {
				if err := client.LinkArticleIP(ctx, r.id, r.manualTypeTag); err != nil {
					return count, fmt.Errorf("link ip: %w", err)
				}
			}
			// 关联封面图片 -> Image 节点 + HAS_IMAGE 关系。
			// 先 UpsertImage 写入 Image 节点（含 url 属性，供以图搜文匹配），
			// 再 LinkArticleImage 建立 Article-[:HAS_IMAGE]->Image 关系。
			if r.coverURL != "" {
				imageID := r.id + ":cover"
				if err := client.UpsertImage(ctx, graph.ImageInput{
					ID:        imageID,
					ArticleID: r.id,
					URL:       r.coverURL,
				}); err != nil {
					return count, fmt.Errorf("upsert image: %w", err)
				}
				if err := client.LinkArticleImage(ctx, r.id, imageID); err != nil {
					return count, fmt.Errorf("link image: %w", err)
				}
			}
		}

		if len(batchRows) < batch {
			break
		}
		offset += batch
	}
	return count, nil
}

// behaviorRow 行为行（来自本地 reco_event_logs 表）。
type behaviorRow struct {
	id        int64
	requestID string
	userID    string
	sessionID string
	articleID string
	eventType string
	ts        time.Time
}

// backfillBehaviors 分批查询用户行为并写入图谱（Behavior 节点 + User-Behavior 关系）。
func backfillBehaviors(ctx context.Context, db *sql.DB, client *graph.Client,
	since time.Time, hasSince bool, batch int, dryRun bool) (int, error) {
	query := `
SELECT
	id,
	COALESCE(rec_request_id, ''),
	COALESCE(user_id, ''),
	COALESCE(session_id, ''),
	COALESCE(article_id, ''),
	COALESCE(event_type, ''),
	event_ts
FROM reco_event_logs
WHERE event_type IN ('click', 'like', 'dislike')`
	if hasSince {
		query += " AND event_ts > $3"
	}
	query += " ORDER BY event_ts, id LIMIT $1 OFFSET $2"

	count, offset := 0, 0
	for {
		var rows *sql.Rows
		var err error
		if hasSince {
			rows, err = db.QueryContext(ctx, query, batch, offset, since)
		} else {
			rows, err = db.QueryContext(ctx, query, batch, offset)
		}
		if err != nil {
			return count, fmt.Errorf("query behaviors: %w", err)
		}
		var batchRows []behaviorRow
		for rows.Next() {
			var r behaviorRow
			if err := rows.Scan(&r.id, &r.requestID, &r.userID, &r.sessionID,
				&r.articleID, &r.eventType, &r.ts); err != nil {
				rows.Close()
				return count, fmt.Errorf("scan behavior: %w", err)
			}
			batchRows = append(batchRows, r)
		}
		rows.Close()
		if len(batchRows) == 0 {
			break
		}

		for _, r := range batchRows {
			count++
			if dryRun {
				continue
			}
			behaviorID := fmt.Sprintf("%d", r.id)
			// Behavior 节点：用 BehaviorInput 结构体传入（operations.go 定义）。
			if err := client.RecordBehavior(ctx, graph.BehaviorInput{
				ID:        behaviorID,
				UserID:    r.userID,
				ArticleID: r.articleID,
				Type:      r.eventType,
				CreatedAt: r.ts,
				Weight:    behaviorWeight(r.eventType),
			}); err != nil {
				return count, fmt.Errorf("record behavior %s: %w", behaviorID, err)
			}
			if r.userID != "" {
				if err := client.LinkUserBehavior(ctx, r.userID, behaviorID); err != nil {
					return count, fmt.Errorf("link user behavior: %w", err)
				}
			}
		}

		if len(batchRows) < batch {
			break
		}
		offset += batch
	}
	return count, nil
}

// behaviorWeight 根据行为类型返回权重（与召回排序对齐）。
// click=0.5, like=1.0, dislike=-0.5, 其余=0.3。
func behaviorWeight(eventType string) float64 {
	switch eventType {
	case "click":
		return 0.5
	case "like":
		return 1.0
	case "dislike":
		return -0.5
	default:
		return 0.3
	}
}

// backfillCoOccurrence 分批计算同 session 内被一起点击的文章对，写入 CO_OCCURRED_WITH。
//
// 策略：
//  1. 分批拉取 distinct session_id（点击事件）。
//  2. 每个 session 查询其 distinct article_id，生成文章对（无序，a<b 规范化）。
//  3. 内存累计 pair -> count，最终以 count 作为 weight 写入（operations.go 的
//     LinkCoOccurrence 接受单个 weight float64，取共现次数作为权重）。
//
// 注：pair 计数在内存中累积，适用于中规模数据；若 session/文章对量级极大，
//
//	可改为按 session 分桶流式写图（依赖 LinkCoOccurrence 的 Cypher 累加语义）。
func backfillCoOccurrence(ctx context.Context, db *sql.DB, client *graph.Client,
	since time.Time, hasSince bool, batch int, dryRun bool) (int, error) {
	// 1. 分批拉取 distinct sessions。
	sessQuery := `SELECT session_id FROM reco_event_logs
WHERE event_type = 'click' AND session_id <> '' AND article_id <> ''`
	if hasSince {
		sessQuery += " AND event_ts > $3"
	}
	sessQuery += " GROUP BY session_id ORDER BY session_id LIMIT $1 OFFSET $2"

	artQuery := `SELECT DISTINCT article_id FROM reco_event_logs
WHERE event_type = 'click' AND session_id = $1 AND article_id <> ''`

	pairCount := make(map[string]int)
	sessOffset := 0
	for {
		var rows *sql.Rows
		var err error
		if hasSince {
			rows, err = db.QueryContext(ctx, sessQuery, batch, sessOffset, since)
		} else {
			rows, err = db.QueryContext(ctx, sessQuery, batch, sessOffset)
		}
		if err != nil {
			return 0, fmt.Errorf("query sessions: %w", err)
		}
		var sessions []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				rows.Close()
				return 0, fmt.Errorf("scan session: %w", err)
			}
			sessions = append(sessions, s)
		}
		rows.Close()
		if len(sessions) == 0 {
			break
		}

		for _, sess := range sessions {
			artRows, err := db.QueryContext(ctx, artQuery, sess)
			if err != nil {
				return 0, fmt.Errorf("query session articles: %w", err)
			}
			var articles []string
			for artRows.Next() {
				var a string
				if err := artRows.Scan(&a); err != nil {
					artRows.Close()
					return 0, fmt.Errorf("scan article_id: %w", err)
				}
				articles = append(articles, a)
			}
			artRows.Close()
			// 生成无序文章对，规范化 a<b 防止重复计数。
			for i := 0; i < len(articles); i++ {
				for j := i + 1; j < len(articles); j++ {
					a, b := articles[i], articles[j]
					if a > b {
						a, b = b, a
					}
					pairCount[a+"\x00"+b]++
				}
			}
		}

		if len(sessions) < batch {
			break
		}
		sessOffset += batch
	}

	// 2. 写入共现关系：以共现次数作为 weight（operations.go LinkCoOccurrence 签名）。
	written := 0
	for key, c := range pairCount {
		written++
		if dryRun {
			continue
		}
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) != 2 {
			continue
		}
		a, b := parts[0], parts[1]
		if err := client.LinkCoOccurrence(ctx, a, b, float64(c)); err != nil {
			return written, fmt.Errorf("link co-occurrence %s|%s: %w", a, b, err)
		}
	}
	return written, nil
}

// parseTagsJSON 解析 secondary_tags 字段（Postgres JSONB -> []string）。
// 与 storage/source_article_repo.go 的 parseJSONTextArray 行为一致。
func parseTagsJSON(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	clean := make([]string, 0, len(out))
	for _, s := range out {
		s = strings.TrimSpace(s)
		if s != "" {
			clean = append(clean, s)
		}
	}
	return clean
}
