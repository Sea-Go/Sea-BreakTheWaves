package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

const wikiNativeProposal = `{"title":"可维护外部知识","markdown":"# 可维护外部知识\n仅记录原文支持的内容。","source_refs":[{"revision_id":"source-rev-1","locator":"paragraph:1"},{"revision_id":"source-rev-2","locator":"paragraph:2"}]}`

type wikiNativeDCFixture struct {
	t          *testing.T
	bearer     string
	mu         sync.Mutex
	receipts   map[string][]byte
	bodyHashes map[string]string
	keys       []string
	traces     []string
	calls      atomic.Int32
	providers  atomic.Int32
	lostFirst  atomic.Bool
	badFinish  atomic.Bool
	blockNext  atomic.Bool
	entered    chan struct{}
	exited     chan struct{}
	shutdown   chan struct{}
}

func (f *wikiNativeDCFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" ||
		r.Header.Get("Authorization") != "Bearer "+f.bearer ||
		r.Header.Get("X-Sea-Model-Callpoint") != btwruntime.WikiCompileCallPoint ||
		len(r.Header.Values("X-Sea-Model-Callpoint")) != 1 ||
		len(r.Header.Values("Idempotency-Key")) != 1 ||
		!strings.HasPrefix(r.Header.Get("Idempotency-Key"), "wiki-compile-") ||
		r.Header.Get("traceparent") == "" {
		f.t.Error("Factory did not send an official DC Wiki callpoint request with frozen bearer")
		http.Error(w, "wrong model contract", http.StatusBadRequest)
		return
	}
	if f.blockNext.CompareAndSwap(true, false) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(f.entered)
		select {
		case <-r.Context().Done():
		case <-f.shutdown:
		}
		close(f.exited)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 128<<10))
	if err != nil {
		f.t.Error(err)
		http.Error(w, "body read failed", http.StatusBadRequest)
		return
	}
	var wire struct {
		Model               string          `json:"model"`
		MaxCompletionTokens int             `json:"max_completion_tokens"`
		Stream              bool            `json:"stream"`
		Tools               json.RawMessage `json:"tools"`
		Messages            []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &wire) != nil || wire.Model != btwruntime.WikiCompileCallPoint ||
		wire.MaxCompletionTokens != 1024 || wire.Stream || len(wire.Messages) != 2 ||
		(len(wire.Tools) != 0 && string(wire.Tools) != "[]" && string(wire.Tools) != "null") {
		f.t.Error("official SDK request omitted one-call no-tool 1024-token contract")
		http.Error(w, "wrong model body", http.StatusBadRequest)
		return
	}
	var prompt struct {
		ModuleID string `json:"module_id"`
		PageID   string `json:"page_id"`
		Sources  []struct {
			RevisionID string `json:"revision_id"`
			Hash       string `json:"content_hash"`
			Paragraphs []struct {
				Locator string `json:"locator"`
				Text    string `json:"text"`
			} `json:"paragraphs"`
		} `json:"fixed_source_pack"`
	}
	if json.Unmarshal([]byte(wire.Messages[1].Content), &prompt) != nil ||
		prompt.ModuleID != wikiFactoryInput().ModuleID || prompt.PageID != wikiFactoryInput().PageID ||
		len(prompt.Sources) != 2 || prompt.Sources[0].RevisionID != "source-rev-1" ||
		prompt.Sources[1].RevisionID != "source-rev-2" ||
		prompt.Sources[0].Hash != wikiFactoryInput().Sources[0].SHA256 ||
		prompt.Sources[1].Hash != wikiFactoryInput().Sources[1].SHA256 ||
		prompt.Sources[0].Paragraphs[0].Locator != "paragraph:1" ||
		prompt.Sources[1].Paragraphs[1].Locator != "paragraph:2" {
		f.t.Error("official model saw sources other than RTW frozen originals")
		http.Error(w, "wrong RTW source", http.StatusBadRequest)
		return
	}
	budget := strconv.FormatInt(int64(len(body))+512+64*int64(len(wire.Messages)), 10)
	if r.Header.Get("X-Sea-Model-Input-Tokens") != budget ||
		r.Header.Get("X-Sea-Model-Context-Tokens") != budget ||
		r.Header.Get("X-Sea-Model-Output-Tokens") != "1024" {
		f.t.Error("DC headers differ from actual official SDK budget")
		http.Error(w, "wrong budget", http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	bodySHA := sha256.Sum256(body)
	f.mu.Lock()
	if old, exists := f.bodyHashes[key]; exists && old != hex.EncodeToString(bodySHA[:]) {
		f.mu.Unlock()
		http.Error(w, "same logical compile changed provider body", http.StatusConflict)
		return
	}
	f.keys = append(f.keys, key)
	f.traces = append(f.traces, r.Header.Get("traceparent"))
	receipt, exists := f.receipts[key]
	if !exists {
		f.providers.Add(1)
		finish := any("stop")
		if f.badFinish.CompareAndSwap(true, false) {
			finish = nil
		}
		receipt, err = json.Marshal(map[string]any{
			"id": "wiki-native-fixture", "object": "chat.completion", "model": btwruntime.WikiCompileCallPoint,
			"choices": []any{map[string]any{"index": 0,
				"message":       map[string]any{"role": "assistant", "content": wikiNativeProposal},
				"finish_reason": finish}},
		})
		if err != nil {
			f.mu.Unlock()
			f.t.Error(err)
			http.Error(w, "fixture encoding failed", http.StatusInternalServerError)
			return
		}
		f.receipts[key] = receipt
		f.bodyHashes[key] = hex.EncodeToString(bodySHA[:])
	}
	f.mu.Unlock()
	if !exists && f.lostFirst.CompareAndSwap(true, false) {
		connection, _, hijackErr := w.(http.Hijacker).Hijack()
		if hijackErr != nil {
			f.t.Error(hijackErr)
			return
		}
		_ = connection.Close()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(receipt)
}

func wikiNativeRunRequest(input content.WikiCompileInput, jobID string) btwruntime.Request {
	return btwruntime.Request{Subject: btwruntime.SubjectRef{AuthorityID: "ridethewind.knowledge",
		TenantID: "wiki-compile-run", SubjectID: input.CompileID},
		SessionID: "wiki-compile/" + input.CompileID + "/" + input.AttemptID,
		RunID:     "wiki-compile:" + jobID + ":" + input.AttemptID,
		Message:   model.NewUserMessage("caller-controlled text must not reach model")}
}

func wikiNativeAcceptRun(ctx context.Context, run WikiCompileRun,
	input content.WikiCompileInput, jobID string) (content.WikiCompileCandidate, btwruntime.Result, error) {
	var candidate content.WikiCompileCandidate
	var graphDone int
	q := wikiNativeRunRequest(input, jobID)
	// A caller-supplied Graph option cannot replace Open's frozen source pack.
	q.Options = []agent.RunOption{agent.MergeRuntimeState(map[string]any{"wiki_compile_fixed_input": "untrusted"})}
	result, err := run.Run(ctx, q, func(_ context.Context, e *event.Event) error {
		got, done, checkErr := content.WikiCompileCandidateFromCompletion(e, input)
		if checkErr != nil {
			return checkErr
		}
		if done {
			graphDone++
			candidate = got
		}
		return nil
	})
	if err == nil && (!result.Completed || graphDone != 1) {
		return candidate, result, fmt.Errorf("candidate/Runner completion mismatch: %d/%t", graphDone, result.Completed)
	}
	return candidate, result, err
}

func TestWikiNativeFactoryOfficialGraphLostReplyAndSameLogicalReplay(t *testing.T) {
	if os.Getenv("SEA_WIKI_NATIVE_FACTORY_HTTP_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWikiNativeFactoryOfficialGraphLostReplyAndSameLogicalReplay$", "-test.v")
		cmd.Env = append(os.Environ(), "SEA_WIKI_NATIVE_FACTORY_HTTP_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("official Wiki Factory/Graph HTTP child failed: %v\n%s", err, output)
		}
		return
	}
	a := wikiFactorySession(t, "11111111-1111-4111-8111-111111111111", 7)
	fixture := &wikiNativeDCFixture{t: t, bearer: a.NativeBearer(), receipts: map[string][]byte{},
		bodyHashes: map[string]string{}, entered: make(chan struct{}), exited: make(chan struct{}),
		shutdown: make(chan struct{})}
	fixture.lostFirst.Store(true)
	server := httptest.NewServer(fixture)
	defer server.Close()
	defer close(fixture.shutdown)
	provider := WikiCompileNativeSessionFunc(func(context.Context) (WikiCompileModelSession, error) { return a, nil })
	factory, err := NewWikiCompileNativeRunFactory(WikiCompileNativeRunFactoryConfig{DCGatewayRoot: server.URL}, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	if _, err := factory.CurrentNativeSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	var logs wikiNativeLogBuffer
	exporter := &factSpanExporter{}
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-wiki-native-factory-http",
		Environment: "test", Version: "fixture-v1", InstanceID: "factory-http", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: exporter})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	defer observed.Close(context.Background())
	if err := factory.BindTelemetry(observed); err != nil {
		t.Fatal(err)
	}
	input := wikiFactoryInput()
	first, err := factory.Open(context.Background(), input, a)
	if err != nil || first.ModelSessionProof() != a.Proof() {
		t.Fatal(err)
	}
	firstCandidate, firstResult, firstErr := wikiNativeAcceptRun(context.Background(), first, input, "job-first")
	if firstErr == nil || firstCandidate.ContentSHA256 != "" ||
		fixture.calls.Load() != 1 || fixture.providers.Load() != 1 {
		t.Fatalf("lost model reply silently accepted candidate/retried Provider: %+v %+v %v calls=%d providers=%d",
			firstCandidate, firstResult, firstErr, fixture.calls.Load(), fixture.providers.Load())
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	retry := wikiFactoryInput()
	retry.AttemptID, retry.LeaseEpoch = "attempt-10", 5
	second, err := factory.Open(context.Background(), retry, a)
	if err != nil {
		t.Fatal(err)
	}
	secondCandidate, secondResult, secondErr := wikiNativeAcceptRun(context.Background(), second, retry, "job-second")
	if secondErr != nil || !secondResult.Completed || secondCandidate.AttemptID != retry.AttemptID ||
		secondCandidate.LeaseEpoch != retry.LeaseEpoch || secondCandidate.ContentSHA256 == "" ||
		fixture.calls.Load() != 2 || fixture.providers.Load() != 1 ||
		!reflect.DeepEqual(secondCandidate.SourceRefs, []content.WikiCompileSourceRef{
			{RevisionID: "source-rev-1", Locator: "paragraph:1"},
			{RevisionID: "source-rev-2", Locator: "paragraph:2"}}) {
		t.Fatalf("same logical Compile did not replay one durable model receipt into a fresh attempt: %+v %+v %v calls=%d providers=%d",
			secondCandidate, secondResult, secondErr, fixture.calls.Load(), fixture.providers.Load())
	}
	if second.ModelSessionProof() != a.Proof() {
		t.Fatal("official model Proof changed after HTTP")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	nativeSecond := second.(*wikiCompileNativeRun)
	nativeSecond.nodes.mu.Lock()
	started, nodeFinished, pending := nativeSecond.nodes.started, nativeSecond.nodes.finished, len(nativeSecond.nodes.activeKeys)
	nativeSecond.nodes.mu.Unlock()
	if started < 3 || started != nodeFinished || pending != 0 {
		t.Fatalf("Close did not await native Graph node callbacks: started=%d finished=%d pending=%d",
			started, nodeFinished, pending)
	}
	fixture.mu.Lock()
	keys := append([]string(nil), fixture.keys...)
	traces := append([]string(nil), fixture.traces...)
	fixture.mu.Unlock()
	// This fixed key includes the true JCS hash of RTW's selected original
	// [{content_hash,revision_id}] order: sourceRefsSHA=ca62247e...f9c3.
	const fixedRTWJCSModelKey = "wiki-compile-03b39ad897b47c032a547603fc58b6396da18ef0dd3d0ad240f9559f1be4e7a0"
	if len(keys) != 2 || keys[0] != keys[1] || keys[0] != fixedRTWJCSModelKey || len(traces) != 2 ||
		!strings.HasPrefix(traces[0], "00-") || !strings.HasPrefix(traces[1], "00-") {
		t.Fatalf("retry changed logical model key or lost native TraceContext: keys=%v traces=%v", keys, traces)
	}
	// A new CompileID has a new key; a model reply missing finish_reason may
	// reach DC but must never yield a candidate or accepted Wiki object.
	fixture.badFinish.Store(true)
	bad := wikiFactoryInput()
	bad.CompileID, bad.AttemptID = "compile-18", "attempt-11"
	badRun, err := factory.Open(context.Background(), bad, a)
	if err != nil {
		t.Fatal(err)
	}
	badCandidate, _, badErr := wikiNativeAcceptRun(context.Background(), badRun, bad, "job-bad")
	if badErr == nil || badCandidate.ContentSHA256 != "" || fixture.providers.Load() != 2 {
		t.Fatalf("missing complete model response escaped Graph: %+v %v providers=%d", badCandidate, badErr, fixture.providers.Load())
	}
	if err := badRun.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.blockNext.Store(true)
	cancelInput := wikiFactoryInput()
	cancelInput.CompileID, cancelInput.AttemptID = "compile-19", "attempt-12"
	cancelRun, err := factory.Open(context.Background(), cancelInput, a)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		candidate content.WikiCompileCandidate
		err       error
	}
	finished := make(chan outcome, 1)
	go func() {
		candidate, _, runErr := wikiNativeAcceptRun(ctx, cancelRun, cancelInput, "job-cancel")
		finished <- outcome{candidate, runErr}
	}()
	select {
	case <-fixture.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("native Wiki model HTTP did not begin")
	}
	cancel()
	select {
	case out := <-finished:
		if out.err == nil || out.candidate.ContentSHA256 != "" {
			t.Fatalf("cancelled model HTTP produced a Wiki candidate: %+v %v", out.candidate, out.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled native Wiki model did not finish")
	}
	select {
	case <-fixture.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled model HTTP handler stayed active")
	}
	if err := cancelRun.Close(); err != nil {
		t.Fatal(err)
	}
	if err := factory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := observed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var nativeRoot, nativeAgent, nativeChat, nativeValidate bool
	for _, span := range exporter.Snapshot() {
		if span.InstrumentationScope().Name != "trpc.agent.go" {
			continue
		}
		switch span.Name() {
		case "invoke_agent wiki_compile_candidate":
			nativeRoot = true
		case "invoke_agent draft_wiki_from_sources":
			nativeAgent = true
		case "chat knowledge-wiki-compiler":
			nativeChat = true
		case "workflow execute_function_node validate_wiki_candidate":
			nativeValidate = true
		}
	}
	if !nativeRoot || !nativeAgent || !nativeChat || !nativeValidate {
		t.Fatalf("official tRPC Wiki Graph/LLM/validation spans missing: %t/%t/%t/%t",
			nativeRoot, nativeAgent, nativeChat, nativeValidate)
	}
	if bytes.Contains(logs.Snapshot(), []byte(a.NativeBearer())) ||
		bytes.Contains(logs.Snapshot(), []byte(wikiNativeProposal)) {
		t.Fatal("Wiki bearer or model prose entered application logs")
	}
}

func TestWikiNativeFactoryCloseWaitsForValidatedCandidateSink(t *testing.T) {
	if os.Getenv("SEA_WIKI_NATIVE_FACTORY_CLOSE_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWikiNativeFactoryCloseWaitsForValidatedCandidateSink$")
		cmd.Env = append(os.Environ(), "SEA_WIKI_NATIVE_FACTORY_CLOSE_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native Wiki Close/Sink child failed: %v\n%s", err, output)
		}
		return
	}
	a := wikiFactorySession(t, "11111111-1111-4111-8111-111111111111", 7)
	fixture := &wikiNativeDCFixture{t: t, bearer: a.NativeBearer(), receipts: map[string][]byte{},
		bodyHashes: map[string]string{}, shutdown: make(chan struct{})}
	server := httptest.NewServer(fixture)
	defer server.Close()
	defer close(fixture.shutdown)
	provider := WikiCompileNativeSessionFunc(func(context.Context) (WikiCompileModelSession, error) { return a, nil })
	factory, err := NewWikiCompileNativeRunFactory(WikiCompileNativeRunFactoryConfig{DCGatewayRoot: server.URL}, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	if _, err := factory.CurrentNativeSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	var logs wikiNativeLogBuffer
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-wiki-native-close",
		Environment: "test", Version: "fixture-v1", InstanceID: "factory-close", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: &factSpanExporter{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	defer observed.Close(context.Background())
	if err := factory.BindTelemetry(observed); err != nil {
		t.Fatal(err)
	}
	input := wikiFactoryInput()
	run, err := factory.Open(context.Background(), input, a)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce atomic.Bool
	finishSink := func() {
		if releaseOnce.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer finishSink()
	finished := make(chan error, 1)
	go func() {
		_, runErr := run.Run(context.Background(), wikiNativeRunRequest(input, "job-close"),
			func(_ context.Context, e *event.Event) error {
				candidate, done, err := content.WikiCompileCandidateFromCompletion(e, input)
				if err != nil || !done || candidate.ContentSHA256 == "" {
					return ErrWikiCompileRun
				}
				close(entered)
				<-release
				return nil
			})
		finished <- runErr
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("one native Wiki candidate never reached post-EOF Sink")
	}
	closed := make(chan error, 1)
	go func() { closed <- factory.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Factory.Close returned while validated Sink was active: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	finishSink()
	select {
	case err := <-finished:
		if !errors.Is(err, ErrWikiCompileNativeFactory) {
			t.Fatalf("shutting Factory published a candidate as success: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Factory.Close did not settle blocked Sink")
	}
	select {
	case err := <-closed:
		if err != nil || run.ModelSessionProof() != a.Proof() || fixture.providers.Load() != 1 {
			t.Fatalf("Factory.Close lost frozen proof or repeated provider: %v proof=%+v providers=%d",
				err, run.ModelSessionProof(), fixture.providers.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Factory.Close remained blocked after Sink settled")
	}
}
