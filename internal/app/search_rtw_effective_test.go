package app

import (
	"context"
	"errors"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
)

func TestRTWEffectiveRevisionChecker(t *testing.T) {
	ref := artifacts.Reference([]byte("index"))
	w := ridethewind.SearchSnapshot{ModuleId: "module", ReleaseId: "release", Generation: 2,
		PublicationRevision: "7", ValidRevisionIds: []string{"revision"},
		Indexes: map[string]ridethewind.CitationObject{
			"dense": {Key: ref.Key, Sha256: ref.SHA256}, "sparse": {Key: ref.Key, Sha256: ref.SHA256},
			"multivector": {Key: ref.Key, Sha256: ref.SHA256},
		}}
	provider, err := NewRTWSearchSnapshotProvider(snapshotClientFunc(func(context.Context, string) (ridethewind.SearchSnapshot, error) {
		return w, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	checker, err := NewRTWEffectiveRevisionChecker(provider)
	if err != nil {
		t.Fatal(err)
	}
	fixed := searchdomain.Snapshot{ModuleID: "module", ReleaseID: "release", Generation: 2,
		PublicationRevision: "7", ValidRevisionIDs: []string{"revision"},
		Indexes: map[searchdomain.Lane]corpus.Ref{searchdomain.Dense: ref, searchdomain.Sparse: ref, searchdomain.MultiVector: ref}}
	for name, change := range map[string]func(*ridethewind.SearchSnapshot){
		"active":           nil,
		"withdrawn":        func(s *ridethewind.SearchSnapshot) { s.ValidRevisionIds = nil },
		"pointer moved":    func(s *ridethewind.SearchSnapshot) { s.PublicationRevision = "8" },
		"generation moved": func(s *ridethewind.SearchSnapshot) { s.Generation = 3 },
	} {
		t.Run(name, func(t *testing.T) {
			before := w
			if change != nil {
				change(&w)
			}
			defer func() { w = before }()
			ok, err := checker.Check(context.Background(), fixed, corpus.Chunk{RevisionID: "revision"})
			if err != nil || ok != (change == nil) {
				t.Fatalf("effective check = %v, %v", ok, err)
			}
		})
	}
	if _, err := NewRTWEffectiveRevisionChecker(nil); !errors.Is(err, ErrRTWEffectiveRevision) {
		t.Fatalf("missing provider accepted: %v", err)
	}
}
