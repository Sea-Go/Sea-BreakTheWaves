package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/evaluation/grounding"
)

func sourceFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	casePath, usagePath, rtwPath, tracePath := os.Getenv("SEA_GROUNDING_CASE"),
		os.Getenv("SEA_GROUNDING_USAGE_REPORT"), os.Getenv("SEA_GROUNDING_RTW_REPORT"),
		os.Getenv("SEA_GROUNDING_RTW_TRACE_LOG")
	provided := 0
	for _, path := range []string{casePath, usagePath, rtwPath, tracePath} {
		if path != "" {
			provided++
		}
	}
	if provided == 4 {
		return casePath, usagePath, rtwPath, tracePath
	}
	if provided != 0 {
		t.Fatal("set all same-run grounding evidence paths or none")
	}
	directory := t.TempDir()
	searchID, answerID := "search-cli-fixture", "answer-cli-fixture"
	quote, answer := "The published source says calm water.", "The water is always calm."
	quoteSHA := grounding.Digest([]byte(quote))
	durableRef := "search-citations/sha256/" + grounding.Digest([]byte(searchID))
	rtwRaw, err := json.Marshal(map[string]any{
		"schema_version": "sea.search.live-summary-rtw.v1", "delivery_execution": "observed",
		"search_id": searchID, "answer_id": answerID, "answer": answer,
		"evidence_id": "ev-cli-fixture", "quote": quote, "quote_hash": quoteSHA,
		"citation_receipt_ref": durableRef, "rtw_answer_rows": 1,
		"rtw_answer_citation_rows": 1, "rtw_search_citation_rows": 1,
		"rtw_product_history_present": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	usageRaw, err := json.Marshal(map[string]any{
		"schema_version": "sea.search.live-summary-usage.v1", "status": "functional_chain_passed",
		"search_id": searchID, "answer_id": answerID, "rtw_answer_sha256": grounding.Digest(rtwRaw),
		"evidence_quote": quote, "evidence_quote_sha256": quoteSHA, "rtw_accepted_answer": answer,
		"citation_receipt_ref": durableRef, "model_interaction_id": "interaction-cli-fixture",
		"model_response_sha256": grounding.Digest([]byte("model response")),
		"quality_gate":          "synthetic-only", "activation": "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	traceID := strings.Repeat("a", 32)
	var traceLines [][]byte
	for _, event := range []string{"knowledge.product.search.succeeded",
		"knowledge.search.citations.accept.succeeded", "knowledge.answer.accept.succeeded"} {
		raw, marshalErr := json.Marshal(map[string]any{"event": event, "search_id": searchID,
			"answer_id": answerID, "operation_id": "search-citations:" + searchID,
			"trace_id": traceID, "outcome": "succeeded"})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		traceLines = append(traceLines, raw)
	}
	traceRaw := append(bytes.Join(traceLines, []byte{'\n'}), '\n')
	usagePath, rtwPath, tracePath = filepath.Join(directory, "usage.json"),
		filepath.Join(directory, "rtw.json"), filepath.Join(directory, "trace.jsonl")
	for path, raw := range map[string][]byte{usagePath: usageRaw, rtwPath: rtwRaw, tracePath: traceRaw} {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	value, err := grounding.FreezeCase(usagePath, rtwPath, tracePath)
	if err != nil {
		t.Fatal(err)
	}
	caseRaw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	casePath = filepath.Join(directory, "case.json")
	if err := os.WriteFile(casePath, append(caseRaw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return casePath, usagePath, rtwPath, tracePath
}

func TestAuthoritativeModeRefreezesSourcesBeforeWorkerRead(t *testing.T) {
	casePath, usagePath, rtwPath, tracePath := sourceFixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-worker-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	directory := t.TempDir()
	token := filepath.Join(directory, "worker-token")
	if err := os.WriteFile(token, []byte("test-worker-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, "pending.json")
	if err := evaluateRTW(casePath, usagePath, rtwPath, tracePath, server.URL, token, output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Decision string `json:"decision"`
	}
	raw, err := os.ReadFile(output)
	if err != nil || json.Unmarshal(raw, &result) != nil || result.Decision != "pending_human_review" || calls.Load() != 1 {
		t.Fatalf("RTW 404 was not pending after source refreeze: %+v calls=%d err=%v", result, calls.Load(), err)
	}
	mutated, err := os.ReadFile(rtwPath)
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if err := json.Unmarshal(mutated, &source); err != nil {
		t.Fatal(err)
	}
	source["quote"] = "changed offline quote"
	mutated, _ = json.Marshal(source)
	mutatedPath := filepath.Join(directory, "mutated-rtw.json")
	if err := os.WriteFile(mutatedPath, mutated, 0600); err != nil {
		t.Fatal(err)
	}
	if err := evaluateRTW(casePath, usagePath, mutatedPath, tracePath, server.URL, token,
		filepath.Join(directory, "must-not-exist.json")); err == nil || calls.Load() != 1 {
		t.Fatal("changed original source reached RTW Worker read")
	}
	canonicalCase, err := os.ReadFile(casePath)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, canonicalCase); err != nil {
		t.Fatal(err)
	}
	changedCasePath := filepath.Join(directory, "same-value-different-bytes.json")
	if err := os.WriteFile(changedCasePath, compact.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := evaluateRTW(changedCasePath, usagePath, rtwPath, tracePath, server.URL, token,
		filepath.Join(directory, "must-not-exist-2.json")); err == nil || calls.Load() != 1 {
		t.Fatal("nonidentical submitted case bytes reached RTW Worker read")
	}
}
