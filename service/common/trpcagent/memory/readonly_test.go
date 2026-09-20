package memory

import (
	"context"
	"errors"
	"testing"

	trpcmemory "trpc.group/trpc-go/trpc-agent-go/memory"
)

type reader struct{ err error }

func (r reader) Read(context.Context, string, string, int) ([]trpcmemory.Entry, error) {
	if r.err != nil {
		return nil, r.err
	}
	return []trpcmemory.Entry{{ID: "one"}}, nil
}

func TestReadOnlyMemoryRejectsWrites(t *testing.T) {
	svc, err := NewReadOnly(reader{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AddMemory(context.Background(), trpcmemory.UserKey{AppName: "app", UserID: "u"}, "x", nil); err == nil {
		t.Fatal("expected write rejection")
	}
	got, err := svc.ReadMemories(context.Background(), trpcmemory.UserKey{AppName: "app", UserID: "u"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "one" {
		t.Fatalf("unexpected memories: %+v", got)
	}
}

func TestReadOnlyMemoryRequiresReader(t *testing.T) {
	if _, err := NewReadOnly(nil); err == nil {
		t.Fatal("expected error")
	}
	if _, err := NewReadOnly(reader{err: errors.New("down")}); err != nil {
		t.Fatal(err)
	}
}
