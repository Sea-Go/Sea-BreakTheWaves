package usermodel_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/warehouse/featurebaseline"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/sourcecoverage"
)

func appendArchivedCoverageFacts(t *testing.T, store *usermodel.Store, index []byte) coveredListProof {
	t.Helper()
	proof := coveredListProof{}
	scanner := bufio.NewScanner(bytes.NewReader(index))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var row sourcecoverage.EventIndexRow
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			t.Fatal("archived coverage row malformed")
		}
		proof[row.Offset] = row
		var source eventing.Event
		var payload struct {
			TargetType     string  `json:"target_type"`
			TargetID       string  `json:"target_id"`
			TargetRevision *string `json:"target_revision"`
		}
		if json.Unmarshal(row.EventSpec, &source) != nil || json.Unmarshal(source.Payload, &payload) != nil {
			t.Fatal("archived EventSpec malformed")
		}
		offset, err := strconv.ParseInt(row.Offset, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		occurred, err1 := time.Parse(time.RFC3339Nano, source.OccurredAt)
		received, err2 := time.Parse(time.RFC3339Nano, row.ReceivedAt)
		if err1 != nil || err2 != nil {
			t.Fatal("archived EventSpec time malformed")
		}
		valueRef := payload.TargetType + "/" + payload.TargetID
		if payload.TargetRevision != nil {
			valueRef += "/revision/" + *payload.TargetRevision
		}
		fact := usermodel.Event{Subject: usermodel.SubjectRef(row.Subject),
			EventKey: usermodel.EventKey{Producer: row.Producer, EventID: row.EventID},
			Action:   usermodel.Assert, Kind: usermodel.ProductAction, Predicate: "favorite",
			ValueRef: valueRef, ItemID: payload.TargetID,
			EvidenceRef: "dc:event:" + row.Producer + ":" + row.Offset, EvidenceHash: row.InputHash,
			OccurredAt: occurred, ObservedAt: received, SourcePartition: "dc:" + row.Producer,
			SourceSequence: offset}
		if source.EventType == "rtw.favorite.retract" {
			fact.Action = usermodel.Retract
			fact.Supersedes = &usermodel.EventKey{Producer: row.Producer,
				EventID: "favorite." + source.AggregateID + ".v1"}
		}
		if receipt, err := store.Append(context.Background(), fact); err != nil || receipt.Status != "accepted" {
			t.Fatalf("PG fact for published row %s: %+v %v", row.Offset, receipt, err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return proof
}

// The combined WS07→H10 run archived immutable source/candidate bytes after
// its S3 endpoint closed. This fixture serves those exact bytes at the exact
// original URLs via a dial override; refs and hashes are never rewritten.
func TestAcceptCombinedPublisherAndH10OriginalArtifact(t *testing.T) {
	artifactDir := os.Getenv("SEA_COVERAGE_COMBINED_ARTIFACT_DIR")
	if artifactDir == "" {
		t.Skip("set SEA_COVERAGE_COMBINED_ARTIFACT_DIR to combined cross-domain evidence")
	}
	store, pool := coveredBaselineStore(t)
	ctx := context.Background()
	var refs struct {
		G1            sourcecoverage.GlobalPrefixRef    `json:"G1"`
		U1            sourcecoverage.SubjectCoverageRef `json:"U1W1"`
		CandidateHash string                            `json:"candidate_w1_after_tail_sha256"`
	}
	refBody, err := os.ReadFile(filepath.Join(artifactDir, "two-joined-ref.json"))
	if err != nil || json.Unmarshal(refBody, &refs) != nil {
		t.Fatalf("combined Publisher/H10 refs unavailable: %v", err)
	}
	indexW1, err := os.ReadFile(filepath.Join(artifactDir, "artifacts/two/G1/event-index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	batchW1, err := os.ReadFile(filepath.Join(artifactDir, "artifacts/two/G1/batch-evidence.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	indexW3, err := os.ReadFile(filepath.Join(artifactDir, "artifacts/two/G3/event-index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sparse, err := os.ReadFile(filepath.Join(artifactDir, "artifacts/two/U1W1/sparse-index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	candidateRaw, err := os.ReadFile(filepath.Join(artifactDir, "artifacts/two/candidates/candidate_w1_after_tail_sha256.json"))
	if err != nil || coveredBytesHash(candidateRaw) != refs.CandidateHash {
		t.Fatalf("combined H10 candidate bytes/hash unavailable: %v", err)
	}
	var candidate featurebaseline.CoveredCandidate
	if err := json.Unmarshal(candidateRaw, &candidate); err != nil {
		t.Fatal(err)
	}
	if candidate.AsOf.After(time.Now().UTC()) {
		t.Fatal("combined candidate business time is not yet eligible")
	}
	root := strings.Split(refs.G1.EventIndexURL, "/"+refs.G1.WarehouseGeneration+"/")[0]
	candidateURL := root + "/" + candidate.Generation + "/covered-baseline/" + refs.CandidateHash + ".json"
	contents := map[string][]byte{refs.G1.EventIndexURL: indexW1,
		refs.G1.BatchEvidenceURL: batchW1, refs.U1.SparseIndexURL: sparse,
		candidateURL: candidateRaw}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := "http://" + r.Host + r.URL.RequestURI()
		body, ok := contents[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", target.Host)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 3 * time.Second, Transport: transport}
	fetch := func(address, hash string) []byte {
		t.Helper()
		response, err := client.Get(address)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
		if err != nil || response.StatusCode != http.StatusOK || coveredBytesHash(body) != hash {
			t.Fatalf("original URL/path or object SHA differed: status=%d err=%v", response.StatusCode, err)
		}
		return body
	}
	if !bytes.Equal(fetch(refs.G1.EventIndexURL, refs.G1.EventIndexSHA256), indexW1) ||
		!bytes.Equal(fetch(refs.G1.BatchEvidenceURL, refs.G1.BatchEvidenceSHA256), batchW1) ||
		!bytes.Equal(fetch(refs.U1.SparseIndexURL, refs.U1.SparseIndexSHA256), sparse) ||
		!bytes.Equal(fetch(candidateURL, refs.CandidateHash), candidateRaw) {
		t.Fatal("combined source object changed in HTTP fixture")
	}
	proof := appendArchivedCoverageFacts(t, store, indexW3)
	verifier, err := usermodel.NewCoverageVerifier(store, proof)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyPrefix(ctx, refs.G1, indexW1, batchW1); err != nil {
		t.Fatalf("combined Publisher exact global index: %v", err)
	}
	if _, err := verifier.VerifySubject(ctx, refs.U1, sparse); err != nil {
		t.Fatalf("combined Publisher exact subject index: %v", err)
	}
	state, err := store.CoveredStateAt(ctx, candidate.Subject, refs.G1, candidate.AsOf)
	if err != nil || len(state.ActiveAtPrefix) != 1 || len(state.Tail) != 1 ||
		state.Tail[0].Status != "accepted" || state.CurrentComplete {
		t.Fatalf("combined H10 historical W1 state: %+v %v", state, err)
	}
	receipt, err := store.AcceptCoveredBaseline(ctx, usermodel.CoveredBaselineSubmission{
		ArtifactURL: candidateURL, ArtifactSHA256: refs.CandidateHash,
		CandidateJSON: candidateRaw, FeatureSpec: featurebaseline.FavoriteCandidateSpec()})
	if err != nil || receipt.Status != "accepted_historical_default_off" || receipt.Replay {
		t.Fatalf("combined Publisher/Verifier/H10 original candidate not accepted: %+v %v", receipt, err)
	}
	var saved []byte
	if err := pool.QueryRow(ctx, `SELECT candidate_raw FROM usermodel_covered_baselines_v2
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND revision=$4`,
		candidate.Subject.AuthorityID, candidate.Subject.TenantID, candidate.Subject.SubjectID,
		candidate.Revision).Scan(&saved); err != nil || !bytes.Equal(saved, candidateRaw) {
		t.Fatalf("combined original candidate bytes not retained: %v", err)
	}
	if replay, err := store.AcceptCoveredBaseline(ctx, usermodel.CoveredBaselineSubmission{
		ArtifactURL: candidateURL, ArtifactSHA256: refs.CandidateHash,
		CandidateJSON: candidateRaw, FeatureSpec: featurebaseline.FavoriteCandidateSpec()}); err != nil || !replay.Replay {
		t.Fatalf("combined immutable candidate replay: %+v %v", replay, err)
	}
	var pointers int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM usermodel_serving_pointers`).Scan(&pointers); err != nil || pointers != 0 {
		t.Fatalf("combined historical acceptance activated Serving: count=%d err=%v", pointers, err)
	}
}
