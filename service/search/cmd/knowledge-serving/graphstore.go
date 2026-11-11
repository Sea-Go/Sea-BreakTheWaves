// 图修订存储:PG 元数据权威(graph_revisions/graph_pointers)+ Neo4j 当前修订投影。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

type GraphRevisionMeta struct {
	RevisionID  int       `json:"revision_id"`
	GraphID     string    `json:"graph_id"`
	BasedOn     *int      `json:"based_on"`
	SourceHash  string    `json:"source_hash"` // RTW manifest 修订的 content_hash(锚定原始数据)
	OpSummary   string    `json:"op_summary"`
	ArtifactKey string    `json:"artifact_key"`
	ArtifactSHA string    `json:"artifact_sha"`
	NodeCount   int       `json:"node_count"`
	EdgeCount   int       `json:"edge_count"`
	CreatedAt   time.Time `json:"created_at"`
}

type GraphStore struct {
	pool    *pgxpool.Pool
	objects artifacts.Store
	neo     neo4j.DriverWithContext
}

func NewGraphStore(ctx context.Context, contentDSN, neoURI, neoUser, neoPass string, objects artifacts.Store) (*GraphStore, error) {
	pool, err := pgxpool.New(ctx, contentDSN)
	if err != nil {
		return nil, fmt.Errorf("content dsn: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("content ping: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS graph_revisions (
			revision_id bigserial PRIMARY KEY,
			graph_id text NOT NULL,
			based_on bigint,
			source_hash text NOT NULL,
			op_summary text NOT NULL,
			artifact_key text NOT NULL,
			artifact_sha text NOT NULL,
			node_count int NOT NULL,
			edge_count int NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS graph_revisions_graph ON graph_revisions(graph_id, revision_id);
		CREATE TABLE IF NOT EXISTS graph_pointers (
			graph_id text PRIMARY KEY,
			active_revision_id bigint NOT NULL REFERENCES graph_revisions(revision_id)
		);`); err != nil {
		return nil, fmt.Errorf("migrate graph tables: %w", err)
	}
	neo, err := neo4j.NewDriverWithContext(neoURI, neo4j.BasicAuth(neoUser, neoPass, ""))
	if err != nil {
		return nil, fmt.Errorf("neo4j driver: %w", err)
	}
	if err := neo.VerifyConnectivity(ctx); err != nil {
		return nil, fmt.Errorf("neo4j connectivity: %w", err)
	}
	return &GraphStore{pool: pool, objects: objects, neo: neo}, nil
}

func (s *GraphStore) Close(ctx context.Context) {
	s.pool.Close()
	_ = s.neo.Close(ctx)
}

// ArtifactRef 把图内容作为不可变工件写入对象存储(与 RTW 工件同库同寻址)。
func (s *GraphStore) putGraphArtifact(ctx context.Context, g *GraphContent) (key, sha string, err error) {
	raw, err := CanonicalGraphJSON(g)
	if err != nil {
		return "", "", err
	}
	ref, err := s.objects.Put(ctx, raw)
	if err != nil {
		return "", "", fmt.Errorf("put artifact: %w", err)
	}
	return ref.Key, ref.SHA256, nil
}

func (s *GraphStore) getGraphArtifact(ctx context.Context, key, sha string) (*GraphContent, error) {
	raw, err := s.objects.Get(ctx, refFromKeySHA(key, sha))
	if err != nil {
		return nil, err
	}
	var g GraphContent
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// SeedRevision 从 manifest 原文重放派生修订 1(幂等:同 graph_id+source_hash 已存在则返回既有)。
func (s *GraphStore) SeedRevision(ctx context.Context, graphID, sourceHash, manifestJSON, opSummary string) (*GraphRevisionMeta, *GraphContent, error) {
	if existing, err := s.FindSeed(ctx, graphID, sourceHash); err == nil && existing != nil {
		g, err := s.getGraphArtifact(ctx, existing.ArtifactKey, existing.ArtifactSHA)
		if err != nil {
			return nil, nil, err
		}
		return existing, g, nil
	}
	g, err := ReplayGraph([]byte(manifestJSON), graphID)
	if err != nil {
		return nil, nil, err
	}
	key, sha, err := s.putGraphArtifact(ctx, g)
	if err != nil {
		return nil, nil, err
	}
	meta, err := s.appendRevision(ctx, graphID, nil, sourceHash, opSummary, key, sha, g)
	if err != nil {
		return nil, nil, err
	}
	if err := s.project(ctx, graphID, meta.RevisionID, g); err != nil {
		return nil, nil, err
	}
	return meta, g, nil
}

func (s *GraphStore) FindSeed(ctx context.Context, graphID, sourceHash string) (*GraphRevisionMeta, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT revision_id, graph_id, based_on, source_hash, op_summary, artifact_key, artifact_sha, node_count, edge_count, created_at
		 FROM graph_revisions WHERE graph_id=$1 AND source_hash=$2 AND based_on IS NULL ORDER BY revision_id LIMIT 1`, graphID, sourceHash)
	return scanRevision(row)
}

// AppendRevision 基于当前活动修订应用操作集,追加新修订并切指针+重建投影。
func (s *GraphStore) AppendRevision(ctx context.Context, graphID, opSummary string, ops []GraphOp) (*GraphRevisionMeta, *GraphContent, error) {
	current, content, err := s.ActiveRevision(ctx, graphID)
	if err != nil {
		return nil, nil, err
	}
	next, err := ApplyOps(content, ops)
	if err != nil {
		return nil, nil, err
	}
	key, sha, err := s.putGraphArtifact(ctx, next)
	if err != nil {
		return nil, nil, err
	}
	meta, err := s.appendRevision(ctx, graphID, &current.RevisionID, current.SourceHash, opSummary, key, sha, next)
	if err != nil {
		return nil, nil, err
	}
	if err := s.project(ctx, graphID, meta.RevisionID, next); err != nil {
		return nil, nil, err
	}
	return meta, next, nil
}

func (s *GraphStore) appendRevision(ctx context.Context, graphID string, basedOn *int, sourceHash, opSummary, key, sha string, g *GraphContent) (*GraphRevisionMeta, error) {
	var meta GraphRevisionMeta
	err := s.pool.QueryRow(ctx, `
		WITH ins AS (
			INSERT INTO graph_revisions(graph_id, based_on, source_hash, op_summary, artifact_key, artifact_sha, node_count, edge_count)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING revision_id, created_at
		)
		INSERT INTO graph_pointers(graph_id, active_revision_id)
		VALUES($1, (SELECT revision_id FROM ins))
		ON CONFLICT (graph_id) DO UPDATE SET active_revision_id=(SELECT revision_id FROM ins)
		RETURNING active_revision_id`, graphID, basedOn, sourceHash, opSummary, key, sha, len(g.Nodes), len(g.Edges)).Scan(&meta.RevisionID)
	if err != nil {
		return nil, fmt.Errorf("append revision: %w", err)
	}
	// 重投影为指针切换的一部分;投影失败时指针已前移,由读取端回退到工件。
	meta.GraphID, meta.BasedOn, meta.SourceHash, meta.OpSummary = graphID, basedOn, sourceHash, opSummary
	meta.ArtifactKey, meta.ArtifactSHA = key, sha
	meta.NodeCount, meta.EdgeCount = len(g.Nodes), len(g.Edges)
	return &meta, nil
}

func (s *GraphStore) ActiveRevision(ctx context.Context, graphID string) (*GraphRevisionMeta, *GraphContent, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT r.revision_id, r.graph_id, r.based_on, r.source_hash, r.op_summary, r.artifact_key, r.artifact_sha, r.node_count, r.edge_count, r.created_at
		FROM graph_pointers p JOIN graph_revisions r ON r.revision_id=p.active_revision_id
		WHERE p.graph_id=$1`, graphID)
	meta, err := scanRevision(row)
	if err != nil {
		return nil, nil, fmt.Errorf("读活动修订: %w", err)
	}
	g, err := s.getGraphArtifact(ctx, meta.ArtifactKey, meta.ArtifactSHA)
	if err != nil {
		return nil, nil, err
	}
	return meta, g, nil
}

func (s *GraphStore) ListRevisions(ctx context.Context, graphID string) ([]GraphRevisionMeta, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT revision_id, graph_id, based_on, source_hash, op_summary, artifact_key, artifact_sha, node_count, edge_count, created_at
		FROM graph_revisions WHERE graph_id=$1 ORDER BY revision_id`, graphID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GraphRevisionMeta
	for rows.Next() {
		m, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func scanRevision(row interface{ Scan(dest ...any) error }) (*GraphRevisionMeta, error) {
	var m GraphRevisionMeta
	if err := row.Scan(&m.RevisionID, &m.GraphID, &m.BasedOn, &m.SourceHash, &m.OpSummary, &m.ArtifactKey, &m.ArtifactSHA, &m.NodeCount, &m.EdgeCount, &m.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("修订不存在")
		}
		return nil, err
	}
	return &m, nil
}

// ---------- Neo4j 投影 ----------

var neoLabelByType = map[string]string{
	"layer": "CourseLayer", "unit": "CourseUnit", "concept": "CourseConcept",
	"question": "CourseQuestion", "material": "CourseMaterial",
}
var neoRelByType = map[string]string{
	"belongs_to": "C_BELONGS_TO", "prerequisite": "C_PREREQUISITE", "covers": "C_COVERS",
	"tests": "C_TESTS", "anchors": "C_ANCHORS", "reads": "C_READS",
}

// project 事务内整图重建该 graph_id 的投影(删旧写新)。投影只是视图,权威在工件+PG。
func (s *GraphStore) project(ctx context.Context, graphID string, revisionID int, g *GraphContent) error {
	session := s.neo.NewSession(ctx, neo4j.SessionConfig{DatabaseName: ""})
	defer session.Close(ctx)
	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		if _, err := tx.Run(ctx,
			"MATCH (n) WHERE n.graph_id=$gid DETACH DELETE n", map[string]any{"gid": graphID}); err != nil {
			return nil, err
		}
		for _, n := range g.Nodes {
			props := map[string]any{"graph_id": graphID, "revision_id": revisionID, "node_id": n.ID, "type": n.Type}
			for k, v := range n.Props {
				props[k] = v
			}
			label := neoLabelByType[n.Type]
			if _, err := tx.Run(ctx,
				fmt.Sprintf("CREATE (:`%s` $props)", label), map[string]any{"props": props}); err != nil {
				return nil, fmt.Errorf("写节点 %s: %w", n.ID, err)
			}
		}
		for _, e := range g.Edges {
			props := map[string]any{"graph_id": graphID, "revision_id": revisionID}
			for k, v := range e.Props {
				props[k] = v
			}
			rel := neoRelByType[e.Type]
			if _, err := tx.Run(ctx,
				fmt.Sprintf("MATCH (a),(b) WHERE a.graph_id=$gid AND a.node_id=$from AND b.graph_id=$gid AND b.node_id=$to CREATE (a)-[:`%s` $props]->(b)", rel),
				map[string]any{"gid": graphID, "from": e.From, "to": e.To, "props": props}); err != nil {
				return nil, fmt.Errorf("写边 %s(%s→%s): %w", e.Type, e.From, e.To, err)
			}
		}
		return nil, nil
	})
	return err
}

// GraphQueryResult 是子图查询结果(节点+边对——画图需要的形状)。
type GraphQueryResult struct {
	Nodes []map[string]any `json:"nodes"`
	Edges []map[string]any `json:"edges"`
}

// QueryCurrent 返回当前修订的子图;nodeType 为空=全部;keyword 匹配 title/name/text 的包含。
// hops 控制邻域扩展(0=仅匹配节点,1=加一跳邻居)。
func (s *GraphStore) QueryCurrent(ctx context.Context, graphID, nodeType, keyword string, hops int) (*GraphQueryResult, error) {
	if hops < 0 || hops > 3 {
		hops = 1
	}
	session := s.neo.NewSession(ctx, neo4j.SessionConfig{})
	defer session.Close(ctx)
	label := ""
	if nodeType != "" {
		l, ok := neoLabelByType[nodeType]
		if !ok {
			return nil, fmt.Errorf("未知节点类型 %q", nodeType)
		}
		label = ":" + l
	}
	where := "n.graph_id = $gid"
	if keyword != "" {
		where += " AND (coalesce(n.title,'') + coalesce(n.name,'') + coalesce(n.text,'')) CONTAINS $kw"
	}
	// 三步式(避免聚合语法兼容问题):起点 → 逐跳扩展 → 子图内取节点+边。
	// 1) 起点
	frontier := map[string]bool{}
	seedQuery := fmt.Sprintf("MATCH (n%s) WHERE %s RETURN n.node_id AS id LIMIT 500", label, where)
	res, err := session.Run(ctx, seedQuery, map[string]any{"gid": graphID, "kw": keyword})
	if err != nil {
		return nil, err
	}
	for res.Next(ctx) {
		if id, ok := res.Record().Get("id"); ok {
			frontier[id.(string)] = true
		}
	}
	if err := res.Err(); err != nil {
		return nil, err
	}
	if len(frontier) == 0 {
		return &GraphQueryResult{Nodes: []map[string]any{}, Edges: []map[string]any{}}, nil
	}
	ids := func(set map[string]bool) []string {
		out := make([]string, 0, len(set))
		for id := range set {
			out = append(out, id)
		}
		return out
	}
	// 2) 逐跳扩展
	nodeSet := map[string]bool{}
	for id := range frontier {
		nodeSet[id] = true
	}
	for hop := 0; hop < hops; hop++ {
		res, err := session.Run(ctx,
			"MATCH (a)-[r]-(b) WHERE a.node_id IN $frontier AND b.graph_id = $gid RETURN DISTINCT b.node_id AS id LIMIT 1000",
			map[string]any{"frontier": ids(frontier), "gid": graphID})
		if err != nil {
			return nil, err
		}
		next := map[string]bool{}
		for res.Next(ctx) {
			if id, ok := res.Record().Get("id"); ok {
				next[id.(string)] = true
				nodeSet[id.(string)] = true
			}
		}
		if err := res.Err(); err != nil {
			return nil, err
		}
		if len(next) == 0 {
			break
		}
		frontier = next
	}
	// 3) 子图节点与边
	all := ids(nodeSet)
	res, err = session.Run(ctx,
		"MATCH (a)-[r]->(b) WHERE a.node_id IN $all AND b.node_id IN $all AND a.graph_id = $gid "+
			"RETURN properties(a) AS from_props, properties(b) AS to_props, type(r) AS rel, properties(r) AS rel_props, a.node_id AS from_id, b.node_id AS to_id LIMIT 2000",
		map[string]any{"all": all, "gid": graphID})
	if err != nil {
		return nil, err
	}
	out := &GraphQueryResult{Nodes: []map[string]any{}, Edges: []map[string]any{}}
	seenNode := map[string]bool{}
	for res.Next(ctx) {
		rec := res.Record()
		fp, _ := rec.Get("from_props")
		tp, _ := rec.Get("to_props")
		rel, _ := rec.Get("rel")
		rp, _ := rec.Get("rel_props")
		fi, _ := rec.Get("from_id")
		ti, _ := rec.Get("to_id")
		if m, ok := fp.(map[string]any); ok {
			if id := m["node_id"].(string); !seenNode[id] {
				seenNode[id] = true
				out.Nodes = append(out.Nodes, m)
			}
		}
		if m, ok := tp.(map[string]any); ok {
			if id := m["node_id"].(string); !seenNode[id] {
				seenNode[id] = true
				out.Nodes = append(out.Nodes, m)
			}
		}
		edge := map[string]any{"type": rel, "from": fi, "to": ti}
		if m, ok := rp.(map[string]any); ok {
			edge["props"] = m
		}
		out.Edges = append(out.Edges, edge)
	}
	return out, res.Err()
}

func refFromKeySHA(key, sha string) corpus.Ref {
	return corpus.Ref{Key: key, SHA256: sha}
}
