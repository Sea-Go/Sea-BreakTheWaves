package artifact

import (
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// atomicClock is a deterministic, thread-safe clock for Switcher tests.
type atomicClock struct{ nanos atomic.Int64 }

func (c *atomicClock) Now() time.Time      { return time.Unix(0, c.nanos.Load()) }
func (c *atomicClock) Set(t time.Time)     { c.nanos.Store(t.UnixNano()) }
func (c *atomicClock) Add(d time.Duration) { c.nanos.Add(int64(d)) }

func manifestWithKey(key string) WholeDocIndexManifest {
	m := WholeDocIndexManifest{
		ModuleID:  "mod-7",
		ReleaseID: "rel-" + key,
		Docs: []DocEntry{{
			DocKey: key, StructureRef: "s://st", DenseRef: "s://de", SparseRef: "s://sp",
			MultiRef: "s://mu", MultiTokens: 16, EncoderID: "enc-1",
			SourceChars: 100, BudgetBytes: 4096,
		}},
	}
	AssignID(&m)
	return m
}

func TestSwitcherLoadActivateLifecycle(t *testing.T) {
	base := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	clock := &atomicClock{}
	clock.Set(base)
	s := NewSwitcher(clock.Now)
	if s == nil {
		t.Fatal("NewSwitcher returned nil")
	}

	m1, m2 := manifestWithKey("a"), manifestWithKey("b")

	if _, ok := s.Current(); ok {
		t.Fatal("Current before any Activate must be false")
	}
	if err := s.Activate(m1.ManifestID); !errors.Is(err, ErrManifestNotLoaded) {
		t.Fatalf("Activate of unloaded manifest = %v, want ErrManifestNotLoaded", err)
	}

	if err := s.Load(m1); err != nil {
		t.Fatalf("Load m1: %v", err)
	}
	if _, ok := s.Current(); ok {
		t.Fatal("Load must not activate")
	}

	if err := s.Activate(m1.ManifestID); err != nil {
		t.Fatalf("Activate m1: %v", err)
	}
	cur, ok := s.Current()
	if !ok || cur.ManifestID != m1.ManifestID {
		t.Fatalf("Current = (%v, %v), want m1", cur, ok)
	}
	if got := s.Retained(); len(got) != 0 {
		t.Fatalf("Retained after first Activate = %d entries, want 0", len(got))
	}

	// Idempotent re-activation of the current manifest.
	if err := s.Activate(m1.ManifestID); err != nil {
		t.Fatalf("re-Activate m1: %v", err)
	}
	if got := s.Retained(); len(got) != 0 {
		t.Fatalf("re-activating current must not demote it, retained = %d", len(got))
	}

	if err := s.Load(m2); err != nil {
		t.Fatalf("Load m2: %v", err)
	}
	if err := s.Activate(m2.ManifestID); err != nil {
		t.Fatalf("Activate m2: %v", err)
	}
	if cur, ok := s.Current(); !ok || cur.ManifestID != m2.ManifestID {
		t.Fatalf("Current = (%v, %v), want m2", cur, ok)
	}
	got := s.Retained()
	if len(got) != 1 || got[0].ManifestID != m1.ManifestID {
		t.Fatalf("Retained = %+v, want [m1]", got)
	}

	// Retention window: kept until expiry, invisible at and after expiry.
	clock.Add(RetentionTTL - 1)
	if got := s.Retained(); len(got) != 1 {
		t.Fatalf("1ns before expiry: retained = %d, want 1", len(got))
	}
	clock.Add(1)
	if got := s.Retained(); len(got) != 0 {
		t.Fatalf("at expiry: retained = %d, want 0", len(got))
	}
}

func TestSwitcherRollBackFromRetained(t *testing.T) {
	clock := &atomicClock{}
	clock.Set(time.Unix(0, 0))
	s := NewSwitcher(clock.Now)
	m1, m2, m3 := manifestWithKey("a"), manifestWithKey("b"), manifestWithKey("c")
	for _, m := range []WholeDocIndexManifest{m1, m2, m3} {
		if err := s.Load(m); err != nil {
			t.Fatalf("Load: %v", err)
		}
	}
	for _, id := range []string{m1.ManifestID, m2.ManifestID, m3.ManifestID} {
		if err := s.Activate(id); err != nil {
			t.Fatalf("Activate: %v", err)
		}
	}
	if got := s.Retained(); len(got) != 2 {
		t.Fatalf("retained = %d, want 2", len(got))
	}
	// Roll back to m2: it leaves retained, m3 is demoted.
	if err := s.Activate(m2.ManifestID); err != nil {
		t.Fatalf("roll-back Activate: %v", err)
	}
	ids := []string{s.Retained()[0].ManifestID, s.Retained()[1].ManifestID}
	if !slices.Equal(ids, []string{m1.ManifestID, m3.ManifestID}) {
		t.Fatalf("retained after roll back = %v, want [m1, m3]", ids)
	}
	cur, _ := s.Current()
	if cur.ManifestID != m2.ManifestID {
		t.Fatalf("current = %s, want m2", cur.ManifestID)
	}
}

func TestSwitcherExpire(t *testing.T) {
	base := time.Unix(1000, 0)
	clock := &atomicClock{}
	clock.Set(base)
	s := NewSwitcher(clock.Now)
	m1, m2, m3 := manifestWithKey("a"), manifestWithKey("b"), manifestWithKey("c")
	for _, m := range []WholeDocIndexManifest{m1, m2, m3} {
		if err := s.Load(m); err != nil {
			t.Fatalf("Load: %v", err)
		}
	}
	// m1 demoted at base, m2 at base+1h.
	if err := s.Activate(m1.ManifestID); err != nil {
		t.Fatalf("Activate m1: %v", err)
	}
	if err := s.Activate(m2.ManifestID); err != nil {
		t.Fatalf("Activate m2: %v", err)
	}
	clock.Add(time.Hour)
	if err := s.Activate(m3.ManifestID); err != nil {
		t.Fatalf("Activate m3: %v", err)
	}
	if n := s.Expire(base.Add(RetentionTTL)); n != 1 {
		t.Fatalf("Expire at m1's deadline removed %d, want 1", n)
	}
	if got := s.Retained(); len(got) != 1 || got[0].ManifestID != m2.ManifestID {
		t.Fatalf("retained after expire = %+v, want [m2]", got)
	}
	if n := s.Expire(base.Add(RetentionTTL)); n != 0 {
		t.Fatalf("second Expire removed %d, want 0", n)
	}
	if n := s.Expire(base.Add(time.Hour).Add(RetentionTTL)); n != 1 {
		t.Fatalf("final Expire removed %d, want 1", n)
	}
	if got := s.Retained(); len(got) != 0 {
		t.Fatalf("retained after final expire = %d, want 0", len(got))
	}
}

func TestSwitcherLoadRejectsInvalidAndMismatched(t *testing.T) {
	s := NewSwitcher(nil) // nil clock must fall back to time.Now

	invalid := manifestWithKey("a")
	invalid.Docs[0].MultiTokens = MaxMultiTokens + 1
	if err := s.Load(invalid); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("Load(invalid) = %v, want ErrInvalidManifest", err)
	}

	mismatch := manifestWithKey("a")
	mismatch.Docs[0].BudgetBytes = 999 // body no longer matches the stored id
	if err := s.Load(mismatch); !errors.Is(err, ErrManifestIDMismatch) {
		t.Fatalf("Load(mismatch) = %v, want ErrManifestIDMismatch", err)
	}
	if err := s.Activate(mismatch.ManifestID); !errors.Is(err, ErrManifestNotLoaded) {
		t.Fatalf("Activate(mismatch) = %v, want ErrManifestNotLoaded", err)
	}
}

func TestSwitcherReturnsDeepCopies(t *testing.T) {
	s := NewSwitcher(nil)
	m1 := manifestWithKey("a")
	if err := s.Load(m1); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.Activate(m1.ManifestID); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	// Mutating the caller's manifest after Load must not affect the switcher.
	m1.Docs[0].DocKey = "tampered"
	cur, _ := s.Current()
	if cur.Docs[0].DocKey != "a" {
		t.Fatalf("switcher state tampered through Load alias: %q", cur.Docs[0].DocKey)
	}

	// Mutating a returned copy must not affect current or retained state.
	cur.Docs[0].DocKey = "mutated"
	again, _ := s.Current()
	if again.Docs[0].DocKey != "a" {
		t.Fatalf("switcher state tampered through Current alias: %q", again.Docs[0].DocKey)
	}
	m2 := manifestWithKey("b")
	if err := s.Load(m2); err != nil {
		t.Fatalf("Load m2: %v", err)
	}
	if err := s.Activate(m2.ManifestID); err != nil {
		t.Fatalf("Activate m2: %v", err)
	}
	ret := s.Retained()
	ret[0].Docs[0].DocKey = "mutated"
	if got := s.Retained()[0].Docs[0].DocKey; got != "a" {
		t.Fatalf("switcher state tampered through Retained alias: %q", got)
	}
}

// TestSwitcherConcurrent hammers Current/Retained/Activate/Expire/Load in
// parallel; run with -race to prove atomic switching.
func TestSwitcherConcurrent(t *testing.T) {
	clock := &atomicClock{}
	clock.Set(time.Unix(0, 0))
	s := NewSwitcher(clock.Now)

	ms := make([]WholeDocIndexManifest, 3)
	for i := range ms {
		ms[i] = manifestWithKey(string(rune('a' + i)))
		if err := s.Load(ms[i]); err != nil {
			t.Fatalf("Load: %v", err)
		}
	}
	if err := s.Activate(ms[0].ManifestID); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				switch g % 4 {
				case 0:
					_ = s.Activate(ms[i%len(ms)].ManifestID)
				case 1:
					if cur, ok := s.Current(); ok && cur.Docs[0].DocKey == "" {
						panic("current must keep a valid doc")
					}
				case 2:
					for _, r := range s.Retained() {
						_ = r.ManifestID
					}
				case 3:
					_ = s.Expire(time.Unix(0, 0))
				}
			}
		}(g)
	}
	wg.Wait()

	cur, ok := s.Current()
	if !ok {
		t.Fatal("a manifest must remain current after concurrent activation")
	}
	loaded := make([]string, 0, len(ms))
	for _, m := range ms {
		loaded = append(loaded, m.ManifestID)
	}
	if !slices.Contains(loaded, cur.ManifestID) {
		t.Fatalf("current id %s not among loaded manifests", cur.ManifestID)
	}
	if err := s.Activate("missing"); !errors.Is(err, ErrManifestNotLoaded) {
		t.Fatalf("Activate(missing) = %v, want ErrManifestNotLoaded", err)
	}
}
