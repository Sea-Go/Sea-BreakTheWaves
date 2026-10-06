package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/google/uuid"
)

type benchmarkCase struct {
	Query       string        `json:"query"`
	Context     string        `json:"context"`
	Scenario    string        `json:"scenario"`
	Ontology    item.Ontology `json:"ontology"`
	RelevantIDs []string      `json:"relevant_ids"`
	Limit       int           `json:"limit"`
}
type levelReport struct {
	Executed         int      `json:"executed"`
	Errors           int      `json:"errors"`
	Degraded         int      `json:"degraded"`
	Insufficient     int      `json:"insufficient_evidence"`
	P50MS            float64  `json:"p50_ms"`
	P95MS            float64  `json:"p95_ms"`
	P99MS            float64  `json:"p99_ms"`
	QueriesPerSecond float64  `json:"queries_per_second"`
	QualityEvaluated bool     `json:"quality_evaluated"`
	RecallAtLimit    *float64 `json:"recall_at_limit,omitempty"`
	NDCGAtLimit      *float64 `json:"ndcg_at_limit,omitempty"`
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(len(values)))) - 1
	return values[max(0, min(i, len(values)-1))]
}
func runAPI(endpoint, filename string) error {
	return runAPITo(endpoint, filename, os.Stdout)
}
func runAPITo(endpoint, filename string, output io.Writer) error {
	if endpoint == "" || filename == "" {
		return fmt.Errorf("api benchmark requires explicit isolated -api-url and -queries-file")
	}
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	cases := []benchmarkCase{}
	scan := bufio.NewScanner(file)
	scan.Buffer(make([]byte, 64*1024), 4<<20)
	for scan.Scan() {
		var value benchmarkCase
		if err := json.Unmarshal(scan.Bytes(), &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.Query) == "" || value.Ontology.UserID == "" {
			return fmt.Errorf("query and ontology.user_id required")
		}
		if value.Scenario == "" {
			value.Scenario = "search"
		}
		if value.Limit < 1 {
			value.Limit = 10
		}
		cases = append(cases, value)
	}
	if err := scan.Err(); err != nil {
		return err
	}
	if len(cases) == 0 {
		return fmt.Errorf("benchmark query list is empty")
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	reports := map[string]levelReport{}
	for _, level := range []string{"fast", "balanced", "deep"} {
		start := time.Now()
		durations := []float64{}
		report := levelReport{}
		recallSum, ndcgSum, judged := 0., 0., 0
		for _, sample := range cases {
			request := item.Request{RequestID: uuid.NewString(), Ontology: &sample.Ontology, Scenario: sample.Scenario, Query: sample.Query, Context: sample.Context, Limit: sample.Limit, SearchLevel: level}
			body, err := json.Marshal(request)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/api/search", bytes.NewReader(body))
			if err != nil {
				cancel()
				return err
			}
			httpRequest.Header.Set("Content-Type", "application/json")
			begin := time.Now()
			response, err := client.Do(httpRequest)
			report.Executed++
			if err != nil {
				cancel()
				durations = append(durations, time.Since(begin).Seconds()*1000)
				report.Errors++
				continue
			}
			raw, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
			response.Body.Close()
			cancel()
			durations = append(durations, time.Since(begin).Seconds()*1000)
			if readErr != nil || response.StatusCode != 200 {
				report.Errors++
				continue
			}
			var result item.Result
			if json.Unmarshal(raw, &result) != nil {
				report.Errors++
				continue
			}
			if result.Status == "degraded" {
				report.Degraded++
			}
			if result.Status == "insufficient_evidence" {
				report.Insufficient++
			}
			if len(sample.RelevantIDs) == 0 {
				continue
			}
			relevance := map[string]bool{}
			for _, id := range sample.RelevantIDs {
				relevance[id] = true
			}
			found := map[string]bool{}
			dcg := 0.
			for rank, hit := range result.Hits {
				if rank >= sample.Limit {
					break
				}
				id := hit.Item.ID
				root := hit.Item.RootID
				if relevance[id] || relevance[root] {
					key := id
					if relevance[root] {
						key = root
					}
					if !found[key] {
						found[key] = true
						dcg += 1 / math.Log2(float64(rank+2))
					}
				}
			}
			ideal := 0.
			for rank := 0; rank < min(len(relevance), sample.Limit); rank++ {
				ideal += 1 / math.Log2(float64(rank+2))
			}
			recallSum += float64(len(found)) / float64(len(relevance))
			if ideal > 0 {
				ndcgSum += dcg / ideal
			}
			judged++
		}
		sort.Float64s(durations)
		report.P50MS = percentile(durations, .5)
		report.P95MS = percentile(durations, .95)
		report.P99MS = percentile(durations, .99)
		report.QueriesPerSecond = float64(report.Executed) / max(time.Since(start).Seconds(), .000001)
		if judged > 0 {
			recall, ndcg := recallSum/float64(judged), ndcgSum/float64(judged)
			report.RecallAtLimit = &recall
			report.NDCGAtLimit = &ndcg
			report.QualityEvaluated = true
		}
		reports[level] = report
	}
	return json.NewEncoder(output).Encode(map[string]any{"mode": "api", "actual_requests": true, "synthetic_data": false, "levels": reports, "resource_usage": "collect backend RAM, disk and index sizes from the isolated services"})
}
