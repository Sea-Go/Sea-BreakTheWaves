package featurebaseline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/sourcecoverage"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/usermodel"
)

var ErrCoveredContract = errors.New("covered feature candidate contract mismatch")
var ErrCoveredUnsupportedSpec = errors.New("covered feature candidate supports fact counts only")

// CoveredState mirrors the narrow WS08 CoveredStateAt result. The real PG
// adapter copies the same fields after verifying the full global index; an
// in-memory adapter tests this interface without reimplementing PG folding.
type CoveredState struct {
	Coverage        sourcecoverage.SubjectCoverageRef
	StateVersion    int64
	ActiveAtPrefix  []usermodel.Fact
	Tail            []CoveredTailEvent
	CurrentComplete bool
}

type CoveredTailEvent struct {
	usermodel.EventKey
	Offset int64  `json:"offset"`
	Status string `json:"status"`
}

// CoveredStateReader must return the verified SubjectCoverageRef, historical
// state at W, and later tail in one repeatable-read source snapshot. A hash
// shaped like a receipt is not proof that the global [1,W] index was checked.
type CoveredStateReader interface {
	CoveredStateAt(context.Context, usermodel.SubjectRef, sourcecoverage.GlobalPrefixRef, time.Time) (CoveredState, error)
}

// CoveredStateFunc lets the WS08 PG Store be adapted without adding a second
// historical-fold implementation or exposing its SQL to this module.
type CoveredStateFunc func(context.Context, usermodel.SubjectRef, sourcecoverage.GlobalPrefixRef, time.Time) (CoveredState, error)

func (f CoveredStateFunc) CoveredStateAt(ctx context.Context, subject usermodel.SubjectRef,
	prefix sourcecoverage.GlobalPrefixRef, cutoff time.Time) (CoveredState, error) {
	return f(ctx, subject, prefix, cutoff)
}

// CoveredObjects is the immutable artifact seam. Production's local S3 adapter
// and a deterministic memory adapter both obey hash verification on readback.
type CoveredObjects interface {
	ReadFixed(context.Context, string, string) ([]byte, error)
	PutFixed(context.Context, string, []byte) error
}

type CoveredBuilder struct {
	State        CoveredStateReader
	Objects      CoveredObjects
	ArtifactRoot string
	Now          func() time.Time
}

// CoveredCandidate is a v2 default-off artifact, not a usermodel v1 baseline
// or an accepted FeatureSnapshot. Accept v2 and Serving own later publication.
type CoveredCandidate struct {
	SchemaVersion     string                            `json:"schema_version"`
	Status            string                            `json:"status"`
	Subject           usermodel.SubjectRef              `json:"subject_ref"`
	Generation        string                            `json:"generation"`
	Revision          int64                             `json:"revision"`
	SpecVersion       string                            `json:"feature_spec_version"`
	SpecHash          string                            `json:"feature_spec_hash"`
	Coverage          sourcecoverage.SubjectCoverageRef `json:"subject_coverage"`
	AsOf              time.Time                         `json:"as_of"`
	AvailableAt       time.Time                         `json:"available_at"`
	InputStateVersion int64                             `json:"input_state_version"`
	CurrentComplete   bool                              `json:"current_complete"`
	Contributions     []usermodel.Fact                  `json:"contributions"`
	Tail              []CoveredTailEvent                `json:"tail"`
	Values            []usermodel.FeatureValue          `json:"values"`
}

type CoveredBuildResult struct {
	Candidate    CoveredCandidate `json:"candidate"`
	ArtifactURL  string           `json:"artifact_url"`
	ArtifactHash string           `json:"artifact_sha256"`
}

// BuildCovered accepts only a coverage ref returned by the trusted WS08
// reader. It never infers W from a per-subject v1 watermark or Current.Active.
func (b CoveredBuilder) BuildCovered(ctx context.Context, subject usermodel.SubjectRef,
	spec usermodel.FeatureSpec, generation string, revision int64,
	prefix sourcecoverage.GlobalPrefixRef) (CoveredBuildResult, error) {
	var out CoveredBuildResult
	if b.State == nil || b.Objects == nil || !name.MatchString(generation) || revision <= 0 {
		return out, ErrCoveredContract
	}
	root, err := coveredObjectRoot(b.ArtifactRoot)
	if err != nil {
		return out, err
	}
	manifestHash, err := sourcecoverage.GlobalManifestHash(prefix)
	if err != nil || manifestHash != prefix.ManifestSHA256 {
		return out, fmt.Errorf("%w: unbound global prefix", ErrCoveredContract)
	}
	through, err := coveredOffset(prefix.ThroughOffset)
	if err != nil {
		return out, err
	}
	prepared, specHash, err := usermodel.PrepareFeatureSpec(spec)
	if err != nil {
		return out, err
	}
	for _, def := range prepared.Features {
		if def.Source != "fact" || def.Mode != "count" {
			return out, ErrCoveredUnsupportedSpec
		}
	}
	now := time.Now
	if b.Now != nil {
		now = b.Now
	}
	cutoff := now().UTC().Truncate(time.Microsecond)
	if cutoff.IsZero() {
		return out, ErrCoveredContract
	}
	state, err := b.State.CoveredStateAt(ctx, subject, prefix, cutoff)
	if err != nil {
		return out, fmt.Errorf("read verified covered history: %w", err)
	}
	if err := checkCoveredReceipt(subject, prefix, state.Coverage); err != nil || state.StateVersion < 0 {
		return out, ErrCoveredContract
	}
	if !coveredURLWithin(root, state.Coverage.SparseIndexURL) {
		return out, fmt.Errorf("%w: sparse index outside artifact root", ErrCoveredContract)
	}
	sparse, err := b.Objects.ReadFixed(ctx, state.Coverage.SparseIndexURL, state.Coverage.SparseIndexSHA256)
	if err != nil || coveredHash(sparse) != state.Coverage.SparseIndexSHA256 {
		return out, fmt.Errorf("%w: sparse index bytes differ", ErrCoveredContract)
	}
	index, err := readCoveredSparse(sparse, subject, prefix.Producer, through, state.Coverage.EventCount)
	if err != nil {
		return out, err
	}
	contributions, err := checkCoveredContributions(subject, prefix.Producer, cutoff, state.ActiveAtPrefix, index)
	if err != nil {
		return out, err
	}
	tail, err := checkCoveredTail(prefix.Producer, through, state.Tail, state.CurrentComplete)
	if err != nil {
		return out, err
	}
	values := coveredCountValues(prepared, contributions, cutoff)
	candidate := CoveredCandidate{SchemaVersion: "sea.user-feature-baseline.covered.v2", Status: "candidate_default_off",
		Subject: subject, Generation: generation, Revision: revision, SpecVersion: prepared.Version,
		SpecHash: specHash, Coverage: state.Coverage, AsOf: cutoff, AvailableAt: cutoff,
		InputStateVersion: state.StateVersion, CurrentComplete: state.CurrentComplete,
		Contributions: contributions, Tail: tail, Values: values}
	body, err := json.Marshal(candidate)
	if err != nil {
		return out, err
	}
	artifactHash := coveredHash(body)
	artifactURL := strings.TrimRight(root.String(), "/") + "/" + generation + "/covered-baseline/" + artifactHash + ".json"
	if err := b.Objects.PutFixed(ctx, artifactURL, body); err != nil {
		return out, fmt.Errorf("write immutable covered candidate: %w", err)
	}
	readback, err := b.Objects.ReadFixed(ctx, artifactURL, artifactHash)
	if err != nil || !bytes.Equal(readback, body) {
		return out, fmt.Errorf("%w: candidate readback differs", ErrCoveredContract)
	}
	return CoveredBuildResult{Candidate: candidate, ArtifactURL: artifactURL, ArtifactHash: artifactHash}, nil
}

func coveredHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func coveredOffset(raw string) (int64, error) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 1 || n > sourcecoverage.MaxDCOffset || strconv.FormatInt(n, 10) != raw {
		return 0, ErrCoveredContract
	}
	return n, nil
}

func checkCoveredReceipt(subject usermodel.SubjectRef, prefix sourcecoverage.GlobalPrefixRef,
	ref sourcecoverage.SubjectCoverageRef) error {
	if ref.Subject != (sourcecoverage.SubjectRef{AuthorityID: subject.AuthorityID, TenantID: subject.TenantID, SubjectID: subject.SubjectID}) ||
		ref.Prefix != prefix || ref.SchemaVersion != sourcecoverage.SchemaVersion {
		return ErrCoveredContract
	}
	hash, err := sourcecoverage.SubjectReceiptHash(ref)
	if err != nil || hash != ref.ReceiptSHA256 {
		return ErrCoveredContract
	}
	return nil
}

func coveredObjectRoot(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, ErrCoveredContract
	}
	return parsed, nil
}

func coveredURLWithin(root *url.URL, raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != root.Scheme || parsed.Host != root.Host ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	rootPath := strings.TrimRight(root.EscapedPath(), "/")
	return rootPath == "" || strings.HasPrefix(parsed.EscapedPath(), rootPath+"/")
}

func readCoveredSparse(body []byte, subject usermodel.SubjectRef, producer string, through, expectedCount int64) (map[string]sourcecoverage.EventIndexRow, error) {
	if len(body) > 32<<20 || expectedCount < 0 || expectedCount > through {
		return nil, ErrCoveredContract
	}
	rows := make([]sourcecoverage.EventIndexRow, 0)
	seen := make(map[string]sourcecoverage.EventIndexRow)
	var previous int64
	if len(body) != 0 {
		if body[len(body)-1] != '\n' {
			return nil, ErrCoveredContract
		}
		for _, line := range bytes.Split(body[:len(body)-1], []byte{'\n'}) {
			var row sourcecoverage.EventIndexRow
			decoder := json.NewDecoder(bytes.NewReader(line))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&row) != nil || decoder.Decode(new(any)) != io.EOF {
				return nil, ErrCoveredContract
			}
			offset, err := coveredOffset(row.Offset)
			if err != nil || offset > through || offset <= previous || row.Producer != producer ||
				row.Subject != (sourcecoverage.SubjectRef{AuthorityID: subject.AuthorityID, TenantID: subject.TenantID, SubjectID: subject.SubjectID}) ||
				row.EventID == "" || !digest.MatchString(row.InputHash) || row.RTWSourceHash != row.InputHash || seen[row.EventID].EventID != "" {
				return nil, ErrCoveredContract
			}
			previous = offset
			rows = append(rows, row)
			seen[row.EventID] = row
		}
	}
	canonical, _, count, err := sourcecoverage.SubjectIndexJSONL(sourcecoverage.SubjectRef{
		AuthorityID: subject.AuthorityID, TenantID: subject.TenantID, SubjectID: subject.SubjectID}, rows)
	if err != nil || count != expectedCount || !bytes.Equal(canonical, body) {
		return nil, ErrCoveredContract
	}
	return seen, nil
}

func checkCoveredContributions(subject usermodel.SubjectRef, producer string, cutoff time.Time,
	active []usermodel.Fact, index map[string]sourcecoverage.EventIndexRow) ([]usermodel.Fact, error) {
	result := slices.Clone(active)
	seen := make(map[string]bool, len(result))
	for i := range result {
		fact := &result[i]
		row, exists := index[fact.EventID]
		knownAction := fact.Action == usermodel.Assert || fact.Action == usermodel.Correct
		knownKind := fact.Kind == usermodel.ProductAction || fact.Kind == usermodel.Reading ||
			fact.Kind == usermodel.Impression || fact.Kind == usermodel.LocalSemantic || fact.Kind == usermodel.SelfReport
		if !exists || seen[fact.EventID] || fact.Subject != subject || fact.Producer != producer ||
			fact.Status != "accepted" || !knownAction || !knownKind ||
			fact.EvidenceHash != row.InputHash || fact.OccurredAt.After(cutoff) || fact.ObservedAt.After(cutoff) {
			return nil, ErrCoveredContract
		}
		seen[fact.EventID] = true
		// Attribution is a mutable read projection, not an immutable source
		// contribution and cannot change the covered artifact on replay.
		fact.ImpressionLinked, fact.LinkedImpression = false, ""
	}
	sort.Slice(result, func(i, j int) bool { return result[i].EventID < result[j].EventID })
	return result, nil
}

func checkCoveredTail(producer string, through int64, input []CoveredTailEvent, complete bool) ([]CoveredTailEvent, error) {
	if complete && len(input) > 0 {
		return nil, ErrCoveredContract
	}
	tail := slices.Clone(input)
	seen := make(map[string]bool, len(tail))
	for _, event := range tail {
		if event.Producer != producer || event.EventID == "" || seen[event.EventID] ||
			event.Offset <= through || event.Offset > sourcecoverage.MaxDCOffset ||
			(event.Status != "accepted" && event.Status != "pending_dependency") {
			return nil, ErrCoveredContract
		}
		seen[event.EventID] = true
	}
	sort.Slice(tail, func(i, j int) bool {
		if tail[i].Offset == tail[j].Offset {
			return tail[i].EventID < tail[j].EventID
		}
		return tail[i].Offset < tail[j].Offset
	})
	return tail, nil
}

func coveredCountValues(spec usermodel.FeatureSpec, facts []usermodel.Fact, cutoff time.Time) []usermodel.FeatureValue {
	values := make([]usermodel.FeatureValue, 0, len(spec.Features))
	for _, def := range spec.Features {
		var count int64
		for _, fact := range facts {
			if fact.Kind != def.Kind || fact.Predicate != def.Predicate ||
				def.WindowSeconds > 0 && !fact.OccurredAt.After(cutoff.Add(-time.Duration(def.WindowSeconds)*time.Second)) {
				continue
			}
			count++
		}
		values = append(values, usermodel.FeatureValue{Name: def.Name, Value: strconv.FormatInt(count, 10), Missing: count == 0})
	}
	return values
}
