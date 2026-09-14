package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

func runtimeRequest() Request {
	return Request{Subject: SubjectRef{"authority", "tenant", "subject"}, SessionID: "session", RunID: "run", Message: model.NewUserMessage("hello")}
}
func TestOpenAIModelRunnerLifecycle(t *testing.T) {
	for _, mode := range []string{"stream", "plain", "truncated", "api_error", "cancel", "sink_error"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			exited := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				defer close(exited)
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("model gateway routing")
				}
				switch mode {
				case "plain":
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"id":"r","object":"chat.completion","model":"chat-fixture","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
				case "api_error":
					w.WriteHeader(429)
					io.WriteString(w, `{"error":{"message":"fixture quota","type":"rate_limit_error"}}`)
				default:
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"id\":\"r\",\"object\":\"chat.completion.chunk\",\"model\":\"chat-fixture\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ans\"},\"finish_reason\":null}]}\n\n")
					w.(http.Flusher).Flush()
					if mode == "cancel" || mode == "sink_error" {
						<-r.Context().Done()
						return
					}
					if mode == "truncated" {
						return
					}
					io.WriteString(w, "data: {\"id\":\"r\",\"object\":\"chat.completion.chunk\",\"model\":\"chat-fixture\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"wer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}
			}))
			defer server.Close()
			m, e := NewModel(ModelConfig{BaseURL: server.URL, Token: "fixture-token", CallPoint: "callpoint"})
			if e != nil {
				t.Fatal(e)
			}
			a := llmagent.New("assistant", llmagent.WithModel(m), llmagent.WithGenerationConfig(model.GenerationConfig{Stream: mode != "plain"}))
			sessions := inmemory.NewSessionService()
			defer sessions.Close()
			r, e := New("runtime-test", a, sessions, observedForTest(t))
			if e != nil {
				t.Fatal(e)
			}
			defer r.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var deltas, full strings.Builder
			sinkErr := errors.New("sink stopped")
			result, e := r.Run(ctx, runtimeRequest(), func(_ context.Context, ev *event.Event) error {
				if ev.Response == nil {
					return nil
				}
				for _, choice := range ev.Choices {
					if ev.IsPartial {
						deltas.WriteString(choice.Delta.Content)
					} else if choice.Message.Role == model.RoleAssistant {
						full.WriteString(choice.Message.Content)
					}
				}
				if ev.IsPartial && mode == "cancel" {
					cancel()
				}
				if ev.IsPartial && mode == "sink_error" {
					return sinkErr
				}
				return nil
			})
			switch mode {
			case "stream", "plain":
				if e != nil || !result.Completed || full.String() != "answer" {
					t.Fatalf("result %+v full=%q err=%v", result, full.String(), e)
				}
				if mode == "stream" && deltas.String() != "answer" {
					t.Fatal("lost ordinary deltas", deltas.String())
				}
			case "cancel":
				if !errors.Is(e, context.Canceled) {
					t.Fatalf("cancel swallowed: %v", e)
				}
			case "sink_error":
				if !errors.Is(e, sinkErr) {
					t.Fatalf("sink error swallowed: %v", e)
				}
			default:
				if e == nil {
					t.Fatalf("%s accepted as successful completion %+v", mode, result)
				}
			}
			select {
			case <-exited:
			case <-time.After(time.Second):
				t.Fatal("model handler still running after Run returned")
			}
			if calls.Load() != 1 {
				t.Fatal("unexpected automatic model retry", calls.Load())
			}
		})
	}
}

type runnerStub struct{ events []*event.Event }

func (s runnerStub) Run(ctx context.Context, _ string, _ string, _ model.Message, _ ...agent.RunOption) (<-chan *event.Event, error) {
	ch := make(chan *event.Event, len(s.events))
	for _, e := range s.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}
func (s runnerStub) Close() error { return nil }
func completion() *event.Event {
	return &event.Event{RequestID: "run", Response: &model.Response{Done: true, Object: model.ObjectTypeRunnerCompletion}}
}
func TestEOFAndTerminalErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []*event.Event
		want   error
	}{
		{"missing", []*event.Event{{RequestID: "run", Response: &model.Response{Done: true, Object: model.ObjectTypeChatCompletion}}}, ErrIncomplete},
		{"duplicate", []*event.Event{completion(), completion()}, ErrDuplicateCompletion},
		{"error_then_completion", []*event.Event{{RequestID: "run", Response: &model.Response{Done: true, Error: &model.ResponseError{Type: "test", Message: "terminal"}}}, completion()}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runtime{runner: runnerStub{tc.events}, active: map[string]context.CancelFunc{}, observed: observedForTest(t)}
			_, err := r.Run(context.Background(), runtimeRequest(), nil)
			if err == nil {
				t.Fatal("accepted invalid stream")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error %v", err)
			}
		})
	}
}
func TestSubjectScopeNoDelimiterCollision(t *testing.T) {
	a, _ := (SubjectRef{"a:b", "c", "d"}).UserKey()
	b, _ := (SubjectRef{"a", "b:c", "d"}).UserKey()
	if a == b {
		t.Fatal("subject collision")
	}
	if _, e := (SubjectRef{SubjectID: "s"}).UserKey(); e == nil {
		t.Fatal("partial subject accepted")
	}
}

type sessionFailure struct{ session.Service }

func (s sessionFailure) AppendEvent(ctx context.Context, sess *session.Session, e *event.Event, opts ...session.Option) error {
	if e.IsUserMessage() {
		return s.Service.AppendEvent(ctx, sess, e, opts...)
	}
	return errors.New("fixture persistence failed")
}

type responseModel struct{}

func (responseModel) Info() model.Info { return model.Info{Name: "fixture"} }
func (responseModel) GenerateContent(context.Context, *model.Request) (<-chan *model.Response, error) {
	ch := make(chan *model.Response, 1)
	finish := "stop"
	ch <- &model.Response{Done: true, Choices: []model.Choice{{Message: model.NewAssistantMessage("ok"), FinishReason: &finish}}}
	close(ch)
	return ch, nil
}
func TestSessionFailureCannotBecomeSuccess(t *testing.T) {
	base := inmemory.NewSessionService()
	defer base.Close()
	a := llmagent.New("assistant", llmagent.WithModel(responseModel{}))
	r, e := New("app", a, sessionFailure{base}, observedForTest(t))
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	_, e = r.Run(context.Background(), runtimeRequest(), nil)
	if e == nil || !strings.Contains(fmt.Sprint(e), "fixture persistence failed") {
		t.Fatalf("persistence failure swallowed: %v", e)
	}
}
