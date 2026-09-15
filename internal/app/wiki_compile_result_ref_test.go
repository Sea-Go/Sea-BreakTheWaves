package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

const resultFixtureJobID = "11111111-1111-4111-8111-111111111111"
const resultFixtureAttemptID = "22222222-2222-4222-8222-222222222222"
const resultFixtureCompileID = "compile_44444444-4444-4444-8444-444444444444"
const resultFixtureRevisionID = "revision_55555555-5555-4555-8555-555555555555"
const resultFixtureJCSSHA256 = "b0a0447d2736bf4880c946a8e6dfb6602c2de5f889fb17bef1d0db26579eb63f"

type resultRefTrackedStore struct {
	artifacts.Store
	putCount            int
	corruptCandidateRef corpus.Ref
}

func (s *resultRefTrackedStore) Put(ctx context.Context, body []byte) (corpus.Ref, error) {
	s.putCount++
	return s.Store.Put(ctx, body)
}
func (s *resultRefTrackedStore) Get(ctx context.Context, ref corpus.Ref) ([]byte, error) {
	if ref == s.corruptCandidateRef {
		return []byte("forged local candidate"), nil
	}
	return s.Store.Get(ctx, ref)
}

func wikiResultFixture(t *testing.T) (WikiCompileCompletionEvidence, string, *resultRefTrackedStore) {
	t.Helper()
	root := t.TempDir()
	local, err := artifacts.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	const markdown = "# Wiki title\nOnly the frozen source."
	object, err := local.Put(context.Background(), []byte(markdown))
	if err != nil {
		t.Fatal(err)
	}
	const guidance = "use only fixed RTW source revisions"
	ticket := wikiCompileTicket{SchemaVersion: wikiCompileTicketVersion,
		SourceEventID:        "evt_33333333-3333-4333-8333-333333333333",
		SourceEventJCSDigest: strings.Repeat("b", 64),
		CompileID:            resultFixtureCompileID, ModuleID: "module_wiki", PageID: "page-one",
		BaseRevisionID: "revision_base", SourceRevisionIDs: []string{"source-r1", "source-r2"},
		GuidanceSHA256:   wikiCompileGuidanceHash(guidance),
		CompileInputHash: strings.Repeat("a", 64), Generation: "1", CancelVersion: "0"}
	input, err := json.Marshal(ticket)
	if err != nil {
		t.Fatal(err)
	}
	request := jobs.Submit{Producer: wikiCompileProducer,
		OperationID: "command:" + strings.Repeat("c", 64),
		RunRef:      "wiki-compile/" + resultFixtureCompileID,
		JobType:     WikiCompileJobType, ResourceProfile: wikiCompileResource,
		Input: input, Deadline: "2099-01-01T00:00:00Z", MaxAttempts: 3}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	const lease = "2098-12-31T23:00:00Z" // static golden; public builder checks current time
	job := jobs.Job{ID: resultFixtureJobID, InputHash: artifacts.Hash(canonical), Request: request,
		State: "running", Attempt: 1, LeaseEpoch: 1, CancelVersion: 0,
		AttemptID: resultFixtureAttemptID, WorkerID: "wiki-local-worker", LeaseExpiresAt: lease}
	accepted := ridethewind.Compile{CompileId: ticket.CompileID, ModuleId: ticket.ModuleID,
		PageId: ticket.PageID, BaseRevisionId: ticket.BaseRevisionID,
		SourceRevisionIds: append([]string(nil), ticket.SourceRevisionIDs...), Guidance: guidance,
		InputHash: ticket.CompileInputHash, State: "ACCEPTED", Generation: 1,
		AttemptId: job.AttemptID, LeaseEpoch: job.LeaseEpoch, CancelVersion: job.CancelVersion,
		LeaseExpiresAt: lease, RevisionId: resultFixtureRevisionID,
		ResultHash: strings.Repeat("d", 64)}
	candidate := content.WikiCompileCandidate{CompileID: accepted.CompileId,
		ModuleID: accepted.ModuleId, PageID: accepted.PageId, BaseRevision: accepted.BaseRevisionId,
		Generation: accepted.Generation, InputHash: accepted.InputHash,
		AttemptID: job.AttemptID, LeaseEpoch: job.LeaseEpoch, CancelVersion: job.CancelVersion,
		Title: "Wiki title", Markdown: markdown, ContentSHA256: object.SHA256,
		SourceRefs: []content.WikiCompileSourceRef{{RevisionID: "source-r1", Locator: "paragraph:1"},
			{RevisionID: "source-r2", Locator: "paragraph:2"}}}
	revision := ridethewind.Revision{RevisionId: accepted.RevisionId,
		ModuleId: accepted.ModuleId, EntityId: accepted.PageId, Kind: "wiki",
		BaseRevisionId: accepted.BaseRevisionId, Title: candidate.Title,
		MediaType: "text/markdown", ObjectKey: object.Key, ContentHash: object.SHA256,
		Content: candidate.Markdown, CreatedBy: "btw.compile/" + accepted.CompileId,
		SourceRefs: []ridethewind.SourceRef{{RevisionId: "source-r1", Locator: "paragraph:1"},
			{RevisionId: "source-r2", Locator: "paragraph:2"}}}
	evidence := WikiCompileCompletionEvidence{Job: job, Accepted: accepted,
		Revision: revision, Candidate: candidate, CandidateObject: object}
	return evidence, root, &resultRefTrackedStore{Store: local}
}

// This second reader uses the same sha256/<hash> namespace as RTW's Local
// ObjectStore without calling the BTW Store.Get method. It proves the stored
// manifest is addressable by its returned URI/hash under a shared Local root.
func readIndependentLocalResult(root string, ref corpus.Ref) ([]byte, error) {
	if !artifacts.ValidHash(ref.SHA256) || ref.Key != "sha256/"+ref.SHA256 {
		return nil, errors.New("invalid independent result reference")
	}
	f, err := os.Open(filepath.Join(root, ref.Key))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, artifacts.MaxBytes+1))
	if err != nil || len(body) > artifacts.MaxBytes || artifacts.Hash(body) != ref.SHA256 {
		return nil, errors.New("independent Local result hash mismatch")
	}
	return body, nil
}

func TestWikiCompileResultRefCanonicalLocalCrossReaderAndRetry(t *testing.T) {
	e, root, tracked := wikiResultFixture(t)
	ctx := context.Background()
	ref, err := BuildWikiCompileResultRef(ctx, e, tracked)
	if err != nil || ref.URI != "sha256:"+ref.Hash || ref.Hash != resultFixtureJCSSHA256 ||
		!artifacts.ValidHash(ref.Hash) ||
		ref.MediaType != wikiCompileResultMediaType || tracked.putCount != 1 {
		t.Fatalf("first Wiki technical ResultRef: %+v puts=%d err=%v", ref, tracked.putCount, err)
	}
	resultObject := corpus.Ref{Key: "sha256/" + ref.Hash, SHA256: ref.Hash}
	body, err := readIndependentLocalResult(root, resultObject)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := jsoncanonicalizer.Transform(body)
	if err != nil || !bytes.Equal(body, canonical) {
		t.Fatalf("result object is not unique JCS bytes: %s %v", body, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || len(fields) != 13 {
		t.Fatalf("result object did not contain exactly thirteen keys: %s %v", body, err)
	}
	for key := range fields {
		if key != strings.ToLower(key) || strings.ContainsRune(key, '\\') {
			t.Fatalf("nonliteral result manifest key: %q", key)
		}
	}
	for _, key := range []string{"generation", "lease_epoch", "cancel_version"} {
		if len(fields[key]) < 2 || fields[key][0] != '"' {
			t.Fatalf("large numeric field not encoded as decimal string: %s", fields[key])
		}
	}
	if string(fields["schema_version"]) != `"sea.wiki.compile-result.v1"` ||
		string(fields["job_id"]) != `"`+e.Job.ID+`"` ||
		string(fields["job_input_hash"]) != `"`+e.Job.InputHash+`"` ||
		string(fields["compile_input_hash"]) != `"`+e.Accepted.InputHash+`"` ||
		string(fields["candidate_content_sha256"]) != `"`+e.CandidateObject.SHA256+`"` ||
		string(fields["rtw_accept_result_hash"]) != `"`+e.Accepted.ResultHash+`"` {
		t.Fatalf("JCS manifest did not preserve separate DC/RTW/content hashes: %s", body)
	}
	again, err := BuildWikiCompileResultRef(ctx, e, tracked)
	if err != nil || again != ref || tracked.putCount != 2 {
		t.Fatalf("identical retry changed ResultRef bytes or hash: first=%+v retry=%+v puts=%d err=%v",
			ref, again, tracked.putCount, err)
	}
	second, err := readIndependentLocalResult(root, resultObject)
	if err != nil || !bytes.Equal(second, body) {
		t.Fatalf("same-key retry changed stored JCS byte: %v", err)
	}
	t.Logf("Wiki ResultRef Local JCS golden SHA256=%s", ref.Hash)
}

func TestWikiCompileResultRefRejectsFalseEvidenceBeforeManifestPut(t *testing.T) {
	checks := []struct {
		name   string
		change func(*WikiCompileCompletionEvidence, *resultRefTrackedStore)
	}{
		{"old-dc-state", func(e *WikiCompileCompletionEvidence, _ *resultRefTrackedStore) { e.Job.State = "cancelled" }},
		{"stale-dc-cancel", func(e *WikiCompileCompletionEvidence, _ *resultRefTrackedStore) { e.Job.CancelVersion = 1 }},
		{"stale-dc-epoch", func(e *WikiCompileCompletionEvidence, _ *resultRefTrackedStore) { e.Job.LeaseEpoch = 2 }},
		{"wrong-dc-input-hash", func(e *WikiCompileCompletionEvidence, _ *resultRefTrackedStore) {
			e.Job.InputHash = strings.Repeat("e", 64)
		}},
		{"old-rtw-generation", func(e *WikiCompileCompletionEvidence, _ *resultRefTrackedStore) { e.Accepted.Generation = 2 }},
		{"withdrawn-wiki", func(e *WikiCompileCompletionEvidence, _ *resultRefTrackedStore) { e.Revision.Withdrawn = true }},
		{"wrong-page-base", func(e *WikiCompileCompletionEvidence, _ *resultRefTrackedStore) {
			e.Revision.BaseRevisionId = "another-base"
		}},
		{"forged-source", func(e *WikiCompileCompletionEvidence, _ *resultRefTrackedStore) {
			e.Candidate.SourceRefs[0].RevisionID = "source-forged"
			e.Revision.SourceRefs[0].RevisionId = "source-forged"
		}},
		{"duplicate-source", func(e *WikiCompileCompletionEvidence, _ *resultRefTrackedStore) {
			e.Candidate.SourceRefs = append(e.Candidate.SourceRefs, e.Candidate.SourceRefs[0])
			e.Revision.SourceRefs = append(e.Revision.SourceRefs, e.Revision.SourceRefs[0])
		}},
		{"wrong-revision-bytes", func(e *WikiCompileCompletionEvidence, _ *resultRefTrackedStore) { e.Revision.Content = "fake" }},
		{"uncanonical-title", func(e *WikiCompileCompletionEvidence, _ *resultRefTrackedStore) {
			e.Candidate.Title = " Wiki title "
			e.Revision.Title = e.Candidate.Title
		}},
		{"forged-candidate-sha", func(e *WikiCompileCompletionEvidence, _ *resultRefTrackedStore) {
			e.Candidate.ContentSHA256 = strings.Repeat("f", 64)
		}},
		{"wrong-candidate-key", func(e *WikiCompileCompletionEvidence, _ *resultRefTrackedStore) {
			e.CandidateObject.Key = "sha256/" + strings.Repeat("f", 64)
		}},
		{"unreadable-candidate", func(e *WikiCompileCompletionEvidence, store *resultRefTrackedStore) {
			store.corruptCandidateRef = e.CandidateObject
		}},
		{"expired-dc-lease", func(e *WikiCompileCompletionEvidence, _ *resultRefTrackedStore) {
			e.Job.LeaseExpiresAt = "2020-01-01T00:00:00Z"
			e.Accepted.LeaseExpiresAt = e.Job.LeaseExpiresAt
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			e, _, tracked := wikiResultFixture(t)
			check.change(&e, tracked)
			if ref, err := BuildWikiCompileResultRef(context.Background(), e, tracked); err == nil || ref != (jobs.ResultRef{}) || tracked.putCount != 0 {
				t.Fatalf("false evidence wrote a technical manifest before RTW/DC proof: ref=%+v puts=%d err=%v",
					ref, tracked.putCount, err)
			}
		})
	}
}
