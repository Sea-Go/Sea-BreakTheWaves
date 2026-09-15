package search

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	tracecollector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	otlpEvidenceEnv = "SEA_BTW_OTLP_TRACE_EVIDENCE_DIR"
	otlpChildEnv    = "SEA_BTW_OTLP_TRACE_CHILD"
)

type otlpTraceCapture struct {
	mu      sync.Mutex
	batches []*tracecollector.ExportTraceServiceRequest
}

func (c *otlpTraceCapture) append(batch *tracecollector.ExportTraceServiceRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batches = append(c.batches, proto.Clone(batch).(*tracecollector.ExportTraceServiceRequest))
}

func (c *otlpTraceCapture) snapshot() []*tracecollector.ExportTraceServiceRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]*tracecollector.ExportTraceServiceRequest, len(c.batches))
	for i, batch := range c.batches {
		result[i] = proto.Clone(batch).(*tracecollector.ExportTraceServiceRequest)
	}
	return result
}

func TestSignedSearchExportsQueryableNativeOTLP(t *testing.T) {
	if os.Getenv(otlpChildEnv) != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, "-test.run=^TestSignedSearchExportsQueryableNativeOTLP$", "-test.v")
		command.Env = append(os.Environ(), otlpChildEnv+"=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("signed search OTLP subprocess: %v\n%s", err, output)
		}
		if testing.Verbose() {
			t.Log(strings.TrimSpace(string(output)))
		}
		return
	}

	capture := &otlpTraceCapture{}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/traces" ||
			!strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-protobuf") {
			http.Error(w, "unsupported local OTLP fixture request", http.StatusBadRequest)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20+1))
		if err != nil || len(raw) > 8<<20 {
			http.Error(w, "invalid local OTLP fixture body", http.StatusBadRequest)
			return
		}
		var batch tracecollector.ExportTraceServiceRequest
		if err := proto.Unmarshal(raw, &batch); err != nil || len(batch.ResourceSpans) == 0 {
			http.Error(w, "invalid local OTLP fixture protobuf", http.StatusBadRequest)
			return
		}
		capture.append(&batch)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	var logs bytes.Buffer
	bundle, err := telemetry.New(context.Background(), telemetry.Config{
		Service: "sea-btw-search-api", Environment: "test", Version: strings.Repeat("b", 40),
		InstanceID: "ws09f-local-fixture", Output: &logs, Level: slog.LevelInfo,
		SampleRatio: 1, OTLPEndpoint: receiver.URL + "/v1/traces",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Bool
	model := &fixtureModel{accepted: &accepted}
	history := &acceptedHistory{}
	boundary, err := searchdomain.NewRootSessionBoundary(fixedDelivery(t, &accepted, fixedSnapshot()), model, history, bundle)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte("o"), 32)
	resolver, err := NewSignedScopeResolver(key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_789_344_000, 0)
	resolver.now = func() time.Time { return now }
	handler, err := NewHandler(resolver, boundary, bundle)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)

	requestBody := PublicRequest{ModuleID: "module-1", Query: "why", Depth: searchdomain.Fast, Intelligence: searchdomain.Low}
	requestRaw, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatal(err)
	}
	requestHash := sha256.Sum256(requestRaw)
	payload := signedScope{
		Audience:  searchScopeAudience,
		Subject:   btwruntime.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "123"},
		SessionID: "conversation-ws09f", SearchID: "search_ws09f_native_trace", AnswerID: "answer_ws09f_native_trace",
		Snapshot: fixedSnapshot(), RequestHash: hex.EncodeToString(requestHash[:]),
		IssuedAtUnix: now.Unix(), ExpiresAtUnix: now.Add(120 * time.Second).Unix(),
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+Route, bytes.NewReader(requestRaw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(searchScopeHeader, signedPayload(t, key, payload))
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseRaw, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("signed search status=%d body=%s err=%v", response.StatusCode, responseRaw, readErr)
	}
	server.Close()
	if err := boundary.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	batches := capture.snapshot()
	traceID, names := assertQueryableNativeOTLP(t, batches, payload.SearchID)
	assertSearchTerminalLog(t, logs.Bytes(), traceID, payload.SearchID, payload.AnswerID)
	if directory := strings.TrimSpace(os.Getenv(otlpEvidenceEnv)); directory != "" {
		writeOTLPEvidence(t, directory, batches, logs.Bytes(), traceID, payload.SearchID, names)
	}
}

func assertQueryableNativeOTLP(t *testing.T, batches []*tracecollector.ExportTraceServiceRequest, searchID string) (string, []string) {
	t.Helper()
	if len(batches) == 0 {
		t.Fatal("local OTLP receiver fixture received no protobuf exports")
	}
	type capturedSpan struct {
		span    *tracepb.Span
		scope   string
		service string
	}
	spans := map[string]capturedSpan{}
	var traceID string
	serviceSeen, scopeSeen := false, false
	for _, batch := range batches {
		for _, resource := range batch.ResourceSpans {
			service := otlpStringAttribute(resource.GetResource().GetAttributes(), "service.name")
			if service == "sea-btw-search-api" {
				serviceSeen = true
			}
			for _, scoped := range resource.ScopeSpans {
				if scoped.Scope.GetName() == "trpc.agent.go" {
					scopeSeen = true
				}
				for _, span := range scoped.Spans {
					id := hex.EncodeToString(span.TraceId)
					if span.Name == "search.http.summary" && otlpStringAttribute(span.Attributes, "search_id") == searchID {
						traceID = id
					}
					spans[span.Name+"\x00"+id] = capturedSpan{span: span, scope: scoped.Scope.GetName(), service: service}
				}
			}
		}
	}
	if !serviceSeen || !scopeSeen || len(traceID) != 32 {
		t.Fatalf("OTLP identity incomplete: service=%t scope=%t trace_id=%q", serviceSeen, scopeSeen, traceID)
	}
	required := []string{
		"search.http.summary",
		"runtime.run",
		"invoke_agent search_summary_root",
		"workflow execute_graph search_summary_root",
		"workflow execute_function_node search_and_accept",
		"workflow execute_function_node search_summary",
		"workflow execute_function_node validate_answer",
	}
	for _, name := range required {
		if spans[name+"\x00"+traceID].span == nil {
			t.Fatalf("same-trace OTLP span %q missing", name)
		}
	}
	for _, name := range required {
		item := spans[name+"\x00"+traceID]
		if item.service != "sea-btw-search-api" {
			t.Fatalf("same-trace span %q service=%q", name, item.service)
		}
		if strings.HasPrefix(name, "invoke_agent ") || strings.HasPrefix(name, "workflow ") {
			if item.scope != "trpc.agent.go" {
				t.Fatalf("framework span %q scope=%q", name, item.scope)
			}
		}
	}
	app := spans["search.http.summary\x00"+traceID].span
	runtime := spans["runtime.run\x00"+traceID].span
	root := spans["invoke_agent search_summary_root\x00"+traceID].span
	graph := spans["workflow execute_graph search_summary_root\x00"+traceID].span
	if !bytes.Equal(runtime.ParentSpanId, app.SpanId) || !bytes.Equal(root.ParentSpanId, runtime.SpanId) ||
		!bytes.Equal(graph.ParentSpanId, root.SpanId) {
		t.Fatal("application→Runtime→Agent→Graph OTLP ancestry is broken")
	}
	for _, name := range required[4:] {
		if !bytes.Equal(spans[name+"\x00"+traceID].span.ParentSpanId, graph.SpanId) {
			t.Fatalf("Function span %q is not a direct Graph child", name)
		}
	}
	return traceID, required
}

func otlpStringAttribute(attributes []*commonpb.KeyValue, key string) string {
	for _, attribute := range attributes {
		if attribute.GetKey() == key {
			return attribute.GetValue().GetStringValue()
		}
	}
	return ""
}

func assertSearchTerminalLog(t *testing.T, raw []byte, traceID, searchID, answerID string) {
	t.Helper()
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
		var record map[string]any
		if json.Unmarshal(line, &record) == nil && record["event"] == "search.http.summary.finished" &&
			record["outcome"] == "succeeded" && record["trace_id"] == traceID &&
			record["search_id"] == searchID && record["operation_id"] == answerID &&
			record["service"] == "sea-btw-search-api" && record["component"] == "search" &&
			record["log_source"] == "application" {
			return
		}
	}
	t.Fatal("structured terminal log is not bound to signed SearchID and native trace")
}

func writeOTLPEvidence(t *testing.T, directory string, batches []*tracecollector.ExportTraceServiceRequest,
	logs []byte, traceID, searchID string, names []string) {
	t.Helper()
	if !filepath.IsAbs(directory) {
		t.Fatal("OTLP evidence directory must be absolute")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	sourceRaw, err := exec.Command("git", "rev-parse", "HEAD").Output()
	sourceSHA := strings.TrimSpace(string(sourceRaw))
	if err != nil || len(sourceSHA) != 40 {
		t.Fatal("BTW source SHA unavailable for OTLP evidence")
	}
	var resources []*tracepb.ResourceSpans
	for number, batch := range batches {
		resources = append(resources, batch.ResourceSpans...)
		raw, err := proto.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "otlp-export-"+strconv.Itoa(number)+".pb")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	traceRaw, err := protojson.MarshalOptions{UseProtoNames: false}.Marshal(&tracepb.TracesData{ResourceSpans: resources})
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(traceRaw, &document); err != nil {
		t.Fatal(err)
	}
	tempo, err := json.Marshal(map[string]json.RawMessage{"batches": document["resourceSpans"]})
	if err != nil {
		t.Fatal(err)
	}
	report, err := json.MarshalIndent(map[string]any{
		"schema_version":        "sea.ws09f.btw-otlp-evidence.v1",
		"source_sha":            sourceSHA,
		"evidence_level":        "LOCAL_FIXTURE",
		"collector_runtime":     "not_run",
		"trace_backend_runtime": "not_run",
		"otlp_receiver":         "in_process_http_protobuf_fixture",
		"trace_id":              traceID, "search_id": searchID,
		"service_name": "sea-btw-search-api", "instrumentation_scope": "trpc.agent.go",
		"required_spans":       names,
		"tempo_trace_file":     "tempo-trace.json",
		"application_log_file": "application.jsonl",
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"tempo-trace.json":  tempo,
		"application.jsonl": logs,
		"report.json":       append(report, '\n'),
	} {
		if err := os.WriteFile(filepath.Join(directory, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
