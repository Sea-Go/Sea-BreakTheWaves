package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/jackc/pgx/v5/pgxpool"
	tracecollector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

type lockedOutput struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *lockedOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(data)
}
func (w *lockedOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// The DSNs must identify disposable databases. The companion acceptance.sh
// provisions them; the ordinary package test never touches an external DB.
func TestActualWorkerProcessPreparesClaim(t *testing.T) {
	contentDSN := os.Getenv("BTW_WORKER_TEST_CONTENT_DSN")
	sessionDSN := os.Getenv("BTW_WORKER_TEST_SESSION_DSN")
	if contentDSN == "" || sessionDSN == "" {
		t.Skip("set isolated BTW_WORKER_TEST_*_DSN via cmd/worker/acceptance.sh")
	}
	artifactDir := t.TempDir()
	objects, err := artifacts.NewLocal(artifactDir)
	if err != nil {
		t.Fatal(err)
	}
	profiles := []ridethewind.RetrievalProfile{
		{Lane: "dense", Encoder: "dense@1", Tokenizer: "tokens@1", Space: "dense@1", Dimensions: 2},
		{Lane: "sparse", Encoder: "sparse@1", Tokenizer: "tokens@1", Space: "sparse@1", Dimensions: 10},
		{Lane: "multivector", Encoder: "multi@1", Tokenizer: "tokens@1", Space: "multi@1", Dimensions: 2, Mask: "valid", Aggregation: "maxsim"},
	}
	manifest := content.ReleaseManifest{SchemaVersion: 1, ModuleID: "module", ReleaseID: "release",
		SourceRevisionIDs: []string{"revision"}, WikiRevisionIDs: []string{}, ChunkingProfile: "paragraph-v1", RetrievalProfiles: profiles}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestRef, err := objects.Put(context.Background(), manifestRaw)
	if err != nil {
		t.Fatal(err)
	}
	text := "First paragraph.\r\n\r\nSecond paragraph."
	textRef, err := objects.Put(context.Background(), []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	input := content.BuildInput{BuildID: "build", ModuleID: "module", ReleaseID: "release", Generation: 1,
		InputHash: manifestRef.SHA256, OperationID: "prepare-operation", Revisions: []string{"revision"}}
	inputRaw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	leaseExpires := time.Now().UTC().Add(90 * time.Second).Truncate(time.Microsecond).Format(time.RFC3339Nano)
	job := jobs.Job{ID: "job", InputHash: artifacts.Hash(inputRaw), State: "running", AttemptID: "attempt", WorkerID: "local-worker-1",
		LeaseEpoch: 1, LeaseExpiresAt: leaseExpires, Request: jobs.Submit{Producer: "ridethewind", OperationID: input.OperationID,
			RunRef: "run", JobType: "content.prepare.v1", ResourceProfile: "cpu", Input: inputRaw}}
	var claims atomic.Int64
	completed := make(chan jobs.Result, 1)
	dc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer worker-dc-test" || r.Header.Get("Traceparent") == "" {
			t.Errorf("DC request lost authorization or propagated trace: %s %s", r.Method, r.URL.Path)
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/jobs/claim":
			if claims.Add(1) == 1 {
				_ = json.NewEncoder(w).Encode(job)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		case "POST /v1/jobs/job/complete":
			var receipt jobs.Complete
			if err := json.NewDecoder(r.Body).Decode(&receipt); err != nil {
				t.Errorf("decode completion: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			select {
			case completed <- receipt.Result:
			default:
				t.Error("duplicate DC completion")
			}
			_ = json.NewEncoder(w).Encode(jobs.CompletionReceipt{JobID: job.ID, AttemptID: job.AttemptID,
				LeaseEpoch: job.LeaseEpoch, CancelVersion: job.CancelVersion, ResultHash: artifacts.Hash([]byte("receipt")),
				TechnicalState: receipt.Result.State})
		default:
			t.Errorf("unexpected DC request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer dc.Close()
	build := ridethewind.Build{BuildId: input.BuildID, ModuleId: input.ModuleID, ReleaseId: input.ReleaseID,
		Generation: input.Generation, ManifestHash: input.InputHash, State: "BUILDING"}
	release := ridethewind.Release{ReleaseId: input.ReleaseID, ModuleId: input.ModuleID, Ordinal: 1,
		SourceRevisionIds: manifest.SourceRevisionIDs, WikiRevisionIds: manifest.WikiRevisionIDs,
		ChunkingProfile: manifest.ChunkingProfile, RetrievalProfiles: profiles, ManifestHash: manifestRef.SHA256, ManifestRef: manifestRef.Key}
	revision := ridethewind.Revision{RevisionId: "revision", ModuleId: input.ModuleID, EntityId: "book", Kind: "source",
		Title: "Book", MediaType: "text/plain", Content: text, ContentHash: textRef.SHA256, ObjectKey: textRef.Key}
	var buildMu sync.Mutex
	var buildClaims atomic.Int64
	var rtwTraceID string
	rtw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparent := r.Header.Get("Traceparent")
		if r.Header.Get("Authorization") != "Bearer secret-rtw" || traceparent == "" {
			t.Errorf("RTW request lost authorization or propagated trace: %s %s", r.Method, r.URL.Path)
		}
		parts := strings.Split(traceparent, "-")
		if len(parts) == 4 {
			buildMu.Lock()
			if rtwTraceID == "" {
				rtwTraceID = parts[1]
			} else if rtwTraceID != parts[1] {
				t.Error("one content attempt crossed multiple traces")
			}
			buildMu.Unlock()
		}
		var data any
		switch r.Method + " " + r.URL.Path {
		case "GET /internal/v1/knowledge/builds/build":
			buildMu.Lock()
			data = build
			buildMu.Unlock()
		case "POST /internal/v1/knowledge/builds/build/claim":
			var request ridethewind.ClaimBuildReq
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode RTW claim: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			buildClaims.Add(1)
			buildMu.Lock()
			if request.LeaseEpoch != 0 {
				t.Errorf("prepare worker sent DC epoch as RTW fence: %d", request.LeaseEpoch)
			}
			if build.AttemptId != request.AttemptId {
				build.LeaseEpoch++
			}
			build.AttemptId, build.CancelVersion, build.LeaseExpiresAt =
				request.AttemptId, request.CancelVersion, request.LeaseExpiresAt
			data = build
			buildMu.Unlock()
		case "GET /internal/v1/knowledge/releases/release":
			data = release
		case "GET /internal/v1/knowledge/revisions/revision":
			data = revision
		default:
			t.Errorf("unexpected RTW request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": data})
	}))
	defer rtw.Close()
	var traceMu sync.Mutex
	var spanNames []string
	var nativeTraceID string
	var traceBatches [][]byte
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" || r.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Errorf("unexpected OTLP request: %s %s", r.URL.Path, r.Header.Get("Content-Type"))
		}
		var body bytes.Buffer
		_, _ = body.ReadFrom(r.Body)
		var export tracecollector.ExportTraceServiceRequest
		if err := proto.Unmarshal(body.Bytes(), &export); err != nil {
			t.Errorf("decode OTLP: %v", err)
		} else {
			traceMu.Lock()
			traceBatches = append(traceBatches, append([]byte(nil), body.Bytes()...))
			for _, resource := range export.GetResourceSpans() {
				for _, scope := range resource.GetScopeSpans() {
					for _, span := range scope.GetSpans() {
						name := scope.GetScope().GetName() + ":" + span.GetName()
						spanNames = append(spanNames, name)
						if name == "trpc.agent.go:invoke_agent content_prepare" {
							nativeTraceID = hex.EncodeToString(span.GetTraceId())
						}
					}
				}
			}
			traceMu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	metricsAddr := listener.Addr().String()
	_ = listener.Close()
	version, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "worker")
	buildCmd := exec.Command("go", "build", "-mod=readonly", "-o", binary, ".")
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build worker: %v: %s", err, output)
	}
	env := validEnvironment()
	env["BTW_DC_URL"], env["BTW_RTW_URL"] = dc.URL, rtw.URL
	env["BTW_DC_TOKEN"] = "worker-dc-test"
	env["BTW_CONTENT_POSTGRES_DSN"], env["BTW_SESSION_POSTGRES_DSN"] = contentDSN, sessionDSN
	env["BTW_CONTENT_MIGRATE"], env["BTW_SESSION_INITIALIZE"] = "true", "true"
	env["BTW_ARTIFACT_DIR"], env["BTW_METRICS_ADDR"] = artifactDir, metricsAddr
	env["BTW_OTLP_TRACES_URL"], env["BTW_SERVICE_VERSION"] = collector.URL+"/v1/traces", strings.TrimSpace(string(version))
	var output lockedOutput
	cmd := exec.Command(binary)
	cmd.Stdout, cmd.Stderr = &output, &output
	for _, inherited := range os.Environ() {
		key, _, _ := strings.Cut(inherited, "=")
		if _, override := env[key]; !override {
			cmd.Env = append(cmd.Env, inherited)
		}
	}
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(output.String(), `"event":"content.worker.started"`) {
		if time.Now().After(deadline) {
			t.Fatalf("worker did not start; output=%s", output.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	var completion jobs.Result
	select {
	case completion = <-completed:
	case <-time.After(20 * time.Second):
		t.Fatalf("worker did not complete fixed claim; output=%s", output.String())
	}
	if completion.State != "succeeded" || completion.Ref == nil || completion.Ref.MediaType != "application/vnd.sea.chunk-manifest+json" {
		t.Fatalf("invalid technical completion: %+v", completion)
	}
	deadline = time.Now().Add(10 * time.Second)
	for !strings.Contains(output.String(), `"event":"content.worker.prepare.finished"`) {
		if time.Now().After(deadline) {
			t.Fatalf("worker did not finish Runner Graph: %s", output.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	resp, err := http.Get("http://" + metricsAddr + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	var metricBody bytes.Buffer
	_, _ = metricBody.ReadFrom(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(metricBody.String(), "sea_btw_log_write_failures_total") ||
		!strings.Contains(metricBody.String(), "trpc_agent_go_agent_") {
		t.Fatalf("metrics status=%d, expected BTW registry", resp.StatusCode)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("worker exit: %v; output=%s", err, output.String())
	}
	if claims.Load() == 0 || buildClaims.Load() != 1 {
		t.Fatalf("actual worker claims DC=%d RTW=%d", claims.Load(), buildClaims.Load())
	}
	if completion.Ref.URI != "sha256:"+completion.Ref.Hash {
		t.Fatal("technical completion did not reference a content-addressed artifact")
	}
	chunkRef := corpus.Ref{Key: "sha256/" + completion.Ref.Hash, SHA256: completion.Ref.Hash}
	chunkRaw, err := objects.Get(context.Background(), chunkRef)
	if err != nil {
		t.Fatal(err)
	}
	var chunks corpus.ChunkManifest
	if err := json.Unmarshal(chunkRaw, &chunks); err != nil || chunks.ReleaseID != input.ReleaseID ||
		chunks.InputManifestHash != input.InputHash || len(chunks.Chunks) == 0 {
		t.Fatalf("chunk artifact does not match fixed build: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), contentDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var state string
	var stored []byte
	if err := pool.QueryRow(context.Background(), "SELECT state,chunks FROM content_builds WHERE build_id=$1", input.BuildID).Scan(&state, &stored); err != nil {
		t.Fatal(err)
	}
	var storedRef corpus.Ref
	if err := json.Unmarshal(stored, &storedRef); err != nil || state != "BUILDING" || storedRef != chunkRef {
		t.Fatalf("content ledger incorrectly promoted or lost chunks: state=%s ref=%+v err=%v", state, storedRef, err)
	}
	buildMu.Lock()
	remoteState := build.State
	remoteTraceID := rtwTraceID
	buildMu.Unlock()
	if remoteState != "BUILDING" {
		t.Fatal("worker promoted RTW build without three retrieval lanes")
	}
	logs := strings.TrimSpace(output.String())
	if !strings.Contains(logs, `"event":"content.worker.stopped"`) ||
		!strings.Contains(logs, `"event":"runtime.run.finished"`) ||
		!strings.Contains(logs, `"event":"content.prepare.finished"`) {
		t.Fatalf("missing stop log: %s", logs)
	}
	if strings.Contains(logs, `"event":"telemetry.export.failed"`) {
		t.Fatalf("framework metric scrape or trace export failed: %s", logs)
	}
	for _, line := range strings.Split(logs, "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("non-JSON worker output: %v: %s", err, line)
		}
		for _, field := range []string{"timestamp", "level", "message", "service", "environment", "service_version", "instance_id", "component", "log_source", "event"} {
			if record[field] == nil {
				t.Fatalf("log field %s missing: %s", field, line)
			}
		}
	}
	for _, secret := range []string{env["BTW_DC_TOKEN"], contentDSN, sessionDSN} {
		if strings.Contains(logs, secret) {
			t.Fatal("worker log exposed a configured secret")
		}
	}
	traceMu.Lock()
	exported := append([]string(nil), spanNames...)
	frameworkTraceID := nativeTraceID
	traceMu.Unlock()
	if !strings.Contains(strings.Join(exported, ","), "trpc.agent.go:invoke_agent content_prepare") {
		t.Fatalf("framework GraphAgent span was not exported: %v", exported)
	}
	if remoteTraceID == "" || frameworkTraceID != remoteTraceID {
		t.Fatalf("RTW trace %q differs from framework GraphAgent trace %q", remoteTraceID, frameworkTraceID)
	}
	if evidence := os.Getenv("BTW_WORKER_EVIDENCE_DIR"); evidence != "" {
		if err := os.MkdirAll(evidence, 0700); err != nil {
			t.Fatal(err)
		}
		for name, raw := range map[string][]byte{
			"worker.jsonl": []byte(logs + "\n"), "metrics.prom": metricBody.Bytes(),
		} {
			if err := os.WriteFile(filepath.Join(evidence, name), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		traceMu.Lock()
		batches := append([][]byte(nil), traceBatches...)
		traceMu.Unlock()
		for i, raw := range batches {
			if err := os.WriteFile(filepath.Join(evidence, fmt.Sprintf("otlp-%d.pb", i)), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		summary, err := json.MarshalIndent(map[string]any{
			"code_commit": strings.TrimSpace(string(version)), "job_type": "content.prepare.v1",
			"dc_state": completion.State, "chunk_manifest_sha256": completion.Ref.Hash,
			"content_state": state, "rtw_state": remoteState, "chunk_count": len(chunks.Chunks),
			"native_trace_id": frameworkTraceID, "rtw_trace_id": remoteTraceID,
			"framework_span_names": exported, "otlp_batches": len(batches),
		}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(evidence, "summary.json"), append(summary, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
