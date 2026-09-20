package memory

import (
	"context"
	"fmt"

	trpcmemory "trpc.group/trpc-go/trpc-agent-go/memory"
	trpcsession "trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// Reader is the Async-owned projection contract consumed by Search and
// Recommend. Implementations must scope every read by app and user.
type Reader interface {
	Read(ctx context.Context, appName, userID string, limit int) ([]trpcmemory.Entry, error)
}

type readOnlyService struct {
	reader Reader
	tools  []tool.Tool
}

// NewReadOnly wraps an Async-owned profile projection as framework Memory.
// Mutating framework methods fail closed: only Async may update profile state.
func NewReadOnly(reader Reader) (trpcmemory.Service, error) {
	if reader == nil {
		return nil, fmt.Errorf("memory reader is required")
	}
	return &readOnlyService{reader: reader}, nil
}

func (s *readOnlyService) mutationError() error {
	return fmt.Errorf("memory is read-only; updates belong to async service")
}

func (s *readOnlyService) AddMemory(context.Context, trpcmemory.UserKey, string, []string, ...trpcmemory.AddOption) error {
	return s.mutationError()
}
func (s *readOnlyService) UpdateMemory(context.Context, trpcmemory.Key, string, []string, ...trpcmemory.UpdateOption) error {
	return s.mutationError()
}
func (s *readOnlyService) DeleteMemory(context.Context, trpcmemory.Key) error {
	return s.mutationError()
}
func (s *readOnlyService) ClearMemories(context.Context, trpcmemory.UserKey) error {
	return s.mutationError()
}

func (s *readOnlyService) ReadMemories(ctx context.Context, userKey trpcmemory.UserKey, limit int) ([]*trpcmemory.Entry, error) {
	items, err := s.reader.Read(ctx, userKey.AppName, userKey.UserID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*trpcmemory.Entry, 0, len(items))
	for i := range items {
		item := items[i]
		out = append(out, &item)
	}
	return out, nil
}

func (s *readOnlyService) SearchMemories(ctx context.Context, userKey trpcmemory.UserKey, _ string, _ ...trpcmemory.SearchOption) ([]*trpcmemory.Entry, error) {
	return s.ReadMemories(ctx, userKey, 20)
}

func (s *readOnlyService) Tools() []tool.Tool { return s.tools }

func (s *readOnlyService) EnqueueAutoMemoryJob(context.Context, *trpcsession.Session) error {
	return s.mutationError()
}

func (s *readOnlyService) Close() error { return nil }
