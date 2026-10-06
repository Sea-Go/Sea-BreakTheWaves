package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type ClickHouse struct {
	URL, Database, User, Password string
	HTTP                          *http.Client
}

func (c *ClickHouse) query(ctx context.Context, q string, body io.Reader) error {
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(c.Database) {
		return item.ErrInvalid
	}
	r, e := http.NewRequestWithContext(ctx, "POST", c.URL, body)
	if e != nil {
		return e
	}
	values := r.URL.Query()
	values.Set("query", q)
	r.URL.RawQuery = values.Encode()
	if c.User != "" {
		r.SetBasicAuth(c.User, c.Password)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, e := client.Do(r)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("clickhouse %d: %s", response.StatusCode, raw)
	}
	_, e = io.Copy(io.Discard, response.Body)
	return e
}
func (c *ClickHouse) Migrate(ctx context.Context) error {
	if e := c.query(ctx, "CREATE DATABASE IF NOT EXISTS "+c.Database, nil); e != nil {
		return e
	}
	return c.query(ctx, "CREATE TABLE IF NOT EXISTS "+c.Database+`.events (event_id String, request_id String, user_id String, item_id String, root_id String, duplicate_id String, source_revision String, scenario String, type LowCardinality(String), position Int32, occurred_at DateTime64(3,'UTC'), features Array(Float64), label_source LowCardinality(String), received_at DateTime64(3,'UTC') DEFAULT now64(3)) ENGINE = ReplacingMergeTree(received_at) ORDER BY event_id`, nil)
}
func (c *ClickHouse) Write(ctx context.Context, events []item.Behavior) error {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	for _, v := range events {
		row := map[string]any{"event_id": v.EventID, "root_id": v.RootID, "duplicate_id": v.DuplicateID, "source_revision": v.SourceRevision, "request_id": v.RequestID, "user_id": v.UserID, "item_id": v.ItemID, "scenario": v.Scenario, "type": v.Type, "position": v.Position, "occurred_at": v.OccurredAt.UTC().Format("2006-01-02 15:04:05.000"), "features": v.Features, "label_source": v.LabelSource}
		if v.Features == nil {
			row["features"] = []float64{}
		}
		if e := enc.Encode(row); e != nil {
			return e
		}
	}
	return c.query(ctx, "INSERT INTO "+c.Database+".events FORMAT JSONEachRow", strings.NewReader(body.String()))
}

// ExportTraining joins actual candidate features with observed impressions and feedback.
// The observation cutoff is explicit; unexposed candidates never become negatives.
func (c *ClickHouse) ExportTraining(ctx context.Context, scenarios []string, asOf time.Time, window time.Duration, out io.Writer) error {
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(c.Database) || len(scenarios) == 0 || window < 0 {
		return item.ErrInvalid
	}
	quoted := []string{}
	for _, v := range scenarios {
		quoted = append(quoted, "'"+strings.ReplaceAll(strings.ReplaceAll(v, "\\", "\\\\"), "'", "\\'")+"'")
	}
	cutoff := asOf.UTC().Format("2006-01-02 15:04:05.000")
	query := fmt.Sprintf(`SELECT request_id AS "group", item_id AS candidate_id, tupleElement(candidate,1) AS root_id, tupleElement(candidate,2) AS duplicate_id, tupleElement(candidate,2) AS candidate_group, tupleElement(candidate,3) AS source_revision, tupleElement(candidate,4) AS features, if(positives>0,3,if(clicks>0,2,0)) AS label, 'behavior' AS label_source, true AS exposed, true AS observation_complete FROM (SELECT request_id,item_id,user_id,scenario, argMaxIf(tuple(root_id,duplicate_id,source_revision,features),received_at,type='candidate') AS candidate, countIf(type='candidate') AS candidates,countIf(type='impression') AS impressions,countIf(type='click') AS clicks,countIf(type='positive') AS positives,countIf(type='negative') AS negatives,minIf(occurred_at,type='impression') AS shown_at FROM %s.events FINAL WHERE occurred_at<=toDateTime64('%s',3,'UTC') AND scenario IN (%s) GROUP BY request_id,item_id,user_id,scenario) WHERE candidates>0 AND impressions>0 AND (clicks>0 OR positives>0 OR negatives>0 OR shown_at<=toDateTime64('%s',3,'UTC')-INTERVAL %d SECOND) FORMAT JSONEachRow`, c.Database, cutoff, strings.Join(quoted, ","), cutoff, int64(window.Seconds()))

	request, e := http.NewRequestWithContext(ctx, "POST", c.URL, strings.NewReader(query))
	if e != nil {
		return e
	}
	if c.User != "" {
		request.SetBasicAuth(c.User, c.Password)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	response, e := client.Do(request)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("ClickHouse training export %d: %s", response.StatusCode, message)
	}
	_, e = io.Copy(out, response.Body)
	return e
}
