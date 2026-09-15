package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	frameworktrace "trpc.group/trpc-go/trpc-agent-go/telemetry/trace"
)

func wikiCompileFixtureInput() WikiCompileInput {
	first := "甲来源的第一段。\n\n甲来源的第二段讨论后续维护。"
	second := "乙来源先定义外部知识。\n\n乙来源第二段要求引用原文。"
	return WikiCompileInput{CompileID: "compile-17", ModuleID: "module-knowledge",
		PageID: "《外部知识页》", BaseRevision: "wiki-base-3", Generation: 7,
		InputHash: strings.Repeat("a", 64), AttemptID: "attempt-9", LeaseEpoch: 4,
		CancelVersion: 2, Guidance: "只用已冻结来源编制可维护知识。",
		SourceIDs: []string{"source-rev-1", "source-rev-2"},
		Sources: []WikiCompileSource{
			{RevisionID: "source-rev-1", Kind: "source", Title: "甲来源", Content: first, SHA256: artifacts.Hash([]byte(first))},
			{RevisionID: "source-rev-2", Kind: "source", Title: "乙来源", Content: second, SHA256: artifacts.Hash([]byte(second))},
		}}
}

const wikiCompileValidProposal = `{"title":"可维护外部知识","markdown":"# 可维护外部知识\n仅记录原文支持的内容。","source_refs":[{"revision_id":"source-rev-1","locator":"paragraph:1"},{"revision_id":"source-rev-2","locator":"paragraph:2"}]}`

type wikiCompileFixtureModel struct {
	calls  atomic.Int32
	onCall func(context.Context, *model.Request) (string, error)
}

func (*wikiCompileFixtureModel) Info() model.Info {
	return model.Info{Name: "wiki-compile-fixed-fixture"}
}

func (m *wikiCompileFixtureModel) GenerateContent(ctx context.Context, q *model.Request) (<-chan *model.Response, error) {
	m.calls.Add(1)
	answer := wikiCompileValidProposal
	if m.onCall != nil {
		var err error
		answer, err = m.onCall(ctx, q)
		if err != nil {
			return nil, err
		}
	}
	responses := make(chan *model.Response, 1)
	finish := "stop"
	responses <- &model.Response{Done: true, Choices: []model.Choice{{
		Message: model.NewAssistantMessage(answer), FinishReason: &finish,
	}}}
	close(responses)
	return responses, nil
}

func runWikiCompileFixture(t *testing.T, ctx context.Context, input WikiCompileInput,
	m model.Model, sessions *inmemory.SessionService) []*event.Event {
	t.Helper()
	ag, err := NewWikiCompileGraphAgent(m)
	if err != nil {
		t.Fatal(err)
	}
	option, err := WikiCompileRunOption(input)
	if err != nil {
		t.Fatal(err)
	}
	r := runner.NewRunner("wiki-compile-fixture", ag,
		runner.WithSessionService(sessions), runner.WithPlugins(prepareErrorOwnership{}))
	defer func() {
		if err := r.Close(); err != nil {
			t.Errorf("close native Wiki Runner: %v", err)
		}
	}()
	stream, err := r.Run(ctx, "uid-1001", "compile-session", model.NewUserMessage("compile fixed sources"), option)
	if err != nil {
		t.Fatal(err)
	}
	var events []*event.Event
	for e := range stream {
		if e != nil {
			events = append(events, e)
		}
	}
	return events
}

func TestWikiCompileNativeRunnerEOFAndFixedScope(t *testing.T) {
	input := wikiCompileFixtureInput()
	m := &wikiCompileFixtureModel{onCall: func(ctx context.Context, q *model.Request) (string, error) {
		if q == nil || q.Stream || q.MaxTokens == nil || *q.MaxTokens != 1024 || len(q.Tools) != 0 ||
			!oteltrace.SpanFromContext(ctx).SpanContext().IsValid() {
			return "", fmt.Errorf("Wiki model call lacked bounded native no-tool contract")
		}
		var prompt struct {
			ModuleID string `json:"module_id"`
			PageID   string `json:"page_id"`
			Guidance string `json:"guidance"`
			Sources  []struct {
				RevisionID string `json:"revision_id"`
				Hash       string `json:"content_hash"`
				Paragraphs []struct {
					Locator string `json:"locator"`
					Text    string `json:"text"`
				} `json:"paragraphs"`
			} `json:"fixed_source_pack"`
		}
		seen := false
		for _, message := range q.Messages {
			if message.Role != model.RoleUser || json.Unmarshal([]byte(message.Content), &prompt) != nil {
				continue
			}
			seen = true
		}
		if !seen || prompt.ModuleID != input.ModuleID || prompt.PageID != input.PageID ||
			prompt.Guidance != input.Guidance || len(prompt.Sources) != 2 ||
			prompt.Sources[0].RevisionID != input.Sources[0].RevisionID ||
			prompt.Sources[0].Hash != input.Sources[0].SHA256 ||
			prompt.Sources[0].Paragraphs[0].Locator != "paragraph:1" ||
			prompt.Sources[1].RevisionID != input.Sources[1].RevisionID ||
			prompt.Sources[1].Paragraphs[1].Locator != "paragraph:2" ||
			prompt.Sources[1].Paragraphs[1].Text != "乙来源第二段要求引用原文。" {
			return "", fmt.Errorf("model prompt changed frozen RTW source paragraphs: %+v", prompt)
		}
		return wikiCompileValidProposal, nil
	}}
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	oldTracer, oldProvider := frameworktrace.Tracer, frameworktrace.TracerProvider
	frameworktrace.TracerProvider = provider
	frameworktrace.Tracer = provider.Tracer("trpc.agent.go")
	defer func() {
		frameworktrace.Tracer, frameworktrace.TracerProvider = oldTracer, oldProvider
		_ = provider.Shutdown(context.Background())
	}()
	ctx, parent := provider.Tracer("wiki-test-parent").Start(context.Background(), "wiki-parent")
	sessions := inmemory.NewSessionService()
	defer sessions.Close()
	events := runWikiCompileFixture(t, ctx, input, m, sessions)
	parent.End()
	if m.calls.Load() != 1 {
		t.Fatalf("Wiki Graph used %d model calls", m.calls.Load())
	}
	var candidate WikiCompileCandidate
	var graphDone, runnerDone int
	for _, e := range events {
		if e.IsTerminalError() {
			t.Fatalf("successful Wiki Graph emitted terminal error: %+v", e.Error)
		}
		if e.IsRunnerCompletion() {
			runnerDone++
		}
		got, done, err := WikiCompileCandidateFromCompletion(e, input)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			graphDone++
			candidate = got
		}
	}
	if graphDone != 1 || runnerDone != 1 || candidate.CompileID != input.CompileID ||
		candidate.ModuleID != input.ModuleID || candidate.PageID != input.PageID ||
		candidate.BaseRevision != input.BaseRevision || candidate.Generation != input.Generation ||
		candidate.InputHash != input.InputHash || candidate.AttemptID != input.AttemptID ||
		candidate.LeaseEpoch != input.LeaseEpoch || candidate.CancelVersion != input.CancelVersion ||
		candidate.ContentSHA256 != artifacts.Hash([]byte(candidate.Markdown)) ||
		!reflect.DeepEqual(candidate.SourceRefs, []WikiCompileSourceRef{{"source-rev-1", "paragraph:1"},
			{"source-rev-2", "paragraph:2"}}) {
		t.Fatalf("native Wiki proposal altered fixed attempt or references: %d/%d %+v", graphDone, runnerDone, candidate)
	}
	sess, err := sessions.GetSession(context.Background(), session.Key{
		AppName: "wiki-compile-fixture", UserID: "uid-1001", SessionID: "compile-session"})
	if err != nil || sess == nil || sess.UserID != "uid-1001" || sess.ID != "compile-session" {
		t.Fatalf("native Wiki run lost its fixed user/session: %+v %v", sess, err)
	}
	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, span := range spans.Ended() {
		byName[span.Name()] = span
	}
	parentSpan := byName["wiki-parent"]
	if parentSpan == nil {
		t.Fatal("native Wiki trace lacks parent")
	}
	for _, name := range []string{"invoke_agent wiki_compile_candidate",
		"workflow execute_graph wiki_compile_candidate",
		"workflow execute_function_node fix_source_paragraphs",
		"invoke_agent draft_wiki_from_sources",
		"workflow execute_function_node validate_wiki_candidate"} {
		span := byName[name]
		if span == nil || span.InstrumentationScope().Name != "trpc.agent.go" ||
			span.SpanContext().TraceID() != parentSpan.SpanContext().TraceID() {
			t.Fatalf("missing same-trace native Wiki span %q among %+v", name, byName)
		}
	}
}

func TestWikiCompileRejectsForgedJSONAndSourceRows(t *testing.T) {
	input := wikiCompileFixtureInput()
	good, err := ParseWikiCompileCandidate(wikiCompileValidProposal, input)
	if err != nil || good.ContentSHA256 != artifacts.Hash([]byte(good.Markdown)) {
		t.Fatalf("valid multi-source candidate rejected: %+v %v", good, err)
	}
	for name, raw := range map[string]string{
		"duplicate-root":       `{"title":"a","title":"b","markdown":"body","source_refs":[{"revision_id":"source-rev-1","locator":"paragraph:1"}]}`,
		"duplicate-ref":        `{"title":"a","markdown":"body","source_refs":[{"revision_id":"source-rev-1","revision_id":"source-rev-2","locator":"paragraph:1"}]}`,
		"unknown-root":         `{"title":"a","markdown":"body","compile_id":"forged","source_refs":[{"revision_id":"source-rev-1","locator":"paragraph:1"}]}`,
		"unknown-ref":          `{"title":"a","markdown":"body","source_refs":[{"revision_id":"source-rev-1","locator":"paragraph:1","source_hash":"forged"}]}`,
		"escaped-case-alias":   `{"\u0054itle":"a","markdown":"body","source_refs":[{"revision_id":"source-rev-1","locator":"paragraph:1"}]}`,
		"forged-revision":      `{"title":"a","markdown":"body","source_refs":[{"revision_id":"other-revision","locator":"paragraph:1"}]}`,
		"locator-out-of-range": `{"title":"a","markdown":"body","source_refs":[{"revision_id":"source-rev-2","locator":"paragraph:3"}]}`,
		"locator-leading-zero": `{"title":"a","markdown":"body","source_refs":[{"revision_id":"source-rev-1","locator":"paragraph:01"}]}`,
		"duplicate-source-ref": `{"title":"a","markdown":"body","source_refs":[{"revision_id":"source-rev-1","locator":"paragraph:1"},{"revision_id":"source-rev-1","locator":"paragraph:1"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if candidate, err := ParseWikiCompileCandidate(raw, input); !errors.Is(err, ErrWikiCompileContract) ||
				!reflect.DeepEqual(candidate, WikiCompileCandidate{}) {
				t.Fatalf("invalid model JSON became a Wiki candidate: %+v %v", candidate, err)
			}
		})
	}
	wrongSource := wikiCompileFixtureInput()
	wrongSource.Sources[0].SHA256 = strings.Repeat("0", 64)
	if _, err := WikiCompileRunOption(wrongSource); !errors.Is(err, ErrWikiCompileContract) {
		t.Fatalf("wrong frozen RTW source SHA reached model: %v", err)
	}
	if _, err := ParseWikiCompileCandidate(wikiCompileValidProposal, wrongSource); !errors.Is(err, ErrWikiCompileContract) {
		t.Fatalf("wrong source SHA reached candidate parser: %v", err)
	}
}

func TestWikiCompileRunOptionCopiesFixedSourcesAndCompletionRechecksRefs(t *testing.T) {
	input := wikiCompileFixtureInput()
	option, err := WikiCompileRunOption(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Sources[0].Content = "mutated after option"
	input.SourceIDs[0] = "changed-source"
	input.PageID = "changed-page"
	opts := agent.RunOptions{RuntimeState: map[string]any{"existing": "kept"}}
	option(&opts)
	fixed, ok := opts.RuntimeState[wikiCompileInputKey].(WikiCompileInput)
	if !ok || fixed.PageID != "《外部知识页》" || fixed.SourceIDs[0] != "source-rev-1" ||
		fixed.Sources[0].Content != "甲来源的第一段。\n\n甲来源的第二段讨论后续维护。" ||
		opts.RuntimeState["existing"] != "kept" {
		t.Fatalf("fixed RTW scope mutated after RunOption: %+v", opts.RuntimeState)
	}
	valid, err := ParseWikiCompileCandidate(wikiCompileValidProposal, fixed)
	if err != nil {
		t.Fatal(err)
	}
	e := graph.NewGraphCompletionEvent()
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	e.StateDelta[wikiCompileOutputKey] = raw
	if _, done, err := WikiCompileCandidateFromCompletion(e, fixed); !done || err != nil {
		t.Fatalf("native completion rejected validated candidate: %v", err)
	}
	valid.SourceRefs[0].Locator = "paragraph:99"
	raw, err = json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	e.StateDelta[wikiCompileOutputKey] = raw
	if _, done, err := WikiCompileCandidateFromCompletion(e, fixed); !done || !errors.Is(err, ErrWikiCompileContract) {
		t.Fatalf("forged completion source ref bypassed server validation: %v", err)
	}
	if _, done, err := WikiCompileCandidateFromCompletion(nil, fixed); done || err != nil {
		t.Fatal("nil Event became native Wiki completion")
	}
}

func TestWikiCompileNativeGraphRejectsModelForgeryThroughEOF(t *testing.T) {
	for name, proposed := range map[string]string{
		"forged-reference":  `{"title":"知识页","markdown":"body","source_refs":[{"revision_id":"source-rev-1","locator":"paragraph:99"}]}`,
		"unknown-scope-key": `{"title":"知识页","markdown":"body","compile_id":"other-compile","source_refs":[{"revision_id":"source-rev-1","locator":"paragraph:1"}]}`,
		"duplicate-title":   `{"title":"知识页","title":"覆盖","markdown":"body","source_refs":[{"revision_id":"source-rev-1","locator":"paragraph:1"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			input := wikiCompileFixtureInput()
			m := &wikiCompileFixtureModel{onCall: func(context.Context, *model.Request) (string, error) {
				return proposed, nil
			}}
			sessions := inmemory.NewSessionService()
			defer sessions.Close()
			events := runWikiCompileFixture(t, context.Background(), input, m, sessions)
			var terminal, runnerDone bool
			for _, e := range events {
				terminal = terminal || e.IsTerminalError()
				runnerDone = runnerDone || e.IsRunnerCompletion()
				candidate, done, err := WikiCompileCandidateFromCompletion(e, input)
				if done && err == nil {
					t.Fatalf("forged model output escaped native Graph: %+v", candidate)
				}
			}
			if !terminal || !runnerDone || m.calls.Load() != 1 {
				t.Fatalf("forged native Wiki run lacked terminal/EOF or made extra model calls: terminal=%t runner=%t calls=%d",
					terminal, runnerDone, m.calls.Load())
			}
		})
	}
}

// RTW paragraph:N locators skip whitespace-only blocks but preserve each
// nonempty source block. A leading four-space Markdown code block must reach
// the model with its indentation; its hash remains over the original CRLF bytes.
func TestWikiCompileGraphPreservesRTWCodeBlockParagraphBytes(t *testing.T) {
	const original = "    代码首行  \r\n    保留缩进第二行  \r\n\r\n \t \r\n\r\n尾段保留空格  "
	const firstParagraph = "    代码首行  \n    保留缩进第二行  "
	const secondParagraph = "尾段保留空格  "
	input := wikiCompileFixtureInput()
	input.SourceIDs = []string{"source-code-r1"}
	input.Sources = []WikiCompileSource{{RevisionID: "source-code-r1", Kind: "source",
		Title: "保留Markdown代码", Content: original, SHA256: artifacts.Hash([]byte(original))}}
	const proposal = `{"title":"代码来源页","markdown":"# 代码来源页\n保留来源原文。","source_refs":[{"revision_id":"source-code-r1","locator":"paragraph:1"},{"revision_id":"source-code-r1","locator":"paragraph:2"}]}`
	m := &wikiCompileFixtureModel{onCall: func(_ context.Context, q *model.Request) (string, error) {
		var pack struct {
			Sources []struct {
				RevisionID string `json:"revision_id"`
				Hash       string `json:"content_hash"`
				Paragraphs []struct {
					Locator string `json:"locator"`
					Text    string `json:"text"`
				} `json:"paragraphs"`
			} `json:"fixed_source_pack"`
		}
		found := false
		for _, message := range q.Messages {
			if message.Role == model.RoleUser && json.Unmarshal([]byte(message.Content), &pack) == nil {
				found = true
			}
		}
		if !found || len(pack.Sources) != 1 || pack.Sources[0].RevisionID != "source-code-r1" ||
			pack.Sources[0].Hash != artifacts.Hash([]byte(original)) || len(pack.Sources[0].Paragraphs) != 2 ||
			pack.Sources[0].Paragraphs[0].Locator != "paragraph:1" ||
			pack.Sources[0].Paragraphs[0].Text != firstParagraph ||
			pack.Sources[0].Paragraphs[1].Locator != "paragraph:2" ||
			pack.Sources[0].Paragraphs[1].Text != secondParagraph {
			return "", fmt.Errorf("RTW original paragraph bytes drifted in native Graph prompt: %+v", pack)
		}
		return proposal, nil
	}}
	sessions := inmemory.NewSessionService()
	defer sessions.Close()
	events := runWikiCompileFixture(t, context.Background(), input, m, sessions)
	var candidate WikiCompileCandidate
	var graphDone, runnerDone int
	for _, e := range events {
		if e.IsTerminalError() {
			t.Fatalf("source-preserving Wiki Graph failed: %+v", e.Error)
		}
		if e.IsRunnerCompletion() {
			runnerDone++
		}
		got, done, err := WikiCompileCandidateFromCompletion(e, input)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			graphDone++
			candidate = got
		}
	}
	if graphDone != 1 || runnerDone != 1 || m.calls.Load() != 1 ||
		!reflect.DeepEqual(candidate.SourceRefs, []WikiCompileSourceRef{{"source-code-r1", "paragraph:1"},
			{"source-code-r1", "paragraph:2"}}) ||
		candidate.ContentSHA256 != artifacts.Hash([]byte(candidate.Markdown)) {
		t.Fatalf("RTW paragraph refs did not survive native Runner EOF: graph=%d runner=%d candidate=%+v",
			graphDone, runnerDone, candidate)
	}
	for _, bad := range []string{
		strings.Replace(proposal, `"paragraph:2"`, `"paragraph:3"`, 1),
		strings.Replace(proposal, `"paragraph:1"`, `"paragraph:0"`, 1),
	} {
		if _, err := ParseWikiCompileCandidate(bad, input); !errors.Is(err, ErrWikiCompileContract) {
			t.Fatalf("unavailable RTW code-source locator became candidate: %v", err)
		}
	}
	wrongSHA := input
	wrongSHA.Sources = append([]WikiCompileSource(nil), input.Sources...)
	wrongSHA.Sources[0].SHA256 = artifacts.Hash([]byte(strings.ReplaceAll(original, "\r\n", "\n")))
	if _, err := WikiCompileRunOption(wrongSHA); !errors.Is(err, ErrWikiCompileContract) {
		t.Fatalf("normalized content hash displaced original RTW bytes: %v", err)
	}
}

func TestWikiCompileCanceledModelDoesNotProduceCandidate(t *testing.T) {
	input := wikiCompileFixtureInput()
	entered := make(chan struct{})
	m := &wikiCompileFixtureModel{onCall: func(ctx context.Context, _ *model.Request) (string, error) {
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	ag, err := NewWikiCompileGraphAgent(m)
	if err != nil {
		t.Fatal(err)
	}
	option, err := WikiCompileRunOption(input)
	if err != nil {
		t.Fatal(err)
	}
	sessions := inmemory.NewSessionService()
	defer sessions.Close()
	r := runner.NewRunner("wiki-compile-cancel", ag, runner.WithSessionService(sessions),
		runner.WithPlugins(prepareErrorOwnership{}))
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	stream, err := r.Run(ctx, "uid-1001", "compile-cancel-session", model.NewUserMessage("compile"), option)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("native Wiki model was not reached")
	}
	cancel()
	for e := range stream {
		if candidate, done, err := WikiCompileCandidateFromCompletion(e, input); done && err == nil {
			t.Fatalf("canceled Wiki model emitted an accepted candidate: %+v", candidate)
		}
	}
	if m.calls.Load() != 1 {
		t.Fatalf("canceled Wiki run used %d model calls", m.calls.Load())
	}
}
