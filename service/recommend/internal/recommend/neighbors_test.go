package recommend

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
)

type matureFunc func(context.Context, PoolRelease) (MatureBatch, error)

func (f matureFunc) ReadMature(ctx context.Context, release PoolRelease) (MatureBatch, error) {
	return f(ctx, release)
}

func itemCFPool(t *testing.T) PoolRelease {
	t.Helper()
	source := itemFixture()
	source.current.ValidRevisionIDs = append(source.current.ValidRevisionIDs, "rev-3")
	hash := artifacts.Hash([]byte("rev-3"))
	source.revs["rev-3"] = Revision{RevisionID: "rev-3", ModuleID: "module-1", ItemID: "article-3",
		Kind: "wiki", Title: "Third article", MediaType: "text/markdown", ContentHash: hash,
		ObjectKey: "sha256/" + hash, CreatedAt: time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC)}
	release, err := BuildPool(context.Background(), source, "module-1")
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func matureFixture(rows []MatureObservation) matureFunc {
	return func(context.Context, PoolRelease) (MatureBatch, error) {
		return MatureBatch{Generation: "dws-gen-1", DefinitionVersion: "qualified-visible-v1",
			ManifestRef: "s3://fixture/manifest", ManifestHash: artifacts.Hash([]byte("manifest")),
			Watermark: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), Rows: rows}, nil
	}
}

func matureRow(sample, subject, item, revision, state string) MatureObservation {
	return MatureObservation{SampleID: sample, AuthorityID: "rtw.identity", TenantID: "platform",
		SubjectID: subject, RequestID: "request-" + sample, ImpressionID: "impression-" + sample,
		ItemID: item, RevisionID: revision, Qualification: "eligible", LabelState: state,
		WindowMature: true, WindowEnd: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		AvailableAt: time.Date(2026, 9, 1, 12, 5, 0, 0, time.UTC)}
}

func TestItemCFOnlyMatureAttributedPositivesAndNormalizedTopK(t *testing.T) {
	release := itemCFPool(t)
	rows := []MatureObservation{
		matureRow("a", "u1", "article-1", "rev-1", "POSITIVE"),
		matureRow("b", "u1", "article-2", "rev-2", "POSITIVE"),
		matureRow("c", "u2", "article-1", "rev-1", "POSITIVE"),
		matureRow("d", "u2", "article-2", "rev-2", "POSITIVE"),
		matureRow("e", "u2", "article-3", "rev-3", "POSITIVE"),
		matureRow("f", "u3", "article-3", "rev-3", "OBSERVED_NEGATIVE"),
		matureRow("g", "u4", "article-3", "old-revision", "POSITIVE"),
	}
	artifact, err := ComputeItemCF(context.Background(), release, matureFixture(rows), 1)
	if err != nil || artifact.ID == "" || artifact.PoolReleaseID != release.ID ||
		artifact.BehaviorState != "positive" || artifact.PositiveRows != 5 ||
		artifact.ObservedNegative != 1 || artifact.ExcludedOldItems != 1 || len(artifact.Items) != 3 {
		t.Fatalf("qualified ItemCF result: %+v %v", artifact, err)
	}
	if artifact.Items[0].ItemID != "article-1" || len(artifact.Items[0].Neighbors) != 1 ||
		artifact.Items[0].Neighbors[0].ItemID != "article-2" ||
		math.Abs(artifact.Items[0].Neighbors[0].Score-0.75) > 1e-12 ||
		artifact.Items[0].Neighbors[0].SupportUsers != 2 {
		t.Fatalf("hand-computed normalized AB top1 differs: %+v", artifact.Items)
	}
	repeated := append(append([]MatureObservation{}, rows...),
		matureRow("repeat-positive", "u1", "article-1", "rev-1", "POSITIVE"))
	repeatArtifact, err := ComputeItemCF(context.Background(), release, matureFixture(repeated), 1)
	if err != nil || repeatArtifact.PositiveRows != artifact.PositiveRows+1 ||
		math.Abs(repeatArtifact.Items[0].Neighbors[0].Score-artifact.Items[0].Neighbors[0].Score) > 1e-12 ||
		repeatArtifact.Items[0].Neighbors[0].SupportUsers != artifact.Items[0].Neighbors[0].SupportUsers {
		t.Fatalf("repeat positive impression doubled a user-item edge: %+v %v", repeatArtifact, err)
	}
	replayed, err := ComputeItemCF(context.Background(), release, matureFixture(rows), 1)
	if err != nil || replayed.ID != artifact.ID {
		t.Fatalf("fixed ItemCF input did not reproduce: %+v %v", replayed, err)
	}
	changedWatermark := matureFunc(func(ctx context.Context, r PoolRelease) (MatureBatch, error) {
		batch, err := matureFixture(rows).ReadMature(ctx, r)
		batch.Watermark = batch.Watermark.Add(24 * time.Hour)
		return batch, err
	})
	newArtifact, err := ComputeItemCF(context.Background(), release, changedWatermark, 1)
	if err != nil || newArtifact.ID == artifact.ID {
		t.Fatalf("different DWS cutoff reused an ItemCF artifact ID: %s %s %v", artifact.ID, newArtifact.ID, err)
	}
}

func TestItemCFDistinguishesMissingMatureRowsAndObservedNegative(t *testing.T) {
	release := itemCFPool(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ComputeItemCF(cancelled, release, matureFunc(func(context.Context, PoolRelease) (MatureBatch, error) {
		t.Fatal("cancelled ItemCF entered DWS source")
		return MatureBatch{}, nil
	}), 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ItemCF did not stop: %v", err)
	}
	empty, err := ComputeItemCF(context.Background(), release, matureFixture(nil), 2)
	if err != nil || empty.BehaviorState != "no_mature_rows" || len(empty.Items) != 0 || empty.ObservedNegative != 0 {
		t.Fatalf("no mature rows became a negative: %+v %v", empty, err)
	}
	negative, err := ComputeItemCF(context.Background(), release,
		matureFixture([]MatureObservation{matureRow("negative", "u1", "article-1", "rev-1", "OBSERVED_NEGATIVE")}), 2)
	if err != nil || negative.BehaviorState != "mature_no_positive" || negative.ObservedNegative != 1 || len(negative.Items) != 0 {
		t.Fatalf("observed negative was confused with no rows or positive: %+v %v", negative, err)
	}
}

func TestItemCFRejectsUnshownPendingOrUnattributedRows(t *testing.T) {
	release := itemCFPool(t)
	base := matureRow("sample", "u1", "article-1", "rev-1", "POSITIVE")
	for _, tc := range []struct {
		name string
		edit func(*MatureObservation)
	}{
		{"unshown", func(r *MatureObservation) { r.Qualification = "missing_served_candidate" }},
		{"pending", func(r *MatureObservation) { r.LabelState = "PENDING" }},
		{"unmatured", func(r *MatureObservation) { r.WindowMature = false }},
		{"no impression", func(r *MatureObservation) { r.ImpressionID = "" }},
		{"past watermark", func(r *MatureObservation) { r.WindowEnd = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC) }},
		{"late available label", func(r *MatureObservation) { r.AvailableAt = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC) }},
		{"label before maturity", func(r *MatureObservation) { r.AvailableAt = r.WindowEnd.Add(-time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := base
			tc.edit(&row)
			if _, err := ComputeItemCF(context.Background(), release, matureFixture([]MatureObservation{row}), 1); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid DWS row entered ItemCF: %v", err)
			}
		})
	}
	duplicate := base
	duplicate.SampleID = "other-sample"
	if _, err := ComputeItemCF(context.Background(), release, matureFixture([]MatureObservation{base, duplicate}), 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate logical impression with a new sample ID entered ItemCF: %v", err)
	}
}
