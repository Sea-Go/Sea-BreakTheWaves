package search

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

func rootFixtureRequest(id string) SummaryRequest {
	return SummaryRequest{SearchID: "search-" + id, AnswerID: "answer-" + id, Subject: btwSubject(),
		SessionID: "root-session", Search: searchRequest(Fast, Low)}
}

func TestRootSummaryRunOptionFixesRequestAndRejectsInvalidScope(t *testing.T) {
	q := rootFixtureRequest("fixed")
	option, err := RootSummaryRunOption(q)
	if err != nil {
		t.Fatal(err)
	}
	q.Search.Snapshot.ValidRevisionIDs[0] = "withdrawn"
	q.Search.Snapshot.Indexes[Dense] = ref("changed")
	options := agent.RunOptions{RuntimeState: map[string]any{"other": "preserved"}}
	option(&options)
	fixed, ok := options.RuntimeState[rootSummaryRequestKey].(SummaryRequest)
	if !ok || fixed.Search.Snapshot.ValidRevisionIDs[0] != "rev-a" ||
		fixed.Search.Snapshot.Indexes[Dense] != ref("a") || options.RuntimeState["other"] != "preserved" {
		t.Fatalf("request was not fixed: %+v", options.RuntimeState)
	}
	q = rootFixtureRequest("invalid")
	q.Subject = btwruntime.SubjectRef{}
	if _, err := RootSummaryRunOption(q); !errors.Is(err, btwruntime.ErrInvalidSubject) {
		t.Fatalf("invalid subject passed root option: %v", err)
	}
	q = rootFixtureRequest("invalid")
	q.Search.Query = " "
	if _, err := RootSummaryRunOption(q); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid query passed root option: %v", err)
	}
}

func TestRootSummarySingleRunnerNativeGraphAndChat(t *testing.T) {
	if os.Getenv("SEARCH_ROOT_GRAPH_CHILD") != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestRootSummarySingleRunnerNativeGraphAndChat$")
		cmd.Env = append(os.Environ(), "SEARCH_ROOT_GRAPH_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("root graph subprocess: %v\n%s", err, output)
		}
		return
	}

	var logs bytes.Buffer
	exporter := &searchSpanExporter{}
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-search-root-test",
		Environment: "test", Version: "fixture-v1", InstanceID: "root-test", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: exporter})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	sessions := inmemory.NewSessionService()
	var acceptedFirst atomic.Bool
	d := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(exactSource),
		AcceptFunc(func(ctx context.Context, p EvidencePack) (CitationReceipt, error) {
			acceptedFirst.Store(true)
			return accepted(ctx, p)
		}))
	m := &summaryModel{receipt: &acceptedFirst}
	root, err := NewRootSummarizer(d, m, sessions, observed)
	if err != nil {
		t.Fatal(err)
	}
	ctx, parent := otel.Tracer("root-test").Start(context.Background(), "request-parent")
	q := rootFixtureRequest("success")
	out, err := root.Summarize(ctx, q)
	parent.End()
	if err != nil || out.SummaryStatus != "succeeded" || out.Answer != "fixture summary" ||
		len(out.Citations) != 1 || out.Citations[0] != out.Search.Pack.Evidence[0].ID ||
		!acceptedFirst.Load() || !m.sawReceipt.Load() || !m.sawTrace.Load() ||
		m.sawTools.Load() || m.calls.Load() != 1 {
		t.Fatalf("single runner root result=%+v err=%v model=%+v", out, err, m)
	}
	if hash, err := out.Search.Pack.Hash(); err != nil || hash != out.Search.Receipt.PackHash {
		t.Fatalf("citation receipt not fixed: %s %v", hash, err)
	}
	if _, err := root.Summarize(nil, rootFixtureRequest("nil-context")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil context accepted: %v", err)
	}

	m.invalid.Store(true)
	bad, err := root.Summarize(context.Background(), rootFixtureRequest("bad-citation"))
	if err == nil || !strings.Contains(err.Error(), ErrSummary.Error()) ||
		bad.Answer != "" || bad.SummaryStatus != "failed" {
		t.Fatalf("unsupported model citation escaped root: %+v %v", bad, err)
	}
	m.invalid.Store(false)
	empty := fixtureDelivery(t, nil, SourceReadFunc(exactSource), AcceptFunc(accepted))
	emptyRoot, err := NewRootSummarizer(empty, m, sessions, observed)
	if err != nil {
		t.Fatal(err)
	}
	callsBeforeEmpty := m.calls.Load()
	insufficient, err := emptyRoot.Summarize(context.Background(), rootFixtureRequest("empty"))
	if err != nil || insufficient.SummaryStatus != "insufficient" ||
		insufficient.Answer != "" || m.calls.Load() != callsBeforeEmpty {
		t.Fatalf("empty evidence called model or failed: %+v %v", insufficient, err)
	}
	if err := emptyRoot.Close(); err != nil {
		t.Fatal(err)
	}

	for _, failure := range []struct {
		name   string
		source SourceReader
		accept CitationAcceptor
	}{
		{"source", SourceReadFunc(func(context.Context, Snapshot, VerifiedCandidate) (corpus.Chunk, error) {
			return corpus.Chunk{}, errors.New("rtw read failed")
		}), AcceptFunc(accepted)},
		{"receipt", SourceReadFunc(exactSource), AcceptFunc(func(context.Context, EvidencePack) (CitationReceipt, error) {
			return CitationReceipt{}, errors.New("rtw accept failed")
		})},
	} {
		failedDelivery := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, failure.source, failure.accept)
		failedRoot, err := NewRootSummarizer(failedDelivery, m, sessions, observed)
		if err != nil {
			t.Fatal(err)
		}
		callsBefore := m.calls.Load()
		failed, err := failedRoot.Summarize(context.Background(), rootFixtureRequest(failure.name))
		if err == nil || failed.Answer != "" || failed.SummaryStatus != "failed" || m.calls.Load() != callsBefore {
			t.Fatalf("%s failure reached model or escaped: %+v %v", failure.name, failed, err)
		}
		if err := failedRoot.Close(); err != nil {
			t.Fatal(err)
		}
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancelDelivery := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(exactSource),
		AcceptFunc(func(ctx context.Context, p EvidencePack) (CitationReceipt, error) {
			receipt, err := accepted(ctx, p)
			cancel()
			return receipt, err
		}))
	cancelRoot, err := NewRootSummarizer(cancelDelivery, m, sessions, observed)
	if err != nil {
		t.Fatal(err)
	}
	callsBeforeCancel := m.calls.Load()
	cancelled, err := cancelRoot.Summarize(cancelCtx, rootFixtureRequest("cancel"))
	if !errors.Is(err, context.Canceled) || cancelled.Answer != "" ||
		cancelled.SummaryStatus != "failed" || m.calls.Load() != callsBeforeCancel {
		t.Fatalf("cancel after receipt reached model: %+v %v", cancelled, err)
	}
	if err := cancelRoot.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Close(); err != nil {
		t.Fatal(err)
	}
	metrics := httptest.NewRecorder()
	observed.MetricsHandler().ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "trpc_agent_go_agent_") ||
		!strings.Contains(metrics.Body.String(), "trpc_agent_go_chat_") {
		t.Fatalf("native framework metrics absent: %s", metrics.Body.String())
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()
	if err := observed.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	named := map[string]sdktrace.ReadOnlySpan{}
	counts := map[string]int{}
	for _, span := range exporter.snapshot() {
		if span.SpanContext().TraceID() == parent.SpanContext().TraceID() {
			named[span.Name()] = span
			counts[span.Name()]++
		}
	}
	for _, name := range []string{"request-parent", "runtime.run", "invoke_agent search_summary_root",
		"workflow execute_graph search_summary_root",
		"workflow execute_function_node search_and_accept",
		"workflow execute_function_node search_summary",
		"invoke_agent search_summary", "chat summary-fixture", "workflow execute_function_node validate_answer"} {
		if named[name] == nil {
			t.Fatalf("missing native span %q: %v", name, exporter.names())
		}
		if counts[name] != 1 {
			t.Fatalf("one request created %d spans named %q", counts[name], name)
		}
		if name != "request-parent" && name != "runtime.run" &&
			named[name].InstrumentationScope().Name != "trpc.agent.go" {
			t.Fatalf("%q is not a framework-native span: %s", name, named[name].InstrumentationScope().Name)
		}
	}
	for name := range counts {
		if strings.Contains(name, "execute_tool") {
			t.Fatalf("no-tool summary invoked %q", name)
		}
	}
	requireParent := func(childName, parentName string) {
		t.Helper()
		child, parent := named[childName], named[parentName]
		if child.Parent().SpanID() != parent.SpanContext().SpanID() ||
			child.SpanContext().TraceID() != parent.SpanContext().TraceID() {
			t.Fatalf("native span chain broken: %s -> %s", childName, parentName)
		}
	}
	requireParent("runtime.run", "request-parent")
	requireParent("invoke_agent search_summary_root", "runtime.run")
	requireParent("workflow execute_graph search_summary_root", "invoke_agent search_summary_root")
	requireParent("workflow execute_function_node search_and_accept", "workflow execute_graph search_summary_root")
	requireParent("workflow execute_function_node search_summary", "workflow execute_graph search_summary_root")
	requireParent("invoke_agent search_summary", "workflow execute_function_node search_summary")
	requireParent("chat summary-fixture", "invoke_agent search_summary")
	requireParent("workflow execute_function_node validate_answer", "workflow execute_graph search_summary_root")
}
