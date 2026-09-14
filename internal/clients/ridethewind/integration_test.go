package ridethewind_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	sdk "github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	contentmigration "github.com/Sea-Go/Sea-BreakTheWaves/migrations/content"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
	jobType := "content-" + unique
	receipt, e := dc.SubmitJob(ctx, jobs.Submit{Producer: "btw-content-test", OperationID: input.OperationID, RunRef: "run-" + unique, JobType: jobType, ResourceProfile: "cpu", Input: rawInput, Deadline: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), MaxAttempts: 1})
	if e != nil {
		t.Fatal(e)
	}
	granted, e := dc.ClaimJob(ctx, jobs.Claim{WorkerID: "content-worker", JobType: jobType, ResourceProfile: "cpu", LeaseSeconds: 45})
	if e != nil || granted.ID != receipt.ID {
		t.Fatalf("DC granted job %+v %v", granted, e)
	}
	expires, e := time.Parse(time.RFC3339Nano, granted.LeaseExpiresAt)
	if e != nil {
		t.Fatal(e)
	}
	claim := sdk.ClaimBuildReq{BuildId: build.BuildId, Generation: build.Generation, ManifestHash: build.ManifestHash, CancelVersion: granted.CancelVersion, AttemptId: granted.AttemptID, LeaseEpoch: granted.LeaseEpoch, LeaseExpiresAt: granted.LeaseExpiresAt}
	if _, e = worker.ClaimBuild(ctx, claim); e != nil {
		t.Fatal(e)
	}
	prepareContent(t, ctx, worker, release, input, content.Fence{BuildID: build.BuildId, AttemptID: granted.AttemptID, LeaseEpoch: granted.LeaseEpoch, CancelVersion: granted.CancelVersion, ExpiresAt: expires})
	result := sdk.AcceptBuildReq{BuildId: build.BuildId, Generation: build.Generation, ManifestHash: build.ManifestHash, CancelVersion: build.CancelVersion, AttemptId: granted.AttemptID, LeaseEpoch: granted.LeaseEpoch, State: "FAILED", ErrorCode: "fixture_no_model"}
	accepted, e := worker.AcceptBuild(ctx, result)
	if e != nil || accepted.State != "FAILED" {
		t.Fatalf("build result %+v %v", accepted, e)
	}
	if _, e = worker.AcceptBuild(ctx, result); e != nil {
		t.Fatal("idempotent build result", e)
	}
	if _, e = dc.CompleteJob(ctx, granted.ID, jobs.Complete{Lease: jobs.Lease{WorkerID: granted.WorkerID, AttemptID: granted.AttemptID, LeaseEpoch: granted.LeaseEpoch, CancelVersion: granted.CancelVersion}, Result: jobs.Result{State: "failed", ErrorCode: "fixture_no_model"}}); e != nil {
		t.Fatal(e)
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

func prepareContent(t *testing.T, ctx context.Context, worker *sdk.Client, release sdk.Release, input content.BuildInput, fence content.Fence) {
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
	preparer, e := content.NewPreparer(worker, objects, store, chunker)
	if e != nil {
		t.Fatal(e)
	}
	prepared, e := preparer.Prepare(ctx, input, fence)
	if e != nil {
		t.Fatal(e)
	}
	if prepared.Build.State != "BUILDING" || prepared.Build.Chunks == nil || *prepared.Build.Chunks != prepared.Ref || len(prepared.Manifest.Chunks) == 0 || prepared.Manifest.InputManifestHash != release.ManifestHash {
		t.Fatalf("invalid prepared content %+v", prepared)
	}
	replay, e := preparer.Prepare(ctx, input, fence)
	if e != nil || replay.Ref != prepared.Ref {
		t.Fatalf("fixed input replay differs %+v %v", replay, e)
	}
	if _, e = objects.Get(ctx, prepared.Ref); e != nil {
		t.Fatal(e)
	}
	t.Logf("real DC attempt %s -> RTW claim -> %d chunks; fixed chunk hash %s stored in content PG, state BUILDING", fence.AttemptID, len(prepared.Manifest.Chunks), prepared.Ref.SHA256)
}
