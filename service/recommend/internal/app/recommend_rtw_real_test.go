package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	recommendmigration "github.com/Sea-Go/Sea-BreakTheWaves/migrations/recommend"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	usermodel "github.com/Sea-Go/Sea-BreakTheWaves/service/common/usermodelcontract"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/internal/recommend"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRTWRealItemPoolHandoff is the BTW half of an optional RTW-owned real
// publication/PG acceptance. An RTW test first manually publishes the content
// and passes its isolated Worker URL and PostgreSQL DSN in a private file.
func TestRTWRealItemPoolHandoff(t *testing.T) {
	path := os.Getenv("SEA_RTW_ITEM_POOL_FIXTURE")
	if path == "" {
		t.Skip("requires RTW current publication and isolated PostgreSQL fixture")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("RTW item fixture must be a regular private file: %v", err)
	}
	var fixture struct {
		RTWBase     string `json:"rtw_base"`
		WorkerToken string `json:"worker_token"`
		PGDSN       string `json:"pg_dsn"`
		ModuleID    string `json:"module_id"`
		RevisionID  string `json:"revision_id"`
		ItemID      string `json:"item_id"`
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &fixture) != nil || fixture.RTWBase == "" ||
		fixture.WorkerToken == "" || fixture.PGDSN == "" || fixture.ModuleID == "" ||
		fixture.RevisionID == "" || fixture.ItemID == "" {
		t.Fatal("incomplete RTW real item pool fixture")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, fixture.PGDSN)
	if err != nil {
		t.Fatal("open isolated PostgreSQL admin connection")
	}
	defer admin.Close()
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "recommend_rtw_" + hex.EncodeToString(nonce[:])
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	}()
	config, err := pgxpool.ParseConfig(fixture.PGDSN)
	if err != nil {
		t.Fatal("parse isolated PostgreSQL DSN")
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, recommendmigration.SQL); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, recommendmigration.PairSQL); err != nil {
		t.Fatal(err)
	}
	client, err := ridethewind.New(httpclient.Config{BaseURL: fixture.RTWBase, Token: fixture.WorkerToken})
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewRTWItemSource(client)
	if err != nil {
		t.Fatal(err)
	}
	store, err := recommend.NewStore(pool, source)
	if err != nil {
		t.Fatal(err)
	}
	release, err := store.Rebuild(ctx, fixture.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.Candidates(ctx, fixture.ModuleID, recommend.MaxCandidates)
	if err != nil || page.PoolReleaseID != release.ID || page.FeatureHash != release.FeatureHash ||
		!reflect.DeepEqual(page.Publication, release.Publication) || page.BehaviorState != "not_connected" {
		t.Fatalf("real RTW publication did not become fixed PG candidate pool: %+v %v", page, err)
	}
	var found bool
	for _, candidate := range page.Candidates {
		if candidate.Item.ItemID == fixture.ItemID && candidate.Item.RevisionID == fixture.RevisionID &&
			candidate.Sources[0] == "new_content" {
			found = true
		}
	}
	if !found {
		t.Fatalf("RTW published item %s/%s missing from bounded pool", fixture.ItemID, fixture.RevisionID)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM recommend_pool_releases WHERE pool_release_id=$1`, release.ID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("immutable RTW item release not committed once: %d %v", rows, err)
	}
	// Fixed artifact refs below are only a prebuild fixture. The content
	// identities come from the real RTW release above; no fake DC probe exists.
	pairRef := usermodel.PairRef{PairID: "pair-rtw-prebuild", SpaceID: "space-rtw-prebuild",
		EncoderID: "user-encoder-prebuild", Kind: "fixed_baseline",
		FeatureSpecVersion: "user-features-prebuild", FeatureSpecHash: artifacts.Hash([]byte("user-spec")),
		Dimension: 2, Metric: "dot"}
	proposalInput := recommend.PairProposal{ModuleID: release.Publication.ModuleID,
		PoolReleaseID: release.ID, ItemFeatureHash: release.FeatureHash, Pair: pairRef,
		UserEncoder: recommend.EncoderArtifact{EncoderID: pairRef.EncoderID,
			Weights: artifacts.Reference([]byte("fixed-user-weights")), InputSpecHash: pairRef.FeatureSpecHash,
			SpaceID: pairRef.SpaceID, Dimension: 2, Metric: "dot"},
		ItemEncoder: recommend.EncoderArtifact{EncoderID: "item-encoder-prebuild",
			Weights: artifacts.Reference([]byte("fixed-item-weights")), InputSpecHash: release.FeatureHash,
			SpaceID: pairRef.SpaceID, Dimension: 2, Metric: "dot"},
		CandidateManifest: artifacts.Reference([]byte("fixed-candidate-manifest")),
		RankerKind:        "rule_freshness", PolicyVersion: "freshness-prebuild"}
	pairs, err := recommend.NewPairStore(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := pairs.RegisterProposal(ctx, proposalInput)
	if err != nil {
		t.Fatal(err)
	}
	covered := make([]recommend.IndexedItem, len(release.Items))
	for i, item := range release.Items {
		covered[i] = recommend.IndexedItem{ItemID: item.ItemID, RevisionID: item.RevisionID,
			ContentHash: item.ContentHash}
	}
	index, err := pairs.RegisterItemIndex(ctx, recommend.ItemIndexGeneration{ProposalID: proposal.ID,
		PoolReleaseID: release.ID, PairID: pairRef.PairID, SpaceID: pairRef.SpaceID,
		Dimension: 2, Metric: "dot", ItemEncoder: proposal.ItemEncoder,
		Index:         artifacts.Reference([]byte("fixed-item-index")),
		BuildManifest: artifacts.Reference([]byte("fixed-index-build-manifest")), Items: covered})
	if err != nil || index.ID == "" {
		t.Fatalf("real RTW item set did not enter immutable prebuild: %+v %v", index, err)
	}
	if _, err := pairs.Approve(ctx, proposal.ID, index.ID,
		recommend.Approval{Ref: "no-real-dc-approval", Revision: 1}); !errors.Is(err, recommend.ErrPairPending) {
		t.Fatalf("fixed prebuild without real probes was approved: %v", err)
	}
	plan, err := pairs.Plan(ctx, release.Publication.ModuleID,
		usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "test-user"}, 1, nil)
	if err != nil || plan.Status != "pending" || len(plan.Items) != 0 {
		t.Fatalf("real RTW content plus fixture index fabricated a Slate: %+v %v", plan, err)
	}
}
