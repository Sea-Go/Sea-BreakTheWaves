package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
)

func TestAPIBenchmarkMeasuresCompleteThreeLevelResponses(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" || r.Method != http.MethodPost {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		var request item.Request
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Ontology == nil || request.Ontology.UserID != "reader" || request.Query != "insects" {
			http.Error(w, "wrong request", http.StatusBadRequest)
			return
		}
		if request.SearchLevel != "fast" && request.SearchLevel != "balanced" && request.SearchLevel != "deep" {
			http.Error(w, "wrong search level", http.StatusBadRequest)
			return
		}
		calls.Add(1)
		response, _ := json.Marshal(item.Result{Status: "ok", Hits: []item.Hit{{Item: item.Item{ID: "source", RootID: "source"}}}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(response[:1])
		w.(http.Flusher).Flush()
		time.Sleep(25 * time.Millisecond)
		_, _ = w.Write(response[1:])
	}))
	defer server.Close()
	queries := filepath.Join(t.TempDir(), "queries.jsonl")
	if err := os.WriteFile(queries, []byte(`{"query":"insects","ontology":{"user_id":"reader"},"relevant_ids":["source"]}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runAPITo(server.URL, queries, &output); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Levels map[string]levelReport `json:"levels"`
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || len(report.Levels) != 3 {
		t.Fatalf("benchmark did not execute all levels: calls=%d report=%s", calls.Load(), output.String())
	}
	for _, level := range []string{"fast", "balanced", "deep"} {
		got := report.Levels[level]
		if got.Executed != 1 || got.Errors != 0 || got.P50MS < 20 || !got.QualityEvaluated || got.RecallAtLimit == nil || *got.RecallAtLimit != 1 {
			t.Fatalf("%s response body was not measured and judged: %+v", level, got)
		}
	}
}
