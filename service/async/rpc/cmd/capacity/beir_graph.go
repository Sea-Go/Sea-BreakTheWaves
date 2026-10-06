package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/graph"
	"github.com/google/uuid"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

func runBEIRGraph(dir string, count, queryCount int, output io.Writer) error {
	uri := os.Getenv("BTW_TEST_NEO4J_URI")
	if dir == "" || uri == "" || count < 1 || queryCount < 1 {
		return fmt.Errorf("graph experiment requires -beir-dir, -items, -queries and isolated BTW_TEST_NEO4J_URI")
	}
	documents, err := readJSONL[beirDocument](dir + "/corpus.jsonl")
	if err != nil {
		return err
	}
	if count > len(documents) {
		return fmt.Errorf("requested %d graph roots from %d documents", count, len(documents))
	}
	if queryCount > count {
		queryCount = count
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	driver, err := neo4j.NewDriver(uri, neo4j.BasicAuth(os.Getenv("BTW_TEST_NEO4J_USER"), os.Getenv("BTW_TEST_NEO4J_PASSWORD"), ""))
	if err != nil {
		return err
	}
	defer driver.Close(context.Background())
	client := &graph.Client{Driver: driver, Database: os.Getenv("BTW_TEST_NEO4J_DATABASE")}
	if err := client.Ensure(ctx); err != nil {
		return err
	}
	prefix := "btw-bench-" + strings.ReplaceAll(uuid.NewString(), "-", "") + "-"
	defer func() {
		cleanupCtx, closeCleanup := context.WithTimeout(context.Background(), time.Minute)
		defer closeCleanup()
		options := []neo4j.ExecuteQueryConfigurationOption{}
		if client.Database != "" {
			options = append(options, neo4j.ExecuteQueryWithDatabase(client.Database))
		}
		_, _ = neo4j.ExecuteQuery[*neo4j.EagerResult](cleanupCtx, driver, `MATCH (i:ContentItem) WHERE i.root_id STARTS WITH $prefix DETACH DELETE i`, map[string]any{"prefix": prefix}, neo4j.EagerResultTransformer, options...)
		_, _ = neo4j.ExecuteQuery[*neo4j.EagerResult](cleanupCtx, driver, `MATCH (c:ContentConcept) WHERE c.label STARTS WITH $prefix DETACH DELETE c`, map[string]any{"prefix": prefix}, neo4j.EagerResultTransformer, options...)
	}()
	start := time.Now()
	seeds := make([]string, 0, count)
	for offset := 0; offset < count; offset += 100 {
		end := min(offset+100, count)
		roots := make([]item.Item, 0, end-offset)
		objects := make([]item.Item, 0, (end-offset)*2)
		for _, doc := range documents[offset:end] {
			root := prefix + doc.ID
			revision := prefix + "source"
			title := prefix + doc.Title
			runes := int64(utf8.RuneCountInString(doc.Text))
			firstEnd := min(runes, 100)
			roots = append(roots, item.Item{ID: root, RootID: root, RootKind: "article", Kind: "article", RevisionID: revision, Title: doc.Title})
			objects = append(objects,
				item.Item{ID: root + "-fact", RootID: root, RootKind: "article", Kind: "fact", RevisionID: revision, Title: title, Location: item.Location{RevisionID: revision, SectionID: "abstract", Start: 0, End: firstEnd}},
				item.Item{ID: root + "-context", RootID: root, RootKind: "article", Kind: "fact", RevisionID: revision, Title: prefix + "context " + doc.Title, Location: item.Location{RevisionID: revision, SectionID: "abstract", Start: firstEnd, End: runes}},
			)
			seeds = append(seeds, title)
		}
		if err := client.Upsert(ctx, roots, nil); err != nil {
			return err
		}
		if err := client.Upsert(ctx, objects, nil); err != nil {
			return err
		}
	}
	buildSeconds := time.Since(start).Seconds()
	for n := 0; n < min(queryCount, 5); n++ {
		seed := seeds[n*count/queryCount]
		for hops := 1; hops <= 3; hops++ {
			if _, err := client.Search(ctx, []string{seed}, []string{"article"}, hops, 64); err != nil {
				return err
			}
		}
	}
	observed := map[int][]float64{1: {}, 2: {}, 3: {}}
	hitTotals := map[int]int{}
	for n := 0; n < queryCount; n++ {
		seed := seeds[n*count/queryCount]
		for offset := 0; offset < 3; offset++ {
			hops := (n+offset)%3 + 1
			begin := time.Now()
			hits, err := client.Search(ctx, []string{seed}, []string{"article"}, hops, 64)
			observed[hops] = append(observed[hops], time.Since(begin).Seconds()*1000)
			if err != nil || len(hits) == 0 {
				return fmt.Errorf("graph %d-hop query failed: %v", hops, err)
			}
			hitTotals[hops] += len(hits)
		}
	}
	tracks := map[string]any{}
	for hops := 1; hops <= 3; hops++ {
		durations := observed[hops]
		totalMS := 0.0
		for _, d := range durations {
			totalMS += d
		}
		sort.Float64s(durations)
		tracks[fmt.Sprintf("%d_hop", hops)] = map[string]any{"queries": queryCount, "p50_ms": percentile(durations, .5), "p95_ms": percentile(durations, .95), "p99_ms": percentile(durations, .99), "sequential_queries_per_second": float64(queryCount) * 1000 / totalMS, "mean_candidates": float64(hitTotals[hops]) / float64(queryCount)}
	}
	return json.NewEncoder(output).Encode(map[string]any{
		"experiment": "beir_scifact_synthetic_graph", "dataset": "BEIR SciFact corpus titles", "source_documents": count,
		"projection":  "two synthetic semantic objects per source, with title-derived concept labels and illustrative abstract positions; latency only, not semantic quality",
		"graph_nodes": 5 * count, "query_seeds": queryCount, "backend": "BTW Neo4j adapter over isolated native Neo4j",
		"build_seconds": buildSeconds, "warmup_queries_per_hop": min(queryCount, 5), "query_schedule": "interleaved 1/2/3-hop order", "tracks": tracks,
	})
}
