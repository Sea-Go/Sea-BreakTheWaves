package app_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	contentmigration "github.com/Sea-Go/Sea-BreakTheWaves/migrations/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/jobs"
	sdk "github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	frameworkruntime "github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type integrationTraceSink struct{}

func (integrationTraceSink) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (integrationTraceSink) Shutdown(context.Context) error                             { return nil }

func TestRealKnowledgeWorkerHTTP(t *testing.T) {
	endpoint := os.Getenv("SEA_TEST_RTW_URL")
	if endpoint == "" {
		t.Skip("run internal/runtime/acceptance.sh for isolated RTW HTTP")
	}
	encode := base64.RawURLEncoding.EncodeToString
	claims, _ := json.Marshal(map[string]any{"userId": "runtime-admin", "exp": time.Now().Add(time.Hour).Unix()})
	unsigned := encode([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + encode(claims)
	mac := hmac.New(sha256.New, []byte("runtime-fixture-secret"))
	mac.Write([]byte(unsigned))
	token := unsigned + "." + encode(mac.Sum(nil))
	admin, e := httpclient.New(httpclient.Config{BaseURL: endpoint, Token: token})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	create := func(path string, q any, out any) {
		t.Helper()
		raw, _, e := admin.Do(ctx, "POST", "/v1/knowledge"+path, nil, q, "")
		if e != nil {
			t.Fatal(e)
		}
		var env struct {
			Code int             `json:"code"`
			Data json.RawMessage `json:"data"`
		}
		if e = json.Unmarshal(raw, &env); e != nil || env.Code != 200 {
			t.Fatalf("admin response %s %v", raw, e)
		}
		if e = json.Unmarshal(env.Data, out); e != nil {
			t.Fatal(e)
		}
	}
	unique := fmt.Sprint(time.Now().UnixNano())
	var module struct {
		ID string `json:"id"`
	}
	create("/modules", map[string]any{"title": "Runtime integration", "idempotency_key": "module-" + unique}, &module)
	var revision sdk.Revision
	create("/modules/"+module.ID+"/sources", map[string]any{"title": "Fixed source", "content": "# Sea\nImmutable fixture.", "media_type": "text/markdown", "provenance": "fixture://runtime", "idempotency_key": "source"}, &revision)
	worker, e := sdk.New(httpclient.Config{BaseURL: endpoint, Token: "runtime-worker"})
	if e != nil {
		t.Fatal(e)
	}
	read, e := worker.GetRevision(ctx, revision.RevisionId)
	if e != nil || read.Content != "# Sea\nImmutable fixture." || read.ContentHash != revision.ContentHash {
		t.Fatalf("fixed revision %+v %v", read, e)
	}
	profiles := []sdk.RetrievalProfile{{Lane: "dense", Encoder: "dense@1", Tokenizer: "tokenizer@1", Space: "dense@1", Dimensions: 2}, {Lane: "sparse", Encoder: "sparse@1", Tokenizer: "tokenizer@1", Space: "sparse@1", Dimensions: 10}, {Lane: "multivector", Encoder: "multi@1", Tokenizer: "tokenizer@1", Space: "multi@1", Dimensions: 2, Mask: "valid", Aggregation: "maxsim"}}
	var release sdk.Release
	create("/modules/"+module.ID+"/releases", map[string]any{"source_revision_ids": []string{revision.RevisionId}, "wiki_revision_ids": []string{}, "chunking_profile": "paragraph-v1", "retrieval_profiles": profiles, "idempotency_key": "release"}, &release)
	if got, e := worker.GetRelease(ctx, release.ReleaseId); e != nil || got.ManifestHash != release.ManifestHash {
		t.Fatalf("frozen release %+v %v", got, e)
	}
	var build sdk.Build
	create("/releases/"+release.ReleaseId+"/index-builds", map[string]any{"idempotency_key": "build"}, &build)
	if _, e = worker.GetBuild(ctx, build.BuildId); e != nil {
		t.Fatal(e)
	}
	input := content.BuildInput{BuildID: build.BuildId, ModuleID: module.ID, ReleaseID: release.ReleaseId, Generation: build.Generation, InputHash: release.ManifestHash, OperationID: "prepare-" + unique, Revisions: append(append([]string(nil), release.SourceRevisionIds...), release.WikiRevisionIds...)}
	rawInput, e := json.Marshal(input)
	if e != nil {
		t.Fatal(e)
	}
	dc, e := datacenter.New(httpclient.Config{BaseURL: os.Getenv("SEA_TEST_DC_URL"), Token: "runtime-fixture"})
	if e != nil {
		t.Fatal(e)
	}
	jobType := app.PrepareJobType
	receipt, e := dc.SubmitJob(ctx, jobs.Submit{Producer: "btw-content-test", OperationID: input.OperationID, RunRef: "run-" + unique, JobType: jobType, ResourceProfile: "cpu", Input: rawInput, Deadline: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), MaxAttempts: 1})
	if e != nil {
		t.Fatal(e)
	}
	granted, e := dc.ClaimJob(ctx, jobs.Claim{WorkerID: "content-worker", JobType: jobType, ResourceProfile: "cpu", LeaseSeconds: 45})
	if e != nil || granted.ID != receipt.ID {
		t.Fatalf("DC granted job %+v %v", granted, e)
	}
	prepared := prepareContent(t, ctx, dc, worker, release, granted)
	if prepared.ChunkManifest.SHA256 == "" || prepared.State != "BUILDING" || prepared.BuildID != build.BuildId {
		t.Fatalf("invalid framework preparation receipt %+v", prepared)
	}
	completedJob, e := dc.GetJob(ctx, granted.ID)
	if e != nil || completedJob.State != "succeeded" || completedJob.Result == nil || completedJob.Result.Ref == nil ||
		completedJob.Result.Ref.Hash != prepared.ChunkManifest.SHA256 {
		t.Fatalf("DC technical chunk job was not completed from Graph receipt: %+v %v", completedJob, e)
	}
	assertPrepareTraceAcrossServices(t, os.Getenv("SEA_ACCEPTANCE_TEMP"))
	if actual, e := worker.GetBuild(ctx, build.BuildId); e != nil || actual.State != "BUILDING" {
		t.Fatalf("chunk job incorrectly promoted RTW build to READY: %+v %v", actual, e)
	}
	result := sdk.AcceptBuildReq{BuildId: build.BuildId, Generation: build.Generation, ManifestHash: build.ManifestHash, CancelVersion: build.CancelVersion, AttemptId: granted.AttemptID, LeaseEpoch: granted.LeaseEpoch, State: "FAILED", ErrorCode: "fixture_no_model"}
	accepted, e := worker.AcceptBuild(ctx, result)
	if e != nil || accepted.State != "FAILED" {
		t.Fatalf("build result %+v %v", accepted, e)
	}
	if _, e = worker.AcceptBuild(ctx, result); e != nil {
		t.Fatal("idempotent build result", e)
	}
	result.LeaseEpoch = 2
	if _, e = worker.AcceptBuild(ctx, result); e == nil {
		t.Fatal("accepted incorrect lease")
	}
	var compile sdk.Compile
	create("/modules/"+module.ID+"/compiles", map[string]any{"page_id": "page", "source_revision_ids": []string{revision.RevisionId}, "guidance": "summarize", "idempotency_key": "compile"}, &compile)
	if _, e = worker.GetCompile(ctx, compile.CompileId); e != nil {
		t.Fatal(e)
	}
	if _, e = worker.ClaimCompile(ctx, sdk.ClaimCompileReq{CompileId: compile.CompileId, Generation: compile.Generation, InputHash: compile.InputHash, CancelVersion: compile.CancelVersion, AttemptId: "compile-attempt", LeaseEpoch: 1, LeaseExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}); e != nil {
		t.Fatal(e)
	}
	got, e := worker.AcceptCompile(ctx, sdk.AcceptCompileReq{CompileId: compile.CompileId, Generation: compile.Generation, InputHash: compile.InputHash, CancelVersion: compile.CancelVersion, AttemptId: "compile-attempt", LeaseEpoch: 1, State: "FAILED", ErrorCode: "fixture_no_model"})
	if e != nil || got.State != "FAILED" {
		t.Fatalf("compile result %+v %v", got, e)
	}
	t.Logf("real RTW HTTP fixed revision=%s release=%s; build/compile fences and failure receipts passed", revision.RevisionId, release.ReleaseId)
}

func assertPrepareTraceAcrossServices(t *testing.T, directory string) {
	t.Helper()
	type observedRow struct {
		Event   string `json:"event"`
		TraceID string `json:"trace_id"`
	}
	readEvents := func(name string) []observedRow {
		t.Helper()
		raw, err := os.ReadFile(directory + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var rows []observedRow
		for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var row observedRow
			if err := json.Unmarshal(line, &row); err != nil {
				t.Fatalf("%s non-JSON log: %v", name, err)
			}
			rows = append(rows, row)
		}
		return rows
	}
	var traceID string
	for _, row := range readEvents("btw-worker.jsonl") {
		if row.Event == "content.worker.prepare.started" {
			traceID = row.TraceID
		}
	}
	if traceID == "" {
		t.Fatal("worker prepare did not emit a real Trace ID")
	}
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		seenRTW, seenDC := false, false
		for _, row := range readEvents("rtw.log") {
			seenRTW = seenRTW || row.TraceID == traceID && row.Event == "knowledge.build.claim.succeeded"
		}
		for _, row := range readEvents("dc.log") {
			seenDC = seenDC || row.TraceID == traceID && row.Event == "platform.job.complete.finished"
		}
		if seenRTW && seenDC {
			t.Logf("same actual Trace ID %s joins BTW Graph, RTW build claim and DC completion", traceID)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("framework request trace %s did not reach RTW/DC: RTW=%t DC=%t", traceID, seenRTW, seenDC)
		}
	}
}

func prepareContent(t *testing.T, ctx context.Context, dc *datacenter.Client, worker *sdk.Client, release sdk.Release, granted jobs.Job) content.PrepareGraphReceipt {
	t.Helper()
	cfg, e := pgxpool.ParseConfig(os.Getenv("SEA_TEST_CONTENT_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if _, e = pool.Exec(ctx, contentmigration.SQL); e != nil {
		t.Fatal(e)
	}
	objects, e := artifacts.NewLocal(os.Getenv("SEA_TEST_OBJECT_DIRECTORY"))
	if e != nil {
		t.Fatal(e)
	}
	store := content.NewStore(pool)
	chunker, e := content.NewChunker(content.ChunkConfig{ID: release.ChunkingProfile, Size: 64, Overlap: 8})
	if e != nil {
		t.Fatal(e)
	}
	output, e := os.Create(os.Getenv("SEA_ACCEPTANCE_TEMP") + "/btw-worker.jsonl")
	if e != nil {
		t.Fatal(e)
	}
	defer output.Close()
	observed, e := telemetry.New(ctx, telemetry.Config{Service: "btw-knowledge-integration", Environment: "test", Version: "test-revision",
		InstanceID: "worker-one", Output: output, Level: slog.LevelInfo, TraceExporter: integrationTraceSink{}, SampleRatio: 1})
	if e != nil {
		t.Fatal(e)
	}
	defer observed.Close(context.Background())
	if e = observed.InstallGlobals(); e != nil {
		t.Fatal(e)
	}
	preparer, e := content.NewPreparer(worker, objects, store, chunker, observed)
	if e != nil {
		t.Fatal(e)
	}
	agent, e := content.NewPrepareGraphAgent(preparer)
	if e != nil {
		t.Fatal(e)
	}
	sessionDSN := os.Getenv("SEA_RUNTIME_TEST_DSN")
	sessionPool, e := pgxpool.New(ctx, sessionDSN)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = sessionPool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS content_agent"); e != nil {
		t.Fatal(e)
	}
	sessionPool.Close()
	runner, e := frameworkruntime.OpenPostgres("content-prepare", agent, frameworkruntime.PostgresConfig{
		DSN: sessionDSN, Schema: "content_agent", TablePrefix: "sea_", Initialize: true}, observed)
	if e != nil {
		t.Fatal(e)
	}
	defer runner.Close()
	prepareWorker, e := app.NewPrepareWorker(app.PrepareWorkerConfig{WorkerID: granted.WorkerID, ResourceProfile: "cpu", LeaseSeconds: 45},
		dc, worker, runner, store, objects, observed)
	if e != nil {
		t.Fatal(e)
	}
	prepared, e := prepareWorker.ProcessClaim(ctx, granted)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = objects.Get(ctx, prepared.ChunkManifest); e != nil {
		t.Fatal(e)
	}
	t.Logf("real DC attempt %s -> RTW claim -> GraphAgent -> chunk artifact %s, count=%d, state BUILDING", granted.AttemptID, prepared.ChunkManifest.SHA256, prepared.ChunkCount)
	return prepared
}
