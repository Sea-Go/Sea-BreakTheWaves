package artifact

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// RetentionTTL is how long a replaced manifest stays queryable in retained
// before it expires: 14 days.
const RetentionTTL = 14 * 24 * time.Hour

// ErrManifestNotLoaded marks an Activate call for a manifest that was never
// registered with Load.
var ErrManifestNotLoaded = errors.New("manifest not loaded")

// ErrManifestIDMismatch marks a Load whose self-reported manifest_id differs
// from the content-addressed recomputation.
var ErrManifestIDMismatch = errors.New("manifest_id mismatch")

type retainedManifest struct {
	manifest  WholeDocIndexManifest
	expiresAt time.Time
}

// Switcher is the double-buffered manifest switch for whole-doc index
// releases. Load only registers a manifest; Activate atomically makes a
// registered manifest current and moves the previous current into retained
// with an expiry of clock()+RetentionTTL. The zero value is not usable;
// construct with NewSwitcher. All methods are safe for concurrent use.
type Switcher struct {
	mu       sync.RWMutex
	clock    func() time.Time
	loaded   map[string]WholeDocIndexManifest
	current  *WholeDocIndexManifest
	retained []retainedManifest
}

// NewSwitcher builds a Switcher. A nil clock falls back to time.Now; tests
// inject a deterministic clock.
func NewSwitcher(clock func() time.Time) *Switcher {
	if clock == nil {
		clock = time.Now
	}
	return &Switcher{clock: clock, loaded: make(map[string]WholeDocIndexManifest)}
}

// Load registers a manifest without activating it. The manifest must pass
// Validate and its manifest_id must equal ManifestID(m); the entry is stored
// as a deep copy. Re-loading the same ID overwrites the registration.
func (s *Switcher) Load(m WholeDocIndexManifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	id := ManifestID(m)
	if m.ManifestID != id {
		return fmt.Errorf("%w: stored %q, recomputed %q", ErrManifestIDMismatch, m.ManifestID, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded[id] = copyManifest(m)
	return nil
}

// Activate atomically switches current to the manifest registered under
// manifestID, which must already have been Load-ed. The replaced current
// moves to retained with expiry clock()+RetentionTTL; activating a manifest
// that sits in retained (a roll-back) also removes it from retained.
// Activating the already-current manifest is a no-op.
func (s *Switcher) Activate(manifestID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.loaded[manifestID]
	if !ok {
		return fmt.Errorf("%w: %q", ErrManifestNotLoaded, manifestID)
	}
	if s.current != nil && s.current.ManifestID == manifestID {
		return nil
	}
	if s.current != nil {
		s.retained = append(s.retained, retainedManifest{
			manifest:  *s.current,
			expiresAt: s.clock().Add(RetentionTTL),
		})
	}
	cm := copyManifest(m)
	s.current = &cm
	s.retained = dropRetained(s.retained, manifestID)
	return nil
}

// Current returns a deep copy of the active manifest, or ok=false when no
// manifest has ever been activated.
func (s *Switcher) Current() (WholeDocIndexManifest, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == nil {
		return WholeDocIndexManifest{}, false
	}
	return copyManifest(*s.current), true
}

// Retained returns deep copies of the retained manifests that have not yet
// expired at the injected clock's now, in the order they were demoted.
func (s *Switcher) Retained() []WholeDocIndexManifest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.clock()
	out := make([]WholeDocIndexManifest, 0, len(s.retained))
	for _, r := range s.retained {
		if r.expiresAt.After(now) {
			out = append(out, copyManifest(r.manifest))
		}
	}
	return out
}

// Expire drops retained manifests whose expiry is at or before now and
// returns how many were removed.
func (s *Switcher) Expire(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.retained[:0]
	removed := 0
	for _, r := range s.retained {
		if !r.expiresAt.After(now) {
			removed++
			continue
		}
		kept = append(kept, r)
	}
	s.retained = kept
	return removed
}

// dropRetained removes every entry whose manifest ID matches id, preserving
// the order of the remaining entries.
func dropRetained(rs []retainedManifest, id string) []retainedManifest {
	out := rs[:0]
	for _, r := range rs {
		if r.manifest.ManifestID != id {
			out = append(out, r)
		}
	}
	return out
}

// copyManifest deep-copies the docs slice so callers can never mutate
// switcher state through a returned manifest.
func copyManifest(m WholeDocIndexManifest) WholeDocIndexManifest {
	out := m
	if m.Docs != nil {
		out.Docs = make([]DocEntry, len(m.Docs))
		copy(out.Docs, m.Docs)
	}
	return out
}
