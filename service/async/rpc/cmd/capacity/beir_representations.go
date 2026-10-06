package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/datacenter"
	idx "github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/items"
)

type beirTrack struct {
	Metrics   beirMetrics `json:"metrics"`
	P50MS     float64     `json:"search_p50_ms,omitempty"`
	P95MS     float64     `json:"search_p95_ms,omitempty"`
	durations []float64
}

func beirRankedIDs(rows []idx.Candidate) []string {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.ItemID
	}
	return ids
}

func beirFuse(lists ...[]idx.Candidate) []string {
	scores := map[string]float64{}
	for _, list := range lists {
		seen := map[string]bool{}
		for rank, row := range list {
			if seen[row.ItemID] {
				continue
			}
			seen[row.ItemID] = true
			scores[row.ItemID] += 1 / float64(60+rank+1)
		}
	}
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if scores[ids[i]] == scores[ids[j]] {
			return ids[i] < ids[j]
		}
		return scores[ids[i]] > scores[ids[j]]
	})
	return ids
}

func runBEIRRepresentations(dir, endpoint string, sampleDocs, queryLimit int, output io.Writer) error {
	if dir == "" || endpoint == "" || sampleDocs < 1 || queryLimit < 1 {
		return fmt.Errorf("representation experiment needs -beir-dir, -datacenter-url, positive -sample-docs and -queries")
	}
	documents, err := readJSONL[beirDocument](dir + "/corpus.jsonl")
	if err != nil {
		return err
	}
	queries, err := readJSONL[beirQuery](dir + "/queries.jsonl")
	if err != nil {
		return err
	}
	qrels, err := readQrels(dir + "/qrels/test.tsv")
	if err != nil {
		return err
	}
	selectedQueries := []beirQuery{}
	for _, query := range queries {
		if len(qrels[query.ID]) > 0 {
			selectedQueries = append(selectedQueries, query)
		}
	}
	sort.Slice(selectedQueries, func(i, j int) bool { return selectedQueries[i].ID < selectedQueries[j].ID })
	if len(selectedQueries) > queryLimit {
		selectedQueries = selectedQueries[:queryLimit]
	}
	positive := map[string]bool{}
	for _, query := range selectedQueries {
		for id := range qrels[query.ID] {
			positive[id] = true
		}
	}
	if len(selectedQueries) == 0 || len(positive) > sampleDocs {
		return fmt.Errorf("sample must contain all judged positives for selected queries")
	}
	byID := map[string]beirDocument{}
	negatives := []beirDocument{}
	for _, doc := range documents {
		byID[doc.ID] = doc
		if !positive[doc.ID] {
			negatives = append(negatives, doc)
		}
	}
	for id := range positive {
		if _, ok := byID[id]; !ok {
			return fmt.Errorf("judged document %s absent from corpus", id)
		}
	}
	sort.Slice(negatives, func(i, j int) bool {
		a := sha256.Sum256([]byte("BTW-BEIR-37:" + negatives[i].ID))
		b := sha256.Sum256([]byte("BTW-BEIR-37:" + negatives[j].ID))
		return string(a[:]) < string(b[:])
	})
	ids := []string{}
	for id := range positive {
		ids = append(ids, id)
	}
	for _, doc := range negatives {
		if len(ids) >= sampleDocs {
			break
		}
		ids = append(ids, doc.ID)
	}
	if len(ids) != sampleDocs {
		return fmt.Errorf("sample has only %d documents", len(ids))
	}
	sort.Strings(ids)
	identity := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	connector, err := datacenter.New(endpoint, datacenter.DeploymentIdentity())
	if err != nil {
		return err
	}
	manifest, err := connector.Manifest(ctx)
	if err != nil {
		return err
	}
	encoder := connector.Inference()
	retrieval := manifest.Retrieval
	index := idx.NewExact()
	if err := index.Ensure(ctx, retrieval); err != nil {
		return err
	}
	start := time.Now()
	textBytes := 0
	subvectors := 0
	for _, id := range ids {
		doc := byID[id]
		text := strings.TrimSpace(doc.Title + "\n" + doc.Text)
		textBytes += len(text)
		encoded, err := encoder.EncodeModes(ctx, retrieval.Model, retrieval.Space, []string{text}, true, true, true)
		if err != nil {
			return fmt.Errorf("encode BEIR document %s: %w", id, err)
		}
		rows := make([]idx.Record, 0, len(encoded[0].Segments))
		for n, segment := range encoded[0].Segments {
			subvectors += len(segment.Multi)
			rows = append(rows, idx.Record{ID: id + "/" + strconv.Itoa(n), ItemID: id, RootID: id, RootKind: "article", RevisionID: "beir-source", Encoding: segment})
		}
		if err := index.Upsert(ctx, retrieval, rows); err != nil {
			return err
		}
	}
	buildSeconds := time.Since(start).Seconds()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	tracks := map[string]*beirTrack{}
	for _, lane := range []string{"dense", "sparse", "multi", "dense_sparse_rrf", "three_lane_rrf"} {
		tracks[lane] = &beirTrack{durations: []float64{}}
	}
	balancedEncodeTimes := []float64{}
	deepEncodeTimes := []float64{}
	for _, query := range selectedQueries {
		begin := time.Now()
		balanced, err := encoder.EncodeModes(ctx, retrieval.Model, retrieval.Space, []string{query.Text}, true, true, false)
		balancedEncodeTimes = append(balancedEncodeTimes, time.Since(begin).Seconds()*1000)
		if err != nil {
			return err
		}
		begin = time.Now()
		deep, err := encoder.EncodeModes(ctx, retrieval.Model, retrieval.Space, []string{query.Text}, true, true, true)
		deepEncodeTimes = append(deepEncodeTimes, time.Since(begin).Seconds()*1000)
		if err != nil {
			return err
		}
		lanes := map[string][]idx.Candidate{}
		for _, lane := range []string{"dense", "sparse", "multi"} {
			input := balanced[0]
			if lane == "multi" {
				input = deep[0]
			}
			begin = time.Now()
			lanes[lane], err = index.Search(ctx, retrieval, lane, input, []string{"article"}, 10)
			tracks[lane].durations = append(tracks[lane].durations, time.Since(begin).Seconds()*1000)
			if err != nil {
				return err
			}
		}
		choices := map[string][]string{
			"dense": beirRankedIDs(lanes["dense"]), "sparse": beirRankedIDs(lanes["sparse"]), "multi": beirRankedIDs(lanes["multi"]),
			"dense_sparse_rrf": beirFuse(lanes["dense"], lanes["sparse"]),
			"three_lane_rrf":   beirFuse(lanes["dense"], lanes["sparse"], lanes["multi"]),
		}
		for name, ranked := range choices {
			m := evaluateBEIR(ranked, qrels[query.ID])
			track := tracks[name]
			track.Metrics.Recall10 += m.Recall10
			track.Metrics.NDCG10 += m.NDCG10
			track.Metrics.MRR10 += m.MRR10
		}
	}
	n := float64(len(selectedQueries))
	for _, track := range tracks {
		track.Metrics.Recall10 /= n
		track.Metrics.NDCG10 /= n
		track.Metrics.MRR10 /= n
		if len(track.durations) > 0 {
			sort.Float64s(track.durations)
			track.P50MS = percentile(track.durations, .5)
			track.P95MS = percentile(track.durations, .95)
		}
	}
	sort.Float64s(balancedEncodeTimes)
	sort.Float64s(deepEncodeTimes)
	corpusHash, err := fileSHA256(dir + "/corpus.jsonl")
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]any{
		"experiment": "beir_scifact_representations", "dataset": "BEIR SciFact test", "corpus_sha256": corpusHash,
		"original_corpus_documents": len(documents), "sample_documents": len(ids), "sample_queries": len(selectedQueries), "judged_positive_documents_included": len(positive),
		"sampling":              "all positives for selected lexicographically first test queries plus SHA256-ordered distractors; not the official full-corpus benchmark",
		"sample_doc_ids_sha256": hex.EncodeToString(identity[:]), "sample_text_bytes": textBytes, "subvectors": subvectors,
		"backend": "real BGE-M3 inference plus BTW Exact numerical reference, not Milvus", "build_seconds": buildSeconds,
		"go_heap_after_index_bytes": memory.HeapAlloc, "balanced_query_encode_p50_ms": percentile(balancedEncodeTimes, .5),
		"deep_query_encode_p50_ms": percentile(deepEncodeTimes, .5), "tracks": tracks,
	})
}
