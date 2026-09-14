package app

import (
	"context"
	"errors"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
)

type snapshotClientFunc func(context.Context, string) (ridethewind.SearchSnapshot, error)

func (f snapshotClientFunc) GetCurrentSearchSnapshot(ctx context.Context, id string) (ridethewind.SearchSnapshot, error) {
	return f(ctx, id)
}

func TestRTWSearchSnapshotProviderRejectsUntrustedOrMalformedProjection(t *testing.T) {
	ref := artifacts.Reference([]byte("fixed lane"))
	good := ridethewind.SearchSnapshot{ModuleId: "module-1", ReleaseId: "release-1", Generation: 3,
		PublicationRevision: "7", ValidRevisionIds: []string{"revision-1"},
		Indexes: map[string]ridethewind.CitationObject{
			"dense":       {Key: ref.Key, Sha256: ref.SHA256},
			"sparse":      {Key: ref.Key, Sha256: ref.SHA256},
			"multivector": {Key: ref.Key, Sha256: ref.SHA256},
		}}
	for name, change := range map[string]func(*ridethewind.SearchSnapshot){
		"other module":         func(s *ridethewind.SearchSnapshot) { s.ModuleId = "other" },
		"zero pointer":         func(s *ridethewind.SearchSnapshot) { s.PublicationRevision = "0" },
		"noncanonical pointer": func(s *ridethewind.SearchSnapshot) { s.PublicationRevision = "07" },
		"missing lane":         func(s *ridethewind.SearchSnapshot) { delete(s.Indexes, "sparse") },
		"forged key": func(s *ridethewind.SearchSnapshot) {
			lane := s.Indexes["dense"]
			lane.Key = "sha256/forged"
			s.Indexes["dense"] = lane
		},
		"repeated revision": func(s *ridethewind.SearchSnapshot) {
			s.ValidRevisionIds = append(s.ValidRevisionIds, "revision-1")
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := good
			bad.Indexes = make(map[string]ridethewind.CitationObject, len(good.Indexes))
			for k, v := range good.Indexes {
				bad.Indexes[k] = v
			}
			bad.ValidRevisionIds = append([]string(nil), good.ValidRevisionIds...)
			change(&bad)
			provider, err := NewRTWSearchSnapshotProvider(snapshotClientFunc(func(context.Context, string) (ridethewind.SearchSnapshot, error) {
				return bad, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Current(context.Background(), good.ModuleId); !errors.Is(err, ErrRTWSearchSnapshot) {
				t.Fatalf("malformed published snapshot accepted: %v", err)
			}
		})
	}
	provider, err := NewRTWSearchSnapshotProvider(snapshotClientFunc(func(_ context.Context, id string) (ridethewind.SearchSnapshot, error) {
		if id != good.ModuleId {
			t.Fatalf("client requested wrong module: %s", id)
		}
		return good, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := provider.Current(context.Background(), good.ModuleId)
	if err != nil || actual.ModuleID != good.ModuleId || actual.PublicationRevision != "7" ||
		len(actual.Indexes) != 3 || actual.Indexes[searchdomain.Dense].SHA256 != ref.SHA256 {
		t.Fatalf("valid RTW snapshot projection failed: %+v %v", actual, err)
	}
}

func TestRTWSearchSnapshotProviderKeepsEmptyRevisionArray(t *testing.T) {
	ref := artifacts.Reference([]byte("empty publication lane"))
	wire := ridethewind.SearchSnapshot{ModuleId: "module-empty", ReleaseId: "release-empty",
		Generation: 1, PublicationRevision: "1", ValidRevisionIds: []string{},
		Indexes: map[string]ridethewind.CitationObject{
			"dense":       {Key: ref.Key, Sha256: ref.SHA256},
			"sparse":      {Key: ref.Key, Sha256: ref.SHA256},
			"multivector": {Key: ref.Key, Sha256: ref.SHA256},
		}}
	provider, err := NewRTWSearchSnapshotProvider(snapshotClientFunc(func(context.Context, string) (ridethewind.SearchSnapshot, error) {
		return wire, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := provider.Current(context.Background(), wire.ModuleId)
	if err != nil || got.ValidRevisionIDs == nil || len(got.ValidRevisionIDs) != 0 {
		t.Fatalf("empty RTW revision array changed: %+v %v", got, err)
	}
}
