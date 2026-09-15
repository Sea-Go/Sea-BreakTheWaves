package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

func wikiFixtureBearer() string {
	return "wh_access_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
}

func wikiFixtureRef(t *testing.T) WikiCompileModelInvocationRef {
	t.Helper()
	sourceSHA, err := WikiCompileSourceRefsSHA([]WikiCompileSourceIdentity{
		{RevisionID: "source-r1", ContentHash: strings.Repeat("a", 64)},
		{RevisionID: "source-r2", ContentHash: strings.Repeat("b", 64)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return WikiCompileModelInvocationRef{
		CompileID: "compile-17", InputHash: strings.Repeat("c", 64),
		Generation: 7, SourceRefsSHA: sourceSHA,
		ModelStage: WikiCompileModelStageWikiDraft,
	}
}

func TestWikiCallPointSourceRefsUsesJCSGolden(t *testing.T) {
	refs := []WikiCompileSourceIdentity{
		{RevisionID: "source-r1", ContentHash: strings.Repeat("a", 64)},
		{RevisionID: "source-r2", ContentHash: strings.Repeat("b", 64)},
	}
	got, err := WikiCompileSourceRefsSHA(refs)
	const want = "05ff2bbbe82f0503f5ced13557cd20f9b9019b640c906e22513b7d6a350e52d5"
	const oldStructOrder = "3551dbc842fa54b29b29c9137b65529b15e1d706a90b92828e0d4e9fa61bf908"
	if err != nil || got != want || got == oldStructOrder {
		t.Fatalf("source identity hash is Go struct order rather than JCS: got=%q err=%v", got, err)
	}
	raw, err := json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte(`[{"revision_id":`)) {
		t.Fatal("red fixture stopped representing the original Go struct order")
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	expected := `[{"content_hash":"` + strings.Repeat("a", 64) +
		`","revision_id":"source-r1"},{"content_hash":"` + strings.Repeat("b", 64) +
		`","revision_id":"source-r2"}]`
	if string(canonical) != expected {
		t.Fatal("JCS changed member order or RTW frozen source sequence")
	}
}

func wikiFixtureRequest(content string) *model.Request {
	return &model.Request{Messages: []model.Message{
		model.NewSystemMessage("Compile the frozen wiki evidence."),
		model.NewUserMessage(content),
	}, GenerationConfig: model.GenerationConfig{MaxTokens: model.IntPtr(1024), Stream: false}}
}

func collectWikiModel(t *testing.T, ctx context.Context, m model.Model,
	request *model.Request) ([]*model.Response, error) {
	t.Helper()
	ch, err := m.GenerateContent(ctx, request)
	if err != nil {
		return nil, err
	}
	var responses []*model.Response
	select {
	case <-ctx.Done():
		for response := range ch {
			responses = append(responses, response)
		}
		return responses, ctx.Err()
	default:
	}
	for response := range ch {
		responses = append(responses, response)
	}
	return responses, nil
}

func wikiModelSucceeded(responses []*model.Response) bool {
	for _, r := range responses {
		if r != nil && r.Done && r.Error == nil && len(r.Choices) == 1 &&
			r.Choices[0].FinishReason != nil && *r.Choices[0].FinishReason == "stop" {
			return true
		}
	}
	return false
}

func wikiCompletion() []byte {
	return []byte(`{"id":"wiki-r","object":"chat.completion","model":"knowledge-wiki-compiler","choices":[{"index":0,"message":{"role":"assistant","content":"{\"title\":\"Wiki draft\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":80,"completion_tokens":16,"total_tokens":96}}`)
}

type wikiGatewayFixture struct {
	t                   *testing.T
	mu                  sync.Mutex
	receipts            map[string][]byte
	bodyHashes          map[string]string
	calls               atomic.Int64
	providers           atomic.Int64
	lostFirst           atomic.Bool
	expectedTraceparent string
}

func (f *wikiGatewayFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/fixture/receipts/") {
		key := strings.TrimPrefix(r.URL.Path, "/fixture/receipts/")
		f.mu.Lock()
		receipt := append([]byte(nil), f.receipts[key]...)
		f.mu.Unlock()
		if len(receipt) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(receipt)
		return
	}
	f.calls.Add(1)
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" ||
		r.Header.Get("Authorization") != "Bearer "+wikiFixtureBearer() ||
		len(r.Header.Values("X-Sea-Model-Callpoint")) != 1 ||
		r.Header.Get("X-Sea-Model-Callpoint") != WikiCompileCallPoint ||
		len(r.Header.Values("Idempotency-Key")) != 1 ||
		r.Header.Get("X-Sea-Model-Output-Tokens") != "1024" ||
		r.Header.Get("X-WhaleHall-Model-Agent") != "" {
		f.t.Errorf("model adapter sent a different DC native/callpoint budget wire: method=%s path=%s", r.Method, r.URL.Path)
		http.Error(w, "bad adapter wire", http.StatusBadRequest)
		return
	}
	if f.expectedTraceparent != "" && r.Header.Get("Traceparent") != f.expectedTraceparent {
		f.t.Error("native Wiki Graph trace context did not reach DC model gateway")
		http.Error(w, "missing traceparent", http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !strings.HasPrefix(key, "wiki-compile-") || len(key) != len("wiki-compile-")+64 {
		f.t.Error("noncanonical stable model key")
		http.Error(w, "bad key", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, wikiCompileMaxWireBytes+1))
	if err != nil || len(body) > wikiCompileMaxWireBytes {
		f.t.Error("unbounded model body")
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var wire struct {
		Model               string `json:"model"`
		Messages            []any  `json:"messages"`
		MaxCompletionTokens int    `json:"max_completion_tokens"`
	}
	if json.Unmarshal(body, &wire) != nil || wire.Model != WikiCompileCallPoint ||
		len(wire.Messages) != 2 || wire.MaxCompletionTokens != 1024 {
		f.t.Error("SDK did not serialize the exact logical model and max_completion_tokens")
		http.Error(w, "bad SDK body", http.StatusBadRequest)
		return
	}
	inputBudget := strconv.FormatInt(int64(len(body))+512+64*int64(len(wire.Messages)), 10)
	if r.Header.Get("X-Sea-Model-Input-Tokens") != inputBudget ||
		r.Header.Get("X-Sea-Model-Context-Tokens") != inputBudget {
		f.t.Errorf("caller-declared budgets do not match actual SDK bytes: input=%s context=%s want=%s",
			r.Header.Get("X-Sea-Model-Input-Tokens"), r.Header.Get("X-Sea-Model-Context-Tokens"), inputBudget)
		http.Error(w, "bad budget", http.StatusBadRequest)
		return
	}
	digest := sha256.Sum256(body)
	bodyHash := hex.EncodeToString(digest[:])
	f.mu.Lock()
	if old, ok := f.bodyHashes[key]; ok && old != bodyHash {
		f.mu.Unlock()
		http.Error(w, "same key different model body", http.StatusConflict)
		return
	}
	receipt := f.receipts[key]
	first := len(receipt) == 0
	if first {
		f.providers.Add(1)
		receipt = wikiCompletion()
		f.receipts[key] = append([]byte(nil), receipt...)
		f.bodyHashes[key] = bodyHash
	}
	f.mu.Unlock()
	if first && f.lostFirst.CompareAndSwap(true, false) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			f.t.Error(err)
			return
		}
		_ = conn.Close() // Durable fixture receipt exists; HTTP reply is lost.
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(receipt)
}

func TestWikiCallPointOfficialAdapterLostReplyAndReplay(t *testing.T) {
	previousPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(previousPropagator)
	traceID := trace.TraceID{}
	spanID := trace.SpanID{}
	copy(traceID[:], bytes.Repeat([]byte{0x11}, len(traceID)))
	copy(spanID[:], bytes.Repeat([]byte{0x22}, len(spanID)))
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	})
	fixture := &wikiGatewayFixture{t: t, receipts: map[string][]byte{},
		bodyHashes:          map[string]string{},
		expectedTraceparent: "00-" + traceID.String() + "-" + spanID.String() + "-01"}
	fixture.lostFirst.Store(true)
	server := httptest.NewServer(fixture)
	defer server.Close()
	cfg := WikiCompileCallPointModelConfig{BaseURL: server.URL,
		NativeBearer: wikiFixtureBearer(), CallPoint: WikiCompileCallPoint}
	ref := wikiFixtureRef(t)
	m, err := NewWikiCompileCallPointModel(cfg, ref)
	if err != nil || m.Info().Name != WikiCompileCallPoint {
		t.Fatalf("official callpoint model constructor failed: %v", err)
	}
	ctx, cancel := context.WithTimeout(trace.ContextWithSpanContext(context.Background(), spanContext), 8*time.Second)
	defer cancel()
	first, err := collectWikiModel(t, ctx, m, wikiFixtureRequest("Frozen source content."))
	if err != nil || wikiModelSucceeded(first) || fixture.calls.Load() != 1 ||
		fixture.providers.Load() != 1 {
		t.Fatalf("lost reply caused hidden retry or accepted a candidate: err=%v calls=%d providers=%d",
			err, fixture.calls.Load(), fixture.providers.Load())
	}
	fixture.mu.Lock()
	var key string
	for candidate := range fixture.receipts {
		key = candidate
	}
	fixture.mu.Unlock()
	get, err := http.Get(server.URL + "/fixture/receipts/" + key)
	if err != nil {
		t.Fatal(err)
	}
	original, err := io.ReadAll(get.Body)
	get.Body.Close()
	if err != nil || get.StatusCode != http.StatusOK ||
		!bytes.Equal(original, wikiCompletion()) {
		t.Fatal("durable fixture Get did not preserve the lost reply")
	}
	replayed, err := collectWikiModel(t, ctx, m, wikiFixtureRequest("Frozen source content."))
	if err != nil || !wikiModelSucceeded(replayed) || fixture.calls.Load() != 2 ||
		fixture.providers.Load() != 1 {
		t.Fatalf("same logical compile did not replay one provider receipt: err=%v calls=%d providers=%d",
			err, fixture.calls.Load(), fixture.providers.Load())
	}
	different, err := collectWikiModel(t, ctx, m,
		wikiFixtureRequest("Altered source content under the same CompileID."))
	if err != nil || wikiModelSucceeded(different) || fixture.calls.Load() != 2 ||
		fixture.providers.Load() != 1 {
		t.Fatalf("same-key different body reached provider: err=%v calls=%d providers=%d",
			err, fixture.calls.Load(), fixture.providers.Load())
	}
	var conflict bool
	for _, response := range different {
		if response != nil && response.Error != nil &&
			strings.Contains(response.Error.Message, ErrWikiCompileInvocationConflict.Error()) {
			conflict = true
		}
	}
	if !conflict {
		t.Fatal("same-key different body was not reported as a typed model error")
	}
	restarted, err := NewWikiCompileCallPointModel(cfg, ref)
	if err != nil {
		t.Fatal(err)
	}
	again, err := collectWikiModel(t, ctx, restarted, wikiFixtureRequest("Frozen source content."))
	if err != nil || !wikiModelSucceeded(again) || fixture.calls.Load() != 3 ||
		fixture.providers.Load() != 1 {
		t.Fatal("a new worker/model instance did not use the same durable replay key")
	}
}

func TestWikiCallPointRejectsUnissuedScopeAndBadBudgetBeforeNetwork(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected outbound request", http.StatusBadRequest)
	}))
	defer server.Close()
	cfg := WikiCompileCallPointModelConfig{BaseURL: server.URL,
		NativeBearer: wikiFixtureBearer(), CallPoint: WikiCompileCallPoint}
	ref := wikiFixtureRef(t)
	for _, bad := range []WikiCompileCallPointModelConfig{
		{BaseURL: server.URL, NativeBearer: "dc-jobs-service-token", CallPoint: WikiCompileCallPoint},
		{BaseURL: server.URL, NativeBearer: "eyJhbGciOiJIUzI1NiJ9.payload.signature", CallPoint: WikiCompileCallPoint},
		{BaseURL: server.URL, NativeBearer: "wh_access_bad", CallPoint: WikiCompileCallPoint},
		{BaseURL: server.URL, NativeBearer: wikiFixtureBearer(), CallPoint: "search-summary"},
		{BaseURL: server.URL + "?redirect=1", NativeBearer: wikiFixtureBearer(), CallPoint: WikiCompileCallPoint},
	} {
		if _, err := NewWikiCompileCallPointModel(bad, ref); !errors.Is(err, ErrWikiCompileCallPoint) {
			t.Fatalf("invalid native session/callpoint reached model constructor: %v", err)
		}
	}
	wrong := ref
	wrong.SourceRefsSHA = strings.Repeat("0", 63)
	if _, err := NewWikiCompileCallPointModel(cfg, wrong); !errors.Is(err, ErrWikiCompileCallPoint) {
		t.Fatalf("invalid frozen source fingerprint accepted: %v", err)
	}
	m, err := NewWikiCompileCallPointModel(cfg, ref)
	if err != nil {
		t.Fatal(err)
	}
	q := wikiFixtureRequest("Fixed content")
	q.MaxTokens = model.IntPtr(1023)
	if _, err := m.GenerateContent(context.Background(), q); !errors.Is(err, ErrWikiCompileCallPoint) {
		t.Fatalf("output budget mismatch reached network: %v", err)
	}
	q = wikiFixtureRequest("Fixed content")
	q.Stream = true
	if _, err := m.GenerateContent(context.Background(), q); !errors.Is(err, ErrWikiCompileCallPoint) {
		t.Fatalf("Wiki draft escaped nonstream budget: %v", err)
	}
	q = wikiFixtureRequest("Fixed content")
	q.Messages[1].ContentParts = []model.ContentPart{{Type: model.ContentTypeImage}}
	if _, err := m.GenerateContent(context.Background(), q); !errors.Is(err, ErrWikiCompileCallPoint) {
		t.Fatalf("nontext input escaped conservative budget: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("constructor/request reject performed %d outbound calls", calls.Load())
	}
	first, err := WikiCompileSourceRefsSHA([]WikiCompileSourceIdentity{
		{RevisionID: "source-r1", ContentHash: strings.Repeat("a", 64)},
		{RevisionID: "source-r2", ContentHash: strings.Repeat("b", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	reordered, err := WikiCompileSourceRefsSHA([]WikiCompileSourceIdentity{
		{RevisionID: "source-r2", ContentHash: strings.Repeat("b", 64)},
		{RevisionID: "source-r1", ContentHash: strings.Repeat("a", 64)}})
	if err != nil || first == reordered {
		t.Fatal("source revision order did not contribute to frozen invocation")
	}
	if _, err := WikiCompileSourceRefsSHA([]WikiCompileSourceIdentity{
		{RevisionID: "source-r1", ContentHash: strings.Repeat("a", 64)},
		{RevisionID: "source-r1", ContentHash: strings.Repeat("a", 64)}}); err == nil {
		t.Fatal("duplicate source revision escaped the canonical ref")
	}
}

func TestWikiCallPointEmptyCandidateAndCancellationRemainFailures(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"model callpoint is not configured","type":"not-configured"}}`)
	}))
	defer server.Close()
	m, err := NewWikiCompileCallPointModel(WikiCompileCallPointModelConfig{
		BaseURL: server.URL, NativeBearer: wikiFixtureBearer(), CallPoint: WikiCompileCallPoint,
	}, wikiFixtureRef(t))
	if err != nil {
		t.Fatal(err)
	}
	responses, err := collectWikiModel(t, context.Background(), m, wikiFixtureRequest("Fixed wiki source"))
	if err != nil || wikiModelSucceeded(responses) || calls.Load() != 1 {
		t.Fatalf("empty callpoint candidate produced a Wiki draft: err=%v calls=%d", err, calls.Load())
	}
	var failed bool
	for _, response := range responses {
		if response != nil && response.Error != nil {
			failed = true
		}
	}
	if !failed {
		t.Fatal("DC 503 was not delivered as a terminal model error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	afterCancel, _ := collectWikiModel(t, ctx, m, wikiFixtureRequest("Fixed wiki source"))
	if wikiModelSucceeded(afterCancel) || calls.Load() != 1 {
		t.Fatal("canceled Wiki compile leaked an additional provider call or candidate")
	}
}

func TestWikiCallPointActiveCancellationAndRedirectDoNotYieldCandidate(t *testing.T) {
	entered := make(chan struct{}, 1)
	finished := make(chan struct{}, 1)
	shutdown := make(chan struct{})
	var activeCalls atomic.Int64
	active := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		activeCalls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-shutdown:
		}
		finished <- struct{}{}
	}))
	defer active.Close()
	defer close(shutdown)
	m, err := NewWikiCompileCallPointModel(WikiCompileCallPointModelConfig{
		BaseURL: active.URL, NativeBearer: wikiFixtureBearer(), CallPoint: WikiCompileCallPoint,
	}, wikiFixtureRef(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := m.GenerateContent(ctx, wikiFixtureRequest("Fixed wiki source"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("active model request did not reach the one provider fixture")
	}
	cancel()
	var responses []*model.Response
	for response := range ch {
		responses = append(responses, response)
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation left a model handler running")
	}
	if wikiModelSucceeded(responses) || activeCalls.Load() != 1 {
		t.Fatal("canceled model call leaked a Wiki candidate or retried")
	}
	var followed atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Add(1)
		_, _ = w.Write(wikiCompletion())
	}))
	defer target.Close()
	var redirects atomic.Int64
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirects.Add(1)
		http.Redirect(w, r, target.URL+"/v1/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	redirectModel, err := NewWikiCompileCallPointModel(WikiCompileCallPointModelConfig{
		BaseURL: redirect.URL, NativeBearer: wikiFixtureBearer(), CallPoint: WikiCompileCallPoint,
	}, wikiFixtureRef(t))
	if err != nil {
		t.Fatal(err)
	}
	denied, err := collectWikiModel(t, context.Background(), redirectModel,
		wikiFixtureRequest("Fixed wiki source"))
	if err != nil || wikiModelSucceeded(denied) || redirects.Load() != 1 ||
		followed.Load() != 0 {
		t.Fatalf("SDK or HTTP client followed a redirected model mutation: err=%v redirects=%d followed=%d",
			err, redirects.Load(), followed.Load())
	}
}
