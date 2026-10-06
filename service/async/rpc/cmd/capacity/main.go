// Command capacity estimates whole-item capacity or benchmarks an explicit index.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/inference"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	idx "github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/items"
	mc "github.com/milvus-io/milvus/client/v2/milvusclient"
	"math"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	count := flag.Int64("items", 10000, "whole items")
	dimensions := flag.Int("dimensions", 1024, "vector dimensions")
	sub := flag.Int("subvectors", 32, "subvectors per item")
	body := flag.Int64("body-bytes", 8192, "average full source size")
	replicas := flag.Int64("replicas", 1, "index replicas")
	factor := flag.Float64("index-factor", 1.5, "assumed index overhead/raw ratio")
	build := flag.Float64("build-factor", 2, "assumed temporary build/raw ratio")
	mode := flag.String("mode", "estimate", "estimate|exact|milvus|api|beir-lexical|beir-representations|beir-graph")
	address := flag.String("milvus", "127.0.0.1:19530", "isolated benchmark server")
	beirDir := flag.String("beir-dir", "", "directory containing BEIR corpus.jsonl, queries.jsonl and qrels/test.tsv")
	elasticURL := flag.String("elasticsearch-url", "", "isolated Elasticsearch URL for BEIR lexical experiment")
	dataCenterURL := flag.String("datacenter-url", "", "DataCenter origin for BEIR representation experiment")
	sampleDocs := flag.Int("sample-docs", 24, "positive-inclusive BEIR representation sample size")
	apiURL := flag.String("api-url", "", "isolated BTW API URL for all three search levels")
	queriesFile := flag.String("queries-file", "", "JSONL with query, ontology and optional relevant_ids")
	queries := flag.Int("queries", 20, "query count")
	flag.Parse()
	if *mode == "api" {
		return runAPI(*apiURL, *queriesFile)
	}
	if *mode == "beir-lexical" {
		return runBEIRLexical(*beirDir, *elasticURL, *queries, os.Stdout)
	}
	if *mode == "beir-representations" {
		return runBEIRRepresentations(*beirDir, *dataCenterURL, *sampleDocs, *queries, os.Stdout)
	}
	if *mode == "beir-graph" {
		return runBEIRGraph(*beirDir, int(*count), *queries, os.Stdout)
	}
	if *count < 1 || *dimensions < 1 || *sub < 1 || *queries < 1 {
		return fmt.Errorf("positive sizes required")
	}
	raw := *count * int64(*dimensions) * 4
	multi := raw * int64(*sub)
	report := map[string]any{"mode": *mode, "whole_items": *count, "semantic_objects": 0, "subvectors": *count * int64(*sub), "dimensions": *dimensions, "body_bytes": *count * *body, "dense_raw_bytes": raw, "multi_raw_bytes": multi, "replicas": *replicas, "estimated_index_bytes": float64(raw+multi) * *factor * float64(*replicas), "estimated_build_temporary_bytes": float64(raw+multi) * *build, "sparse_bytes": "measure from actual nonzero terms", "ram": "measure actual server working set; not inferred from disk size", "quality_evaluated": false, "synthetic_data": true}
	if *mode == "estimate" {
		report["executed_items"] = 0
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	ctx := context.Background()
	name := fmt.Sprintf("bench%d", time.Now().UnixNano())
	retrieval := item.Retrieval{Model: "synthetic-benchmark", Space: "synthetic-benchmark", Dimensions: *dimensions}
	retrieval.Dense = name + "_dense"
	retrieval.Sparse = name + "_sparse"
	retrieval.Multi = name + "_multi"
	var index idx.Index
	if *mode == "exact" {
		if *count > 100000 {
			return fmt.Errorf("exact reference is limited to 100000 items; use Milvus for capacity tests")
		}
		index = idx.NewExact()
	} else if *mode == "milvus" {
		client, e := mc.New(ctx, &mc.ClientConfig{Address: *address})
		if e != nil {
			return e
		}
		defer client.Close(ctx)
		index = &idx.Milvus{Client: client, BatchSize: 256, QueryTokenLimit: 1024}
	} else {
		return fmt.Errorf("unknown mode")
	}
	if e := index.Ensure(ctx, retrieval); e != nil {
		return e
	}
	rng := rand.New(rand.NewSource(37))
	samples := []inference.Encoding{}
	start := time.Now()
	batch := []idx.Record{}
	for i := int64(0); i < *count; i++ {
		v := make([]float32, *dimensions)
		norm := 0.
		for j := range v {
			v[j] = rng.Float32()*2 - 1
			norm += float64(v[j] * v[j])
		}
		for j := range v {
			v[j] /= float32(math.Sqrt(norm))
		}
		multi := make([][]float32, *sub)
		for j := range multi {
			multi[j] = append([]float32(nil), v...)
		}
		segment := inference.Segment{Dense: v, Sparse: inference.Sparse{Indices: []uint32{uint32(i % 10000)}, Values: []float32{1}}, Multi: multi}
		id := fmt.Sprint(i)
		batch = append(batch, idx.Record{ID: id, ItemID: id, RootID: id, RootKind: "article", RevisionID: "1", Location: item.Location{RevisionID: "1", SectionID: "s", End: 1}, Encoding: segment})
		if len(samples) < *queries {
			samples = append(samples, inference.Encoding{Model: retrieval.Model, Space: retrieval.Space, Segments: []inference.Segment{segment}})
		}
		if len(batch) == 128 {
			if e := index.Upsert(ctx, retrieval, batch); e != nil {
				return e
			}
			batch = nil
		}
	}
	if e := index.Upsert(ctx, retrieval, batch); e != nil {
		return e
	}
	report["build_seconds"] = time.Since(start).Seconds()
	report["ingest_items_per_second"] = float64(*count) / time.Since(start).Seconds()
	durations := []float64{}
	for _, q := range samples {
		start := time.Now()
		for _, lane := range []string{"dense", "sparse", "multi"} {
			if _, e := index.Search(ctx, retrieval, lane, q, []string{"article"}, 10); e != nil {
				return e
			}
		}
		durations = append(durations, time.Since(start).Seconds()*1000)
	}
	sort.Float64s(durations)
	report["query_p50_ms"] = durations[len(durations)/2]
	report["query_p95_ms"] = durations[min(len(durations)-1, int(float64(len(durations))*.95))]
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	report["client_heap_bytes"] = mem.HeapAlloc
	report["executed_items"] = *count
	// Exercise an update and deletion; numbers describe this test, not a production SLA.
	start = time.Now()
	record := idx.Record{ID: "0", ItemID: "0", RootID: "0", RootKind: "article", RevisionID: "2", Location: item.Location{RevisionID: "2", SectionID: "s", End: 1}, Encoding: samples[0].Segments[0]}
	if e := index.Upsert(ctx, retrieval, []idx.Record{record}); e != nil {
		return e
	}
	if e := index.Delete(ctx, retrieval, "0"); e != nil {
		return e
	}
	report["update_delete_seconds"] = time.Since(start).Seconds()
	report["benchmark_index"] = name
	return json.NewEncoder(os.Stdout).Encode(report)
}
