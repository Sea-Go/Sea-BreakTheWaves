// Package graph projects grounded content relationships for bounded retrieval.
package graph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

type Client struct {
	Driver   neo4j.Driver
	Database string
}
type Candidate struct {
	ItemID, RevisionID string
	Location           item.Location
	Hops               int
	Path               []string
}

func Normalize(kind, label string) string {
	return strings.ToLower(strings.TrimSpace(kind)) + ":" + strings.ToLower(strings.TrimSpace(label))
}
func (c *Client) execute(ctx context.Context, query string, params map[string]any, read bool) (*neo4j.EagerResult, error) {
	if c == nil || c.Driver == nil {
		return nil, item.ErrUnavailable
	}
	options := []neo4j.ExecuteQueryConfigurationOption{}
	if c.Database != "" {
		options = append(options, neo4j.ExecuteQueryWithDatabase(c.Database))
	}
	if read {
		options = append(options, neo4j.ExecuteQueryWithReadersRouting())
	}
	return neo4j.ExecuteQuery[*neo4j.EagerResult](ctx, c.Driver, query, params, neo4j.EagerResultTransformer, options...)
}
func (c *Client) Ensure(ctx context.Context) error {
	for _, query := range []string{
		"CREATE CONSTRAINT btw_content_item IF NOT EXISTS FOR (n:ContentItem) REQUIRE n.key IS UNIQUE",
		"CREATE CONSTRAINT btw_content_concept IF NOT EXISTS FOR (n:ContentConcept) REQUIRE n.key IS UNIQUE",
		"CREATE INDEX btw_concept_label IF NOT EXISTS FOR (n:ContentConcept) ON (n.label)",
		"CREATE INDEX btw_item_root IF NOT EXISTS FOR (n:ContentItem) ON (n.root_id)",
		"CREATE INDEX btw_item_revision IF NOT EXISTS FOR (n:ContentItem) ON (n.revision_id)",
	} {
		if _, err := c.execute(ctx, query, nil, false); err != nil {
			return err
		}
	}
	return nil
}
func (c *Client) Upsert(ctx context.Context, objects []item.Item, relations []item.Relation) error {
	if len(objects) > 0 {
		rows := make([]map[string]any, 0, len(objects))
		for _, v := range objects {
			if v.ID == "" || v.RevisionID == "" || v.RootID == "" {
				return item.ErrInvalid
			}
			rows = append(rows, map[string]any{"key": v.ID + ":" + v.RevisionID, "item_id": v.ID, "root_id": v.RootID, "root_key": v.RootID + ":" + v.RevisionID, "root_kind": v.RootKind, "revision_id": v.RevisionID, "section_id": v.Location.SectionID, "start": v.Location.Start, "end": v.Location.End, "title": v.Title, "kind": v.Kind, "concept": Normalize(v.Kind, v.Title), "label": strings.ToLower(strings.TrimSpace(v.Title))})
		}
		_, err := c.execute(ctx, `UNWIND $rows AS row MERGE (i:ContentItem {key:row.key}) SET i.item_id=row.item_id,i.root_id=row.root_id,i.root_kind=row.root_kind,i.revision_id=row.revision_id,i.section_id=row.section_id,i.start=row.start,i.end=row.end,i.title=row.title,i.kind=row.kind`, map[string]any{"rows": rows}, false)
		if err != nil {
			return err
		}
		_, err = c.execute(ctx, `UNWIND $rows AS row WITH row WHERE row.item_id <> row.root_id AND row.label <> '' MERGE (c:ContentConcept {key:row.concept}) SET c.label=row.label WITH row,c MATCH (i:ContentItem {key:row.key}),(r:ContentItem {key:row.root_key}) MERGE (c)-[m:MENTIONS {item_key:row.key}]->(i) SET m.revision_id=row.revision_id,m.section_id=row.section_id,m.start=row.start,m.end=row.end MERGE (i)-[:PART_OF]->(r)`, map[string]any{"rows": rows}, false)
		if err != nil {
			return err
		}
	}
	if len(relations) > 0 {
		rows := make([]map[string]any, 0, len(relations))
		for _, v := range relations {
			if v.From == "" || v.To == "" || v.Location.RevisionID == "" {
				continue
			}
			rows = append(rows, map[string]any{"from": v.From + ":" + v.Location.RevisionID, "to": v.To + ":" + v.Location.RevisionID, "kind": v.Type, "revision_id": v.Location.RevisionID, "section_id": v.Location.SectionID, "start": v.Location.Start, "end": v.Location.End})
		}
		if len(rows) > 0 {
			_, err := c.execute(ctx, `UNWIND $rows AS row MATCH (a:ContentItem {key:row.from}),(b:ContentItem {key:row.to}) MERGE (a)-[r:RELATED {kind:row.kind,to_key:row.to}]->(b) SET r.revision_id=row.revision_id,r.section_id=row.section_id,r.start=row.start,r.end=row.end`, map[string]any{"rows": rows}, false)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
func (c *Client) DeleteRoot(ctx context.Context, root string) error {
	_, err := c.execute(ctx, `MATCH (i:ContentItem {root_id:$root}) DETACH DELETE i`, map[string]any{"root": root}, false)
	return err
}
func (c *Client) PruneRevision(ctx context.Context, revision string) error {
	_, err := c.execute(ctx, `MATCH (i:ContentItem {revision_id:$revision}) DETACH DELETE i`, map[string]any{"revision": revision}, false)
	return err
}
func (c *Client) Search(ctx context.Context, seeds []string, kinds []string, hops, limit int) ([]Candidate, error) {
	if hops < 1 || hops > 3 || limit < 1 || len(kinds) == 0 {
		return nil, item.ErrInvalid
	}
	unique := map[string]bool{}
	names := []string{}
	for _, seed := range seeds {
		v := strings.ToLower(strings.TrimSpace(seed))
		if v != "" && !unique[v] {
			unique[v] = true
			names = append(names, v)
		}
	}
	if len(names) == 0 {
		return []Candidate{}, nil
	}
	sort.Strings(names)
	if len(names) > 8 {
		names = names[:8]
	}
	// The only Cypher interpolation is a validated, bounded hop count.
	query := fmt.Sprintf(`MATCH (c:ContentConcept) WHERE c.label IN $names WITH c LIMIT 8 MATCH p=(c)-[:MENTIONS|PART_OF|RELATED*1..%d]-(i:ContentItem) WHERE i.root_kind IN $kinds WITH i,p,head([e IN relationships(p) WHERE e.section_id IS NOT NULL | e]) AS proof RETURN DISTINCT i.item_id AS item_id,i.revision_id AS revision_id,CASE WHEN i.item_id<>i.root_id THEN i.section_id ELSE coalesce(proof.section_id,i.section_id) END AS section_id,CASE WHEN i.item_id<>i.root_id THEN i.start ELSE coalesce(proof.start,i.start) END AS start,CASE WHEN i.item_id<>i.root_id THEN i.end ELSE coalesce(proof.end,i.end) END AS end,length(p) AS hops,[n IN nodes(p) | coalesce(n.title,n.key)] AS path ORDER BY hops,item_id LIMIT $limit`, hops)
	result, err := c.execute(ctx, query, map[string]any{"names": names, "kinds": kinds, "limit": limit}, true)
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(result.Records))
	for _, record := range result.Records {
		getString := func(key string) string { value, _ := record.Get(key); v, _ := value.(string); return v }
		getInt := func(key string) int64 { value, _ := record.Get(key); v, _ := value.(int64); return v }
		id, revision := getString("item_id"), getString("revision_id")
		if id == "" || revision == "" {
			return nil, fmt.Errorf("graph hit lacks item provenance")
		}
		path := []string{}
		if value, ok := record.Get("path"); ok {
			if values, ok := value.([]any); ok {
				for _, v := range values {
					if s, ok := v.(string); ok {
						path = append(path, s)
					}
				}
			}
		}
		out = append(out, Candidate{ItemID: id, RevisionID: revision, Hops: int(getInt("hops")), Path: path, Location: item.Location{RevisionID: revision, SectionID: getString("section_id"), Start: getInt("start"), End: getInt("end")}})
	}
	return out, nil
}
