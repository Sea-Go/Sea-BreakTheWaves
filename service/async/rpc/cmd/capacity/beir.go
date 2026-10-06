package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/elastic"
	"github.com/google/uuid"
)

type beirDocument struct {
	ID    string `json:"_id"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

type beirQuery struct {
	ID   string `json:"_id"`
	Text string `json:"text"`
}

type beirMetrics struct {
	Recall10 float64 `json:"recall_at_10"`
	NDCG10   float64 `json:"ndcg_at_10"`
	MRR10    float64 `json:"mrr_at_10"`
}

func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows := []T{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		var row T
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		rows = append(rows, row)
	}
	return rows, scanner.Err()
}

func readQrels(path string) (map[string]map[string]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = '\t'
	header, err := r.Read()
	if err != nil {
		return nil, err
	}
	if len(header) != 3 || header[0] != "query-id" || header[1] != "corpus-id" || header[2] != "score" {
		return nil, fmt.Errorf("unexpected BEIR qrels header")
	}
	relevance := map[string]map[string]bool{}
	for {
		row, err := r.Read()
		if err == io.EOF {
			return relevance, nil
		}
		if err != nil {
			return nil, err
		}
		score, err := strconv.Atoi(row[2])
		if err != nil {
			return nil, err
		}
		if score <= 0 {
			continue
		}
		if relevance[row[0]] == nil {
			relevance[row[0]] = map[string]bool{}
		}
		relevance[row[0]][row[1]] = true
	}
}

func evaluateBEIR(ids []string, relevant map[string]bool) beirMetrics {
	if len(relevant) == 0 {
		return beirMetrics{}
	}
	seen := map[string]bool{}
	dcg := 0.0
	first := 0.0
	for rank, id := range ids {
		if rank >= 10 {
			break
		}
		if relevant[id] && !seen[id] {
			seen[id] = true
			dcg += 1 / math.Log2(float64(rank+2))
			if first == 0 {
				first = 1 / float64(rank+1)
			}
		}
	}
	ideal := 0.0
	for rank := 0; rank < min(len(relevant), 10); rank++ {
		ideal += 1 / math.Log2(float64(rank+2))
	}
	return beirMetrics{Recall10: float64(len(seen)) / float64(len(relevant)), NDCG10: dcg / ideal, MRR10: first}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func elasticStoreBytes(ctx context.Context, client *elastic.Client) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(client.URL, "/")+"/"+client.Index+"/_stats/store", nil)
	if err != nil {
		return 0, err
	}
	if client.Username != "" {
		req.SetBasicAuth(client.Username, client.Password)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return 0, fmt.Errorf("elasticsearch store stats returned %d", response.StatusCode)
	}
	var stats struct {
		Indices map[string]struct {
			Primaries struct {
				Store struct {
					Size int64 `json:"size_in_bytes"`
				} `json:"store"`
			} `json:"primaries"`
		} `json:"indices"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&stats); err != nil {
		return 0, err
	}
	index, ok := stats.Indices[client.Index]
	if !ok {
		return 0, fmt.Errorf("elasticsearch store stats omit benchmark index")
	}
	return index.Primaries.Store.Size, nil
}

func runBEIRLexical(dir, endpoint string, queryLimit int, output io.Writer) error {
	if dir == "" || endpoint == "" || queryLimit < 1 {
		return fmt.Errorf("beir lexical experiment requires -beir-dir, -elasticsearch-url and positive -queries")
	}
	corpusPath := filepath.Join(dir, "corpus.jsonl")
	queryPath := filepath.Join(dir, "queries.jsonl")
	qrelsPath := filepath.Join(dir, "qrels", "test.tsv")
	documents, err := readJSONL[beirDocument](corpusPath)
	if err != nil {
		return err
	}
	queries, err := readJSONL[beirQuery](queryPath)
	if err != nil {
		return err
	}
	qrels, err := readQrels(qrelsPath)
	if err != nil {
		return err
	}
	if len(documents) == 0 || len(qrels) == 0 {
		return fmt.Errorf("BEIR corpus or test judgments are empty")
	}
	selected := make([]beirQuery, 0, len(qrels))
	for _, query := range queries {
		if len(qrels[query.ID]) > 0 {
			selected = append(selected, query)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
	if len(selected) > queryLimit {
		selected = selected[:queryLimit]
	}
	if len(selected) == 0 {
		return fmt.Errorf("BEIR test queries missing from query file")
	}
	indexName := "btw_bench_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	client := &elastic.Client{URL: endpoint, Index: indexName, Username: os.Getenv("BTW_TEST_ELASTICSEARCH_USER"), Password: os.Getenv("BTW_TEST_ELASTICSEARCH_PASSWORD")}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := client.Ensure(ctx); err != nil {
		return err
	}
	defer func() {
		req, e := http.NewRequest(http.MethodDelete, strings.TrimRight(endpoint, "/")+"/"+indexName, nil)
		if e != nil {
			return
		}
		if client.Username != "" {
			req.SetBasicAuth(client.Username, client.Password)
		}
		response, e := http.DefaultClient.Do(req)
		if e == nil {
			response.Body.Close()
		}
	}()
	start := time.Now()
	textBytes := int64(0)
	for _, doc := range documents {
		if doc.ID == "" || doc.Text == "" {
			return fmt.Errorf("BEIR corpus contains an empty document")
		}
		textBytes += int64(len(doc.Title) + len(doc.Text))
		row := elastic.Document{ID: doc.ID, ItemID: doc.ID, RootID: doc.ID, RootKind: "article", Kind: "article", RevisionID: "beir-source", SectionID: "abstract", Start: 0, End: int64(utf8.RuneCountInString(doc.Text)), Title: doc.Title, Body: doc.Text, Language: "en"}
		if err := client.Upsert(ctx, row); err != nil {
			return err
		}
	}
	if err := client.Refresh(ctx); err != nil {
		return err
	}
	indexSeconds := time.Since(start).Seconds()
	durations := []float64{}
	sums := beirMetrics{}
	queryStart := time.Now()
	for _, query := range selected {
		begin := time.Now()
		hits, err := client.Search(ctx, elastic.Query{Text: query.Text, Kinds: []string{"article"}, Limit: 10})
		durations = append(durations, time.Since(begin).Seconds()*1000)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(hits))
		for _, hit := range hits {
			ids = append(ids, hit.ItemID)
		}
		m := evaluateBEIR(ids, qrels[query.ID])
		sums.Recall10 += m.Recall10
		sums.NDCG10 += m.NDCG10
		sums.MRR10 += m.MRR10
	}
	querySeconds := time.Since(queryStart).Seconds()
	sort.Float64s(durations)
	storeBytes, err := elasticStoreBytes(ctx, client)
	if err != nil {
		return err
	}
	corpusHash, err := fileSHA256(corpusPath)
	if err != nil {
		return err
	}
	qrelsHash, err := fileSHA256(qrelsPath)
	if err != nil {
		return err
	}
	n := float64(len(selected))
	return json.NewEncoder(output).Encode(map[string]any{
		"experiment": "beir_scifact_lexical", "dataset": "BEIR SciFact test", "corpus_sha256": corpusHash, "qrels_sha256": qrelsHash,
		"documents": len(documents), "queries": len(selected), "selection": "first test query IDs in lexicographic order", "text_bytes": textBytes,
		"backend": "BTW Elasticsearch adapter", "index_seconds": indexSeconds, "index_documents_per_second": float64(len(documents)) / indexSeconds, "primary_index_store_bytes": storeBytes,
		"recall_at_10": sums.Recall10 / n, "ndcg_at_10": sums.NDCG10 / n, "mrr_at_10": sums.MRR10 / n,
		"query_p50_ms": percentile(durations, .5), "query_p95_ms": percentile(durations, .95), "query_p99_ms": percentile(durations, .99),
		"sequential_queries_per_second": n / querySeconds, "whole_item_scoring": true,
	})
}
