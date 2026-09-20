package agent

import (
	"context"
	"testing"

	trpcmodel "trpc.group/trpc-go/trpc-agent-go/model"
)

type fixedModel struct{ content string }

func (m fixedModel) GenerateContent(context.Context, *trpcmodel.Request) (<-chan *trpcmodel.Response, error) {
	ch := make(chan *trpcmodel.Response, 1)
	ch <- &trpcmodel.Response{Object: "chat.completion", Done: true, Choices: []trpcmodel.Choice{{
		Message: trpcmodel.Message{Role: "assistant", Content: m.content},
	}}}
	close(ch)
	return ch, nil
}

func (m fixedModel) Info() trpcmodel.Info { return trpcmodel.Info{Name: "fixed"} }

func TestNewSearchAgentValidation(t *testing.T) {
	if _, err := NewSearchAgent(Dependencies{}); err == nil {
		t.Fatal("expected missing model error")
	}
	if _, err := NewSearchAgent(Dependencies{Model: fixedModel{"ok"}}); err == nil {
		t.Fatal("expected missing instruction error")
	}
}
