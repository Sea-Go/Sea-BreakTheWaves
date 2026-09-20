package featurebaseline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

type favoriteFixedEncoder struct{}

func (favoriteFixedEncoder) EncodeUser(_ context.Context, snapshot usermodel.FeatureSnapshot,
	pair usermodel.PairRef) (usermodel.EncoderOutput, error) {
	count, err := strconv.ParseFloat(value(snapshot, "favorite_active_count").Value, 64)
	if err != nil {
		return usermodel.EncoderOutput{}, err
	}
	return usermodel.EncoderOutput{PairID: pair.PairID, SpaceID: pair.SpaceID, EncoderID: pair.EncoderID,
		FeatureSnapshotID: snapshot.ID, Source: "fixed_baseline", Vector: []float64{count}}, nil
}

func TestFavoriteDWDPrefixAndOrphan(t *testing.T) {
	subject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	row := favoriteDWDRow{Producer: favoriteProducer, SourceOffset: 1, EventID: "favorite.9007199254741993.v1",
		EventType: "rtw.favorite.assert", AuthorityID: subject.AuthorityID, TenantID: subject.TenantID, SubjectID: subject.SubjectID,
		FavoriteID: "9007199254741993", FolderID: "9007199254741991", TargetType: "article", TargetID: "article-1",
		Operation: "assert", FavoriteStateDelta: 1, ActiveAfter: 1, EventTime: "2026-09-15T00:00:00Z",
		AvailableAt: "2026-09-15T00:00:00Z", DCReceivedAt: "2026-09-15T00:00:01Z", SourceEventHash: strings.Repeat("a", 64)}
	retract := row
	retract.SourceOffset = 2
	retract.EventID = "favorite.9007199254741993.v2"
	retract.EventType = "rtw.favorite.retract"
	retract.Operation = "retract"
	retract.PredecessorEventID = row.EventID
	retract.FavoriteStateDelta = -1
	retract.ActiveAfter = 0
	encode := func(rows ...favoriteDWDRow) []byte {
		var body []byte
		for _, r := range rows {
			line, _ := json.Marshal(r)
			body = append(body, line...)
			body = append(body, '\n')
		}
		return body
	}
	full := encode(row, retract)
	first, err := verifyFavoriteDWD(full, subject, 1)
	if err != nil || first.Count != 1 || len(first.Active) != 1 {
		t.Fatalf("r1 source: %+v %v", first, err)
	}
	second, err := verifyFavoriteDWD(full, subject, 2)
	if err != nil || second.Count != 0 || first.PrefixHash == second.PrefixHash {
		t.Fatalf("r2 reversible source: %+v %v", second, err)
	}
	if _, err := verifyFavoriteDWD(full, subject, 3); err == nil {
		t.Fatal("future missing offset accepted")
	}
	orphan := retract
	orphan.PredecessorEventID = "favorite.unknown.v1"
	if _, err := verifyFavoriteDWD(encode(row, orphan), subject, 2); err == nil {
		t.Fatal("orphan retract counted as negative")
	}
	gap := retract
	gap.SourceOffset = 3
	if _, err := verifyFavoriteDWD(encode(row, gap), subject, 2); err == nil {
		t.Fatal("DWD global offset gap accepted")
	}
	foreign := retract
	foreign.SubjectID = "1002"
	if _, err := verifyFavoriteDWD(encode(row, foreign), subject, 2); err == nil {
		t.Fatal("mixed-subject producer was treated as subject-contiguous")
	}
}

func TestRealFavoriteCandidateFromDWD(t *testing.T) {
	for _, key := range []string{"SEA_FACT_AUTHORITY_URL", "SEA_FACT_AUTHORITY_TOKEN", "SEA_FACT_DC_URL", "SEA_FACT_DC_TOKEN",
		"FAVORITE_CANDIDATE_DWD_URL", "FAVORITE_CANDIDATE_DWD_SHA256"} {
		if os.Getenv(key) == "" {
			t.Skip("run warehouse/favorite_feature/acceptance.sh with real isolated RTW/DC/DWD")
		}
	}
	ctx := context.Background()
	subject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	observed, err := telemetry.New(ctx, telemetry.Config{Service: "favorite-feature-candidate-test", Environment: "test", Version: "candidate-v1",
		InstanceID: "isolated", Output: io.Discard, Level: slog.LevelInfo, TraceExporter: tracetest.NewInMemoryExporter(), SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := observed.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	r, _ := servingWarehouseRunner(t, observed)
	graph, err := usermodel.NewFactGraphRuntime("favorite-feature-candidate", r.Source, inmemory.NewSessionService(), observed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := graph.Close(); err != nil {
			t.Error(err)
		}
	}()
	client, err := datacenter.New(httpclient.Config{BaseURL: os.Getenv("SEA_FACT_DC_URL"), Token: os.Getenv("SEA_FACT_DC_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	binder, err := app.NewFavoriteAuthorityBinder(app.FavoriteAuthorityBinderConfig{BaseURL: os.Getenv("SEA_FACT_AUTHORITY_URL"), Token: os.Getenv("SEA_FACT_AUTHORITY_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := app.NewFactWorker(app.FactWorkerConfig{Consumer: "btw-favorite-authority", Producer: favoriteProducer, BatchLimit: 1,
		Bindings: []app.FactEventBinding{{EventType: "rtw.favorite.assert", SchemaVersion: 1, Action: usermodel.Assert, EvidenceBinder: binder},
			{EventType: "rtw.favorite.retract", SchemaVersion: 1, Action: usermodel.Retract, EvidenceBinder: binder}}}, client, nil, graph, r.Source, observed)
	if err != nil {
		t.Fatal(err)
	}
	ref := FavoriteDWDRef{URL: os.Getenv("FAVORITE_CANDIDATE_DWD_URL"), SHA256: os.Getenv("FAVORITE_CANDIDATE_DWD_SHA256")}
	spec := FavoriteCandidateSpec()
	first, err := worker.RunOnce(ctx)
	if err != nil || first.Count != 1 || first.AckedOffset != 1 {
		t.Fatalf("RTW/DC Graph assert: %+v %v", first, err)
	}
	c1, err := r.BuildFavoriteCandidate(ctx, subject, 1, "favorite_candidate_g1", ref, 1)
	if err != nil || c1.Status != "candidate_default_off" || c1.ActiveCount != 1 || c1.FeatureBaseline.ContributionCount != 1 || !digest.MatchString(c1.ArtifactSHA256) {
		t.Fatalf("DWD r1 candidate: %+v %v", c1, err)
	}
	_, snap1, err := r.Accept(ctx, c1.FeatureBaseline, spec)
	if err != nil || value(snap1, "favorite_active_count").Value != "1" || snap1.Generation != c1.Generation || snap1.Revision != 1 {
		t.Fatalf("PG snapshot r1: %+v %v", snap1, err)
	}
	pair := usermodel.PairRef{PairID: "candidate-favorite-fixed-v1", SpaceID: "candidate-favorite-count-space-v1",
		EncoderID: "fixed-favorite-count-v1", Kind: "fixed_baseline", FeatureSpecVersion: spec.Version,
		FeatureSpecHash: c1.FeatureSpecHash, Dimension: 1, Metric: "dot"}
	b1, err := r.Source.BuildServingBundle(ctx, subject, pair, favoriteFixedEncoder{})
	if err != nil || b1.FeatureSnapshotID != snap1.ID || b1.Vector[0] != 1 {
		t.Fatalf("candidate bundle r1: %+v %v", b1, err)
	}
	if _, _, err := r.Source.ReadyServingBundle(ctx, subject, pair); !errors.Is(err, usermodel.ErrNotFound) {
		t.Fatalf("unapproved candidate became active: %v", err)
	}
	second, err := worker.RunOnce(ctx)
	if err != nil || second.Count != 1 || second.AckedOffset != 2 {
		t.Fatalf("RTW/DC Graph retract: %+v %v", second, err)
	}
	if _, err := r.BuildFavoriteCandidate(ctx, subject, 2, "favorite_stale_g1", ref, 1); err == nil {
		t.Fatal("old DWD prefix built against new PG source watermark")
	}
	c2, err := r.BuildFavoriteCandidate(ctx, subject, 2, "favorite_candidate_g2", ref, 2)
	if err != nil || c2.ActiveCount != 0 || c2.FeatureBaseline.ContributionCount != 0 || c2.DWDPrefixSHA256 == c1.DWDPrefixSHA256 || !digest.MatchString(c2.ArtifactSHA256) {
		t.Fatalf("DWD r2 candidate: %+v %v", c2, err)
	}
	_, snap2, err := r.Accept(ctx, c2.FeatureBaseline, spec)
	if err != nil || value(snap2, "favorite_active_count").Value != "0" || snap2.ID == snap1.ID {
		t.Fatalf("PG snapshot r2: %+v %v", snap2, err)
	}
	b2, err := r.Source.BuildServingBundle(ctx, subject, pair, favoriteFixedEncoder{})
	if err != nil || b2.FeatureSnapshotID != snap2.ID || b2.Vector[0] != 0 || b2.ID == b1.ID {
		t.Fatalf("candidate bundle r2: %+v %v", b2, err)
	}
	if _, _, err := r.Source.ReadyServingBundle(ctx, subject, pair); !errors.Is(err, usermodel.ErrNotFound) {
		t.Fatalf("default-off candidate became served: %v", err)
	}
	if replay, got, err := r.Accept(ctx, c2.FeatureBaseline, spec); err != nil || !replay.Replay ||
		got.Generation != snap2.Generation || got.Revision != snap2.Revision || value(got, "favorite_active_count").Value != "0" {
		t.Fatalf("replay doubled count: %+v %+v %v", replay, got, err)
	}
	if _, _, err := r.Accept(ctx, c1.FeatureBaseline, spec); !errors.Is(err, usermodel.ErrConflict) {
		t.Fatalf("old baseline rewrote head: %v", err)
	}
	if old, err := r.Source.FeatureSnapshotByID(ctx, subject, snap1.ID); err != nil || old.ID != snap1.ID {
		t.Fatalf("old snapshot changed: %+v %v", old, err)
	}
	if old, err := r.Source.ServingBundleByID(ctx, subject, b1.ID); err != nil || old.ID != b1.ID {
		t.Fatalf("old bundle changed: %+v %v", old, err)
	}
	if old, err := r.getFixed(ctx, r.objectURL(c1.Generation,
		"favorite-candidate/"+c1.ArtifactSHA256+".json"), c1.ArtifactSHA256); err != nil || len(old) == 0 {
		t.Fatalf("old frozen candidate manifest changed: %v", err)
	}
	tampered := ref
	tampered.SHA256 = strings.Repeat("0", 64)
	if _, err := r.BuildFavoriteCandidate(ctx, subject, 3, "favorite_tampered", tampered, 2); err == nil {
		t.Fatal("wrong DWD digest accepted")
	}
	if _, err := r.BuildFavoriteCandidate(ctx, subject, 3, "favorite_missing_offset", ref, 3); err == nil {
		t.Fatal("DWD gap accepted")
	}
	t.Logf("L2 default-off DWD=%s prefix1=%s prefix2=%s candidate1=%s candidate2=%s source1=%s baseline1=%s snapshot1=%s bundle1=%s source2=%s baseline2=%s snapshot2=%s bundle2=%s",
		ref.SHA256, c1.DWDPrefixSHA256, c2.DWDPrefixSHA256, c1.ArtifactSHA256, c2.ArtifactSHA256,
		c1.FeatureBaseline.SourceSHA256, c1.FeatureBaseline.BaselineSHA256, snap1.ID, b1.ID,
		c2.FeatureBaseline.SourceSHA256, c2.FeatureBaseline.BaselineSHA256, snap2.ID, b2.ID)
}

func TestGlobalOffsetIsNotSubjectSequence(t *testing.T) {
	r, _ := servingWarehouseRunner(t)
	ctx := context.Background()
	u1 := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	u2 := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1002"}
	for _, input := range []struct {
		subject  usermodel.SubjectRef
		sequence int64
		id       string
	}{{u1, 1, "favorite.11.v1"}, {u2, 2, "favorite.22.v1"}, {u1, 3, "favorite.33.v1"}} {
		sum := sha256.Sum256([]byte(input.id))
		e := usermodel.Event{Subject: input.subject, EventKey: usermodel.EventKey{Producer: favoriteProducer, EventID: input.id},
			Action: usermodel.Assert, Kind: usermodel.ProductAction, Predicate: "favorite", ValueRef: "article/article-1",
			EvidenceRef: "dc:event:rtw.community.favorite:" + strconv.FormatInt(input.sequence, 10), EvidenceHash: hex.EncodeToString(sum[:]),
			OccurredAt: time.Now().UTC().Add(-time.Minute), ObservedAt: time.Now().UTC().Add(-time.Minute),
			SourcePartition: favoritePartition, SourceSequence: input.sequence}
		if receipt, err := r.Source.Append(ctx, e); err != nil || receipt.Status != "accepted" {
			t.Fatalf("interleaved accepted fact: %+v %v", receipt, err)
		}
	}
	p1, err := r.Source.Current(ctx, u1)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := r.Source.Current(ctx, u2)
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Watermarks) != 1 || p1.Watermarks[0].ContiguousSequence != 1 || p1.Watermarks[0].MaxSeenSequence != 3 || p1.Watermarks[0].Complete ||
		len(p2.Watermarks) != 1 || p2.Watermarks[0].ContiguousSequence != 0 || p2.Watermarks[0].MaxSeenSequence != 2 || p2.Watermarks[0].Complete {
		t.Fatalf("global offset was mistaken for per-subject sequence: u1=%+v u2=%+v", p1.Watermarks, p2.Watermarks)
	}
	if _, err := r.Build(ctx, u2, 1, "favorite_mixed_subject_g1", FavoriteCandidateSpec()); err == nil || !strings.Contains(err.Error(), "no complete accepted source prefix") {
		t.Fatalf("u2 H10 Build should fail without prefix: %v", err)
	}
	// User 1 can build only offset 1; offset 3 remains the near-line tail.
	m, err := r.Build(ctx, u1, 1, "favorite_mixed_subject_u1", FavoriteCandidateSpec())
	if err != nil || len(m.Watermarks) != 1 || m.Watermarks[0].ContiguousSequence != 1 || m.ContributionCount != 1 {
		t.Fatalf("u1 covered beyond gap: %+v %v", m, err)
	}
	_, snap, err := r.Accept(ctx, m, FavoriteCandidateSpec())
	if err != nil || len(snap.Tail) != 1 || value(snap, "favorite_active_count").Value != "2" {
		t.Fatalf("u1 H10 tail boundary: %+v %v", snap, err)
	}
	t.Logf("global offsets [u1:1,u2:2,u1:3] -> u1 contiguous=1,max=3,tail=1; u2 contiguous=0,max=2,Build rejected")
}
