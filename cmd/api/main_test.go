package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/sparse"
)

func testSettings() indexSettings {
	contract := func(id string, kind representation.Kind, dimensions int) representation.Contract {
		return representation.Contract{ID: id, Kind: kind, Dimensions: dimensions,
			TokenizerID: "tokens_v1", Normalization: "none", Metric: "dot", Aggregation: "none"}
	}
	return indexSettings{
		Dense: dense.Config{Document: dense.Encoding{Callpoint: "dense_document", ConfigurationID: "11111111-1111-4111-8111-111111111111", PhysicalModel: "model"},
			Query:    dense.Encoding{Callpoint: "dense_query", ConfigurationID: "22222222-2222-4222-8222-222222222222", PhysicalModel: "model"},
			Contract: contract("dense_v1", representation.Dense, 2), Space: "dense_space", BatchSize: 8, ProbeTopK: 2},
		Sparse: sparse.Config{Document: sparse.Encoding{Callpoint: "sparse_document", ConfigurationID: "33333333-3333-4333-8333-333333333333", PhysicalModel: "model"},
			Query: sparse.Encoding{Callpoint: "sparse_query", ConfigurationID: "44444444-4444-4444-8444-444444444444", PhysicalModel: "model"},
			Contract: representation.Contract{ID: "sparse_v1", Kind: representation.Sparse, Dimensions: 100,
				TokenizerID: "tokens_v1", VocabularyID: "vocab_v1", Normalization: "none", Metric: "dot", Aggregation: "sum", MaxNonzero: 2},
			Space: "sparse_space", BatchSize: 8, ProbeTopK: 2},
		MultiVector: multivector.Config{Document: multivector.Encoding{Callpoint: "multi_document", ConfigurationID: "55555555-5555-4555-8555-555555555555", PhysicalModel: "model"},
			Query: multivector.Encoding{Callpoint: "multi_query", ConfigurationID: "66666666-6666-4666-8666-666666666666", PhysicalModel: "model"},
			Contract: representation.Contract{ID: "multi_v1", Kind: representation.TokenMatrix, Dimensions: 2,
				TokenizerID: "tokens_v1", Normalization: "none", Metric: "maxsim", Aggregation: "sum_maxsim", MaxTokens: 4},
			Space: "multi_space", BatchSize: 8, TokenTopK: 4, ProbeTopK: 2},
	}
}

func testEnv(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	indexFile := filepath.Join(dir, "index.json")
	policyFile := filepath.Join(dir, "policy.json")
	raw, err := json.Marshal(testSettings())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyFile, []byte(`{"version":"local-fast-low-v1","fast_low":{"max_batches":1,"max_subqueries":1,"top_k_per_lane":8,"max_evidence":4,"wall_time":"5s"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"BTW_SEARCH_MODE": "local-exact", "BTW_SEARCH_API_ADDR": freeAddr(t), "BTW_SEARCH_METRICS_ADDR": freeAddr(t),
		"BTW_SEARCH_SCOPE_KEY": strings.Repeat("k", 32), "BTW_SEARCH_TOOLS_SCOPE_KEY": strings.Repeat("t", 32), "BTW_RTW_URL": "http://127.0.0.1:1",
		"BTW_RTW_TOKEN": "test-rtw-token", "BTW_DC_URL": "http://127.0.0.1:1", "BTW_DC_TOKEN": "test-dc-token",
		"BTW_SEARCH_MODEL_URL": "http://127.0.0.1:1/v1", "BTW_SEARCH_MODEL_KEY": "test-model-key",
		"BTW_SEARCH_MODEL_NAME": "test-model", "BTW_ARTIFACT_DIR": filepath.Join(dir, "artifacts"),
		"BTW_SEARCH_INDEX_FILE": indexFile, "BTW_SEARCH_POLICY_FILE": policyFile,
		"BTW_SEARCH_MAX_QUOTE_RUNES": "1024", "BTW_SEARCH_HTTP_TIMEOUT": "5s",
		"BTW_OTLP_TRACES_URL": "http://127.0.0.1:1/v1/traces", "BTW_SERVICE_VERSION": strings.Repeat("a", 40),
		"BTW_ENVIRONMENT": "test", "BTW_INSTANCE_ID": "search-api-test",
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func envMap(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestSearchAPIConfigRejectsIncompleteAndUnsafeModes(t *testing.T) {
	values := testEnv(t)
	if _, err := loadConfig(envMap(values)); err != nil {
		t.Fatalf("complete explicit config rejected: %v", err)
	}
	for _, tc := range []struct{ key, value string }{
		{"BTW_SEARCH_MODE", ""}, {"BTW_SEARCH_MODE", "milvus"},
		{"BTW_SEARCH_SCOPE_KEY", "short"}, {"BTW_SEARCH_TOOLS_SCOPE_KEY", "short"}, {"BTW_SEARCH_API_ADDR", "0.0.0.0:8080"},
		{"BTW_SEARCH_MODEL_KEY", ""}, {"BTW_SEARCH_POLICY_FILE", ""},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			copy := make(map[string]string, len(values))
			for k, v := range values {
				copy[k] = v
			}
			copy[tc.key] = tc.value
			if _, err := loadConfig(envMap(copy)); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestSearchAPIRealSocketRejectsUnsignedRequestBeforeDependencies(t *testing.T) {
	values := testEnv(t)
	otlp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer otlp.Close()
	values["BTW_OTLP_TRACES_URL"] = otlp.URL + "/v1/traces"
	cfg, err := loadConfig(envMap(values))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, &logs) }()
	client := &http.Client{Timeout: time.Second}
	var live *http.Response
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		live, err = client.Get("http://" + cfg.APIAddr + "/livez")
		if err == nil {
			break
		}
		select {
		case failure := <-done:
			t.Fatalf("server failed to start: %v", failure)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("live endpoint unavailable: %v", err)
	}
	_ = live.Body.Close()
	if live.StatusCode != http.StatusNoContent {
		t.Fatalf("liveness = %d", live.StatusCode)
	}
	request, err := http.NewRequest(http.MethodPost, "http://"+cfg.APIAddr+"/v1/search/summary",
		strings.NewReader(`{"module_id":"module","query":"test","depth":"fast","intelligence":"low"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusForbidden || !bytes.Contains(body, []byte("SEARCH_SCOPE_DENIED")) {
		t.Fatalf("unsigned request = %d %s %v", response.StatusCode, body, err)
	}
	toolsRequest, err := http.NewRequest(http.MethodPost, "http://"+cfg.APIAddr+"/v1/search/tools/search",
		strings.NewReader(`{"module_id":"module","query":"test","depth":"fast","intelligence":"low","search_id":"search_test","limits":{"read_calls":1,"quote_runes":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	toolsRequest.Header.Set("Content-Type", "application/json")
	toolsResponse, err := client.Do(toolsRequest)
	if err != nil {
		t.Fatal(err)
	}
	toolsBody, err := io.ReadAll(toolsResponse.Body)
	_ = toolsResponse.Body.Close()
	if err != nil || toolsResponse.StatusCode != http.StatusForbidden ||
		!bytes.Contains(toolsBody, []byte("SEARCH_SCOPE_DENIED")) {
		t.Fatalf("unsigned Tools request = %d %s %v", toolsResponse.StatusCode, toolsBody, err)
	}
	metrics, err := client.Get("http://" + cfg.MetricsAddr + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(metrics.Body)
	_ = metrics.Body.Close()
	if err != nil || metrics.StatusCode != http.StatusOK || !bytes.Contains(data, []byte("sea_btw_operations_total")) {
		t.Fatalf("metrics = %d %v", metrics.StatusCode, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("shutdown timed out")
	}
	if !bytes.Contains(logs.Bytes(), []byte(`"event":"search.api.started"`)) {
		t.Fatal("structured start event missing")
	}
}
