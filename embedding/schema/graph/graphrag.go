package graph

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	types "sea/type"
)

// Store 是 GraphRAG 的 Neo4j 存储封装。
// 目标：
// - parent_node: 粗召回文本信息（标题/封面/type/关键词/二级标题集合）
// - child_node: 精召回 chunk（H2 + 段落内容，包含图片/文字）
// - 精召回时通过图上的 SIMILAR/NEXT 扩展候选（GraphRAG）
//
// 说明：这里用最小可用的 GraphRAG 结构，避免把 Neo4j 细节散落在各个 skill 中。
type Store struct {
	driver neo4j.DriverWithContext
}

func NewStore(driver neo4j.DriverWithContext) *Store {
	if driver == nil {
		return nil
	}
	return &Store{driver: driver}
}

func (s *Store) Close(ctx context.Context) error {
	if s == nil || s.driver == nil {
		return nil
	}
	return s.driver.Close(ctx)
}

func (s *Store) UpsertParent(ctx context.Context, p ParentNode) error {
	if s == nil || s.driver == nil {
		return errors.New("neo4j driver is nil")
	}
	sess := s.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer sess.Close(ctx)

	_, err := sess.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		query := `MERGE (p:Parent {article_id: $article_id})
SET p.node_id = $node_id,
    p.title = $title,
    p.tag = $tag,
    p.keywords = $keywords`
		params := map[string]any{
			"node_id":    p.NodeID,
			"article_id": p.ArticleID,
			"title":      p.Title,
			"tag":        p.Tag,
			"keywords":   p.Keywords,
		}
		_, err := tx.Run(ctx, query, params)
		return nil, err
	})
	return err
}

func (s *Store) UpsertChild(ctx context.Context, c ChildNode) error {
	if s == nil || s.driver == nil {
		return errors.New("neo4j driver is nil")
	}
	sess := s.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer sess.Close(ctx)

	_, err := sess.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		query := `MERGE (c:Child {chunk_id: $chunk_id})
SET c.node_id = $node_id,
    c.title = $title,
    c.tag = $tag,
    c.keywords = $keywords`
		params := map[string]any{
			"node_id":  c.NodeID,
			"chunk_id": c.ChunkID,
			"title":    c.Title,
			"tag":      c.Tag,
			"keywords": c.Keywords,
		}
		_, err := tx.Run(ctx, query, params)
		return nil, err
	})
	return err
}

func (s *Store) LinkHasChild(ctx context.Context, articleID string, chunkID string) error {
	if s == nil || s.driver == nil {
		return errors.New("neo4j driver is nil")
	}
	sess := s.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer sess.Close(ctx)

	_, err := sess.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		query := `MATCH (p:Parent {article_id: $article_id})
MATCH (c:Child {chunk_id: $chunk_id})
MERGE (p)-[:HAS_CHILD]->(c)`
		params := map[string]any{"article_id": articleID, "chunk_id": chunkID}
		_, err := tx.Run(ctx, query, params)
		return nil, err
	})
	return err
}

func (s *Store) LinkNext(ctx context.Context, fromChunkID string, toChunkID string) error {
	if s == nil || s.driver == nil {
		return errors.New("neo4j driver is nil")
	}
	sess := s.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer sess.Close(ctx)

	_, err := sess.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		query := `MATCH (a:Child {chunk_id: $from})
MATCH (b:Child {chunk_id: $to})
MERGE (a)-[:NEXT {weight: 1.0}]->(b)`
		params := map[string]any{"from": fromChunkID, "to": toChunkID}
		_, err := tx.Run(ctx, query, params)
		return nil, err
	})
	return err
}

func (s *Store) LinkSimilar(ctx context.Context, fromChunkID string, toChunkID string, weight float64) error {
	if s == nil || s.driver == nil {
		return errors.New("neo4j driver is nil")
	}
	sess := s.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer sess.Close(ctx)

	_, err := sess.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		query := `MATCH (a:Child {chunk_id: $from})
MATCH (b:Child {chunk_id: $to})
MERGE (a)-[r:SIMILAR]->(b)
SET r.weight = $weight`
		params := map[string]any{"from": fromChunkID, "to": toChunkID, "weight": weight}
		_, err := tx.Run(ctx, query, params)
		return nil, err
	})
	return err
}

// Neighbor 表示从 seed chunk 出发的一跳邻居。
type Neighbor = types.Neighbor

// GetNeighbors 返回 seed chunk 的一跳邻居（SIMILAR / NEXT）。
func (s *Store) GetNeighbors(ctx context.Context, chunkID string, limit int) ([]Neighbor, error) {
	if s == nil || s.driver == nil {
		return nil, errors.New("neo4j driver is nil")
	}
	if limit <= 0 {
		limit = 8
	}

	sess := s.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer sess.Close(ctx)

	rows, err := sess.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		query := `MATCH (c:Child {chunk_id: $chunk_id})-[r:SIMILAR|NEXT]->(n:Child)
RETURN n.chunk_id AS id, coalesce(r.weight, 1.0) AS w, type(r) AS rel
LIMIT $limit`
		res, err := tx.Run(ctx, query, map[string]any{"chunk_id": chunkID, "limit": limit})
		if err != nil {
			return nil, err
		}
		var out []Neighbor
		for res.Next(ctx) {
			rec := res.Record()
			idV, _ := rec.Get("id")
			wV, _ := rec.Get("w")
			relV, _ := rec.Get("rel")

			id, _ := idV.(string)
			w, _ := wV.(float64)
			rel, _ := relV.(string)
			if strings.TrimSpace(id) == "" {
				continue
			}
			out = append(out, Neighbor{ChunkID: id, Weight: w, Rel: rel})
		}
		return out, res.Err()
	})
	if err != nil {
		return nil, err
	}

	if rows == nil {
		return nil, nil
	}
	return rows.([]Neighbor), nil
}

// BuildSimilarEdges 为 chunk 列表构建 SIMILAR 边（topK/threshold 控制图稀疏度）。
// 返回：从 chunkID -> 相似邻居列表（已排序）。
func BuildSimilarEdges(vectors map[string][]float32, topK int, threshold float64) map[string][]Edge {
	if topK <= 0 {
		topK = 4
	}
	if threshold <= 0 {
		threshold = 0.82
	}

	ids := make([]string, 0, len(vectors))
	for id := range vectors {
		ids = append(ids, id)
	}

	edges := map[string][]Edge{}

	// 朴素 O(n^2)：文章 chunk 通常不多，demo 足够。
	for _, from := range ids {
		fromVec := vectors[from]
		var cands []Edge
		for _, to := range ids {
			if to == from {
				continue
			}
			sim := CosineSimilarity(fromVec, vectors[to])
			if sim < threshold {
				continue
			}
			cands = append(cands, Edge{
				FromNodeID: from,
				ToNodeID:   to,
				Weight:     sim,
				Tag:        "SIMILAR",
			})
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].Weight > cands[j].Weight })
		if len(cands) > topK {
			cands = cands[:topK]
		}
		edges[from] = cands
	}

	return edges
}
