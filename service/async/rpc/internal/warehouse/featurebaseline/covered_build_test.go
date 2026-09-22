package featurebaseline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/sourcecoverage"
)

type fixedCoveredReader struct {
	state  CoveredState
	err    error
	calls  int
	cutoff time.Time
}

func (r *fixedCoveredReader) CoveredStateAt(_ context.Context, _ usermodel.SubjectRef,
	_ sourcecoverage.GlobalPrefixRef, cutoff time.Time) (CoveredState, error) {
	r.calls++
	r.cutoff = cutoff
	return r.state, r.err
}

type fixedCoveredObjects struct {
	values map[string][]byte
	writes int
}

func (o *fixedCoveredObjects) ReadFixed(_ context.Context, target, _ string) ([]byte, error) {
	body, ok := o.values[target]
	if !ok {
		return nil, errors.New("object missing")
	}
	return bytes.Clone(body), nil
}

func (o *fixedCoveredObjects) PutFixed(_ context.Context, target string, body []byte) error {
	if old, ok := o.values[target]; ok && !bytes.Equal(old, body) {
		return errors.New("immutable object conflict")
	}
	o.values[target] = bytes.Clone(body)
	o.writes++
	return nil
}

func coveredFixture(t *testing.T, root string, subject usermodel.SubjectRef) (sourcecoverage.GlobalPrefixRef,
	sourcecoverage.SubjectCoverageRef, []byte, usermodel.Fact) {
	t.Helper()
	owner := sourcecoverage.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	event := json.RawMessage(`{"event_id":"favorite.1.v1","producer":"rtw.community.favorite"}`)
	eventHash := coveredHash(event)
	row := sourcecoverage.EventIndexRow{Producer: "rtw.community.favorite", Offset: "1", EventID: "favorite.1.v1",
		InputHash: eventHash, RTWSourceHash: eventHash, ReceiptID: "dc-receipt-1",
		ReceivedAt: "2026-09-15T00:00:01Z", Subject: owner, EventSpec: event}
	_, fullHash, err := sourcecoverage.EventIndexJSONL(row.Producer, 1, []sourcecoverage.EventIndexRow{row})
	if err != nil {
		t.Fatal(err)
	}
	prefix := sourcecoverage.GlobalPrefixRef{SchemaVersion: sourcecoverage.SchemaVersion,
		Producer: row.Producer, Origin: "1", ThroughOffset: "1", BindingPolicyID: "rtw.favorite.authority.v1",
		WarehouseConsumer: "btw-warehouse-favorite", WarehouseGeneration: "wh_favorite_001",
		EventIndexURL: root + "/coverage/full.jsonl", EventIndexSHA256: fullHash,
		BatchEvidenceURL: root + "/coverage/batches.jsonl", BatchEvidenceSHA256: strings.Repeat("a", 64)}
	prefix.ManifestSHA256, err = sourcecoverage.GlobalManifestHash(prefix)
	if err != nil {
		t.Fatal(err)
	}
	slice := []sourcecoverage.EventIndexRow{}
	if subject.SubjectID == owner.SubjectID {
		slice = append(slice, row)
	}
	sparse, sparseHash, count, err := sourcecoverage.SubjectIndexJSONL(sourcecoverage.SubjectRef{
		AuthorityID: subject.AuthorityID, TenantID: subject.TenantID, SubjectID: subject.SubjectID}, slice)
	if err != nil {
		t.Fatal(err)
	}
	ref := sourcecoverage.SubjectCoverageRef{SchemaVersion: sourcecoverage.SchemaVersion,
		Subject: sourcecoverage.SubjectRef{AuthorityID: subject.AuthorityID, TenantID: subject.TenantID, SubjectID: subject.SubjectID},
		Prefix:  prefix, SparseIndexURL: root + "/coverage/" + subject.SubjectID + ".jsonl",
		SparseIndexSHA256: sparseHash, EventCount: count}
	ref.ReceiptSHA256, err = sourcecoverage.SubjectReceiptHash(ref)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 15, 0, 0, 1, 0, time.UTC)
	fact := usermodel.Fact{Event: usermodel.Event{Subject: subject,
		EventKey: usermodel.EventKey{Producer: row.Producer, EventID: row.EventID},
		Action:   usermodel.Assert, Kind: usermodel.ProductAction, Predicate: "favorite",
		ValueRef: "article/article-1", EvidenceRef: "dc:event:rtw.community.favorite:1", EvidenceHash: eventHash,
		OccurredAt: at, ObservedAt: at, SourcePartition: "dc:rtw.community.favorite", SourceSequence: 1},
		Status: "accepted", AcceptedVersion: 1}
	return prefix, ref, sparse, fact
}

func favoriteCoveredSpec() usermodel.FeatureSpec { return FavoriteCandidateSpec() }

func TestBuildCoveredKeepsHistoricalW1AssertAndTailRetract(t *testing.T) {
	const root = "https://objects.example.invalid/fixed"
	subject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	prefix, ref, sparse, fact := coveredFixture(t, root, subject)
	cutoff := time.Date(2026, 9, 15, 0, 1, 0, 0, time.UTC)
	reader := &fixedCoveredReader{state: CoveredState{Coverage: ref, StateVersion: 3,
		ActiveAtPrefix: []usermodel.Fact{fact}, CurrentComplete: false,
		Tail: []CoveredTailEvent{
			{EventKey: usermodel.EventKey{Producer: prefix.Producer, EventID: "favorite.1.v2"}, Offset: 3, Status: "accepted"},
			{EventKey: usermodel.EventKey{Producer: prefix.Producer, EventID: "favorite.2.v1"}, Offset: 2, Status: "pending_dependency"},
		}}}
	objects := &fixedCoveredObjects{values: map[string][]byte{ref.SparseIndexURL: sparse}}
	builder := CoveredBuilder{State: reader, Objects: objects, ArtifactRoot: root, Now: func() time.Time { return cutoff }}
	result, err := builder.BuildCovered(context.Background(), subject, favoriteCoveredSpec(), "covered_w1", 1, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if result.Candidate.Status != "candidate_default_off" || result.Candidate.SchemaVersion != "sea.user-feature-baseline.covered.v2" ||
		result.Candidate.CurrentComplete || result.Candidate.Coverage != ref || len(result.Candidate.Contributions) != 1 ||
		len(result.Candidate.Values) != 1 || result.Candidate.Values[0].Value != "1" || result.Candidate.Values[0].Missing ||
		len(result.Candidate.Tail) != 2 || result.Candidate.Tail[0].Offset != 2 || result.Candidate.Tail[1].Offset != 3 ||
		reader.calls != 1 || !reader.cutoff.Equal(cutoff) {
		t.Fatalf("W1 historical value or tail was lost: %+v", result.Candidate)
	}
	body := objects.values[result.ArtifactURL]
	if len(body) == 0 || coveredHash(body) != result.ArtifactHash || !strings.HasSuffix(result.ArtifactURL, result.ArtifactHash+".json") {
		t.Fatal("v2 candidate was not content-addressed")
	}
	var frozen CoveredCandidate
	if err := json.Unmarshal(body, &frozen); err != nil || frozen.Values[0].Value != "1" || len(frozen.Tail) != 2 {
		t.Fatalf("frozen candidate differs: %+v %v", frozen, err)
	}
	// A second build with the same covered PG snapshot and cutoff reuses bytes.
	replayed, err := builder.BuildCovered(context.Background(), subject, favoriteCoveredSpec(), "covered_w1", 1, prefix)
	if err != nil || replayed.ArtifactHash != result.ArtifactHash || replayed.ArtifactURL != result.ArtifactURL {
		t.Fatalf("exact candidate replay changed identity: %+v %v", replayed, err)
	}
}

func TestBuildCoveredRejectsUnverifiedRefAndTamperedObject(t *testing.T) {
	const root = "https://objects.example.invalid/fixed"
	subject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	prefix, ref, sparse, fact := coveredFixture(t, root, subject)
	reader := &fixedCoveredReader{state: CoveredState{Coverage: ref, StateVersion: 1,
		ActiveAtPrefix: []usermodel.Fact{fact}, CurrentComplete: true}}
	objects := &fixedCoveredObjects{values: map[string][]byte{ref.SparseIndexURL: sparse}}
	builder := CoveredBuilder{State: reader, Objects: objects, ArtifactRoot: root,
		Now: func() time.Time { return time.Date(2026, 9, 15, 0, 1, 0, 0, time.UTC) }}
	bad := prefix
	bad.EventIndexSHA256 = strings.Repeat("b", 64)
	if _, err := builder.BuildCovered(context.Background(), subject, favoriteCoveredSpec(), "covered_bad", 1, bad); !errors.Is(err, ErrCoveredContract) || reader.calls != 0 || objects.writes != 0 {
		t.Fatalf("forged global ref crossed reader seam: %v", err)
	}
	reader.err = errors.New("coverage was not independently verified")
	if _, err := builder.BuildCovered(context.Background(), subject, favoriteCoveredSpec(), "covered_bad", 1, prefix); err == nil || objects.writes != 0 {
		t.Fatalf("unverified PG coverage emitted an artifact: %v", err)
	}
	reader.err = nil
	objects.values[ref.SparseIndexURL] = append(bytes.Clone(sparse), 'x')
	if _, err := builder.BuildCovered(context.Background(), subject, favoriteCoveredSpec(), "covered_bad", 1, prefix); !errors.Is(err, ErrCoveredContract) || objects.writes != 0 {
		t.Fatalf("S3 sparse hash mismatch emitted an artifact: %v", err)
	}
	objects.values[ref.SparseIndexURL] = sparse
	reader.state.ActiveAtPrefix[0].EvidenceHash = strings.Repeat("c", 64)
	if _, err := builder.BuildCovered(context.Background(), subject, favoriteCoveredSpec(), "covered_bad", 1, prefix); !errors.Is(err, ErrCoveredContract) || objects.writes != 0 {
		t.Fatalf("PG fact hash disagreed with sparse source: %v", err)
	}
}

func TestBuildCoveredAcceptsVerifiedEmptySubjectSlice(t *testing.T) {
	const root = "https://objects.example.invalid/fixed"
	subject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1003"}
	prefix, ref, sparse, _ := coveredFixture(t, root, subject)
	reader := &fixedCoveredReader{state: CoveredState{Coverage: ref, StateVersion: 0, CurrentComplete: true}}
	objects := &fixedCoveredObjects{values: map[string][]byte{ref.SparseIndexURL: sparse}}
	result, err := (CoveredBuilder{State: reader, Objects: objects, ArtifactRoot: root,
		Now: func() time.Time { return time.Date(2026, 9, 15, 0, 1, 0, 0, time.UTC) }}).
		BuildCovered(context.Background(), subject, favoriteCoveredSpec(), "covered_empty", 1, prefix)
	if err != nil || ref.EventCount != 0 || len(result.Candidate.Contributions) != 0 ||
		len(result.Candidate.Values) != 1 || result.Candidate.Values[0].Value != "0" || !result.Candidate.Values[0].Missing {
		t.Fatalf("globally proven empty slice changed count: %+v %v", result.Candidate, err)
	}
}

func TestRunnerCoveredObjectsHTTPReadbackAndTamper(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			body, ok := objects[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
		case http.MethodPut:
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			objects[r.URL.Path] = body
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	subject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	root := server.URL + "/fixed"
	prefix, ref, sparse, fact := coveredFixture(t, root, subject)
	mu.Lock()
	objects["/fixed/coverage/1001.jsonl"] = sparse
	mu.Unlock()
	reader := &fixedCoveredReader{state: CoveredState{Coverage: ref, StateVersion: 1, ActiveAtPrefix: []usermodel.Fact{fact}, CurrentComplete: true}}
	builder := CoveredBuilder{State: reader, Objects: RunnerCoveredObjects{Runner: Runner{HTTPClient: server.Client()}},
		ArtifactRoot: root, Now: func() time.Time { return time.Date(2026, 9, 15, 0, 1, 0, 0, time.UTC) }}
	result, err := builder.BuildCovered(context.Background(), subject, favoriteCoveredSpec(), "covered_http", 1, prefix)
	if err != nil || result.ArtifactHash == "" {
		t.Fatalf("local S3 HTTP adapter: %+v %v", result, err)
	}
	mu.Lock()
	objects["/fixed/coverage/1001.jsonl"] = []byte("tampered")
	mu.Unlock()
	if _, err := builder.BuildCovered(context.Background(), subject, favoriteCoveredSpec(), "covered_http", 1, prefix); !errors.Is(err, ErrCoveredContract) {
		t.Fatalf("tampered S3 bytes accepted: %v", err)
	}
}

func TestBuildCoveredRejectsUnsupportedSpecAndInconsistentTail(t *testing.T) {
	const root = "https://objects.example.invalid/fixed"
	subject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	prefix, ref, sparse, fact := coveredFixture(t, root, subject)
	reader := &fixedCoveredReader{state: CoveredState{Coverage: ref, StateVersion: 1, ActiveAtPrefix: []usermodel.Fact{fact},
		CurrentComplete: true, Tail: []CoveredTailEvent{{EventKey: usermodel.EventKey{Producer: prefix.Producer, EventID: "future"}, Offset: 2, Status: "accepted"}}}}
	objects := &fixedCoveredObjects{values: map[string][]byte{ref.SparseIndexURL: sparse}}
	builder := CoveredBuilder{State: reader, Objects: objects, ArtifactRoot: root,
		Now: func() time.Time { return time.Date(2026, 9, 15, 0, 1, 0, 0, time.UTC) }}
	if _, err := builder.BuildCovered(context.Background(), subject, favoriteCoveredSpec(), "covered_bad", 1, prefix); !errors.Is(err, ErrCoveredContract) {
		t.Fatalf("tail was called currently complete: %v", err)
	}
	unsupported := usermodel.FeatureSpec{Version: "latest-v2", Features: []usermodel.FeatureDefinition{{
		Name: "favorite_latest", Source: "fact", Kind: usermodel.ProductAction, Predicate: "favorite",
		Mode: "latest", Default: "none", Vocabulary: []string{"article/a"}, OOV: "other",
	}}}
	if _, err := builder.BuildCovered(context.Background(), subject, unsupported, "covered_bad", 1, prefix); !errors.Is(err, ErrCoveredUnsupportedSpec) {
		t.Fatalf("unimplemented latest projection emitted candidate: %v", err)
	}
}
