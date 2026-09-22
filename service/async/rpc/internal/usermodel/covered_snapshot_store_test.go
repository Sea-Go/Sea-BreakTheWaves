package usermodel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/warehouse/featurebaseline"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/sourcecoverage"
	"github.com/jackc/pgx/v5/pgxpool"
)

func coveredSnapshotStore(t *testing.T) (*usermodel.Store, *pgxpool.Pool) {
	t.Helper()
	store, pool := coveredBaselineStore(t)
	if err := usermodel.MigratePool(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return store, pool
}

func snapshotSubject(id string) usermodel.SubjectRef {
	return usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: id}
}

func snapshotPrefix(t *testing.T, rows []sourcecoverage.EventIndexRow, generation string) (sourcecoverage.GlobalPrefixRef, []byte, []byte) {
	t.Helper()
	index, indexHash, err := sourcecoverage.EventIndexJSONL(rows[0].Producer, int64(len(rows)), rows)
	if err != nil {
		t.Fatal(err)
	}
	batches := make([]sourcecoverage.BatchEvidence, len(rows))
	for i, row := range rows {
		hash, err := sourcecoverage.BatchHash([]sourcecoverage.EventIndexRow{row})
		if err != nil {
			t.Fatal(err)
		}
		position := strconv.Itoa(i + 1)
		batches[i] = sourcecoverage.BatchEvidence{FromOffset: position, ToOffset: position, BatchHash: hash}
	}
	batchBody, batchHash, err := sourcecoverage.BatchEvidenceJSONL(batches, int64(len(rows)))
	if err != nil {
		t.Fatal(err)
	}
	root := "http://objects.test/warehouse-coverage/" + generation
	ref := sourcecoverage.GlobalPrefixRef{SchemaVersion: sourcecoverage.SchemaVersion, Producer: rows[0].Producer, Origin: "1",
		ThroughOffset: strconv.Itoa(len(rows)), BindingPolicyID: usermodel.FavoriteCoveragePolicyID,
		WarehouseConsumer: "btw-warehouse-favorite", WarehouseGeneration: generation,
		EventIndexURL: root + "/event-index/" + indexHash + ".jsonl", EventIndexSHA256: indexHash,
		BatchEvidenceURL: root + "/batch-evidence/" + batchHash + ".jsonl", BatchEvidenceSHA256: batchHash}
	ref.ManifestSHA256, err = sourcecoverage.GlobalManifestHash(ref)
	if err != nil {
		t.Fatal(err)
	}
	return ref, index, batchBody
}

func snapshotSlice(t *testing.T, prefix sourcecoverage.GlobalPrefixRef, subject usermodel.SubjectRef,
	rows []sourcecoverage.EventIndexRow) (sourcecoverage.SubjectCoverageRef, []byte) {
	t.Helper()
	sparse, hash, count, err := sourcecoverage.SubjectIndexJSONL(sourcecoverage.SubjectRef(subject), rows)
	if err != nil {
		t.Fatal(err)
	}
	ref := sourcecoverage.SubjectCoverageRef{SchemaVersion: sourcecoverage.SchemaVersion, Subject: sourcecoverage.SubjectRef(subject),
		Prefix: prefix, SparseIndexURL: "http://objects.test/warehouse-coverage/" + prefix.WarehouseGeneration + "/subject-index/" + hash + ".jsonl",
		SparseIndexSHA256: hash, EventCount: count}
	ref.ReceiptSHA256, err = sourcecoverage.SubjectReceiptHash(ref)
	if err != nil {
		t.Fatal(err)
	}
	return ref, sparse
}

func snapshotProof(t *testing.T, rows []sourcecoverage.EventIndexRow) coveredListProof {
	t.Helper()
	body, _, err := sourcecoverage.EventIndexJSONL(rows[0].Producer, int64(len(rows)), rows)
	if err != nil {
		t.Fatal(err)
	}
	proof := coveredListProof{}
	for _, line := range bytes.Split(bytes.TrimSuffix(body, []byte{'\n'}), []byte{'\n'}) {
		var row sourcecoverage.EventIndexRow
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		proof[row.Offset] = row
	}
	return proof
}

func assertSnapshotValue(t *testing.T, snapshot usermodel.CoveredHistoricalSnapshot, want string) {
	t.Helper()
	if snapshot.Status != "historical_default_off" || len(snapshot.Values) != 1 || snapshot.Values[0].Name != "favorite_active_count" || snapshot.Values[0].Value != want {
		t.Fatalf("historical covered snapshot value=%+v", snapshot)
	}
}

func TestCoveredSnapshotAndFixedBundlePostgres(t *testing.T) {
	store, pool := coveredSnapshotStore(t)
	ctx := context.Background()
	u1, u2 := snapshotSubject("1001"), snapshotSubject("1002")
	r1, assert := coveredBaselineRow(t, u1, "100", 1, "assert", nil)
	r2, other := coveredBaselineRow(t, u2, "200", 2, "assert", nil)
	r3, retract := coveredBaselineRow(t, u1, "100", 3, "retract", &assert.EventKey)
	for _, fact := range []usermodel.Event{assert, other, retract} {
		if got, err := store.Append(ctx, fact); err != nil || got.Status != "accepted" {
			t.Fatalf("PG fact/Outbox: %+v %v", got, err)
		}
	}
	rows := []sourcecoverage.EventIndexRow{r1, r2, r3}
	proof := snapshotProof(t, rows)
	verifier, err := usermodel.NewCoverageVerifier(store, proof)
	if err != nil {
		t.Fatal(err)
	}
	p1, i1, b1 := snapshotPrefix(t, rows[:1], "covered_snapshot_w1")
	if _, err := verifier.VerifyPrefix(ctx, p1, i1, b1); err != nil {
		t.Fatal(err)
	}
	s1, sparse1 := snapshotSlice(t, p1, u1, rows[:1])
	if _, err := verifier.VerifySubject(ctx, s1, sparse1); err != nil {
		t.Fatal(err)
	}
	objects := &coveredMemoryObjects{data: map[string][]byte{s1.SparseIndexURL: bytes.Clone(sparse1)}}
	builder := featurebaseline.CoveredBuilder{State: featurebaseline.UsermodelCoveredReader{Store: store}, Objects: objects,
		ArtifactRoot: "http://objects.test/warehouse-coverage"}
	spec := featurebaseline.FavoriteCandidateSpec()
	w1, err := builder.BuildCovered(ctx, u1, spec, "covered_snapshot_candidate_w1", 1, p1)
	if err != nil || w1.Candidate.Values[0].Value != "1" || len(w1.Candidate.Tail) != 1 || w1.Candidate.CurrentComplete {
		t.Fatalf("H10 historical W1 candidate: %+v %v", w1, err)
	}
	w1raw, err := objects.ReadFixed(ctx, w1.ArtifactURL, w1.ArtifactHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptCoveredBaseline(ctx, usermodel.CoveredBaselineSubmission{ArtifactURL: w1.ArtifactURL,
		ArtifactSHA256: w1.ArtifactHash, CandidateJSON: w1raw, FeatureSpec: spec}); err != nil {
		t.Fatal(err)
	}
	snap1, err := store.FreezeCoveredSnapshot(ctx, u1, 1, spec)
	if err != nil || snap1.Replay || snap1.CurrentCompleteAtBuild {
		t.Fatalf("W1 historical snapshot: %+v %v", snap1, err)
	}
	assertSnapshotValue(t, snap1, "1")
	wrongSpec := spec
	wrongSpec.Version = "wrong-version"
	if _, err := store.FreezeCoveredSnapshot(ctx, u1, 1, wrongSpec); !errors.Is(err, usermodel.ErrCoveredSnapshotConflict) {
		t.Fatalf("different FeatureSpec rewrote historical snapshot: %v", err)
	}
	pair, err := usermodel.CoveredFixedCandidatePair(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BuildFixedCoveredBundleCandidate(ctx, u1, snap1.ID, pair); !errors.Is(err, usermodel.ErrCoveredBundlePending) {
		t.Fatalf("W1 tail became current bundle: %v", err)
	}
	if byID, err := store.CoveredSnapshotV2ByID(ctx, u1, snap1.ID); err != nil || byID.ID != snap1.ID {
		t.Fatalf("W1 historical read: %+v %v", byID, err)
	}
	if replay, err := store.FreezeCoveredSnapshot(ctx, u1, 1, spec); err != nil || !replay.Replay || replay.ID != snap1.ID {
		t.Fatalf("W1 replay: %+v %v", replay, err)
	}
	p3, i3, b3 := snapshotPrefix(t, rows, "covered_snapshot_w3")
	if _, err := verifier.VerifyPrefix(ctx, p3, i3, b3); err != nil {
		t.Fatal(err)
	}
	s3, sparse3 := snapshotSlice(t, p3, u1, rows)
	if _, err := verifier.VerifySubject(ctx, s3, sparse3); err != nil {
		t.Fatal(err)
	}
	objects.data[s3.SparseIndexURL] = bytes.Clone(sparse3)
	w3, err := builder.BuildCovered(ctx, u1, spec, "covered_snapshot_candidate_w3", 2, p3)
	if err != nil || w3.Candidate.Values[0].Value != "0" || !w3.Candidate.CurrentComplete {
		t.Fatalf("H10 complete W3 candidate: %+v %v", w3, err)
	}
	w3raw, err := objects.ReadFixed(ctx, w3.ArtifactURL, w3.ArtifactHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptCoveredBaseline(ctx, usermodel.CoveredBaselineSubmission{ArtifactURL: w3.ArtifactURL,
		ArtifactSHA256: w3.ArtifactHash, CandidateJSON: w3raw, FeatureSpec: spec}); err != nil {
		t.Fatal(err)
	}
	snap3, err := store.FreezeCoveredSnapshot(ctx, u1, 2, spec)
	if err != nil || snap3.Replay || !snap3.CurrentCompleteAtBuild || snap3.ID == snap1.ID {
		t.Fatalf("W3 snapshot: %+v %v", snap3, err)
	}
	assertSnapshotValue(t, snap3, "0")
	if _, err := store.BuildFixedCoveredBundleCandidate(ctx, u1, snap1.ID, pair); !errors.Is(err, usermodel.ErrCoveredBundlePending) {
		t.Fatalf("old revision built bundle: %v", err)
	}
	for name, mutate := range map[string]func(*usermodel.PairRef){
		"pair":    func(p *usermodel.PairRef) { p.PairID = "wrong-pair" },
		"space":   func(p *usermodel.PairRef) { p.SpaceID = "wrong-space" },
		"encoder": func(p *usermodel.PairRef) { p.EncoderID = "wrong-encoder" },
		"spec": func(p *usermodel.PairRef) {
			p.FeatureSpecHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		},
		"kind":      func(p *usermodel.PairRef) { p.Kind = "model" },
		"dimension": func(p *usermodel.PairRef) { p.Dimension = 2 },
		"metric":    func(p *usermodel.PairRef) { p.Metric = "cosine" },
	} {
		t.Run("wrong-"+name, func(t *testing.T) {
			wrong := pair
			mutate(&wrong)
			if _, err := store.BuildFixedCoveredBundleCandidate(ctx, u1, snap3.ID, wrong); !errors.Is(err, usermodel.ErrCoveredBundleConflict) {
				t.Fatalf("wrong pair accepted: %v", err)
			}
		})
	}
	bundle, err := store.BuildFixedCoveredBundleCandidate(ctx, u1, snap3.ID, pair)
	if err != nil || bundle.Replay || bundle.Status != "candidate_default_off" || len(bundle.Vector) != 1 || bundle.Vector[0] != 0 {
		t.Fatalf("W3 fixed bundle candidate: %+v %v", bundle, err)
	}
	if replay, err := store.BuildFixedCoveredBundleCandidate(ctx, u1, snap3.ID, pair); err != nil || !replay.Replay || replay.ID != bundle.ID {
		t.Fatalf("W3 bundle replay: %+v %v", replay, err)
	}
	if byID, err := store.CoveredBundleCandidateByID(ctx, u1, bundle.ID); err != nil || byID.ID != bundle.ID {
		t.Fatalf("bundle historical read: %+v %v", byID, err)
	}
	u3 := snapshotSubject("1003")
	emptyRef, emptySparse := snapshotSlice(t, p3, u3, rows)
	if len(emptySparse) != 0 || emptyRef.EventCount != 0 {
		t.Fatal("third subject lacks a proved empty slice")
	}
	if _, err := verifier.VerifySubject(ctx, emptyRef, emptySparse); err != nil {
		t.Fatal(err)
	}
	objects.data[emptyRef.SparseIndexURL] = bytes.Clone(emptySparse)
	emptyCandidate, err := builder.BuildCovered(ctx, u3, spec, "covered_snapshot_empty_w3", 1, p3)
	if err != nil || emptyCandidate.Candidate.Values[0].Value != "0" {
		t.Fatalf("empty H10 candidate: %+v %v", emptyCandidate, err)
	}
	emptyRaw, err := objects.ReadFixed(ctx, emptyCandidate.ArtifactURL, emptyCandidate.ArtifactHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptCoveredBaseline(ctx, usermodel.CoveredBaselineSubmission{ArtifactURL: emptyCandidate.ArtifactURL,
		ArtifactSHA256: emptyCandidate.ArtifactHash, CandidateJSON: emptyRaw, FeatureSpec: spec}); err != nil {
		t.Fatal(err)
	}
	emptySnapshot, err := store.FreezeCoveredSnapshot(ctx, u3, 1, spec)
	if err != nil || emptySnapshot.InputStateVersion != 0 {
		t.Fatalf("empty historical snapshot: %+v %v", emptySnapshot, err)
	}
	if emptyBundle, err := store.BuildFixedCoveredBundleCandidate(ctx, u3, emptySnapshot.ID, pair); err != nil || emptyBundle.Vector[0] != 0 || emptyBundle.Status != "candidate_default_off" {
		t.Fatalf("empty fixed candidate: %+v %v", emptyBundle, err)
	}
	if _, err := store.CoveredBundleCandidateByID(ctx, u2, bundle.ID); !errors.Is(err, usermodel.ErrNotFound) {
		t.Fatalf("cross-subject bundle read: %v", err)
	}
	if old, err := store.CoveredSnapshotV2ByID(ctx, u1, snap1.ID); err != nil || old.ID != snap1.ID || old.Values[0].Value != "1" {
		t.Fatalf("W1 history changed after W3: %+v %v", old, err)
	}
	if _, _, err := store.ReadyServingBundle(ctx, u1, pair); err == nil {
		t.Fatal("unapproved v2 candidate entered v1 ReadyServingBundle")
	}
	var pointers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM usermodel_serving_pointers`).Scan(&pointers); err != nil || pointers != 0 {
		t.Fatalf("v1 pointer changed: %d %v", pointers, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE usermodel_covered_snapshots_v2 SET status='historical_default_off' WHERE snapshot_id=$1`, snap1.ID); err == nil {
		t.Fatal("snapshot UPDATE bypassed immutable trigger")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM usermodel_covered_bundle_candidates_v2 WHERE bundle_id=$1`, bundle.ID); err == nil {
		t.Fatal("bundle DELETE bypassed immutable trigger")
	}
	_, future := coveredBaselineRow(t, u1, "300", 4, "assert", nil)
	if _, err := store.Append(ctx, future); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BuildFixedCoveredBundleCandidate(ctx, u1, snap3.ID, pair); !errors.Is(err, usermodel.ErrCoveredBundlePending) {
		t.Fatalf("changed fact state served old bundle: %v", err)
	}
	t.Logf("PG v2 W1 snapshot=%s W3 snapshot=%s candidate bundle=%s baseline1=%s baseline2=%s", snap1.ID, snap3.ID, bundle.ID, w1.ArtifactHash, w3.ArtifactHash)
}

func TestCoveredSnapshotFromOriginalH10Bytes(t *testing.T) {
	dir := os.Getenv("SEA_COVERAGE_COMBINED_ARTIFACT_DIR")
	if dir == "" {
		t.Skip("set archived combined H10 artifact directory")
	}
	store, pool := coveredSnapshotStore(t)
	ctx := context.Background()
	var refs struct {
		G1   sourcecoverage.GlobalPrefixRef    `json:"G1"`
		U1   sourcecoverage.SubjectCoverageRef `json:"U1W1"`
		Hash string                            `json:"candidate_w1_after_tail_sha256"`
	}
	body, err := os.ReadFile(filepath.Join(dir, "two-joined-ref.json"))
	if err != nil || json.Unmarshal(body, &refs) != nil {
		t.Fatalf("original H10 ref: %v", err)
	}
	indexW1, err := os.ReadFile(filepath.Join(dir, "artifacts/two/G1/event-index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	batchW1, err := os.ReadFile(filepath.Join(dir, "artifacts/two/G1/batch-evidence.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	indexW3, err := os.ReadFile(filepath.Join(dir, "artifacts/two/G3/event-index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sparse, err := os.ReadFile(filepath.Join(dir, "artifacts/two/U1W1/sparse-index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "artifacts/two/candidates/candidate_w1_after_tail_sha256.json"))
	if err != nil || coveredBytesHash(raw) != refs.Hash {
		t.Fatalf("original H10 raw/hash: %v", err)
	}
	var candidate featurebaseline.CoveredCandidate
	if err := json.Unmarshal(raw, &candidate); err != nil {
		t.Fatal(err)
	}
	proof := appendArchivedCoverageFacts(t, store, indexW3)
	verifier, err := usermodel.NewCoverageVerifier(store, proof)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyPrefix(ctx, refs.G1, indexW1, batchW1); err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifySubject(ctx, refs.U1, sparse); err != nil {
		t.Fatal(err)
	}
	root := refs.G1.EventIndexURL[:len(refs.G1.EventIndexURL)-len("/"+refs.G1.WarehouseGeneration+"/event-index/"+refs.G1.EventIndexSHA256+".jsonl")]
	url := root + "/" + candidate.Generation + "/covered-baseline/" + refs.Hash + ".json"
	if _, err := store.AcceptCoveredBaseline(ctx, usermodel.CoveredBaselineSubmission{ArtifactURL: url, ArtifactSHA256: refs.Hash,
		CandidateJSON: raw, FeatureSpec: featurebaseline.FavoriteCandidateSpec()}); err != nil {
		t.Fatal(err)
	}
	var saved []byte
	if err := pool.QueryRow(ctx, `SELECT candidate_raw FROM usermodel_covered_baselines_v2 WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND revision=1`,
		candidate.Subject.AuthorityID, candidate.Subject.TenantID, candidate.Subject.SubjectID).Scan(&saved); err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("accepted original bytea differs: %v", err)
	}
	snapshot, err := store.FreezeCoveredSnapshot(ctx, candidate.Subject, 1, featurebaseline.FavoriteCandidateSpec())
	if err != nil || snapshot.Replay || snapshot.BaselineArtifactSHA256 != refs.Hash || snapshot.Coverage != refs.U1 {
		t.Fatalf("original H10 frozen snapshot: %+v %v", snapshot, err)
	}
	assertSnapshotValue(t, snapshot, "1")
	pair, err := usermodel.CoveredFixedCandidatePair(featurebaseline.FavoriteCandidateSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BuildFixedCoveredBundleCandidate(ctx, candidate.Subject, snapshot.ID, pair); !errors.Is(err, usermodel.ErrCoveredBundlePending) {
		t.Fatalf("original W1 tail became bundle: %v", err)
	}
	t.Logf("original H10 raw=%s historical snapshot=%s tail=%d", refs.Hash, snapshot.ID, len(snapshot.TailAtBuild))
}
