package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

// Each phase is a separate OS process, with a fresh framework service/connection.
// A durable tool result is loaded as history; restoring must not execute it again.
func TestPostgresProcessRecovery(t *testing.T) {
	dsn := os.Getenv("SEA_RUNTIME_TEST_DSN")
	if dsn == "" {
		t.Skip("SEA_RUNTIME_TEST_DSN points to a task-only local PostgreSQL")
	}
	u, e := url.Parse(dsn)
	if e != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("isolated loopback PostgreSQL required")
	}
	if phase := os.Getenv("SEA_RUNTIME_PHASE"); phase != "" {
		recoveryPhase(t, dsn, phase)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	schema := fmt.Sprintf("runtime_%d", time.Now().UnixNano())
	if _, e = pool.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	defer pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	var effects atomic.Int32
	var restored atomic.Bool
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/effect" {
			effects.Add(1)
			io.WriteString(w, `{"saved":"effect-1"}`)
			return
		}
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}
			Model string `json:"model"`
		}
		if e := json.NewDecoder(r.Body).Decode(&request); e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		if r.URL.Path != "/v1/chat/completions" || request.Model != "recovery-fixture" {
			t.Error("model boundary")
		}
		lastUser := ""
		hasFirst := false
		hasTool := false
		hasAnswer := false
		for _, m := range request.Messages {
			if m.Role == "user" {
				lastUser = m.Content
				hasFirst = hasFirst || m.Content == "first"
			}
			hasTool = hasTool || (m.Role == "tool" && strings.Contains(m.Content, "effect-1"))
			hasAnswer = hasAnswer || (m.Role == "assistant" && m.Content == "saved")
		}
		w.Header().Set("Content-Type", "application/json")
		if lastUser == "first" && !hasTool {
			io.WriteString(w, `{"id":"call","object":"chat.completion","model":"fixture","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"effect-call","type":"function","function":{"name":"save_effect","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		answer := "saved"
		if lastUser == "second" {
			restored.Store(hasFirst && hasTool && hasAnswer)
			answer = "restored"
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "answer", "object": "chat.completion", "model": "fixture", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": answer}, "finish_reason": "stop"}}})
	}))
	defer fixture.Close()
	for _, phase := range []string{"first", "second"} {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPostgresProcessRecovery$", "-test.v")
		cmd.Env = append(os.Environ(), "SEA_RUNTIME_PHASE="+phase, "SEA_RUNTIME_SCHEMA="+schema, "SEA_RUNTIME_FIXTURE_URL="+fixture.URL)
		output, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("phase %s: %v\n%s", phase, e, output)
		}
		t.Logf("separate process phase %s passed", phase)
	}
	if effects.Load() != 1 || !restored.Load() {
		t.Fatalf("effects=%d restored=%v", effects.Load(), restored.Load())
	}
	var events int
	if e := pool.QueryRow(ctx, "SELECT count(*) FROM "+schema+".sea_session_events").Scan(&events); e != nil || events < 4 {
		t.Fatalf("persisted events=%d err=%v", events, e)
	}
	t.Logf("PostgreSQL stored %d events; one tool side effect across two processes", events)
}
func recoveryPhase(t *testing.T, dsn, phase string) {
	fixture := os.Getenv("SEA_RUNTIME_FIXTURE_URL")
	m, e := NewModel(ModelConfig{BaseURL: fixture, Token: "fixture", CallPoint: "recovery-fixture"})
	if e != nil {
		t.Fatal(e)
	}
	type effectInput struct{}
	type effectOutput struct {
		Saved string `json:"saved"`
	}
	effect := function.NewFunctionTool(func(ctx context.Context, _ effectInput) (effectOutput, error) {
		q, e := http.NewRequestWithContext(ctx, "POST", fixture+"/effect", nil)
		if e != nil {
			return effectOutput{}, e
		}
		r, e := http.DefaultClient.Do(q)
		if e != nil {
			return effectOutput{}, e
		}
		defer r.Body.Close()
		var out effectOutput
		e = json.NewDecoder(r.Body).Decode(&out)
		return out, e
	}, function.WithName("save_effect"), function.WithDescription("Record a fixture-only side effect."))
	a := llmagent.New("assistant", llmagent.WithModel(m), llmagent.WithTools([]tool.Tool{effect}), llmagent.WithGenerationConfig(model.GenerationConfig{Stream: false}))
	r, e := OpenPostgres("runtime-recovery", a, PostgresConfig{DSN: dsn, Schema: os.Getenv("SEA_RUNTIME_SCHEMA"), TablePrefix: "sea_", Initialize: phase == "first"})
	if e != nil {
		t.Fatal(e)
	}
	q := runtimeRequest()
	q.RunID = phase
	q.Message = model.NewUserMessage(phase)
	text := ""
	result, e := r.Run(context.Background(), q, func(_ context.Context, e *event.Event) error {
		if e.Response != nil && !e.IsPartial {
			for _, c := range e.Choices {
				if c.Message.Role == model.RoleAssistant {
					text = c.Message.Content
				}
			}
		}
		return nil
	})
	if closeErr := r.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	expected := "saved"
	if phase == "second" {
		expected = "restored"
	}
	if e != nil || !result.Completed || text != expected {
		t.Fatalf("result=%+v answer=%q err=%v", result, text, e)
	}
}
