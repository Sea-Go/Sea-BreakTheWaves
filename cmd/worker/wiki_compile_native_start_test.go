package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/google/uuid"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// This fixture is intentionally in-process: the Jobs/RTW contracts are owned
// by other repositories. The model is nevertheless the official HTTP-backed
// tRPC-Agent-Go OpenAI adapter used by the real Wiki Graph/Runner.
const wikiNativeStartProposal = `{"title":"外部知识","markdown":"# 外部知识\n仅依据冻结资料编制。","source_refs":[{"revision_id":"source-r1","locator":"paragraph:1"},{"revision_id":"source-r2","locator":"paragraph:1"}]}`

type wikiNativeStartLogs struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *wikiNativeStartLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *wikiNativeStartLogs) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.b.Bytes()...)
}

type wikiNativeStartOTLP struct {
	t        *testing.T
	mu       sync.Mutex
	requests []*collectortrace.ExportTraceServiceRequest
}

func (c *wikiNativeStartOTLP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/traces" {
		c.t.Error("unexpected local OTLP trace path")
		http.Error(w, "wrong OTLP path", http.StatusBadRequest)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		c.t.Error(err)
		http.Error(w, "bad OTLP body", http.StatusBadRequest)
		return
	}
	var request collectortrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(raw, &request); err != nil {
		c.t.Error(err)
		http.Error(w, "bad OTLP protobuf", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	c.requests = append(c.requests, &request)
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write([]byte{})
}

func (c *wikiNativeStartOTLP) GraphSpanTraceID() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, request := range c.requests {
		for _, resource := range request.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				if scope.Scope == nil || scope.Scope.Name != "trpc.agent.go" {
					continue
				}
				for _, span := range scope.Spans {
					if span.Name == "workflow execute_graph wiki_compile_candidate" {
						return hex.EncodeToString(span.TraceId), true
					}
				}
			}
		}
	}
	return "", false
}

type wikiNativeStartModel struct {
	t       *testing.T
	bearer  string
	jobKey  string
	calls   atomic.Int32
	mu      sync.Mutex
	traceID string
}

func (m *wikiNativeStartModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.calls.Add(1)
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" ||
		r.Header.Get("Authorization") != "Bearer "+m.bearer ||
		r.Header.Get("Authorization") == "Bearer "+m.jobKey ||
		r.Header.Get("X-Sea-Model-Callpoint") != wikiCompileModelCallpoint ||
		!strings.HasPrefix(r.Header.Get("Idempotency-Key"), "wiki-compile-") ||
		r.Header.Get("traceparent") == "" {
		m.t.Error("official Wiki CallPoint request lost native bearer, scope, stable key or traceparent")
		http.Error(w, "bad model contract", http.StatusBadRequest)
		return
	}
	traceparent := strings.Split(r.Header.Get("traceparent"), "-")
	if len(traceparent) != 4 || len(traceparent[1]) != 32 {
		m.t.Error("model traceparent lacks trace id")
		http.Error(w, "bad traceparent", http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.traceID = traceparent[1]
	m.mu.Unlock()
	body, err := io.ReadAll(io.LimitReader(r.Body, 128<<10))
	if err != nil {
		m.t.Error(err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var request struct {
		Model               string          `json:"model"`
		MaxCompletionTokens int             `json:"max_completion_tokens"`
		Stream              bool            `json:"stream"`
		Tools               json.RawMessage `json:"tools"`
		Messages            []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &request) != nil || request.Model != wikiCompileModelCallpoint ||
		request.MaxCompletionTokens != 1024 || request.Stream ||
		len(request.Messages) != 2 ||
		(len(request.Tools) != 0 && string(request.Tools) != "[]" && string(request.Tools) != "null") {
		m.t.Error("official SDK request did not use one non-streaming, no-tool, 1024-token Wiki call")
		http.Error(w, "bad model body", http.StatusBadRequest)
		return
	}
	var pack struct {
		Sources []struct {
			RevisionID string `json:"revision_id"`
			Paragraphs []struct {
				Locator string `json:"locator"`
			} `json:"paragraphs"`
		} `json:"fixed_source_pack"`
	}
	if json.Unmarshal([]byte(request.Messages[1].Content), &pack) != nil ||
		len(pack.Sources) != 2 || pack.Sources[0].RevisionID != "source-r1" ||
		pack.Sources[1].RevisionID != "source-r2" ||
		len(pack.Sources[0].Paragraphs) < 1 || len(pack.Sources[1].Paragraphs) < 1 ||
		pack.Sources[0].Paragraphs[0].Locator != "paragraph:1" ||
		pack.Sources[1].Paragraphs[0].Locator != "paragraph:1" {
		m.t.Error("Graph prompt did not contain RTW fixed source paragraphs")
		http.Error(w, "bad frozen source pack", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"wiki-native-start-local","object":"chat.completion","model":"knowledge-wiki-compiler","choices":[{"index":0,"message":{"role":"assistant","content":` +
		strconvQuote(wikiNativeStartProposal) + `},"finish_reason":"stop"}]}`))
}

func (m *wikiNativeStartModel) TraceID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.traceID
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

type wikiNativeStartJobs struct {
	job       jobs.Job
	owner     *wikiNativeStartOwner
	complete  jobs.Complete
	claims    int
	completes int
	cancel    context.CancelFunc
}

func (f *wikiNativeStartJobs) ClaimJob(_ context.Context, claim jobs.Claim) (jobs.Job, error) {
	f.claims++
	if claim.JobType != app.WikiCompileJobType || claim.ResourceProfile != "cpu" ||
		claim.WorkerID != f.job.WorkerID {
		return jobs.Job{}, errors.New("wrong DC Wiki technical claim")
	}
	if f.claims == 1 {
		return f.job, nil
	}
	f.cancel()
	return jobs.Job{}, datacenter.ErrNoWork
}
func (f *wikiNativeStartJobs) GetJob(_ context.Context, id string) (jobs.Job, error) {
	if id != f.job.ID {
		return jobs.Job{}, errors.New("wrong DC job read")
	}
	return f.job, nil
}
func (*wikiNativeStartJobs) AcknowledgeCancellation(context.Context, string, jobs.Lease) (jobs.Job, error) {
	return jobs.Job{}, errors.New("successful Wiki compile must not cancel")
}
func (f *wikiNativeStartJobs) CompleteJob(_ context.Context, id string,
	req jobs.Complete) (jobs.CompletionReceipt, error) {
	if id != f.job.ID || f.owner.compile.State != "ACCEPTED" ||
		req.Result.State != "succeeded" || req.Result.Ref == nil ||
		req.WorkerID != f.job.WorkerID || req.AttemptID != f.job.AttemptID ||
		req.LeaseEpoch != f.job.LeaseEpoch || req.CancelVersion != f.job.CancelVersion {
		return jobs.CompletionReceipt{}, errors.New("DC technical Complete preceded RTW acceptance or changed lease")
	}
	f.completes++
	f.complete = req
	f.job.State, f.job.Result = "succeeded", &req.Result
	raw, err := json.Marshal(req.Result)
	if err != nil {
		return jobs.CompletionReceipt{}, err
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		return jobs.CompletionReceipt{}, err
	}
	return jobs.CompletionReceipt{JobID: id, AttemptID: req.AttemptID,
		LeaseEpoch: req.LeaseEpoch, CancelVersion: req.CancelVersion,
		TechnicalState: "succeeded", ResultHash: artifacts.Hash(canonical)}, nil
}

type wikiNativeStartOwner struct {
	compile ridethewind.Compile
	sources map[string]ridethewind.Revision
	wiki    ridethewind.Revision
	objects artifacts.Store
	claim   ridethewind.ClaimCompileReq
	accept  ridethewind.AcceptCompileReq
	claims  int
	accepts int
}

func (f *wikiNativeStartOwner) GetCompile(_ context.Context, id string) (ridethewind.Compile, error) {
	if id != f.compile.CompileId {
		return ridethewind.Compile{}, errors.New("wrong RTW Compile read")
	}
	return f.compile, nil
}
func (f *wikiNativeStartOwner) GetRevision(_ context.Context, id string) (ridethewind.Revision, error) {
	if id == f.wiki.RevisionId && id != "" {
		return f.wiki, nil
	}
	revision, ok := f.sources[id]
	if !ok {
		return ridethewind.Revision{}, errors.New("wrong RTW source revision read")
	}
	return revision, nil
}
func (f *wikiNativeStartOwner) ClaimCompile(_ context.Context,
	req ridethewind.ClaimCompileReq) (ridethewind.Compile, error) {
	if f.compile.State != "BUILDING" || req.CompileId != f.compile.CompileId ||
		req.InputHash != f.compile.InputHash || req.Generation != f.compile.Generation ||
		req.CancelVersion != f.compile.CancelVersion || req.AttemptId == "" || req.LeaseEpoch < 1 {
		return ridethewind.Compile{}, errors.New("wrong RTW Compile lease claim")
	}
	f.claims++
	f.claim = req
	f.compile.AttemptId, f.compile.LeaseEpoch, f.compile.LeaseExpiresAt =
		req.AttemptId, req.LeaseEpoch, req.LeaseExpiresAt
	return f.compile, nil
}
func (f *wikiNativeStartOwner) AcceptCompile(ctx context.Context,
	req ridethewind.AcceptCompileReq) (ridethewind.Compile, error) {
	if f.compile.State != "BUILDING" || req.CompileId != f.compile.CompileId ||
		req.State != "READY" || req.AttemptId != f.compile.AttemptId ||
		req.LeaseEpoch != f.compile.LeaseEpoch || req.CancelVersion != f.compile.CancelVersion ||
		req.Generation != f.compile.Generation || req.InputHash != f.compile.InputHash ||
		req.ObjectKey != "sha256/"+req.ContentHash || len(req.SourceRefs) != 2 {
		return ridethewind.Compile{}, errors.New("RTW business Accept received changed candidate")
	}
	markdown, err := f.objects.Get(ctx, corpus.Ref{Key: req.ObjectKey, SHA256: req.ContentHash})
	if err != nil || artifacts.Hash(markdown) != req.ContentHash {
		return ridethewind.Compile{}, errors.New("RTW separate object reader could not read Markdown")
	}
	f.accepts++
	f.accept = req
	f.compile.State, f.compile.RevisionId, f.compile.ResultHash =
		"ACCEPTED", "wiki-revision-1", strings.Repeat("e", 64)
	f.wiki = ridethewind.Revision{RevisionId: f.compile.RevisionId, ModuleId: f.compile.ModuleId,
		EntityId: f.compile.PageId, Kind: "wiki", BaseRevisionId: f.compile.BaseRevisionId,
		Title: req.Title, MediaType: "text/markdown", ObjectKey: req.ObjectKey,
		ContentHash: req.ContentHash, Content: string(markdown),
		SourceRefs: append([]ridethewind.SourceRef(nil), req.SourceRefs...),
		CreatedBy:  "btw.compile/" + f.compile.CompileId}
	return f.compile, nil
}

func wikiNativeStartJob(t *testing.T, worker string) (jobs.Job, ridethewind.Compile,
	map[string]ridethewind.Revision) {
	t.Helper()
	guidance := "仅用两个冻结来源写一页外部知识。"
	compile := ridethewind.Compile{CompileId: "compile-native-start-1", ModuleId: "module-1",
		PageId: "《维护知识》", BaseRevisionId: "wiki-base-1",
		SourceRevisionIds: []string{"source-r1", "source-r2"}, Guidance: guidance,
		InputHash: strings.Repeat("a", 64), State: "BUILDING", Generation: 1}
	sources := make(map[string]ridethewind.Revision)
	for _, spec := range []struct{ id, title, body string }{
		{"source-r1", "甲来源", "甲来源第一段。\n\n甲来源第二段。"},
		{"source-r2", "乙来源", "乙来源第一段。\n\n乙来源第二段。"},
	} {
		hash := artifacts.Hash([]byte(spec.body))
		sources[spec.id] = ridethewind.Revision{RevisionId: spec.id,
			ModuleId: compile.ModuleId, Kind: "source", Title: spec.title,
			Content: spec.body, ContentHash: hash, ObjectKey: "sha256/" + hash}
	}
	ticket, err := json.Marshal(map[string]any{
		"schema_version":          "rtw.wiki.compile-ticket.v1",
		"source_event_id":         "evt_11111111-1111-4111-8111-111111111111",
		"source_event_jcs_sha256": strings.Repeat("b", 64),
		"compile_id":              compile.CompileId, "module_id": compile.ModuleId,
		"page_id": compile.PageId, "base_revision_id": compile.BaseRevisionId,
		"source_revision_ids": compile.SourceRevisionIds,
		"guidance_sha256":     artifacts.Hash([]byte(guidance)),
		"compile_input_hash":  compile.InputHash, "generation": "1", "cancel_version": "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := jobs.Submit{Producer: "ridethewind.knowledge",
		OperationID: "command:" + strings.Repeat("c", 64),
		RunRef:      "wiki-compile/" + compile.CompileId,
		JobType:     app.WikiCompileJobType, ResourceProfile: "cpu", Input: ticket,
		Deadline: now.Add(time.Hour).Format(time.RFC3339Nano), MaxAttempts: 3}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{ID: uuid.NewString(), Request: request, InputHash: artifacts.Hash(canonical),
		State: "running", WorkerID: worker, Attempt: 1, AttemptID: uuid.NewString(),
		LeaseEpoch: 1, LeaseExpiresAt: now.Add(4 * time.Minute).Format(time.RFC3339Nano)}
	return job, compile, sources
}

func wikiNativeStartMetricsAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestWikiCompileNativeStartOfficialGraphManifestAndClose(t *testing.T) {
	const child = "SEA_WIKI_NATIVE_START_HTTP_CHILD"
	if os.Getenv(child) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0],
			"-test.run=^TestWikiCompileNativeStartOfficialGraphManifestAndClose$", "-test.v")
		cmd.Env = append(os.Environ(), child+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated Wiki command/native Graph acceptance failed: %v\n%s", err, output)
		}
		return
	}
	modelBearer := "wh_access_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, 32))
	session, err := app.NewWikiCompileModelSession("11111111-1111-4111-8111-111111111111", modelBearer)
	if err != nil {
		t.Fatal(err)
	}
	model := &wikiNativeStartModel{t: t, bearer: modelBearer, jobKey: "jobs-token-local"}
	modelServer := httptest.NewServer(model)
	defer modelServer.Close()
	collector := &wikiNativeStartOTLP{t: t}
	collectorServer := httptest.NewServer(collector)
	defer collectorServer.Close()
	var sessionReads atomic.Int32
	provider := app.WikiCompileNativeSessionFunc(func(context.Context) (app.WikiCompileModelSession, error) {
		sessionReads.Add(1)
		return session, nil
	})
	factory, err := app.NewWikiCompileNativeRunFactory(app.WikiCompileNativeRunFactoryConfig{
		DCGatewayRoot: modelServer.URL, HTTPClient: modelServer.Client()}, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	values := wikiCompileConfigFixture(t)
	values["BTW_DC_TOKEN"] = model.jobKey
	values["BTW_DC_URL"] = modelServer.URL
	values["BTW_OTLP_TRACES_URL"] = collectorServer.URL + "/v1/traces"
	values["BTW_METRICS_ADDR"] = wikiNativeStartMetricsAddr(t)
	values["BTW_POLL_INTERVAL"] = "100ms"
	cfg, err := loadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	objects, err := artifacts.NewLocal(cfg.Wiki.SharedObjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	rtwReader, err := artifacts.NewLocal(cfg.Wiki.SharedObjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	job, compile, sources := wikiNativeStartJob(t, cfg.WorkerID)
	owner := &wikiNativeStartOwner{compile: compile, sources: sources, objects: rtwReader}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	jobClient := &wikiNativeStartJobs{job: job, owner: owner, cancel: cancel}
	deps := wikiCompileStartDeps{Jobs: jobClient, Owner: owner, Runs: factory,
		Objects: objects, ResultRefContractID: app.WikiCompileResultRefContractID,
		ObjectBackend: "shared-local", SharedObjectRoot: cfg.Wiki.SharedObjectRoot,
		RTWObjectRoot: cfg.Wiki.RTWObjectRoot}
	var output wikiNativeStartLogs
	if err := serveWikiCompileWithDeps(ctx, cfg, &output, deps); err != nil {
		t.Fatalf("Wiki command failed despite exact fixed native ports: %v\n%s", err, output.Bytes())
	}
	if model.calls.Load() != 1 || sessionReads.Load() != 1 ||
		owner.claims != 1 || owner.accepts != 1 || jobClient.completes != 1 ||
		jobClient.job.State != "succeeded" {
		t.Fatalf("Wiki startup did not reach exactly one Graph, RTW Accept and DC Complete: model=%d sessions=%d owner=%+v jobs=%+v",
			model.calls.Load(), sessionReads.Load(), owner, jobClient)
	}
	if err := factory.Close(); err != nil {
		t.Fatalf("native run factory retained a failed resource after process stop: %v", err)
	}
	if _, err := factory.Open(context.Background(), contentInputForClosedFactory(job, compile, sources), session); !errors.Is(err, app.ErrWikiCompileNativeFactory) || model.calls.Load() != 1 {
		t.Fatalf("closed factory reopened a Runner/model after process Bundle.Close: %v", err)
	}
	if err := factory.Close(); err != nil {
		t.Fatalf("idempotent factory Close changed result: %v", err)
	}
	markdown := []byte(owner.wiki.Content)
	if owner.wiki.RevisionId != owner.compile.RevisionId ||
		owner.accept.ContentHash != artifacts.Hash(markdown) ||
		owner.accept.ObjectKey != "sha256/"+owner.accept.ContentHash ||
		owner.wiki.ObjectKey != owner.accept.ObjectKey {
		t.Fatalf("accepted RTW Wiki did not retain exact Markdown bytes: %+v", owner.wiki)
	}
	fromRTW, err := rtwReader.Get(context.Background(), corpus.Ref{
		Key: owner.accept.ObjectKey, SHA256: owner.accept.ContentHash})
	if err != nil || !bytes.Equal(fromRTW, markdown) {
		t.Fatalf("independent RTW-side Local Store readback differs from Graph Markdown: %v", err)
	}
	ref := jobClient.complete.Result.Ref
	if ref == nil || ref.URI != "sha256:"+ref.Hash ||
		ref.MediaType != "application/vnd.sea.wiki-compile-result+json" {
		t.Fatalf("DC success omitted fixed technical ResultRef: %+v", ref)
	}
	manifestBytes, err := rtwReader.Get(context.Background(), corpus.Ref{
		Key: "sha256/" + ref.Hash, SHA256: ref.Hash})
	if err != nil || artifacts.Hash(manifestBytes) != ref.Hash {
		t.Fatalf("RTW-side reader cannot locate immutable DC manifest: %v", err)
	}
	canonical, err := jsoncanonicalizer.Transform(manifestBytes)
	if err != nil || !bytes.Equal(canonical, manifestBytes) {
		t.Fatalf("DC ResultRef did not point at exact JCS bytes: %v", err)
	}
	var manifest map[string]string
	if json.Unmarshal(manifestBytes, &manifest) != nil || len(manifest) != 13 ||
		manifest["job_id"] != job.ID || manifest["job_input_hash"] != job.InputHash ||
		manifest["attempt_id"] != job.AttemptID || manifest["candidate_object_key"] != owner.accept.ObjectKey ||
		manifest["candidate_content_sha256"] != owner.accept.ContentHash ||
		manifest["rtw_accept_result_hash"] != owner.compile.ResultHash ||
		manifest["wiki_revision_id"] != owner.compile.RevisionId ||
		bytes.Contains(manifestBytes, markdown) {
		t.Fatalf("13-key technical manifest does not bind job/RTW/candidate without embedding Markdown: %s", manifestBytes)
	}
	graphTrace, ok := collector.GraphSpanTraceID()
	if !ok || graphTrace == "" || graphTrace != model.TraceID() {
		t.Fatalf("Bundle shutdown did not flush native Graph Span into model's trace: graph=%s model=%s", graphTrace, model.TraceID())
	}
	var events []string
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'}) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("Wiki process emitted nonstructured log: %v", err)
		}
		if event, ok := record["event"].(string); ok {
			events = append(events, event)
		}
	}
	bindStarted := -1
	bindFinished := -1
	closeStarted := -1
	closeFinished := -1
	stop := -1
	for i, event := range events {
		switch event {
		case "content.wiki_compile.factory_bind.started":
			bindStarted = i
		case "content.wiki_compile.factory_bind.finished":
			bindFinished = i
		case "content.wiki_compile.factory_close.started":
			closeStarted = i
		case "content.wiki_compile.factory_close.finished":
			closeFinished = i
		case "content.wiki_compile.stopped":
			stop = i
		}
	}
	if bindStarted < 0 || bindFinished <= bindStarted ||
		closeStarted <= bindFinished || closeFinished <= closeStarted ||
		stop <= closeFinished ||
		strings.Contains(string(output.Bytes()), modelBearer) ||
		strings.Contains(string(output.Bytes()), model.jobKey) ||
		!reflect.DeepEqual([]ridethewind.SourceRef{
			{RevisionId: "source-r1", Locator: "paragraph:1"},
			{RevisionId: "source-r2", Locator: "paragraph:1"}}, owner.accept.SourceRefs) {
		t.Fatalf("Wiki process lost bind/close/stop ordering, redaction or fixed refs: %v", events)
	}
}

// Open-after-close only needs a valid frozen input to distinguish lifecycle
// refusal from a malformed source; it never calls the model endpoint.
func contentInputForClosedFactory(job jobs.Job, compile ridethewind.Compile,
	sources map[string]ridethewind.Revision) content.WikiCompileInput {
	input := content.WikiCompileInput{CompileID: compile.CompileId, ModuleID: compile.ModuleId,
		PageID: compile.PageId, BaseRevision: compile.BaseRevisionId,
		Generation: compile.Generation, InputHash: compile.InputHash,
		AttemptID: job.AttemptID, LeaseEpoch: job.LeaseEpoch, CancelVersion: job.CancelVersion,
		Guidance: compile.Guidance, SourceIDs: append([]string(nil), compile.SourceRevisionIds...)}
	for _, id := range compile.SourceRevisionIds {
		source := sources[id]
		input.Sources = append(input.Sources, content.WikiCompileSource{RevisionID: id,
			Kind: source.Kind, Title: source.Title, Content: source.Content, SHA256: source.ContentHash})
	}
	return input
}
