package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type traceSink struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

func (s *traceSink) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spans = append(s.spans, spans...)
	return nil
}
func (*traceSink) Shutdown(context.Context) error { return nil }
func (s *traceSink) snapshot() []sdktrace.ReadOnlySpan {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sdktrace.ReadOnlySpan(nil), s.spans...)
}

type acceptedHistory struct {
	mu    sync.Mutex
	turns []searchdomain.AcceptedRootTurn
}

func (h *acceptedHistory) Commit(_ context.Context, turn searchdomain.AcceptedRootTurn) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.turns = append(h.turns, turn)
	return nil
}
func (h *acceptedHistory) List(_ context.Context, subject btwruntime.SubjectRef, session string) ([]searchdomain.AcceptedRootTurn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var turns []searchdomain.AcceptedRootTurn
	for _, turn := range h.turns {
		if turn.Request.Subject == subject && turn.Request.SessionID == session {
			turns = append(turns, turn)
		}
	}
	return turns, nil
}

type fixtureModel struct {
	accepted *atomic.Bool
	calls    atomic.Int32
	tools    atomic.Bool
	invalid  atomic.Bool
}

func (*fixtureModel) Info() model.Info { return model.Info{Name: "search-http-fixture"} }
func (m *fixtureModel) GenerateContent(_ context.Context, request *model.Request) (<-chan *model.Response, error) {
	if !m.accepted.Load() {
		return nil, errors.New("model called before citation acceptance")
	}
	m.calls.Add(1)
	if len(request.Tools) != 0 {
		m.tools.Store(true)
	}
	var pack struct {
		Fixed searchdomain.EvidencePack `json:"fixed_evidence_pack"`
	}
	for _, msg := range request.Messages {
		if msg.Role == model.RoleUser {
			_ = json.Unmarshal([]byte(msg.Content), &pack)
		}
	}
	if len(pack.Fixed.Evidence) != 1 {
		return nil, errors.New("summary did not receive fixed evidence")
	}
	answer := fmt.Sprintf(`{"answer":"fixture answer","citations":[%q]}`, pack.Fixed.Evidence[0].ID)
	if m.invalid.Load() {
		answer = `{"answer":"unaccepted model text","citations":["invented"]}`
	}
	finish := "stop"
	stream := make(chan *model.Response, 1)
	stream <- &model.Response{Done: true, Choices: []model.Choice{{Message: model.NewAssistantMessage(answer), FinishReason: &finish}}}
	close(stream)
	return stream, nil
}

func fixedSnapshot() searchdomain.Snapshot {
	return searchdomain.Snapshot{ModuleID: "module-1", ReleaseID: "release-1", Generation: 3,
		PublicationRevision: "pointer-7", ValidRevisionIDs: []string{"revision-1"},
		Indexes: map[searchdomain.Lane]corpus.Ref{
			searchdomain.Dense:       artifacts.Reference([]byte("dense-index")),
			searchdomain.Sparse:      artifacts.Reference([]byte("sparse-index")),
			searchdomain.MultiVector: artifacts.Reference([]byte("multi-index")),
		}}
}

func fixedDelivery(t *testing.T, accepted *atomic.Bool, snap searchdomain.Snapshot) *searchdomain.Delivery {
	t.Helper()
	quote := "citation text"
	chunk := corpus.Chunk{ID: "chunk-1", SourceKind: "wiki", ContentID: "doc-1", RevisionID: "revision-1",
		Location: corpus.Location{Locator: "paragraph:1"}, Original: artifacts.Reference([]byte("original-object")),
		TextHash: artifacts.Hash([]byte(quote))}
	key := searchdomain.Key{SourceKind: chunk.SourceKind, ContentID: chunk.ContentID,
		RevisionID: chunk.RevisionID, ChunkID: chunk.ID}
	hit := searchdomain.LaneHit{Lane: searchdomain.Dense, Index: snap.Indexes[searchdomain.Dense], Rank: 1, RawScore: 0.8}
	searcher := searchdomain.ExecuteFunc(func(_ context.Context, request searchdomain.Request) (searchdomain.Result, error) {
		return searchdomain.Result{Status: "complete", StopReason: "batch_complete",
			Profile: searchdomain.Profile{RequestedDepth: request.Depth, EffectiveDepth: request.Depth,
				RequestedIntelligence: request.Intelligence, EffectiveIntelligence: request.Intelligence, PolicyVersion: "fixture-p1"},
			Snapshot:       request.Snapshot,
			Candidates:     []searchdomain.Candidate{{Key: key, Chunk: chunk, RRFScore: 1.0 / 61, Sources: []searchdomain.LaneHit{hit}}},
			Verified:       []searchdomain.VerifiedCandidate{{Key: key, Chunk: chunk, RRFScore: 1.0 / 61, Sources: []searchdomain.LaneHit{hit}}},
			UsedSubqueries: 1}, nil
	})
	source := searchdomain.SourceReadFunc(func(_ context.Context, _ searchdomain.Snapshot, _ searchdomain.VerifiedCandidate) (corpus.Chunk, error) {
		read := chunk
		read.Text = quote
		return read, nil
	})
	accept := searchdomain.AcceptFunc(func(_ context.Context, pack searchdomain.EvidencePack) (searchdomain.CitationReceipt, error) {
		hash, err := pack.Hash()
		if err != nil {
			return searchdomain.CitationReceipt{}, err
		}
		accepted.Store(true)
		return searchdomain.CitationReceipt{SearchID: pack.SearchID, PackHash: hash, DurableRef: "rtw:fixture:receipt"}, nil
	})
	d, err := searchdomain.NewDelivery(searcher, searchdomain.CheckFunc(func(context.Context, searchdomain.Snapshot, corpus.Chunk) (bool, error) {
		return true, nil
	}), source, accept, searchdomain.EvidenceLimits{MaxReads: 2, MaxQuoteRunes: 100})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestHTTPHandlerFrameworkRootAndPublicProjection(t *testing.T) {
	if os.Getenv("SEARCH_HTTP_CHILD") != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestHTTPHandlerFrameworkRootAndPublicProjection$")
		cmd.Env = append(os.Environ(), "SEARCH_HTTP_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("framework HTTP subprocess: %v\n%s", err, output)
		}
		return
	}
	var logs bytes.Buffer
	exporter := &traceSink{}
	bundle, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-search-http-fixture",
		Environment: "test", Version: "fixture-sha", InstanceID: "fixture", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: exporter})
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Bool
	snapshot := fixedSnapshot()
	history := &acceptedHistory{}
	m := &fixtureModel{accepted: &accepted}
	boundary, err := searchdomain.NewRootSessionBoundary(fixedDelivery(t, &accepted, snapshot), m, history, bundle)
	if err != nil {
		t.Fatal(err)
	}
	var resolves atomic.Int32
	scope := TrustedScope{Subject: btwruntime.SubjectRef{AuthorityID: "rtw.identity", TenantID: "tenant-a", SubjectID: "user-1"},
		SessionID: "conversation-1", SearchID: "search-1", AnswerID: "answer-1", Snapshot: snapshot}
	handler, err := NewHandler(ScopeFunc(func(_ context.Context, r *http.Request, module string) (TrustedScope, error) {
		resolves.Add(1)
		if module != "module-1" || r.Header.Get("X-Fixture-Deny") == "true" {
			return TrustedScope{}, ErrScopeDenied
		}
		resolved := scope
		if r.Header.Get("X-Fixture-Second") == "true" {
			resolved.SearchID, resolved.AnswerID = "search-2", "answer-2"
		}
		return resolved, nil
	}), boundary, bundle)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	request := func(body string) *http.Request {
		r, err := http.NewRequest(http.MethodPost, server.URL+Route, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	spoofed, err := server.Client().Do(request(`{"module_id":"module-1","query":"why","depth":"fast","intelligence":"low","subject_ref":{"subject_id":"attacker"}}`))
	if err != nil {
		t.Fatal(err)
	}
	spoofed.Body.Close()
	if spoofed.StatusCode != http.StatusBadRequest || resolves.Load() != 0 || m.calls.Load() != 0 {
		t.Fatalf("client-selected identity reached resolver/model: status=%d calls=%d model=%d", spoofed.StatusCode, resolves.Load(), m.calls.Load())
	}
	denied := request(`{"module_id":"module-1","query":"why","depth":"fast","intelligence":"low"}`)
	denied.Header.Set("X-Fixture-Deny", "true")
	deniedResponse, err := server.Client().Do(denied)
	if err != nil {
		t.Fatal(err)
	}
	deniedResponse.Body.Close()
	if deniedResponse.StatusCode != http.StatusForbidden || m.calls.Load() != 0 {
		t.Fatalf("denied scope reached model: %d %d", deniedResponse.StatusCode, m.calls.Load())
	}
	tooLarge := request(fmt.Sprintf(`{"module_id":"module-1","query":%q,"depth":"fast","intelligence":"low"}`, strings.Repeat("q", maxBodyBytes)))
	largeResponse, err := server.Client().Do(tooLarge)
	if err != nil {
		t.Fatal(err)
	}
	largeResponse.Body.Close()
	if largeResponse.StatusCode != http.StatusBadRequest || resolves.Load() != 1 || m.calls.Load() != 0 {
		t.Fatalf("oversized body reached resolver/model: status=%d resolver=%d model=%d", largeResponse.StatusCode, resolves.Load(), m.calls.Load())
	}
	good, err := server.Client().Do(request(`{"module_id":"module-1","query":"why","depth":"fast","intelligence":"low"}`))
	if err != nil {
		t.Fatal(err)
	}
	var public Response
	if err := json.NewDecoder(good.Body).Decode(&public); err != nil {
		t.Fatal(err)
	}
	good.Body.Close()
	if good.StatusCode != http.StatusOK || public.SearchID != scope.SearchID || public.AnswerID != scope.AnswerID ||
		public.Status != "succeeded" || public.Answer != "fixture answer" || len(public.Citations) != 1 ||
		public.Citations[0].Quote != "citation text" || public.ReceiptRef != "rtw:fixture:receipt" ||
		!accepted.Load() || m.calls.Load() != 1 || m.tools.Load() {
		t.Fatalf("framework result was not projected after receipt: %+v status=%d model=%d", public, good.StatusCode, m.calls.Load())
	}
	m.invalid.Store(true)
	badRequest := request(`{"module_id":"module-1","query":"why","depth":"fast","intelligence":"low"}`)
	badRequest.Header.Set("X-Fixture-Second", "true")
	bad, err := server.Client().Do(badRequest)
	if err != nil {
		t.Fatal(err)
	}
	var badBody bytes.Buffer
	if _, err := io.Copy(&badBody, bad.Body); err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadGateway || !strings.Contains(badBody.String(), "SEARCH_RUN_FAILED") ||
		strings.Contains(badBody.String(), "unaccepted model text") || strings.Contains(badBody.String(), "invented") ||
		len(history.turns) != 1 {
		t.Fatalf("invalid model answer escaped HTTP or history: status=%d body=%q turns=%d", bad.StatusCode, badBody.String(), len(history.turns))
	}
	metrics := httptest.NewRecorder()
	bundle.MetricsHandler().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), "trpc_agent_go_agent_") ||
		!strings.Contains(metrics.Body.String(), "trpc_agent_go_chat_") {
		t.Fatal("framework native Agent/Chat metrics missing from HTTP process")
	}
	if err := boundary.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := make(map[string]sdktrace.ReadOnlySpan)
	for _, span := range exporter.snapshot() {
		spans[span.Name()] = span
	}
	httpSpan, appSpan, runtimeSpan, agentSpan := spans["POST "+Route], spans["search.http.summary"],
		spans["runtime.run"], spans["invoke_agent search_summary_root"]
	if httpSpan == nil || appSpan == nil || runtimeSpan == nil || agentSpan == nil ||
		appSpan.Parent().SpanID() != httpSpan.SpanContext().SpanID() ||
		runtimeSpan.Parent().SpanID() != appSpan.SpanContext().SpanID() ||
		agentSpan.Parent().SpanID() != runtimeSpan.SpanContext().SpanID() ||
		agentSpan.InstrumentationScope().Name != "trpc.agent.go" ||
		httpSpan.SpanContext().TraceID() != agentSpan.SpanContext().TraceID() {
		t.Fatalf("HTTP→application→Runtime→framework Agent ancestry missing: %v", spans)
	}
	if !strings.Contains(logs.String(), `"event":"search.http.summary.finished"`) {
		t.Fatal("unified application JSON terminal missing")
	}
}
