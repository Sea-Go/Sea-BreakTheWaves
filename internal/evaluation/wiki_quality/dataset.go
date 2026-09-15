package wiki_quality

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
)

type CaseManifest struct {
	SchemaVersion           string       `json:"schema_version"`
	MetricRevision          string       `json:"metric_revision"`
	RubricVersion           string       `json:"rubric_version"`
	CaseID                  string       `json:"case_id"`
	Scope                   Scope        `json:"scope"`
	Sources                 []Source     `json:"sources"`
	Target                  WikiVersion  `json:"target"`
	Candidate               *Candidate   `json:"candidate,omitempty"`
	PreviousAI              *WikiVersion `json:"previous_ai,omitempty"`
	Review                  Review       `json:"review"`
	Cost                    *CostReceipt `json:"cost,omitempty"`
	EvaluationState         State        `json:"evaluation_state"`
	NotEvaluableReason      string       `json:"not_evaluable_reason,omitempty"`
	AuthorityEvidenceSHA256 string       `json:"authority_evidence_sha256,omitempty"`
}

// FrozenCase holds immutable JCS byte artifacts. CaseSHA is the hash of this
// quality manifest, separate from source bytes, candidate/Wiki Markdown,
// RTW Accept, DC ResultRef manifest and DC technical Result hashes.
type FrozenCase struct {
	Manifest       CaseManifest
	ManifestJCS    []byte
	ManifestSHA256 string
	Report         Report
	ReportJCS      []byte
	ReportSHA256   string
}

// EvaluateCase never calls a model or assigns a semantic grade. A real human
// case needs the future RTW EventSpec/PG AuthorityVerifier; no verifier or
// incomplete fact/revision scope yields not_evaluable with a frozen reason.
func EvaluateCase(ctx context.Context, input Input,
	verifiers Verifiers) (FrozenCase, error) {
	if ctx == nil || ctx.Err() != nil {
		return FrozenCase{}, ErrEvidence
	}
	if err := validateInputShape(input); err != nil {
		return FrozenCase{}, err
	}
	facts, judgments, err := validateFacts(input)
	if err != nil {
		return FrozenCase{}, err
	}
	q, err := qualify(ctx, input, verifiers.Facts, facts, judgments)
	if err != nil {
		return FrozenCase{}, err
	}
	review := input.Review
	review.Facts = sortedFacts(review.Facts)
	review.Judgments = sortedJudgments(review.Judgments)
	manifest := CaseManifest{SchemaVersion: CaseSchema, MetricRevision: MetricRevision,
		RubricVersion: RubricVersion, Scope: input.Scope,
		Sources: append([]Source(nil), input.Sources...), Target: input.Target,
		Candidate: input.Candidate, PreviousAI: input.PreviousAI,
		Review: review, Cost: input.Cost, EvaluationState: q.State,
		NotEvaluableReason:      q.Reason,
		AuthorityEvidenceSHA256: q.Receipt.AuthorityEvidenceSHA256}
	_, identitySHA, err := JCS(manifest)
	if err != nil {
		return FrozenCase{}, err
	}
	manifest.CaseID = "wiki-quality-" + identitySHA
	manifestJCS, manifestSHA, err := JCS(manifest)
	if err != nil {
		return FrozenCase{}, err
	}
	costVerified := input.Review.DataKind == SyntheticFixture
	if input.Review.DataKind == HumanAdmin && input.Cost != nil && verifiers.Cost != nil &&
		verifiers.Cost.Verify(ctx, input.Scope, *input.Cost) == nil {
		costVerified = true
	}
	report := factMetrics(input, judgments, q, costVerified)
	report.CaseID, report.CaseManifestSHA256 = manifest.CaseID, manifestSHA
	// A grade-2/3 citation contradiction may downgrade the diagnostic after
	// qualification. Freeze the exact final state in both manifest and report.
	if report.State != manifest.EvaluationState || report.Reason != manifest.NotEvaluableReason {
		manifest.EvaluationState, manifest.NotEvaluableReason = report.State, report.Reason
		manifest.CaseID = ""
		_, identitySHA, err = JCS(manifest)
		if err != nil {
			return FrozenCase{}, err
		}
		manifest.CaseID = "wiki-quality-" + identitySHA
		manifestJCS, manifestSHA, err = JCS(manifest)
		if err != nil {
			return FrozenCase{}, err
		}
		report.CaseID, report.CaseManifestSHA256 = manifest.CaseID, manifestSHA
	}
	reportJCS, reportSHA, err := JCS(report)
	if err != nil {
		return FrozenCase{}, err
	}
	return FrozenCase{Manifest: manifest, ManifestJCS: manifestJCS,
		ManifestSHA256: manifestSHA, Report: report, ReportJCS: reportJCS,
		ReportSHA256: reportSHA}, nil
}

type DatasetEntry struct {
	CaseID                  string   `json:"case_id"`
	WikiRevisionID          string   `json:"wiki_revision_id"`
	ReviewVersion           string   `json:"review_version"`
	RubricVersion           string   `json:"rubric_version"`
	FactIDs                 []string `json:"fact_ids"`
	HumanReviewJCSSHA256    string   `json:"human_review_jcs_sha256"`
	AuthorityEvidenceSHA256 string   `json:"authority_evidence_sha256,omitempty"`
	SourceProducer          string   `json:"source_producer,omitempty"`
	SourceEventID           string   `json:"source_event_id,omitempty"`
	RTWEventJCSSHA256       string   `json:"rtw_event_jcs_sha256,omitempty"`
	DCOffset                string   `json:"dc_offset,omitempty"`
	ReviewerUID             string   `json:"reviewer_uid,omitempty"`
	GradeSource             string   `json:"grade_source"`
	WikiOriginKind          WikiKind `json:"wiki_origin_kind"`
	ManifestSHA256          string   `json:"manifest_sha256"`
	ReportSHA256            string   `json:"report_sha256"`
	EvaluationState         State    `json:"evaluation_state"`
	DataKind                DataKind `json:"data_kind"`
}

type DatasetManifest struct {
	SchemaVersion   string         `json:"schema_version"`
	MetricRevision  string         `json:"metric_revision"`
	DatasetRevision string         `json:"dataset_revision"`
	Entries         []DatasetEntry `json:"entries"`
	Activation      string         `json:"activation"`
}

type FrozenDataset struct {
	Manifest   DatasetManifest
	JCS        []byte
	RootSHA256 string
}

// FreezeDataset sorts immutable case/report hashes and JCS-hashes the ROOT.
// It refuses duplicate CaseIDs or duplicate review versions of one Wiki
// revision; two AI/manual revisions may share one source FactID.
func FreezeDataset(revision string, cases []FrozenCase) (FrozenDataset, error) {
	if !idPattern.MatchString(revision) || len(cases) < 1 || len(cases) > 4096 {
		return FrozenDataset{}, ErrDataset
	}
	entries := make([]DatasetEntry, 0, len(cases))
	caseSeen := map[string]bool{}
	wikiSeen := map[string]bool{}
	for _, c := range cases {
		wikiReviewKey := c.Manifest.Target.RevisionID + "/" + c.Manifest.Review.ReviewVersion
		if c.Manifest.CaseID == "" || caseSeen[c.Manifest.CaseID] ||
			wikiSeen[wikiReviewKey] ||
			c.Manifest.CaseID != c.Report.CaseID ||
			c.Report.CaseManifestSHA256 != c.ManifestSHA256 ||
			!validHash(c.ManifestSHA256) || !validHash(c.ReportSHA256) ||
			Digest(c.ManifestJCS) != c.ManifestSHA256 ||
			Digest(c.ReportJCS) != c.ReportSHA256 {
			return FrozenDataset{}, ErrDataset
		}
		manifestJCS, _, err := JCS(c.Manifest)
		if err != nil || !bytes.Equal(manifestJCS, c.ManifestJCS) {
			return FrozenDataset{}, ErrDataset
		}
		if _, err := DecodeCaseManifest(c.ManifestJCS, c.ManifestSHA256); err != nil {
			return FrozenDataset{}, ErrDataset
		}
		reportJCS, _, err := JCS(c.Report)
		if err != nil || !bytes.Equal(reportJCS, c.ReportJCS) {
			return FrozenDataset{}, ErrDataset
		}
		caseSeen[c.Manifest.CaseID] = true
		wikiSeen[wikiReviewKey] = true
		reviewSHA, err := reviewDigest(Input{Review: c.Manifest.Review})
		if err != nil {
			return FrozenDataset{}, ErrDataset
		}
		factIDs := make([]string, 0, len(c.Manifest.Review.Facts))
		for _, fact := range sortedFacts(c.Manifest.Review.Facts) {
			factIDs = append(factIDs, fact.FactID)
		}
		gradeSource := "synthetic_fixture"
		if c.Manifest.Review.DataKind == HumanAdmin {
			gradeSource = "rtw_human_event"
		}
		rtwEventSHA, dcOffset, sourceProducer, sourceEventID := "", "", "", ""
		if provenance := c.Manifest.Review.SourceProvenance; provenance != nil {
			rtwEventSHA, dcOffset = provenance.RTWEventJCSSHA256, provenance.DCOffset
			sourceProducer, sourceEventID = provenance.Producer, provenance.EventID
		}
		entries = append(entries, DatasetEntry{CaseID: c.Manifest.CaseID,
			WikiRevisionID: c.Manifest.Target.RevisionID,
			ReviewVersion:  c.Manifest.Review.ReviewVersion,
			RubricVersion:  c.Manifest.Review.RubricVersion,
			FactIDs:        factIDs, HumanReviewJCSSHA256: reviewSHA,
			AuthorityEvidenceSHA256: c.Manifest.AuthorityEvidenceSHA256,
			SourceProducer:          sourceProducer, SourceEventID: sourceEventID,
			RTWEventJCSSHA256: rtwEventSHA, DCOffset: dcOffset,
			ReviewerUID: c.Manifest.Review.ReviewerUID, GradeSource: gradeSource,
			WikiOriginKind: c.Manifest.Target.Kind,
			ManifestSHA256: c.ManifestSHA256, ReportSHA256: c.ReportSHA256,
			EvaluationState: c.Report.State, DataKind: c.Manifest.Review.DataKind})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CaseID < entries[j].CaseID })
	manifest := DatasetManifest{SchemaVersion: DatasetSchema,
		MetricRevision: MetricRevision, DatasetRevision: revision,
		Entries: entries, Activation: "none"}
	raw, digest, err := JCS(manifest)
	if err != nil {
		return FrozenDataset{}, errors.Join(ErrDataset, err)
	}
	return FrozenDataset{Manifest: manifest, JCS: raw, RootSHA256: digest}, nil
}

// DecodeCaseManifest is for object-store/warehouse consumers. It verifies the
// exact expected JCS SHA and rejects duplicate/unknown/trailing top-level JSON
// before trusting any case pointer. Nested typed data is checked by re-encode.
func DecodeCaseManifest(raw []byte, expectedSHA string) (CaseManifest, error) {
	var value CaseManifest
	if !validHash(expectedSHA) || len(raw) == 0 || len(raw) > 1<<20 ||
		Digest(raw) != expectedSHA {
		return value, ErrDataset
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return CaseManifest{}, ErrDataset
	}
	canonical, _, err := JCS(value)
	if err != nil || !bytes.Equal(canonical, raw) || value.SchemaVersion != CaseSchema ||
		value.MetricRevision != MetricRevision || value.RubricVersion != RubricVersion {
		return CaseManifest{}, ErrDataset
	}
	identity := value
	identity.CaseID = ""
	_, identitySHA, err := JCS(identity)
	if err != nil || value.CaseID != "wiki-quality-"+identitySHA {
		return CaseManifest{}, ErrDataset
	}
	return value, nil
}

// DecodeDatasetManifest verifies the warehouse/object-store root before a
// reader accepts any Case pointer. Source offsets are case provenance; a root
// cannot turn a sparse Wiki Event subset into an invented DC cursor position.
func DecodeDatasetManifest(raw []byte, expectedSHA string) (DatasetManifest, error) {
	var value DatasetManifest
	if !validHash(expectedSHA) || len(raw) == 0 || len(raw) > 1<<20 ||
		Digest(raw) != expectedSHA {
		return value, ErrDataset
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return DatasetManifest{}, ErrDataset
	}
	canonical, _, err := JCS(value)
	if err != nil || !bytes.Equal(canonical, raw) ||
		value.SchemaVersion != DatasetSchema || value.MetricRevision != MetricRevision ||
		!idPattern.MatchString(value.DatasetRevision) || value.Activation != "none" ||
		len(value.Entries) < 1 || len(value.Entries) > 4096 {
		return DatasetManifest{}, ErrDataset
	}
	seenCase := map[string]bool{}
	seenWikiVersion := map[string]bool{}
	previousID := ""
	for _, entry := range value.Entries {
		wikiVersion := entry.WikiRevisionID + "/" + entry.ReviewVersion
		offset, offsetErr := strconv.ParseUint(entry.DCOffset, 10, 64)
		if !strings.HasPrefix(entry.CaseID, "wiki-quality-") ||
			!validHash(strings.TrimPrefix(entry.CaseID, "wiki-quality-")) ||
			previousID >= entry.CaseID || seenCase[entry.CaseID] ||
			!idPattern.MatchString(entry.WikiRevisionID) ||
			!idPattern.MatchString(entry.ReviewVersion) || seenWikiVersion[wikiVersion] ||
			entry.RubricVersion != RubricVersion ||
			!validHash(entry.HumanReviewJCSSHA256) ||
			(entry.AuthorityEvidenceSHA256 != "" && !validHash(entry.AuthorityEvidenceSHA256)) ||
			(entry.SourceProducer != "" && entry.SourceProducer != "ridethewind.knowledge") ||
			(entry.SourceEventID != "" && !idPattern.MatchString(entry.SourceEventID)) ||
			(entry.RTWEventJCSSHA256 != "" && !validHash(entry.RTWEventJCSSHA256)) ||
			(entry.DCOffset != "" && (offsetErr != nil || offset < 1 ||
				strconv.FormatUint(offset, 10) != entry.DCOffset)) ||
			(entry.DataKind == HumanAdmin && entry.EvaluationState == Observed &&
				(entry.AuthorityEvidenceSHA256 == "" || entry.SourceProducer == "" ||
					entry.SourceEventID == "" || entry.RTWEventJCSSHA256 == "" ||
					entry.DCOffset == "" || entry.ReviewerUID == "")) ||
			(entry.DataKind == HumanAdmin && entry.GradeSource != "rtw_human_event") ||
			(entry.DataKind == SyntheticFixture && entry.GradeSource != "synthetic_fixture") ||
			(entry.WikiOriginKind != AIAccepted && entry.WikiOriginKind != ManualRevisionKind) ||
			(entry.EvaluationState != Observed && entry.EvaluationState != NotEvaluable) ||
			(entry.DataKind != SyntheticFixture && entry.DataKind != HumanAdmin) ||
			!validHash(entry.ManifestSHA256) || !validHash(entry.ReportSHA256) {
			return DatasetManifest{}, ErrDataset
		}
		previousFactID := ""
		for _, factID := range entry.FactIDs {
			if !idPattern.MatchString(factID) || previousFactID >= factID {
				return DatasetManifest{}, ErrDataset
			}
			previousFactID = factID
		}
		seenCase[entry.CaseID], seenWikiVersion[wikiVersion] = true, true
		previousID = entry.CaseID
	}
	return value, nil
}
