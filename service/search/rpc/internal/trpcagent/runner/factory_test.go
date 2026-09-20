package runner

import (
	"context"
	"testing"

	trpcmodel "trpc.group/trpc-go/trpc-agent-go/model"
	runnerframework "trpc.group/trpc-go/trpc-agent-go/runner"

	searchagent "sea/service/search/rpc/internal/trpcagent/agent"
)

type model struct{ content string }

func (m model) GenerateContent(context.Context, *trpcmodel.Request) (<-chan *trpcmodel.Response, error) {
	ch := make(chan *trpcmodel.Response, 1)
	ch <- &trpcmodel.Response{Object: "chat.completion", Done: true, Choices: []trpcmodel.Choice{{
		Message: trpcmodel.Message{Role: "assistant", Content: m.content},
	}}}
	close(ch)
	return ch, nil
}

func (m model) Info() trpcmodel.Info { return trpcmodel.Info{Name: "test"} }

func TestSearchRunnerProducesTerminalEvent(t *testing.T) {
	ag, err := searchagent.NewSearchAgent(searchagent.Dependencies{Model: model{"ok"}, Instruction: "answer"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(Dependencies{AppName: "search-test", Agent: ag})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	events, err := r.Run(context.Background(), "user", "session", trpcmodel.NewUserMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	var sawRunnerDone bool
	for event := range events {
		if event.IsError() {
			t.Fatalf("runner event error")
		}
		if event.IsRunnerCompletion() {
			sawRunnerDone = true
		}
	}
	if !sawRunnerDone {
		t.Fatal("runner completion event missing")
	}
}

func TestSearchRunnerValidation(t *testing.T) {
	if _, err := New(Dependencies{}); err == nil {
		t.Fatal("expected app name error")
	}
	if _, err := New(Dependencies{AppName: "x"}); err == nil {
		t.Fatal("expected agent error")
	}
	_ = runnerframework.NewRunner
}
