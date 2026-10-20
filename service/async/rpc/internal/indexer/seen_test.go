package indexer

import (
	"sync"
	"testing"
)

func TestMemSeenClaimRelease(t *testing.T) {
	s := NewMemSeen()
	if !s.Claim("evt-1") {
		t.Fatal("first claim must win")
	}
	if s.Claim("evt-1") {
		t.Fatal("second claim of the same id must lose")
	}
	if s.Len() != 1 {
		t.Fatalf("len = %d, want 1", s.Len())
	}
	s.Release("evt-1")
	if s.Len() != 0 {
		t.Fatalf("len after release = %d, want 0", s.Len())
	}
	if !s.Claim("evt-1") {
		t.Fatal("claim after release must win（失败可重试）")
	}
	s.Release("never-seen") // no-op，不得 panic
	if s.Len() != 1 {
		t.Fatalf("len = %d, want 1", s.Len())
	}
}

func TestMemSeenConcurrentClaims(t *testing.T) {
	s := NewMemSeen()
	const n = 64
	var mu sync.Mutex
	winners := 0
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.Claim("evt-race") {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("concurrent claims produced %d winners, want exactly 1", winners)
	}
	if s.Len() != 1 {
		t.Fatalf("len = %d, want 1", s.Len())
	}
}
