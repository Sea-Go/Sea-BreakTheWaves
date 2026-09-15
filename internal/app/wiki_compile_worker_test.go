package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"go.opentelemetry.io/otel/trace"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

func wikiWorkerFixtureJob(t *testing.T) (jobs.Job, ridethewind.Compile, map[string]ridethewind.Revision) {
	t.Helper()
	first, second := "第一份资料。\n\n第一份资料的后续。", "第二份资料。\n\n第二份资料的后续。"
	guidance := "只据两份冻结资料写一页知识。"
	compile := ridethewind.Compile{CompileId: "compile-1", ModuleId: "module-1", PageId: "《维护知识》",
		BaseRevisionId: "wiki-base-1", SourceRevisionIds: []string{"source-r1", "source-r2"},
		Guidance: guidance, InputHash: strings.Repeat("a", 64), State: "BUILDING",
		Generation: 1, CancelVersion: 0}
	revisions := map[string]ridethewind.Revision{}
	for _, spec := range []struct{ id, title, body string }{
		{"source-r1", "资料一", first}, {"source-r2", "资料二", second},
	} {
		hash := artifacts.Hash([]byte(spec.body))
		revisions[spec.id] = ridethewind.Revision{RevisionId: spec.id, ModuleId: compile.ModuleId,
			Kind: "source", Title: spec.title, Content: spec.body, ContentHash: hash,
			ObjectKey: "sha256/" + hash}
	}
	ticket := wikiCompileTicket{SchemaVersion: wikiCompileTicketVersion,
		SourceEventID:        "evt_11111111-1111-4111-8111-111111111111",
		SourceEventJCSDigest: strings.Repeat("b", 64), CompileID: compile.CompileId,
		ModuleID: compile.ModuleId, PageID: compile.PageId, BaseRevisionID: compile.BaseRevisionId,
		SourceRevisionIDs: append([]string(nil), compile.SourceRevisionIds...),
		GuidanceSHA256:    artifacts.Hash([]byte(guidance)), CompileInputHash: compile.InputHash,
		Generation: "1", CancelVersion: "0"}
	input, err := json.Marshal(ticket)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := jobs.Submit{Producer: wikiCompileProducer, OperationID: "command:" + strings.Repeat("c", 64),
		RunRef: "wiki-compile/" + compile.CompileId, JobType: WikiCompileJobType,
		ResourceProfile: wikiCompileResource, Input: input,
		Deadline: now.Add(2 * time.Hour).Format(time.RFC3339Nano), MaxAttempts: 3}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{ID: "job-1", InputHash: artifacts.Hash(canonical), Request: request,
		State: "running", WorkerID: "wiki-worker", Attempt: 1, AttemptID: "attempt-1",
		LeaseEpoch: 1, CancelVersion: 0,
		LeaseExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano)}
	return job, compile, revisions
}

func rehashWikiFixtureJob(t *testing.T, job *jobs.Job) {
	t.Helper()
	raw, err := json.Marshal(job.Request)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	job.InputHash = artifacts.Hash(canonical)
}

type wikiWorkerJobsFixture struct {
	job                 jobs.Job
	owner               *wikiWorkerOwnerFixture
	gets, completes     int
	acks                int
	appliedAcks         int
	cancelOnGet         int
	cancelRequestOnGet  int
	terminalOnGet       int
	lostAck             bool
	rejectAck           bool
	autoExpireBeforeAck bool
	lostComplete        bool
}

func (f *wikiWorkerJobsFixture) ClaimJob(context.Context, jobs.Claim) (jobs.Job, error) {
	return f.job, nil
}
func (f *wikiWorkerJobsFixture) GetJob(_ context.Context, _ string) (jobs.Job, error) {
	f.gets++
	if f.cancelOnGet > 0 && f.gets == f.cancelOnGet {
		f.job.State, f.job.CancelVersion = "cancelled", f.job.CancelVersion+1
	}
	if f.cancelRequestOnGet > 0 && f.gets == f.cancelRequestOnGet {
		f.job.State, f.job.CancelVersion = "cancel_requested", f.job.CancelVersion+1
	}
	if f.terminalOnGet > 0 && f.gets == f.terminalOnGet {
		f.job.State, f.job.LeaseExpiresAt = "cancelled", ""
	}
	return f.job, nil
}
func (f *wikiWorkerJobsFixture) AcknowledgeCancellation(_ context.Context, id string, lease jobs.Lease) (jobs.Job, error) {
	f.acks++
	if id != f.job.ID || f.job.State != "cancel_requested" ||
		lease.WorkerID != f.job.WorkerID || lease.AttemptID != f.job.AttemptID ||
		lease.LeaseEpoch != f.job.LeaseEpoch || lease.CancelVersion != f.job.CancelVersion {
		return jobs.Job{}, errors.New("wrong DC cancellation claimant")
	}
	if f.autoExpireBeforeAck {
		// A concurrent DC Get/Claim can expire the lease before this endpoint
		// locks the Job. DC then returns the already-cancelled same lease 200.
		f.job.State, f.job.LeaseExpiresAt = "cancelled", ""
		return f.job, nil
	}
	if f.rejectAck {
		return jobs.Job{}, errors.New("fixture DC cancel ACK conflict")
	}
	f.appliedAcks++
	f.job.State, f.job.LeaseExpiresAt = "cancelled", ""
	if f.lostAck {
		return jobs.Job{}, errors.New("fixture lost DC cancel ACK HTTP response")
	}
	return f.job, nil
}
func (f *wikiWorkerJobsFixture) CompleteJob(_ context.Context, id string, req jobs.Complete) (jobs.CompletionReceipt, error) {
	if f.owner.compile.State != "ACCEPTED" || id != f.job.ID || req.Result.Ref == nil ||
		req.Result.State != "succeeded" || req.AttemptID != f.job.AttemptID ||
		req.LeaseEpoch != f.job.LeaseEpoch || req.CancelVersion != f.job.CancelVersion {
		return jobs.CompletionReceipt{}, errors.New("DC completion preceded RTW business acceptance")
	}
	f.completes++
	f.job.State, f.job.Result = "succeeded", &req.Result
	if f.lostComplete {
		f.lostComplete = false
		return jobs.CompletionReceipt{}, errors.New("fixture lost DC completion HTTP response")
	}
	return jobs.CompletionReceipt{JobID: id, AttemptID: req.AttemptID,
		LeaseEpoch: req.LeaseEpoch, CancelVersion: req.CancelVersion,
		TechnicalState: "succeeded", ResultHash: strings.Repeat("d", 64)}, nil
}

type wikiWorkerOwnerFixture struct {
	compile                ridethewind.Compile
	revisions              map[string]ridethewind.Revision
	objects                artifacts.Store
	claims, accepts, reads int
	lostFirstAccept        bool
	acceptedRequest        ridethewind.AcceptCompileReq
	supersedeOnRead        int
}

type wikiWorkerTrackedObjects struct {
	store artifacts.Store
	puts  int
}

func (s *wikiWorkerTrackedObjects) Put(ctx context.Context, body []byte) (corpus.Ref, error) {
	s.puts++
	return s.store.Put(ctx, body)
}
func (s *wikiWorkerTrackedObjects) Get(ctx context.Context, ref corpus.Ref) ([]byte, error) {
	return s.store.Get(ctx, ref)
}

func (f *wikiWorkerOwnerFixture) GetCompile(context.Context, string) (ridethewind.Compile, error) {
	f.reads++
	if f.supersedeOnRead > 0 && f.reads == f.supersedeOnRead {
		f.compile.State, f.compile.CancelVersion = "SUPERSEDED", f.compile.CancelVersion+1
	}
	return f.compile, nil
}
func (f *wikiWorkerOwnerFixture) GetRevision(_ context.Context, id string) (ridethewind.Revision, error) {
	return f.revisions[id], nil
}
func (f *wikiWorkerOwnerFixture) ClaimCompile(_ context.Context, req ridethewind.ClaimCompileReq) (ridethewind.Compile, error) {
	if f.compile.State != "BUILDING" || req.CompileId != f.compile.CompileId ||
		req.InputHash != f.compile.InputHash || req.Generation != f.compile.Generation ||
		req.CancelVersion != f.compile.CancelVersion || req.AttemptId == "" || req.LeaseEpoch < 1 {
		return ridethewind.Compile{}, errors.New("stale RTW compile claim")
	}
	f.claims++
	f.compile.AttemptId, f.compile.LeaseEpoch, f.compile.LeaseExpiresAt = req.AttemptId, req.LeaseEpoch, req.LeaseExpiresAt
	return f.compile, nil
}
func (f *wikiWorkerOwnerFixture) AcceptCompile(ctx context.Context, req ridethewind.AcceptCompileReq) (ridethewind.Compile, error) {
	f.accepts++
	if f.compile.State == "ACCEPTED" {
		if !reflect.DeepEqual(f.acceptedRequest, req) {
			return ridethewind.Compile{}, errors.New("same compile result differs")
		}
		return f.compile, nil
	}
	if f.compile.State != "BUILDING" || req.CompileId != f.compile.CompileId ||
		req.AttemptId != f.compile.AttemptId || req.LeaseEpoch != f.compile.LeaseEpoch ||
		req.CancelVersion != f.compile.CancelVersion || req.InputHash != f.compile.InputHash ||
		req.State != "READY" || len(req.SourceRefs) != 2 || req.ObjectKey != "sha256/"+req.ContentHash {
		return ridethewind.Compile{}, errors.New("RTW accept received wrong fixed result")
	}
	data, err := f.objects.Get(ctx, corpus.Ref{Key: req.ObjectKey, SHA256: req.ContentHash})
	if err != nil || len(data) == 0 || artifacts.Hash(data) != req.ContentHash {
		return ridethewind.Compile{}, errors.New("RTW cannot read BTW candidate bytes")
	}
	f.acceptedRequest = req
	f.compile.State, f.compile.RevisionId, f.compile.ResultHash = "ACCEPTED", "wiki-revision-1", strings.Repeat("e", 64)
	if f.lostFirstAccept {
		f.lostFirstAccept = false
		return ridethewind.Compile{}, errors.New("fixture lost RTW accept HTTP response")
	}
	return f.compile, nil
}

type wikiWorkerFixedModel struct {
	calls  int
	answer string
}

func (*wikiWorkerFixedModel) Info() model.Info { return model.Info{Name: "wiki-worker-fixed-model"} }
func (m *wikiWorkerFixedModel) GenerateContent(ctx context.Context, q *model.Request) (<-chan *model.Response, error) {
	m.calls++
	if !trace.SpanFromContext(ctx).SpanContext().IsValid() || q.MaxTokens == nil ||
		*q.MaxTokens != 1024 || q.Stream || len(q.Tools) != 0 {
		return nil, errors.New("model call escaped native fixed no-tool profile")
	}
	answer := m.answer
	if answer == "" {
		answer = `{"title":"已编制知识","markdown":"# 已编制知识\n仅用冻结来源。","source_refs":[{"revision_id":"source-r1","locator":"paragraph:1"},{"revision_id":"source-r2","locator":"paragraph:2"}]}`
	}
	responses := make(chan *model.Response, 1)
	finish := "stop"
	responses <- &model.Response{Done: true, Choices: []model.Choice{{
		Message: model.NewAssistantMessage(answer), FinishReason: &finish,
	}}}
	close(responses)
	return responses, nil
}

type wikiWorkerRunFixture struct {
	model         *wikiWorkerFixedModel
	observed      *telemetry.Bundle
	overrideProof *WikiCompileModelSessionProof
	afterRunProof *WikiCompileModelSessionProof
}
type wikiWorkerNativeRun struct {
	runtime       *btwruntime.Runtime
	sessions      *inmemory.SessionService
	proof         WikiCompileModelSessionProof
	afterRunProof *WikiCompileModelSessionProof
}

func (f wikiWorkerRunFixture) Open(_ context.Context, _ content.WikiCompileInput,
	session WikiCompileModelSession) (WikiCompileRun, error) {
	ag, err := content.NewWikiCompileGraphAgent(f.model)
	if err != nil {
		return nil, err
	}
	sessions := inmemory.NewSessionService()
	r, err := btwruntime.New("wiki-worker-native", ag, sessions, f.observed)
	if err != nil {
		_ = sessions.Close()
		return nil, err
	}
	proof := session.Proof()
	if f.overrideProof != nil {
		proof = *f.overrideProof
	}
	return &wikiWorkerNativeRun{runtime: r, sessions: sessions, proof: proof,
		afterRunProof: f.afterRunProof}, nil
}
func (r *wikiWorkerNativeRun) ModelSessionProof() WikiCompileModelSessionProof { return r.proof }
func (r *wikiWorkerNativeRun) Run(ctx context.Context, q btwruntime.Request, sink btwruntime.Sink) (btwruntime.Result, error) {
	result, err := r.runtime.Run(ctx, q, sink)
	if r.afterRunProof != nil {
		r.proof = *r.afterRunProof
	}
	return result, err
}
func (r *wikiWorkerNativeRun) Close() error {
	return errors.Join(r.runtime.Close(), r.sessions.Close())
}

func wikiWorkerFixtureSession(t *testing.T, account string, material byte) WikiCompileModelSession {
	t.Helper()
	token := "wh_access_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{material}, 32))
	session, err := NewWikiCompileModelSession(account, token)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestWikiCompileWorkerNativeAcceptedBeforeOptionalTechnicalACK(t *testing.T) {
	if os.Getenv("SEA_WIKI_WORKER_NATIVE_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWikiCompileWorkerNativeAcceptedBeforeOptionalTechnicalACK$", "-test.v")
		cmd.Env = append(os.Environ(), "SEA_WIKI_WORKER_NATIVE_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native Wiki worker fixture failed: %v\n%s", err, output)
		}
		return
	}
	var logs bytes.Buffer
	// The framework provider calls Shutdown at Bundle.Close; use the existing
	// test exporter that retains ended native spans for after-close inspection.
	exporter := &factSpanExporter{}
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-wiki-worker-test",
		Environment: "test", Version: "candidate", InstanceID: "native-fixture",
		Output: &logs, Level: slog.LevelInfo, TraceExporter: exporter, SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	defer observed.Close(context.Background())
	frozen := wikiWorkerFixtureSession(t, "11111111-1111-4111-8111-111111111111", 0x44)
	job, compile, revisions := wikiWorkerFixtureJob(t)
	objects, err := artifacts.NewLocal(t.TempDir()) // One shared fixture store, never an RTW production URL.
	if err != nil {
		t.Fatal(err)
	}
	owner := &wikiWorkerOwnerFixture{compile: compile, revisions: revisions, objects: objects}
	jobsClient := &wikiWorkerJobsFixture{job: job, owner: owner}
	modelFixture := &wikiWorkerFixedModel{}
	if _, err := NewWikiCompileWorker(WikiCompileWorkerConfig{WorkerID: job.WorkerID,
		LeaseSeconds: 20}, jobsClient, owner,
		wikiWorkerRunFixture{model: modelFixture, observed: observed}, objects, observed); !errors.Is(err, ErrWikiCompileModelIdentity) || jobsClient.completes != 0 || owner.claims != 0 {
		t.Fatalf("direct app assembly bypassed missing native session: %v", err)
	}
	worker, err := NewWikiCompileWorker(WikiCompileWorkerConfig{WorkerID: job.WorkerID,
		LeaseSeconds: 20, ModelSession: frozen}, jobsClient, owner,
		wikiWorkerRunFixture{model: modelFixture, observed: observed}, objects, observed)
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.ProcessClaim(context.Background(), job)
	if !errors.Is(err, ErrWikiCompileTechnicalPending) || result.Accepted.State != "ACCEPTED" ||
		result.TechnicalComplete || jobsClient.completes != 0 || owner.claims != 1 || owner.accepts != 1 ||
		modelFixture.calls != 1 || result.Candidate.ContentSHA256 != artifacts.Hash([]byte(result.Candidate.Markdown)) {
		t.Fatalf("unversioned DC ResultRef escaped partial RTW acceptance: %+v err=%v owner=%+v jobs=%+v",
			result, err, owner, jobsClient)
	}
	ref := jobs.ResultRef{URI: "fixture://accepted-wiki-revision", Hash: strings.Repeat("f", 64), MediaType: "application/json"}
	owner2 := &wikiWorkerOwnerFixture{compile: compile, revisions: revisions, objects: objects, lostFirstAccept: true}
	job2 := job
	job2.ID = "job-2"
	job2.AttemptID = "attempt-2"
	jobs2 := &wikiWorkerJobsFixture{job: job2, owner: owner2, lostComplete: true}
	model2 := &wikiWorkerFixedModel{}
	worker2, err := NewWikiCompileWorker(WikiCompileWorkerConfig{WorkerID: job2.WorkerID, LeaseSeconds: 20,
		ModelSession: frozen,
		CompletionRef: func(context.Context, ridethewind.Compile, content.WikiCompileCandidate) (jobs.ResultRef, error) {
			return ref, nil // Explicitly a test-only placeholder, not the RTW/DC wire contract.
		}}, jobs2, owner2, wikiWorkerRunFixture{model: model2, observed: observed}, objects, observed)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := worker2.ProcessClaim(context.Background(), job2)
	if err != nil || !completed.TechnicalComplete || owner2.accepts != 2 || jobs2.completes != 1 ||
		model2.calls != 1 || jobs2.job.Result == nil || *jobs2.job.Result.Ref != ref ||
		completed.TechnicalReceipt.ResultHash != "" {
		t.Fatalf("RTW lost reply/GetCompile replay or DC lost reply/GetJob recovery failed: %+v err=%v owner=%+v jobs=%+v",
			completed, err, owner2, jobs2)
	}
	// A DC cancellation after native Runner EOF must stop before object Put,
	// RTW Accept and DC technical ACK, even though the model returned text.
	job3 := job
	job3.ID, job3.AttemptID = "job-3", "attempt-3"
	tracked := &wikiWorkerTrackedObjects{store: objects}
	owner3 := &wikiWorkerOwnerFixture{compile: compile, revisions: revisions, objects: tracked}
	jobs3 := &wikiWorkerJobsFixture{job: job3, owner: owner3, cancelRequestOnGet: 3}
	model3 := &wikiWorkerFixedModel{}
	worker3, err := NewWikiCompileWorker(WikiCompileWorkerConfig{WorkerID: job3.WorkerID,
		LeaseSeconds: 20, ModelSession: frozen}, jobs3, owner3,
		wikiWorkerRunFixture{model: model3, observed: observed}, tracked, observed)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := worker3.ProcessClaim(context.Background(), job3); !errors.Is(err, ErrWikiCompileCancelTerminalConfirmed) ||
		result.Accepted.State != "" || tracked.puts != 0 || owner3.accepts != 0 || jobs3.completes != 0 ||
		jobs3.acks != 1 || jobs3.job.State != "cancelled" ||
		jobs3.appliedAcks != 1 ||
		model3.calls != 1 {
		t.Fatalf("DC cancellation after model escaped into Wiki business/technical writes: %+v %v jobs=%+v", result, err, jobs3)
	}
	// A model-generated locator outside RTW's frozen source never becomes an
	// object or a business/technical receipt, regardless of a valid job lease.
	job4 := job
	job4.ID, job4.AttemptID = "job-4", "attempt-4"
	badObjects := &wikiWorkerTrackedObjects{store: objects}
	owner4 := &wikiWorkerOwnerFixture{compile: compile, revisions: revisions, objects: badObjects}
	jobs4 := &wikiWorkerJobsFixture{job: job4, owner: owner4}
	model4 := &wikiWorkerFixedModel{answer: `{"title":"假知识","markdown":"body","source_refs":[{"revision_id":"source-r1","locator":"paragraph:99"}]}`}
	worker4, err := NewWikiCompileWorker(WikiCompileWorkerConfig{WorkerID: job4.WorkerID,
		LeaseSeconds: 20, ModelSession: frozen}, jobs4, owner4,
		wikiWorkerRunFixture{model: model4, observed: observed}, badObjects, observed)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := worker4.ProcessClaim(context.Background(), job4); !errors.Is(err, ErrWikiCompileRun) ||
		result.Accepted.State != "" || badObjects.puts != 0 || owner4.accepts != 0 || jobs4.completes != 0 {
		t.Fatalf("bad model ref escaped into Wiki object/RTW/DC: %+v %v", result, err)
	}
	// A newer same-page Compile can supersede the old RTW generation after
	// model text exists. The old claim must not Put or overwrite a human page.
	job5 := job
	job5.ID, job5.AttemptID = "job-5", "attempt-5"
	oldObjects := &wikiWorkerTrackedObjects{store: objects}
	owner5 := &wikiWorkerOwnerFixture{compile: compile, revisions: revisions,
		objects: oldObjects, supersedeOnRead: 2}
	jobs5 := &wikiWorkerJobsFixture{job: job5, owner: owner5}
	model5 := &wikiWorkerFixedModel{}
	worker5, err := NewWikiCompileWorker(WikiCompileWorkerConfig{WorkerID: job5.WorkerID,
		LeaseSeconds: 20, ModelSession: frozen}, jobs5, owner5,
		wikiWorkerRunFixture{model: model5, observed: observed}, oldObjects, observed)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := worker5.ProcessClaim(context.Background(), job5); !errors.Is(err, ErrWikiCompileFence) ||
		result.Accepted.State != "" || model5.calls != 1 || oldObjects.puts != 0 ||
		owner5.accepts != 0 || jobs5.acks != 0 || jobs5.completes != 0 {
		t.Fatalf("superseded Compile overwrote old Wiki generation: %+v %v", result, err)
	}
	// A session proof B is not interchangeable with startup's frozen account
	// A, even if both look like valid DC native access tokens. The RTW claim
	// can already exist; the exact assertion is zero model/objects/Accept/ACK.
	job6 := job
	job6.ID, job6.AttemptID = "job-6", "attempt-6"
	otherSession := wikiWorkerFixtureSession(t, "22222222-2222-4222-8222-222222222222", 0x55)
	otherProof := otherSession.Proof()
	mismatchedObjects := &wikiWorkerTrackedObjects{store: objects}
	owner6 := &wikiWorkerOwnerFixture{compile: compile, revisions: revisions, objects: mismatchedObjects}
	jobs6 := &wikiWorkerJobsFixture{job: job6, owner: owner6}
	model6 := &wikiWorkerFixedModel{}
	worker6, err := NewWikiCompileWorker(WikiCompileWorkerConfig{WorkerID: job6.WorkerID,
		LeaseSeconds: 20, ModelSession: frozen}, jobs6, owner6,
		wikiWorkerRunFixture{model: model6, observed: observed, overrideProof: &otherProof},
		mismatchedObjects, observed)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := worker6.ProcessClaim(context.Background(), job6); !errors.Is(err, ErrWikiCompileModelIdentity) ||
		result.Accepted.State != "" || owner6.claims != 1 || model6.calls != 0 ||
		mismatchedObjects.puts != 0 || owner6.accepts != 0 || jobs6.completes != 0 {
		t.Fatalf("DC native account B escaped startup's model session A: %+v %v", result, err)
	}
	job7 := job
	job7.ID, job7.AttemptID = "job-7", "attempt-7"
	lateObjects := &wikiWorkerTrackedObjects{store: objects}
	owner7 := &wikiWorkerOwnerFixture{compile: compile, revisions: revisions, objects: lateObjects}
	jobs7 := &wikiWorkerJobsFixture{job: job7, owner: owner7}
	model7 := &wikiWorkerFixedModel{}
	worker7, err := NewWikiCompileWorker(WikiCompileWorkerConfig{WorkerID: job7.WorkerID,
		LeaseSeconds: 20, ModelSession: frozen}, jobs7, owner7,
		wikiWorkerRunFixture{model: model7, observed: observed, afterRunProof: &otherProof},
		lateObjects, observed)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := worker7.ProcessClaim(context.Background(), job7); !errors.Is(err, ErrWikiCompileModelIdentity) ||
		result.Accepted.State != "" || owner7.claims != 1 || model7.calls != 1 ||
		lateObjects.puts != 0 || owner7.accepts != 0 || jobs7.completes != 0 {
		t.Fatalf("changed model proof after Runner returned escaped into object/RTW/DC: %+v %v", result, err)
	}
	badJob := job
	badJob.ID, badJob.InputHash = "job-malformed", strings.Repeat("0", 64)
	badOwner := &wikiWorkerOwnerFixture{compile: compile, revisions: revisions, objects: objects}
	badJobs := &wikiWorkerJobsFixture{job: badJob, owner: badOwner}
	badWorker, err := NewWikiCompileWorker(WikiCompileWorkerConfig{WorkerID: badJob.WorkerID,
		LeaseSeconds: 20, ModelSession: frozen}, badJobs, badOwner,
		wikiWorkerRunFixture{model: &wikiWorkerFixedModel{}, observed: observed}, objects, observed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badWorker.ProcessClaim(context.Background(), badJob); !errors.Is(err, ErrWikiCompileJobContract) ||
		badOwner.reads != 0 || badOwner.claims != 0 || badOwner.accepts != 0 || badJobs.completes != 0 {
		t.Fatalf("bad DC job escaped deterministic first-effect gate: %v", err)
	}
	if err := observed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	appTraces := map[trace.TraceID]bool{}
	for _, span := range exporter.Snapshot() {
		if span.Name() == "content.wiki_compile.process" {
			appTraces[span.SpanContext().TraceID()] = true
		}
	}
	native := false
	for _, span := range exporter.Snapshot() {
		if span.Name() == "workflow execute_graph wiki_compile_candidate" &&
			span.InstrumentationScope().Name == "trpc.agent.go" &&
			appTraces[span.SpanContext().TraceID()] {
			native = true
		}
	}
	if !native || !bytes.Contains(logs.Bytes(), []byte(`"event":"content.wiki_compile.process.finished"`)) ||
		!bytes.Contains(logs.Bytes(), []byte(`"outcome":"partial"`)) ||
		!bytes.Contains(logs.Bytes(), []byte(`"error_code":"DC_CANCELLED_CONFIRMED"`)) ||
		!bytes.Contains(logs.Bytes(), []byte(`"dc_cancel_terminal":true`)) ||
		!bytes.Contains(logs.Bytes(), []byte(`"outcome":"rejected"`)) ||
		!bytes.Contains(logs.Bytes(), []byte(`"error_code":"MODEL_SESSION_MISMATCH"`)) ||
		bytes.Contains(logs.Bytes(), []byte(frozen.NativeBearer())) {
		t.Fatalf("Wiki worker lacked native Graph/structured partial stage: native=%t logs=%s", native, logs.Bytes())
	}
}

func TestWikiCompileWorkerRejectsBadTicketAndSourceBeforeClaim(t *testing.T) {
	job, compile, revisions := wikiWorkerFixtureJob(t)
	if _, err := DecodeWikiCompileClaim(job, job.WorkerID, time.Now()); err != nil {
		t.Fatalf("provider-fixed job ticket rejected: %v", err)
	}
	for name, mutate := range map[string]func(*jobs.Job){
		"wrong-technical-input-hash": func(j *jobs.Job) { j.InputHash = strings.Repeat("0", 64) },
		"old-lease":                  func(j *jobs.Job) { j.LeaseExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano) },
		"wrong-job-type":             func(j *jobs.Job) { j.Request.JobType = "unregistered.wiki"; rehashWikiFixtureJob(t, j) },
		"duplicate-ticket-key": func(j *jobs.Job) {
			j.Request.Input = bytes.Replace(j.Request.Input, []byte(`"compile_id":"compile-1"`), []byte(`"compile_id":"compile-1","compile_id":"forged"`), 1)
		},
		"leading-zero-generation": func(j *jobs.Job) {
			j.Request.Input = bytes.Replace(j.Request.Input, []byte(`"generation":"1"`), []byte(`"generation":"01"`), 1)
			rehashWikiFixtureJob(t, j)
		},
		"cancelled-claim": func(j *jobs.Job) { j.State = "cancelled"; j.CancelVersion++ },
	} {
		t.Run(name, func(t *testing.T) {
			bad := job
			mutate(&bad)
			if _, err := DecodeWikiCompileClaim(bad, job.WorkerID, time.Now()); err == nil {
				t.Fatalf("bad DC Wiki ticket entered RTW/Graph: %+v", bad)
			}
		})
	}
	duplicate := bytes.Replace(job.Request.Input, []byte(`"compile_id":"compile-1"`),
		[]byte(`"compile_id":"compile-1","compile_id":"forged"`), 1)
	if _, err := decodeWikiTicket(duplicate); !errors.Is(err, ErrWikiCompileJobContract) {
		t.Fatalf("duplicate key escaped source ticket parser: %v", err)
	}
	// The source GetRevision is authoritative and must match its original bytes.
	badRevisions := map[string]ridethewind.Revision{}
	for id, revision := range revisions {
		badRevisions[id] = revision
	}
	r := badRevisions[compile.SourceRevisionIds[0]]
	r.Content = "different original bytes"
	badRevisions[r.RevisionId] = r
	owner := &wikiWorkerOwnerFixture{compile: compile, revisions: badRevisions}
	claim, err := DecodeWikiCompileClaim(job, job.WorkerID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	worker := &WikiCompileWorker{owner: owner}
	if _, err := worker.loadSources(context.Background(), compile, claim, job); !errors.Is(err, ErrWikiCompileSource) || owner.claims != 0 {
		t.Fatalf("wrong RTW original source hash reached ClaimCompile: %v", err)
	}
}

// The RTW/DC owner may hand a 0600, token-free JSONL from its real isolated
// Submit→Claim chain. This optional test checks the exact published ticket
// and entire DC Submit JCS hash without starting another PG/DC instance.
func TestWikiCompileWorkerDecodesProviderFrozenTicket(t *testing.T) {
	path := os.Getenv("SEA_WIKI_PROVIDER_TICKET_JSONL")
	if path == "" {
		t.Skip("requires the RTW/DC owner's frozen isolated Wiki ticket JSONL")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		JobID          string            `json:"job_id"`
		DCInputHash    string            `json:"dc_submit_input_hash"`
		RTWInputHash   string            `json:"rtw_compile_input_hash"`
		AttemptID      string            `json:"claim_attempt_id"`
		LeaseEpoch     int64             `json:"claim_lease_epoch"`
		CancelVersion  int64             `json:"claim_cancel_version"`
		LeaseExpiresAt string            `json:"claim_lease_expires_at"`
		Submit         jobs.Submit       `json:"submit"`
		Ticket         wikiCompileTicket `json:"ticket"`
	}
	if json.Unmarshal(bytes.TrimSpace(raw), &record) != nil || record.DCInputHash == record.RTWInputHash {
		t.Fatal("RTW/DC frozen Wiki ticket lost its two distinct hash domains")
	}
	job := jobs.Job{ID: record.JobID, InputHash: record.DCInputHash,
		Request: record.Submit, State: "running", WorkerID: "provider-fixture-worker",
		Attempt: 1, AttemptID: record.AttemptID, LeaseEpoch: record.LeaseEpoch,
		CancelVersion: record.CancelVersion, LeaseExpiresAt: record.LeaseExpiresAt}
	expiry, err := time.Parse(time.RFC3339Nano, record.LeaseExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	// Replay the immutable schema inside its original lease window. This does
	// not assert that the old isolated job is still active at today's clock.
	decoded, err := DecodeWikiCompileClaim(job, job.WorkerID, expiry.Add(-time.Second))
	if err != nil || !reflect.DeepEqual(decoded.Ticket, record.Ticket) ||
		decoded.Ticket.CompileInputHash != record.RTWInputHash ||
		decoded.Generation < 1 || decoded.Cancel != record.CancelVersion {
		t.Fatalf("provider's true DC Submit/Claim was not decoded exactly: err=%v", err)
	}
}
