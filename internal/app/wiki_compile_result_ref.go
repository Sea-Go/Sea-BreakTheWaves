package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/google/uuid"
)

const WikiCompileResultRefContractID = "sea.wiki.compile-result.v1"
const wikiCompileResultMediaType = "application/vnd.sea.wiki-compile-result+json"

var ErrWikiCompileResultEvidence = errors.New("Wiki compile result evidence differs from current DC/RTW/candidate")
var ErrWikiCompileResultArtifact = errors.New("Wiki compile result object is not readable under its SHA256 key")

var wikiResultParagraph = regexp.MustCompile(`^paragraph:[1-9][0-9]*$`)

// WikiCompileCompletionEvidence is supplied only after RTW AcceptCompile has
// returned and the caller has re-read current GetJob/GetCompile/GetRevision.
// This builder makes a candidate ResultRef; it never calls DC Complete or
// moves the Wiki editing head/manual published Release pointer.
type WikiCompileCompletionEvidence struct {
	Job             jobs.Job
	Accepted        ridethewind.Compile
	Revision        ridethewind.Revision
	Candidate       content.WikiCompileCandidate
	CandidateObject corpus.Ref
}

// Exactly thirteen lowercase keys, all fixed source/attempt/result evidence.
// Large generation/epoch/cancel values are canonical decimal JSON strings.
type wikiCompileResultManifest struct {
	SchemaVersion       string `json:"schema_version"`
	JobID               string `json:"job_id"`
	JobInputHash        string `json:"job_input_hash"`
	AttemptID           string `json:"attempt_id"`
	LeaseEpoch          string `json:"lease_epoch"`
	CancelVersion       string `json:"cancel_version"`
	CompileID           string `json:"compile_id"`
	Generation          string `json:"generation"`
	CompileInputHash    string `json:"compile_input_hash"`
	CandidateObjectKey  string `json:"candidate_object_key"`
	CandidateContentSHA string `json:"candidate_content_sha256"`
	WikiRevisionID      string `json:"wiki_revision_id"`
	RTWAcceptResultHash string `json:"rtw_accept_result_hash"`
}

func resultSourceRefsMatch(accepted ridethewind.Compile,
	candidate content.WikiCompileCandidate, revision ridethewind.Revision) bool {
	if len(candidate.SourceRefs) < 1 || len(candidate.SourceRefs) > 64 ||
		len(revision.SourceRefs) != len(candidate.SourceRefs) {
		return false
	}
	allowed := make(map[string]struct{}, len(accepted.SourceRevisionIds))
	for _, id := range accepted.SourceRevisionIds {
		allowed[id] = struct{}{}
	}
	seen := make(map[content.WikiCompileSourceRef]struct{}, len(candidate.SourceRefs))
	for i, ref := range candidate.SourceRefs {
		if _, ok := allowed[ref.RevisionID]; !ok || !wikiResultParagraph.MatchString(ref.Locator) ||
			ref.RevisionID != revision.SourceRefs[i].RevisionId ||
			ref.Locator != revision.SourceRefs[i].Locator {
			return false
		}
		if _, duplicate := seen[ref]; duplicate {
			return false
		}
		seen[ref] = struct{}{}
	}
	return true
}

func wikiResultEvidence(ctx context.Context, e WikiCompileCompletionEvidence,
	store artifacts.Store, now time.Time) (wikiCompileResultManifest, time.Time, error) {
	var manifest wikiCompileResultManifest
	if ctx == nil || ctx.Err() != nil || nilDependency(store) {
		return manifest, time.Time{}, ErrWikiCompileResultEvidence
	}
	jobID, idErr := uuid.Parse(e.Job.ID)
	attemptID, attemptErr := uuid.Parse(e.Job.AttemptID)
	if idErr != nil || attemptErr != nil || jobID.String() != e.Job.ID ||
		attemptID.String() != e.Job.AttemptID || e.Job.Result != nil {
		return manifest, time.Time{}, ErrWikiCompileResultEvidence
	}
	claim, err := DecodeWikiCompileClaim(e.Job, e.Job.WorkerID, now)
	if err != nil {
		return manifest, time.Time{}, errors.Join(ErrWikiCompileResultEvidence, err)
	}
	accepted := e.Accepted
	if !sameAcceptedWikiCompile(accepted, claim, e.Job) ||
		!artifacts.ValidHash(accepted.ResultHash) || accepted.ErrorCode != "" {
		return manifest, time.Time{}, ErrWikiCompileResultEvidence
	}
	rtwExpiry, err := time.Parse(time.RFC3339Nano, accepted.LeaseExpiresAt)
	if err != nil || !rtwExpiry.Equal(claim.LeaseExpiry) || !rtwExpiry.After(now) {
		return manifest, time.Time{}, errors.Join(ErrWikiCompileResultEvidence, ErrWikiCompileLease)
	}
	candidate := e.Candidate
	markdown := []byte(candidate.Markdown)
	if candidate.CompileID != accepted.CompileId || candidate.ModuleID != accepted.ModuleId ||
		candidate.PageID != accepted.PageId || candidate.BaseRevision != accepted.BaseRevisionId ||
		candidate.Generation != accepted.Generation || candidate.InputHash != accepted.InputHash ||
		candidate.AttemptID != e.Job.AttemptID || candidate.LeaseEpoch != e.Job.LeaseEpoch ||
		candidate.CancelVersion != e.Job.CancelVersion ||
		!utf8.ValidString(candidate.Title) || strings.ContainsRune(candidate.Title, 0) ||
		strings.TrimSpace(candidate.Title) == "" ||
		candidate.Title != strings.TrimSpace(candidate.Title) || len(candidate.Title) > 200 ||
		!utf8.Valid(markdown) || bytes.IndexByte(markdown, 0) >= 0 ||
		len(markdown) < 1 || len(markdown) > 48<<10 ||
		strings.TrimSpace(candidate.Markdown) == "" ||
		!artifacts.ValidHash(candidate.ContentSHA256) ||
		artifacts.Hash(markdown) != candidate.ContentSHA256 {
		return manifest, time.Time{}, ErrWikiCompileResultEvidence
	}
	object := e.CandidateObject
	if object.Key != "sha256/"+candidate.ContentSHA256 ||
		object.SHA256 != candidate.ContentSHA256 {
		return manifest, time.Time{}, ErrWikiCompileResultEvidence
	}
	revision := e.Revision
	if revision.RevisionId == "" || revision.RevisionId != accepted.RevisionId ||
		revision.Withdrawn || revision.Kind != "wiki" ||
		revision.ModuleId != accepted.ModuleId || revision.EntityId != accepted.PageId ||
		revision.BaseRevisionId != accepted.BaseRevisionId ||
		revision.Title != candidate.Title || revision.MediaType != "text/markdown" ||
		revision.ObjectKey != object.Key || revision.ContentHash != object.SHA256 ||
		revision.CreatedBy != "btw.compile/"+accepted.CompileId ||
		!bytes.Equal([]byte(revision.Content), markdown) ||
		artifacts.Hash([]byte(revision.Content)) != revision.ContentHash ||
		!resultSourceRefsMatch(accepted, candidate, revision) {
		return manifest, time.Time{}, ErrWikiCompileResultEvidence
	}
	manifest = wikiCompileResultManifest{SchemaVersion: WikiCompileResultRefContractID,
		JobID: e.Job.ID, JobInputHash: e.Job.InputHash, AttemptID: e.Job.AttemptID,
		LeaseEpoch:    strconv.FormatInt(e.Job.LeaseEpoch, 10),
		CancelVersion: strconv.FormatInt(e.Job.CancelVersion, 10),
		CompileID:     accepted.CompileId, Generation: strconv.FormatInt(accepted.Generation, 10),
		CompileInputHash: accepted.InputHash, CandidateObjectKey: object.Key,
		CandidateContentSHA: object.SHA256, WikiRevisionID: revision.RevisionId,
		RTWAcceptResultHash: accepted.ResultHash}
	return manifest, claim.LeaseExpiry, nil
}

// BuildWikiCompileResultRef writes only the JCS result manifest after proving
// the candidate Markdown exists under its SHA key and matches the RTW
// accepted revision byte for byte. A later DC Complete needs another current
// GetJob/revision check and its own compare-and-set lease receipt.
func BuildWikiCompileResultRef(ctx context.Context, e WikiCompileCompletionEvidence,
	store artifacts.Store) (jobs.ResultRef, error) {
	var result jobs.ResultRef
	manifest, expiry, err := wikiResultEvidence(ctx, e, store, time.Now())
	if err != nil {
		return result, err
	}
	markdown, err := store.Get(ctx, e.CandidateObject)
	if err != nil || !bytes.Equal(markdown, []byte(e.Candidate.Markdown)) {
		return result, errors.Join(ErrWikiCompileResultArtifact, err)
	}
	if ctx.Err() != nil || !expiry.After(time.Now()) {
		return result, errors.Join(ErrWikiCompileResultEvidence, ErrWikiCompileLease)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return result, err
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil || len(canonical) > artifacts.MaxBytes {
		return result, ErrWikiCompileResultEvidence
	}
	want := artifacts.Reference(canonical)
	ref, err := store.Put(ctx, canonical)
	if err != nil || ref != want {
		return result, errors.Join(ErrWikiCompileResultArtifact, err)
	}
	readback, err := store.Get(ctx, ref)
	if err != nil || !bytes.Equal(readback, canonical) {
		return result, errors.Join(ErrWikiCompileResultArtifact, err)
	}
	if ctx.Err() != nil || !expiry.After(time.Now()) {
		return result, errors.Join(ErrWikiCompileResultEvidence, ErrWikiCompileLease)
	}
	return jobs.ResultRef{URI: "sha256:" + ref.SHA256, Hash: ref.SHA256,
		MediaType: wikiCompileResultMediaType}, nil
}
