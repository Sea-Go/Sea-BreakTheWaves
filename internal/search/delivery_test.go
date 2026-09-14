package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/sparse"
	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

func sourceChunk(id string) corpus.Chunk {
	c := chunk(id, "rev-a")
	c.Original = ref("d")
	c.Location = corpus.Location{Locator: "paragraph:1", OriginalByteStart: 0, OriginalByteEnd: 13, NormalizedRuneStart: 0, NormalizedRuneEnd: 13}
	c.Text = "citation text"
	c.TextHash = artifacts.Hash([]byte(c.Text))
	return c
}

func fixtureDelivery(t *testing.T, chunks []corpus.Chunk, source SourceReader, accept CitationAcceptor) *Delivery {
	t.Helper()
	calls := new(calls)
	s := service(calls, PlanFunc(oneQuery), CheckFunc(allow), policy())
	s.dense = denseStub{calls, func(q dense.Query) (dense.Result, error) {
		out := dense.Result{Candidates: []dense.Candidate{}}
		for i, c := range chunks {
			out.Candidates = append(out.Candidates, dense.Candidate{
				ModuleID: q.ModuleID, ReleaseID: q.ReleaseID, Generation: q.Generation, Lane: "dense", Chunk: c,
				IndexRef: q.IndexRef, Score: float64(len(chunks) - i), Rank: i + 1})
		}
		return out, nil
	}}
	s.sparse = sparseStub{calls, func(sparse.Query) (sparse.Result, error) { return sparse.Result{Candidates: []sparse.Candidate{}}, nil }}
	s.multi = multiStub{calls, func(multivector.Query) (multivector.Result, error) {
		return multivector.Result{Candidates: []multivector.Candidate{}}, nil
	}}
	d, err := NewDelivery(s, CheckFunc(allow), source, accept, EvidenceLimits{MaxReads: 3, MaxQuoteRunes: 80})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func exactSource(_ context.Context, _ Snapshot, v VerifiedCandidate) (corpus.Chunk, error) {
	c := v.Chunk
	c.Text = "citation text"
	return c, nil
}

func accepted(_ context.Context, p EvidencePack) (CitationReceipt, error) {
	return CitationReceipt{SearchID: p.SearchID, PackHash: p.Hash(), DurableRef: "rtw:fixture:committed"}, nil
}

func searchRequest(depth Depth, level Intelligence) Request {
	return Request{Query: "where", Depth: depth, Intelligence: level, Snapshot: snapshot()}
}

func TestEvidenceRequiresSameRevisionAndDurableReceipt(t *testing.T) {
	var order []string
	source := SourceReadFunc(func(ctx context.Context, s Snapshot, v VerifiedCandidate) (corpus.Chunk, error) {
		order = append(order, "read")
		if s.PublicationRevision != "pointer-7" || v.Chunk.Text != "" {
			t.Fatal("scope or indexed text escaped")
		}
		return exactSource(ctx, s, v)
	})
	accept := AcceptFunc(func(ctx context.Context, p EvidencePack) (CitationReceipt, error) {
		order = append(order, "accept")
		if len(p.Evidence) != 1 || p.Evidence[0].Quote != "citation text" {
			t.Fatal("unverified quote sent to RTW")
		}
		return accepted(ctx, p)
	})
	d := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, source, accept)
	r, err := d.Search(context.Background(), "s-1", searchRequest(Fast, Low))
	if err != nil || r.Pack.Status != "complete" || len(r.Pack.Evidence) != 1 || r.Receipt.PackHash != r.Pack.Hash() || strings.Join(order, ",") != "read,accept" {
		t.Fatalf("delivery %+v %v order=%v", r, err, order)
	}
	if r.Pack.Evidence[0].QuoteHash != artifacts.Hash([]byte("citation text")) {
		t.Fatal("quote hash")
	}
	for _, mutate := range []func(*corpus.Chunk){
		func(c *corpus.Chunk) { c.RevisionID = "old" },
		func(c *corpus.Chunk) { c.Location.Locator = "paragraph:2" },
		func(c *corpus.Chunk) { c.Text = "different text" },
		func(c *corpus.Chunk) { c.Original = ref("e") },
	} {
		calls := 0
		bad := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(func(ctx context.Context, s Snapshot, v VerifiedCandidate) (corpus.Chunk, error) {
			q, _ := exactSource(ctx, s, v)
			mutate(&q)
			return q, nil
		}), AcceptFunc(func(context.Context, EvidencePack) (CitationReceipt, error) { calls++; return CitationReceipt{}, nil }))
		got, e := bad.Search(context.Background(), "s-1", searchRequest(Fast, Low))
		if !errors.Is(e, ErrSourceMismatch) || calls != 0 || len(got.Pack.Evidence) != 0 {
			t.Fatalf("mismatched source reached public pack: %+v %v calls=%d", got, e, calls)
		}
	}
	badReceipt := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(exactSource), AcceptFunc(func(context.Context, EvidencePack) (CitationReceipt, error) {
		return CitationReceipt{SearchID: "s-1", PackHash: "wrong", DurableRef: "rtw:accepted"}, nil
	}))
	got, e := badReceipt.Search(context.Background(), "s-1", searchRequest(Fast, Low))
	if !errors.Is(e, ErrReceipt) || len(got.Pack.Evidence) != 0 {
		t.Fatalf("missing durable receipt leaked quote: %+v %v", got, e)
	}
}

func TestEvidenceEmptyPartialAndCancellation(t *testing.T) {
	var acceptedCalls int
	accept := AcceptFunc(func(ctx context.Context, p EvidencePack) (CitationReceipt, error) {
		acceptedCalls++
		return accepted(ctx, p)
	})
	empty := fixtureDelivery(t, nil, SourceReadFunc(exactSource), accept)
	r, e := empty.Search(context.Background(), "empty", searchRequest(Fast, Low))
	if e != nil || r.Pack.Status != "empty" || acceptedCalls != 0 {
		t.Fatalf("empty %+v %v", r, e)
	}
	partial := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a"), sourceChunk("b")}, SourceReadFunc(func(ctx context.Context, s Snapshot, v VerifiedCandidate) (corpus.Chunk, error) {
		if v.Key.ChunkID == "b" {
			return corpus.Chunk{}, errors.New("RTW unavailable")
		}
		return exactSource(ctx, s, v)
	}), accept)
	q := searchRequest(Fast, Medium)
	q.AllowPartial = true
	r, e = partial.Search(context.Background(), "partial", q)
	if e != nil || r.Pack.Status != "partial" || len(r.Pack.Evidence) != 1 || len(r.Pack.Gaps) != 1 || r.Pack.Gaps[0] != "source_unavailable" {
		t.Fatalf("partial %+v %v", r, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(func(context.Context, Snapshot, VerifiedCandidate) (corpus.Chunk, error) {
		cancel()
		return corpus.Chunk{}, context.Canceled
	}), accept)
	r, e = cancelled.Search(ctx, "cancel", searchRequest(Fast, Low))
	if !errors.Is(e, context.Canceled) || len(r.Pack.Evidence) != 0 {
		t.Fatalf("cancel leaked quote: %+v %v", r, e)
	}
}

func TestDeliveryRejectsGraphAdapterScopeDriftBeforeRTWRead(t *testing.T) {
	readCalls := 0
	d := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(func(ctx context.Context, s Snapshot, v VerifiedCandidate) (corpus.Chunk, error) {
		readCalls++
		return exactSource(ctx, s, v)
	}), AcceptFunc(accepted))
	base := d.search
	for _, mutate := range []func(*Result){
		func(r *Result) { r.Snapshot.ReleaseID = "new-release" },
		func(r *Result) { r.Verified[0].Chunk.Text = "indexed copy" },
		func(r *Result) { r.Verified[0].Key.RevisionID = "withdrawn" },
	} {
		d.search = ExecuteFunc(func(ctx context.Context, q Request) (Result, error) {
			r, err := base.Execute(ctx, q)
			if err == nil {
				mutate(&r)
			}
			return r, err
		})
		got, err := d.Search(context.Background(), "scope-test", searchRequest(Fast, Low))
		if !errors.Is(err, ErrRetrievalContract) || len(got.Pack.Evidence) != 0 || readCalls != 0 {
			t.Fatalf("adapter drift reached RTW: %+v %v read=%d", got, err, readCalls)
		}
	}
}

func TestFailedSourceReadsStillSpendReadBudget(t *testing.T) {
	reads := 0
	d := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a"), sourceChunk("b"), sourceChunk("c"), sourceChunk("d")},
		SourceReadFunc(func(context.Context, Snapshot, VerifiedCandidate) (corpus.Chunk, error) {
			reads++
			return corpus.Chunk{}, errors.New("read failed")
		}), AcceptFunc(accepted))
	q := searchRequest(Detailed, High)
	q.AllowPartial = true
	r, err := d.Search(context.Background(), "budget", q)
	if err != nil || reads != 3 || r.Pack.Status != "empty" || len(r.Pack.Gaps) != 2 || r.Pack.Gaps[1] != "read_budget" {
		t.Fatalf("failed read budget: result=%+v err=%v reads=%d", r, err, reads)
	}
}

func TestTypedToolsShareSnapshotAndCumulativeBudget(t *testing.T) {
	d := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(exactSource), AcceptFunc(accepted))
	s, err := NewToolSession(d, snapshot(), "op", ToolBudget{MaxSearchCalls: 2, MaxReadCalls: 7, MaxQuoteRunes: 200}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := s.Tools()
	if err != nil || len(tools) != 3 {
		t.Fatalf("tools %v %v", tools, err)
	}
	for i, name := range []string{"search_fast", "search_detailed", "read_evidence"} {
		if tools[i].Declaration().Name != name {
			t.Fatalf("tool name %d: %s", i, tools[i].Declaration().Name)
		}
	}
	call := func(index int, args []byte) (any, error) {
		return tools[index].(tool.CallableTool).Call(context.Background(), args)
	}
	value, err := call(0, []byte(`{"query":"where","intelligence":"low"}`))
	if err != nil {
		t.Fatal(err)
	}
	first, ok := value.(SearchToolResult)
	if !ok || first.Search.Pack.Status != "complete" || len(first.Search.Pack.Evidence) != 1 {
		t.Fatalf("typed tool result: %#v", value)
	}
	readArg, _ := json.Marshal(ReadEvidenceInput{SearchID: first.Search.Pack.SearchID, EvidenceID: first.Search.Pack.Evidence[0].ID})
	first.Search.Pack.Evidence[0].Quote = "caller mutation"
	first.Search.Pack.Snapshot.Indexes[Dense] = ref("e")
	read, err := call(2, readArg)
	if err != nil || read.(ReadEvidenceResult).Evidence.Quote != "citation text" {
		t.Fatalf("typed reread %#v %v", read, err)
	}
	_, err = call(2, []byte(`{"search_id":"another","evidence_id":"fake"}`))
	if !errors.Is(err, ErrEvidence) {
		t.Fatalf("cross-search ID accepted: %v", err)
	}
	_, err = call(1, []byte(`{"query":"where","intelligence":"high"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = call(0, []byte(`{"query":"where","intelligence":"low"}`))
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("search call budget reset: %v", err)
	}
	_, err = call(1, []byte(`{"query":"where","intelligence":"low","continue_search_id":"op:1"}`))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("fake continuation: %v", err)
	}
}

func TestToolReservationRefundsActualUsageForFollowupRead(t *testing.T) {
	d := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(exactSource), AcceptFunc(accepted))
	s, err := NewToolSession(d, snapshot(), "budget-refund", ToolBudget{MaxSearchCalls: 1, MaxReadCalls: 2, MaxQuoteRunes: 26}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.SearchFast(context.Background(), SearchToolInput{Query: "where", Intelligence: Low})
	if err != nil || first.Search.Usage.SourceReadAttempts != 1 || first.Search.Usage.QuoteRunes != 13 || first.Remaining.ReadCalls != 1 || first.Remaining.QuoteRunes != 13 {
		t.Fatalf("actual usage not returned: %+v %v", first, err)
	}
	read, err := s.ReadEvidence(context.Background(), ReadEvidenceInput{SearchID: first.Search.Pack.SearchID, EvidenceID: first.Search.Pack.Evidence[0].ID})
	if err != nil || read.Remaining.ReadCalls != 0 || read.Remaining.QuoteRunes != 0 {
		t.Fatalf("bounded follow-up read: %+v %v", read, err)
	}
	_, err = s.ReadEvidence(context.Background(), ReadEvidenceInput{SearchID: first.Search.Pack.SearchID, EvidenceID: first.Search.Pack.Evidence[0].ID})
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("re-read minted budget: %v", err)
	}
}

type searchSpanExporter struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

func (e *searchSpanExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans = append(e.spans, spans...)
	return nil
}
func (*searchSpanExporter) Shutdown(context.Context) error { return nil }
func (e *searchSpanExporter) names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, s := range e.spans {
		out = append(out, s.Name())
	}
	return out
}
func (e *searchSpanExporter) snapshot() []sdktrace.ReadOnlySpan {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]sdktrace.ReadOnlySpan(nil), e.spans...)
}

type summaryModel struct {
	calls      atomic.Int32
	sawTools   atomic.Bool
	sawTrace   atomic.Bool
	receipt    *atomic.Bool
	sawReceipt atomic.Bool
	invalid    atomic.Bool
}

func (*summaryModel) Info() model.Info { return model.Info{Name: "summary-fixture"} }
func (m *summaryModel) GenerateContent(ctx context.Context, q *model.Request) (<-chan *model.Response, error) {
	m.calls.Add(1)
	if len(q.Tools) > 0 {
		m.sawTools.Store(true)
	}
	if trace.SpanFromContext(ctx).SpanContext().IsValid() {
		m.sawTrace.Store(true)
	}
	if m.receipt != nil && m.receipt.Load() {
		m.sawReceipt.Store(true)
	}
	var prompt struct {
		Pack EvidencePack `json:"fixed_evidence_pack"`
	}
	for i := len(q.Messages) - 1; i >= 0; i-- {
		if q.Messages[i].Role == model.RoleUser {
			json.Unmarshal([]byte(q.Messages[i].Content), &prompt)
			break
		}
	}
	answer := `{"answer":"fixture summary","citations":["wrong"]}`
	if len(prompt.Pack.Evidence) > 0 {
		answer = fmt.Sprintf(`{"answer":"fixture summary","citations":[%q]}`, prompt.Pack.Evidence[0].ID)
	}
	if m.invalid.Load() {
		answer = `{"answer":"unsupported","citations":["invented"]}`
	}
	ch := make(chan *model.Response, 1)
	finish := "stop"
	ch <- &model.Response{Done: true, Choices: []model.Choice{{Message: model.NewAssistantMessage(answer), FinishReason: &finish}}}
	close(ch)
	return ch, nil
}

func TestSummaryRunsNativeLLMAgentRunnerAfterReceipt(t *testing.T) {
	var logs bytes.Buffer
	exporter := &searchSpanExporter{}
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-search-test", Environment: "test", Version: "fixture-v1", InstanceID: "search-test", Output: &logs, Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: exporter})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	sessions := inmemory.NewSessionService()
	var receiptDone atomic.Bool
	d := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(exactSource), AcceptFunc(func(ctx context.Context, p EvidencePack) (CitationReceipt, error) {
		receiptDone.Store(true)
		return accepted(ctx, p)
	}))
	m := &summaryModel{receipt: &receiptDone}
	gotModel := model.Model(m)
	summary, err := NewSummarizer(d, gotModel, sessions, observed)
	if err != nil {
		t.Fatal(err)
	}
	q := SummaryRequest{SearchID: "s", AnswerID: "a", Subject: btwSubject(), SessionID: "session", Search: searchRequest(Fast, Low)}
	result, err := summary.Summarize(context.Background(), q)
	if err != nil || result.SummaryStatus != "succeeded" || !receiptDone.Load() || !m.sawReceipt.Load() || m.calls.Load() != 1 || m.sawTools.Load() || !m.sawTrace.Load() || len(result.Citations) != 1 {
		t.Fatalf("summary result %+v err=%v model=%+v", result, err, m)
	}
	m.invalid.Store(true)
	q.SearchID, q.AnswerID = "s2", "a2"
	bad, err := summary.Summarize(context.Background(), q)
	if !errors.Is(err, ErrSummary) || bad.SummaryStatus != "failed" || bad.Answer != "" || len(bad.Search.Pack.Evidence) != 1 {
		t.Fatalf("unsupported citation leaked: %+v %v", bad, err)
	}
	empty := fixtureDelivery(t, nil, SourceReadFunc(exactSource), AcceptFunc(accepted))
	emptySummary, err := NewSummarizer(empty, m, sessions, observed)
	if err != nil {
		t.Fatal(err)
	}
	q.SearchID, q.AnswerID = "s3", "a3"
	noEvidence, err := emptySummary.Summarize(context.Background(), q)
	if err != nil || noEvidence.SummaryStatus != "insufficient" || m.calls.Load() != 2 {
		t.Fatalf("empty called summary model: %+v %v", noEvidence, err)
	}
	if err := emptySummary.Close(); err != nil {
		t.Fatal(err)
	}
	m.invalid.Store(false)
	for _, depth := range []Depth{Fast, Detailed} {
		for _, level := range []Intelligence{Low, Medium, High} {
			name := string(depth) + "_" + string(level)
			t.Run("summary_"+name, func(t *testing.T) {
				q.SearchID, q.AnswerID, q.Search = "summary-"+name, "answer-"+name, searchRequest(depth, level)
				result, err := summary.Summarize(context.Background(), q)
				if err != nil || result.SummaryStatus != "succeeded" || result.Search.Pack.Profile.EffectiveDepth != depth || result.Search.Pack.Profile.EffectiveIntelligence != level || len(result.Citations) != 1 {
					t.Fatalf("matrix summary %+v %v", result, err)
				}
			})
			t.Run("tools_"+name, func(t *testing.T) {
				session, err := NewToolSession(d, snapshot(), "tool-"+name, ToolBudget{MaxSearchCalls: 1, MaxReadCalls: 4, MaxQuoteRunes: 90}, false, false)
				if err != nil {
					t.Fatal(err)
				}
				tools, err := session.Tools()
				if err != nil {
					t.Fatal(err)
				}
				index := 0
				if depth == Detailed {
					index = 1
				}
				args, _ := json.Marshal(SearchToolInput{Query: "where", Intelligence: level})
				got, err := tools[index].(tool.CallableTool).Call(context.Background(), args)
				if err != nil {
					t.Fatal(err)
				}
				result := got.(SearchToolResult)
				if result.Search.Pack.Profile.EffectiveDepth != depth || result.Search.Pack.Profile.EffectiveIntelligence != level || len(result.Search.Pack.Evidence) != 1 || result.Search.Receipt.DurableRef == "" || result.Remaining.SearchCalls != 0 {
					t.Fatalf("matrix tools %+v", result)
				}
			})
		}
	}
	toolSession, err := NewToolSession(d, snapshot(), "outer-op", ToolBudget{MaxSearchCalls: 1, MaxReadCalls: 4, MaxQuoteRunes: 90}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	nativeTools, err := toolSession.Tools()
	if err != nil {
		t.Fatal(err)
	}
	caller := &callerModel{}
	ag := llmagent.New("search_caller", llmagent.WithModel(caller), llmagent.WithTools(nativeTools),
		llmagent.WithGenerationConfig(model.GenerationConfig{Stream: false}))
	outer, err := btwruntime.New("search_caller", ag, sessions, observed)
	if err != nil {
		t.Fatal(err)
	}
	_, err = outer.Run(context.Background(), btwruntime.Request{Subject: btwSubject(), SessionID: "tool-session", RunID: "tool-run", Message: model.NewUserMessage("find evidence")}, nil)
	if err != nil || caller.calls.Load() != 2 || !caller.sawTools.Load() || !caller.sawTrace.Load() || !caller.sawToolResult.Load() || len(toolSession.searches) != 1 {
		t.Fatalf("native tool Runner failed: %v model=%+v searches=%d", err, caller, len(toolSession.searches))
	}
	if err := outer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := summary.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Close(); err != nil {
		t.Fatal(err)
	}
	metrics := httptest.NewRecorder()
	observed.MetricsHandler().ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "trpc_agent_go_agent_") || !strings.Contains(metrics.Body.String(), "trpc_agent_go_chat_") || !strings.Contains(metrics.Body.String(), "trpc_agent_go_tool_") {
		t.Fatalf("framework native metrics absent: %s", metrics.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := observed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	names := strings.Join(exporter.names(), ",")
	if !strings.Contains(names, "search_summary") || !strings.Contains(names, "chat") || !strings.Contains(names, "search_fast") {
		t.Fatalf("native spans absent: %s", names)
	}
	var callerTrace, toolTrace trace.TraceID
	for _, span := range exporter.snapshot() {
		switch span.Name() {
		case "invoke_agent search_caller":
			callerTrace = span.SpanContext().TraceID()
		case "execute_tool search_fast":
			toolTrace = span.SpanContext().TraceID()
		}
	}
	if !callerTrace.IsValid() || callerTrace != toolTrace {
		t.Fatalf("framework Agent/Tool trace split: agent=%s tool=%s names=%s", callerTrace, toolTrace, names)
	}
}

type callerModel struct {
	calls         atomic.Int32
	sawTools      atomic.Bool
	sawTrace      atomic.Bool
	sawToolResult atomic.Bool
}

func (*callerModel) Info() model.Info { return model.Info{Name: "caller-fixture"} }
func (m *callerModel) GenerateContent(ctx context.Context, q *model.Request) (<-chan *model.Response, error) {
	if len(q.Tools) == 3 {
		m.sawTools.Store(true)
	}
	if trace.SpanFromContext(ctx).SpanContext().IsValid() {
		m.sawTrace.Store(true)
	}
	ch := make(chan *model.Response, 1)
	if m.calls.Add(1) == 1 {
		finish := "tool_calls"
		msg := model.NewAssistantMessage("")
		msg.ToolCalls = []model.ToolCall{{ID: "call-search", Type: "function", Function: model.FunctionDefinitionParam{Name: "search_fast", Arguments: []byte(`{"query":"where","intelligence":"low"}`)}}}
		ch <- &model.Response{Done: true, Choices: []model.Choice{{Message: msg, FinishReason: &finish}}}
	} else {
		for _, message := range q.Messages {
			if message.Role == model.RoleTool && strings.Contains(message.Content, "evidence_pack") && strings.Contains(message.Content, "citation_receipt") {
				m.sawToolResult.Store(true)
			}
		}
		finish := "stop"
		ch <- &model.Response{Done: true, Choices: []model.Choice{{Message: model.NewAssistantMessage("outer agent chooses next step"), FinishReason: &finish}}}
	}
	close(ch)
	return ch, nil
}

func btwSubject() btwruntime.SubjectRef {
	return btwruntime.SubjectRef{AuthorityID: "test", TenantID: "tenant", SubjectID: "subject"}
}
