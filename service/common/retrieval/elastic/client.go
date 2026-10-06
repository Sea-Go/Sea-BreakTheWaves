// Package elastic is the full-text projection of accepted text items.
package elastic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
)

type Client struct {
	URL, Index, Username, Password, APIKey string
	HTTP                                   *http.Client
}

type Document struct {
	ID         string `json:"-"`
	ItemID     string `json:"item_id"`
	RootID     string `json:"root_id"`
	RootKind   string `json:"root_kind"`
	Kind       string `json:"kind"`
	RevisionID string `json:"revision_id"`
	SectionID  string `json:"section_id"`
	Start      int64  `json:"start"`
	End        int64  `json:"end"`
	Title      string `json:"title"`
	Summary    string `json:"summary"`
	Body       string `json:"body"`
	Language   string `json:"language"`
}

type Hit struct {
	ItemID, RevisionID, SectionID, Title, Highlight string
	Start, End                                      int64
	Score                                           float64
}

type Query struct {
	Text    string
	Phrases []string
	Kinds   []string
	Limit   int
}

func (c *Client) request(ctx context.Context, method, endpoint string, body any) (*http.Response, error) {
	if c == nil || c.URL == "" || c.Index == "" {
		return nil, item.ErrUnavailable
	}
	var r io.Reader
	if body != nil {
		raw, e := json.Marshal(body)
		if e != nil {
			return nil, e
		}
		r = bytes.NewReader(raw)
	}
	address := strings.TrimRight(c.URL, "/") + "/" + url.PathEscape(c.Index) + endpoint
	req, e := http.NewRequestWithContext(ctx, method, address, r)
	if e != nil {
		return nil, e
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "ApiKey "+c.APIKey)
	} else if c.Username != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return client.Do(req)
}
func check(response *http.Response, accepted ...int) error {
	if response == nil {
		return item.ErrUnavailable
	}
	defer response.Body.Close()
	for _, status := range accepted {
		if response.StatusCode == status {
			_, _ = io.Copy(io.Discard, response.Body)
			return nil
		}
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
	return fmt.Errorf("elasticsearch %d: %s", response.StatusCode, body)
}

// Ensure creates the deployment's search index; it never selects another index.
func (c *Client) Ensure(ctx context.Context) error {
	response, e := c.request(ctx, http.MethodHead, "", nil)
	if e != nil {
		return e
	}
	if response.StatusCode == 200 {
		if err := check(response, 200); err != nil {
			return err
		}
		return c.Verify(ctx)
	}
	if e = check(response, 404); e != nil {
		return e
	}
	mapping := map[string]any{"mappings": map[string]any{"dynamic": "strict", "properties": map[string]any{
		"item_id": map[string]any{"type": "keyword"}, "root_id": map[string]any{"type": "keyword"}, "root_kind": map[string]any{"type": "keyword"}, "kind": map[string]any{"type": "keyword"}, "revision_id": map[string]any{"type": "keyword"}, "section_id": map[string]any{"type": "keyword"},
		"start": map[string]any{"type": "long"}, "end": map[string]any{"type": "long"}, "language": map[string]any{"type": "keyword"},
		"title":   map[string]any{"type": "text", "analyzer": "cjk", "fields": map[string]any{"exact": map[string]any{"type": "keyword"}, "standard": map[string]any{"type": "text", "analyzer": "standard"}}},
		"summary": map[string]any{"type": "text", "analyzer": "cjk"},
		"body":    map[string]any{"type": "text", "analyzer": "cjk", "index_options": "offsets", "fields": map[string]any{"standard": map[string]any{"type": "text", "analyzer": "standard"}}},
	}}}
	response, e = c.request(ctx, http.MethodPut, "", mapping)
	if e != nil {
		return e
	}
	if err := check(response, 200, 201); err != nil {
		return err
	}
	return c.Verify(ctx)
}

// Verify runs once during process assembly, never on every search request.
func (c *Client) Verify(ctx context.Context) error {
	response, err := c.request(ctx, http.MethodGet, "/_mapping", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return item.ErrUnavailable
	}
	var indices map[string]struct {
		Mappings struct {
			Properties map[string]struct {
				Type         string `json:"type"`
				Analyzer     string `json:"analyzer"`
				IndexOptions string `json:"index_options"`
				Fields       map[string]struct {
					Type string `json:"type"`
				} `json:"fields"`
			} `json:"properties"`
		} `json:"mappings"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&indices); err != nil {
		return err
	}
	index, ok := indices[c.Index]
	if !ok {
		return item.ErrUnavailable
	}
	fields := index.Mappings.Properties
	if fields["body"].Type != "text" || fields["body"].Analyzer != "cjk" || fields["body"].IndexOptions != "offsets" || fields["root_kind"].Type != "keyword" || fields["item_id"].Type != "keyword" || fields["title"].Fields["exact"].Type != "keyword" {
		return fmt.Errorf("%w: incompatible full-text mapping", item.ErrUnavailable)
	}
	return nil
}
func (c *Client) Upsert(ctx context.Context, doc Document) error {
	if doc.ID == "" || doc.ItemID == "" || doc.RevisionID == "" || doc.RootKind == "" || doc.End < doc.Start {
		return item.ErrInvalid
	}
	response, e := c.request(ctx, http.MethodPut, "/_doc/"+url.PathEscape(doc.ID), doc)
	if e != nil {
		return e
	}
	return check(response, 200, 201)
}
func (c *Client) Refresh(ctx context.Context) error {
	response, e := c.request(ctx, http.MethodPost, "/_refresh", nil)
	if e != nil {
		return e
	}
	return check(response, 200)
}
func (c *Client) deleteBy(ctx context.Context, field, value string) error {
	response, e := c.request(ctx, http.MethodPost, "/_delete_by_query?wait_for_completion=true&refresh=true", map[string]any{"query": map[string]any{"term": map[string]any{field: value}}})
	if e != nil {
		return e
	}
	return check(response, 200)
}
func (c *Client) DeleteRoot(ctx context.Context, root string) error {
	return c.deleteBy(ctx, "root_id", root)
}
func (c *Client) PruneRevision(ctx context.Context, revision string) error {
	return c.deleteBy(ctx, "revision_id", revision)
}

func (c *Client) Search(ctx context.Context, q Query) ([]Hit, error) {
	if strings.TrimSpace(q.Text) == "" || len(q.Kinds) == 0 || q.Limit < 1 {
		return nil, item.ErrInvalid
	}
	should := []any{map[string]any{"multi_match": map[string]any{"query": q.Text, "fields": []string{"title^3", "summary^2", "body", "title.standard^3", "body.standard"}, "type": "best_fields"}}, map[string]any{"term": map[string]any{"title.exact": map[string]any{"value": q.Text, "boost": 5}}}}
	for _, phrase := range q.Phrases {
		if phrase != "" {
			should = append(should, map[string]any{"match_phrase": map[string]any{"body": map[string]any{"query": phrase, "boost": 3}}})
		}
	}
	body := map[string]any{"size": q.Limit, "query": map[string]any{"bool": map[string]any{"filter": []any{map[string]any{"terms": map[string]any{"root_kind": q.Kinds}}}, "should": should, "minimum_should_match": 1}}, "collapse": map[string]any{"field": "item_id"}, "highlight": map[string]any{"fields": map[string]any{"body": map[string]any{"number_of_fragments": 1, "fragment_size": 160}}}}
	response, e := c.request(ctx, http.MethodPost, "/_search", body)
	if e != nil {
		return nil, e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return nil, fmt.Errorf("elasticsearch %d: %s", response.StatusCode, raw)
	}
	var result struct {
		Hits struct {
			Hits []struct {
				Score     float64             `json:"_score"`
				Source    Document            `json:"_source"`
				Highlight map[string][]string `json:"highlight"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if e = json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&result); e != nil {
		return nil, e
	}
	out := make([]Hit, 0, len(result.Hits.Hits))
	seen := map[string]bool{}
	for _, row := range result.Hits.Hits {
		d := row.Source
		if d.ItemID == "" || d.RevisionID == "" {
			return nil, errors.New("elasticsearch hit lacks item provenance")
		}
		key := d.ItemID + ":" + d.RevisionID
		if seen[key] {
			continue
		}
		seen[key] = true
		h := Hit{ItemID: d.ItemID, RevisionID: d.RevisionID, SectionID: d.SectionID, Title: d.Title, Start: d.Start, End: d.End, Score: row.Score}
		if len(row.Highlight["body"]) > 0 {
			h.Highlight = row.Highlight["body"][0]
		}
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}
