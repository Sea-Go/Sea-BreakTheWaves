package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"go.opentelemetry.io/otel/trace"
)

type wikiExternalConsumerRuntime struct {
	SchemaVersion    string `json:"schema_version"`
	RTWBaseURL       string `json:"rtw_base_url"`
	RTWWorkerToken   string `json:"rtw_worker_token"`
	DCBaseURL        string `json:"dc_base_url"`
	DCJobsToken      string `json:"dc_jobs_token"`
	ObjectRoot       string `json:"object_root"`
	CompileID        string `json:"compile_id"`
	SourceRevisionID string `json:"source_revision_id"`
	JobID            string `json:"job_id"`
	JobInputHash     string `json:"job_input_hash"`
	ReleaseFile      string `json:"release_file"`
}

// Opt-in for the RTW-owned live holder. The model is a fixed native Graph
// fixture; only RTW/DC REST, Jobs, object readback and PG state are real here.
func TestWikiCompileWorkerExternalHTTPConsumer(t *testing.T) {
	runtimePath := os.Getenv("SEA_WIKI_EXTERNAL_RUNTIME_FILE")
	if runtimePath == "" {
		t.Skip("requires RTW-owned live external consumer holder")
	}
	releasePath := filepath.Join(filepath.Dir(runtimePath), "wiki-external-consumer-release")
	// The RTW holder owns this known task file. Release it even if the runtime
	// JSON is corrupt, so the real PG/process parent does not linger five min.
	defer func() {
		if err := os.WriteFile(releasePath, []byte("consumer returned\n"), 0600); err != nil {
			t.Errorf("release RTW-owned holder: %v", err)
		}
	}()
	info, err := os.Stat(runtimePath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("RTW holder runtime is not a private 0600 task file: %v", err)
	}
	raw, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	var runtime wikiExternalConsumerRuntime
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&runtime); err != nil {
		t.Fatal("RTW holder runtime contract differs from v1")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) ||
		runtime.SchemaVersion != "sea.wiki.external-consumer.v1" ||
		!strings.HasPrefix(runtime.RTWBaseURL, "http://127.0.0.1:") ||
		!strings.HasPrefix(runtime.DCBaseURL, "http://127.0.0.1:") ||
		runtime.RTWBaseURL == "" || runtime.DCBaseURL == "" ||
		runtime.RTWWorkerToken == "" || runtime.DCJobsToken == "" ||
		runtime.CompileID == "" || runtime.SourceRevisionID == "" ||
		runtime.JobID == "" || !artifacts.ValidHash(runtime.JobInputHash) ||
		!filepath.IsAbs(runtime.ObjectRoot) || runtime.ReleaseFile != releasePath {
		t.Fatal("RTW holder runtime lacks frozen service/job/object identity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	rtw, err := ridethewind.New(httpclient.Config{BaseURL: runtime.RTWBaseURL,
		Token: runtime.RTWWorkerToken})
	if err != nil {
		t.Fatal(err)
	}
	dc, err := datacenter.New(httpclient.Config{BaseURL: runtime.DCBaseURL,
		Token: runtime.DCJobsToken})
	if err != nil {
		t.Fatal(err)
	}
	objects, err := artifacts.NewLocal(runtime.ObjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	exporter := &factSpanExporter{}
	observed, err := telemetry.New(ctx, telemetry.Config{Service: "btw-wiki-live-consumer",
		Environment: "test", Version: "wiki-result-ref-v1", InstanceID: "holder-consumer",
		Output: &logs, Level: slog.LevelInfo, TraceExporter: exporter, SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := observed.Close(context.Background()); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(runtimePath), "btw-wiki-external-consumer.jsonl"),
			logs.Bytes(), 0600); err != nil {
			t.Error(err)
		}
	}()
	// This account/bearer only satisfies the application's frozen model seam.
	// The fixed model never calls the DataCenter Model Gateway or a Provider.
	frozen := wikiWorkerFixtureSession(t, "11111111-1111-4111-8111-111111111111", 0x44)
	modelAnswer, err := json.Marshal(struct {
		Title      string `json:"title"`
		Markdown   string `json:"markdown"`
		SourceRefs []struct {
			RevisionID string `json:"revision_id"`
			Locator    string `json:"locator"`
		} `json:"source_refs"`
	}{Title: "Maintained external fact", Markdown: "# Maintained external fact\none fixed fact",
		SourceRefs: []struct {
			RevisionID string `json:"revision_id"`
			Locator    string `json:"locator"`
		}{{RevisionID: runtime.SourceRevisionID, Locator: "paragraph:1"}}})
	if err != nil {
		t.Fatal(err)
	}
	modelFixture := &wikiWorkerFixedModel{answer: string(modelAnswer)}
	workerID := "wiki-external-consumer"
	worker, err := NewWikiCompileWorker(WikiCompileWorkerConfig{WorkerID: workerID,
		LeaseSeconds: 60, ModelSession: frozen, CompletionRef: BuildWikiCompileResultRef,
		ResultRefContractID: WikiCompileResultRefContractID}, dc, rtw,
		wikiWorkerRunFixture{model: modelFixture, observed: observed}, objects, observed)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := dc.GetJob(ctx, runtime.JobID)
	if err != nil || queued.State != "queued" || queued.InputHash != runtime.JobInputHash {
		t.Fatalf("external holder did not retain the frozen queued DC Job: %+v %v", queued, err)
	}
	claimed, err := dc.ClaimJob(ctx, jobs.Claim{WorkerID: workerID, JobType: WikiCompileJobType,
		ResourceProfile: wikiCompileResource, LeaseSeconds: 60})
	if err != nil || claimed.ID != runtime.JobID || claimed.InputHash != runtime.JobInputHash {
		t.Fatalf("external consumer did not claim RTW-owned Wiki Job: %+v %v", claimed, err)
	}
	result, err := worker.ProcessClaim(ctx, claimed)
	if err != nil || !result.TechnicalComplete || result.Accepted.State != "ACCEPTED" ||
		result.TechnicalReceipt.TechnicalState != "succeeded" ||
		modelFixture.calls != 1 || result.Candidate.ContentSHA256 == "" {
		t.Fatalf("real RTW/DC Wiki business/technical chain failed: accepted=%s complete=%t model_calls=%d err=%v",
			result.Accepted.State, result.TechnicalComplete, modelFixture.calls, err)
	}
	finalJob, err := dc.GetJob(ctx, runtime.JobID)
	finalCompile, compileErr := rtw.GetCompile(ctx, runtime.CompileID)
	wikiRevision, revisionErr := rtw.GetRevision(ctx, finalCompile.RevisionId)
	if err != nil || compileErr != nil || revisionErr != nil || finalJob.State != "succeeded" ||
		finalCompile.State != "ACCEPTED" || finalCompile.RevisionId != wikiRevision.RevisionId ||
		wikiRevision.ContentHash != result.Candidate.ContentSHA256 || wikiRevision.Withdrawn ||
		finalJob.Result == nil || finalJob.Result.Ref == nil ||
		finalJob.Result.Ref.URI != "sha256:"+finalJob.Result.Ref.Hash ||
		finalJob.Result.Ref.MediaType != wikiCompileResultMediaType {
		t.Fatalf("fresh RTW/DC read did not confirm exact accepted result bytes: job=%s compile=%s revision=%s",
			finalJob.State, finalCompile.State, wikiRevision.RevisionId)
	}
	independentReader, err := artifacts.NewLocal(runtime.ObjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := independentReader.Get(ctx, corpus.Ref{Key: "sha256/" + finalJob.Result.Ref.Hash,
		SHA256: finalJob.Result.Ref.Hash})
	var manifest map[string]json.RawMessage
	canonical, canonicalErr := jsoncanonicalizer.Transform(manifestBytes)
	if err != nil || json.Unmarshal(manifestBytes, &manifest) != nil || canonicalErr != nil ||
		!bytes.Equal(canonical, manifestBytes) || len(manifest) != 13 ||
		!bytes.Equal(manifest["job_input_hash"], []byte(`"`+runtime.JobInputHash+`"`)) ||
		!bytes.Equal(manifest["wiki_revision_id"], []byte(`"`+wikiRevision.RevisionId+`"`)) {
		t.Fatal("independent BTW reader cannot prove exact DC result manifest bytes")
	}
	// The RTW holder independently reads the manifest by the DC Ref hash and
	// verifies JCS/13 keys, current Wiki head and unchanged manual publication.
	if err := observed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	appTrace := make(map[trace.TraceID]bool)
	for _, span := range exporter.Snapshot() {
		if span.Name() == "content.wiki_compile.process" {
			appTrace[span.SpanContext().TraceID()] = true
		}
	}
	native := false
	for _, span := range exporter.Snapshot() {
		if span.Name() == "workflow execute_graph wiki_compile_candidate" &&
			span.InstrumentationScope().Name == "trpc.agent.go" &&
			appTrace[span.SpanContext().TraceID()] {
			native = true
		}
	}
	if !native {
		t.Fatal("actual RTW/DC Job lacked a same-trace native tRPC Graph span")
	}
	if !strings.HasPrefix(result.Candidate.Markdown, "#") {
		t.Fatal("native Graph candidate was not a Wiki page")
	}
}
