package sourceproof

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/warehouse/wikiqualitysource"
)

// ErrDCCutoffAlignment keeps an RTW read-time state from being presented as
// the business state at a DataCenter cutoff without a complete source prefix.
var ErrDCCutoffAlignment = errors.New("Wiki quality RTW source version and DC acknowledged cutoff do not align")

const sourceVersionSchema = "rtw.wiki-quality-source-version-candidate.v1"
const sourceVersionLimit = 4096

// These private Worker DTOs mirror RTW api/knowledge.api. They are not a
// client-supplied SubjectRef or a public product response.
type RTWSourceVersionCatalog struct {
	FactSetRevisionID   string `json:"fact_set_revision_id"`
	WikiRevisionID      string `json:"wiki_revision_id"`
	SourceScopeRevision string `json:"source_scope_revision"`
	EventID             string `json:"event_id"`
	EventRawSHA256      string `json:"event_raw_sha256"`
	EventJCSSHA256      string `json:"event_jcs_sha256"`
	FactSetJCSSHA256    string `json:"fact_set_jcs_sha256"`
}

type RTWSourceVersionSource struct {
	RevisionID      string `json:"revision_id"`
	ContentSHA256   string `json:"content_sha256"`
	WithdrawnAtRead bool   `json:"withdrawn_at_read"`
}

type RTWSourceVersionRequiredHead struct {
	FactID                string `json:"fact_id"`
	SourceRevisionID      string `json:"source_revision_id"`
	SourceContentSHA256   string `json:"source_content_sha256"`
	Present               bool   `json:"present"`
	JudgeRevisionID       string `json:"judge_revision_id"`
	EventID               string `json:"event_id"`
	EventRawSHA256        string `json:"event_raw_sha256"`
	EventJCSSHA256        string `json:"event_jcs_sha256"`
	SourceWithdrawnAtRead bool   `json:"source_withdrawn_at_read"`
}

type RTWSourceVersionEvent struct {
	AggregateVersion int64  `json:"aggregate_version"`
	EventID          string `json:"event_id"`
	EventType        string `json:"event_type"`
	EventJCSSHA256   string `json:"event_jcs_sha256"`
	JCSSource        string `json:"jcs_source"`
	DeliveredAt      string `json:"delivered_at"`
	TargetKind       string `json:"target_kind"`
	TargetID         string `json:"target_id"`
}

type RTWSourceVersionPage struct {
	FromVersion int64                   `json:"from_version"`
	ToVersion   int64                   `json:"to_version"`
	Events      []RTWSourceVersionEvent `json:"events"`
}

type RTWSourceVersionCandidate struct {
	SchemaVersion                 string                         `json:"schema_version"`
	SourceVersion                 int64                          `json:"source_version"`
	EventSequenceAtRead           int64                          `json:"event_sequence_at_read"`
	Catalog                       RTWSourceVersionCatalog        `json:"catalog"`
	Sources                       []RTWSourceVersionSource       `json:"sources"`
	RequiredHeads                 []RTWSourceVersionRequiredHead `json:"required_heads"`
	ModuleLifecycleAtRead         string                         `json:"module_lifecycle_at_read"`
	WikiWithdrawnAtRead           bool                           `json:"wiki_withdrawn_at_read"`
	WikiAndSourcesAvailableAtRead bool                           `json:"wiki_and_sources_available_at_read"`
	AllRequiredHaveHead           bool                           `json:"all_required_have_head"`
	EventCount                    int                            `json:"event_count"`
	Pages                         []RTWSourceVersionPage         `json:"pages"`
}

type RTWSourceVersionReader interface {
	ReadWikiQualitySourceVersionCandidate(context.Context, PinnedRequest, int64) (RTWSourceVersionCandidate, error)
}

// DCCurrentAlignment proves that one complete delivered RTW module version
// exactly matches an immutable acknowledged DC/ODS cutoff. It does not turn
// an administrator's claim into observed human quality or a D07 Dataset.
type DCCurrentAlignment struct {
	ModuleID              string
	FactSetRevisionID     string
	WikiRevisionID        string
	SourceScopeRevision   string
	SourceVersion         int64
	CutoffOffset          int64
	DCIndexSHA256         string
	ODSEvidenceSHA256     string
	RTWCandidateJCSSHA256 string
	RequiredHeads         []RTWSourceVersionRequiredHead
	SourceAvailable       bool
	UnavailableReason     string
	QualityState          string
}

// ReadDCCurrentAlignment obtains the already verified SourceProof ACK/ODS
// prefix, then requests the RTW source version actually observed in that same
// cutoff. A newer RTW write causes RTW to reject the old V, never a fallback.
func (r AuthorityReader) ReadDCCurrentAlignment(ctx context.Context,
	req PinnedRequest, dc DCAckReader, source RTWSourceVersionReader) (DCCurrentAlignment, error) {
	var result DCCurrentAlignment
	if source == nil {
		return result, ErrDCCutoffAlignment
	}
	ack, err := r.ReadAcknowledgedPinnedSource(ctx, req, dc)
	if err != nil {
		return result, err
	}
	snapshot, err := r.ODS.ReadPrefix(ctx, req.CutoffOffset)
	if err != nil {
		return result, err
	}
	version, err := maxModuleVersion(req, ack, snapshot)
	if err != nil {
		return result, err
	}
	witness, err := source.ReadWikiQualitySourceVersionCandidate(ctx, req, version)
	if err != nil {
		return result, err
	}
	return verifyDCCurrentAlignment(req, ack, snapshot, witness)
}

func maxModuleVersion(req PinnedRequest, ack AcknowledgedSourceProjection,
	snapshot ODSSnapshot) (int64, error) {
	if snapshot.Producer != Producer || snapshot.Consumer != wikiqualitysource.DefaultConsumer ||
		snapshot.CommittedOffset < req.CutoffOffset ||
		int64(len(snapshot.Rows)) != req.CutoffOffset ||
		ack.CutoffOffset != req.CutoffOffset ||
		ack.AcknowledgedAtLeast < req.CutoffOffset {
		return 0, ErrDCCutoffAlignment
	}
	_, evidenceSHA, err := jcs(snapshot.Rows)
	if err != nil || evidenceSHA != ack.ODSEvidenceSHA256 {
		return 0, ErrDCCutoffAlignment
	}
	var maxVersion int64
	for i, row := range snapshot.Rows {
		verified, err := verifyODSRow(row, int64(i)+1)
		if err != nil {
			return 0, err
		}
		if verified.Event.AggregateID == req.ModuleID {
			if verified.Event.AggregateVersion < 1 ||
				verified.Event.AggregateVersion > sourceVersionLimit {
				return 0, ErrDCCutoffAlignment
			}
			if verified.Event.AggregateVersion > maxVersion {
				maxVersion = verified.Event.AggregateVersion
			}
		}
	}
	if maxVersion == 0 {
		return 0, ErrDCCutoffAlignment
	}
	return maxVersion, nil
}

func verifyDCCurrentAlignment(req PinnedRequest, ack AcknowledgedSourceProjection,
	snapshot ODSSnapshot, candidate RTWSourceVersionCandidate) (DCCurrentAlignment, error) {
	var result DCCurrentAlignment
	version, err := maxModuleVersion(req, ack, snapshot)
	if err != nil || candidate.SchemaVersion != sourceVersionSchema ||
		candidate.SourceVersion != version || candidate.EventSequenceAtRead != version ||
		candidate.EventCount != int(version) || len(candidate.Pages) == 0 ||
		candidate.Catalog.FactSetRevisionID != req.FactSetRevisionID ||
		candidate.Catalog.WikiRevisionID != req.WikiRevisionID ||
		candidate.Catalog.SourceScopeRevision != req.SourceScopeRevision ||
		candidate.Catalog.EventID != ack.Catalog.Event.EventID ||
		candidate.Catalog.EventRawSHA256 != ack.Catalog.Event.RawSHA256 ||
		candidate.Catalog.EventJCSSHA256 != ack.Catalog.Event.JCSSHA256 ||
		candidate.Catalog.FactSetJCSSHA256 != ack.Catalog.FactSetPayloadJCSSHA256 {
		return result, ErrDCCutoffAlignment
	}
	byVersion := make(map[int64]verifiedODSRow, version)
	for i, row := range snapshot.Rows {
		verified, err := verifyODSRow(row, int64(i)+1)
		if err != nil {
			return result, err
		}
		if verified.Event.AggregateID != req.ModuleID {
			continue
		}
		v := verified.Event.AggregateVersion
		if v < 1 || v > version || byVersion[v].EventID != "" {
			return result, ErrDCCutoffAlignment
		}
		byVersion[v] = verified
	}
	if len(byVersion) != int(version) {
		return result, ErrDCCutoffAlignment
	}
	withdrawnRevisions := map[string]bool{}
	moduleWithdrawn := false
	seenIDs := map[string]bool{}
	expectedVersion := int64(1)
	for _, page := range candidate.Pages {
		if page.FromVersion != expectedVersion || page.ToVersion < page.FromVersion ||
			page.ToVersion-page.FromVersion >= 128 ||
			len(page.Events) != int(page.ToVersion-page.FromVersion+1) {
			return result, ErrDCCutoffAlignment
		}
		for _, event := range page.Events {
			row, found := byVersion[expectedVersion]
			if !found || event.AggregateVersion != expectedVersion ||
				!idPattern.MatchString(event.EventID) || seenIDs[event.EventID] ||
				!idPattern.MatchString(event.EventType) ||
				!validSHA(event.EventJCSSHA256) ||
				event.EventID != row.EventID || event.EventType != row.EventType ||
				event.EventJCSSHA256 != row.DCInputHash ||
				event.DeliveredAt == "" {
				return result, ErrDCCutoffAlignment
			}
			if _, err := time.Parse(time.RFC3339Nano, event.DeliveredAt); err != nil {
				return result, ErrDCCutoffAlignment
			}
			switch event.EventType {
			case wikiqualitysource.FactSetFrozenV1:
				if event.JCSSource != "original_fact_set_event_sidecar" {
					return result, ErrDCCutoffAlignment
				}
			case wikiqualitysource.Judged:
				if event.JCSSource != "original_quality_event_sidecar" {
					return result, ErrDCCutoffAlignment
				}
			default:
				if event.JCSSource != "outbox_jsonb_canonical_at_read" {
					return result, ErrDCCutoffAlignment
				}
			}
			if event.EventType == "knowledge.content.withdrawn.v1" {
				var payload struct {
					Request struct {
						TargetKind string `json:"target_kind"`
						TargetID   string `json:"target_id"`
					} `json:"request"`
				}
				if json.Unmarshal(row.Event.Payload, &payload) != nil ||
					payload.Request.TargetKind != event.TargetKind ||
					payload.Request.TargetID != event.TargetID ||
					!idPattern.MatchString(event.TargetID) {
					return result, ErrDCCutoffAlignment
				}
				switch event.TargetKind {
				case "revision":
					withdrawnRevisions[event.TargetID] = true
				case "module":
					if event.TargetID != req.ModuleID {
						return result, ErrDCCutoffAlignment
					}
					moduleWithdrawn = true
				default:
					return result, ErrDCCutoffAlignment
				}
			} else if event.TargetKind != "" || event.TargetID != "" {
				return result, ErrDCCutoffAlignment
			}
			seenIDs[event.EventID] = true
			expectedVersion++
		}
	}
	if expectedVersion != version+1 {
		return result, ErrDCCutoffAlignment
	}
	if (candidate.ModuleLifecycleAtRead == "WITHDRAWN") != moduleWithdrawn ||
		(candidate.ModuleLifecycleAtRead != "WITHDRAWN" &&
			candidate.ModuleLifecycleAtRead != "ENABLED") ||
		candidate.WikiWithdrawnAtRead != withdrawnRevisions[req.WikiRevisionID] {
		return result, ErrDCCutoffAlignment
	}
	sources := make(map[string]RTWSourceVersionSource, len(candidate.Sources))
	for _, source := range candidate.Sources {
		if !idPattern.MatchString(source.RevisionID) ||
			!validSHA(source.ContentSHA256) ||
			source.WithdrawnAtRead != withdrawnRevisions[source.RevisionID] ||
			sources[source.RevisionID].RevisionID != "" {
			return result, ErrDCCutoffAlignment
		}
		sources[source.RevisionID] = source
	}
	if len(sources) != len(ack.Catalog.Sources) {
		return result, ErrDCCutoffAlignment
	}
	allAvailable := !candidate.WikiWithdrawnAtRead
	for _, historical := range ack.Catalog.Sources {
		source, found := sources[historical.RevisionID]
		if !found || source.ContentSHA256 != historical.ContentSHA256 {
			return result, ErrDCCutoffAlignment
		}
		allAvailable = allAvailable && !source.WithdrawnAtRead
	}
	if candidate.WikiAndSourcesAvailableAtRead != allAvailable ||
		!candidate.AllRequiredHaveHead {
		return result, ErrDCCutoffAlignment
	}
	transported := make(map[string]TransportedJudgment, len(ack.Transported))
	for _, decision := range ack.Transported {
		if transported[decision.FactID].FactID != "" {
			return result, ErrDCCutoffAlignment
		}
		transported[decision.FactID] = decision
	}
	required := map[string]CatalogFact{}
	for _, fact := range ack.Catalog.Facts {
		if fact.Required {
			required[fact.FactID] = fact
		}
	}
	if len(candidate.RequiredHeads) != len(required) || len(transported) != len(required) {
		return result, ErrDCCutoffAlignment
	}
	seenHeads := map[string]bool{}
	for _, head := range candidate.RequiredHeads {
		fact, found := required[head.FactID]
		decision, transportedFound := transported[head.FactID]
		if !found || !transportedFound || seenHeads[head.FactID] ||
			!head.Present || head.SourceRevisionID != fact.SourceRevisionID ||
			head.SourceContentSHA256 != fact.SourceContentSHA256 ||
			head.SourceWithdrawnAtRead != sources[fact.SourceRevisionID].WithdrawnAtRead ||
			head.JudgeRevisionID != decision.JudgeRevisionID ||
			head.EventID != decision.Event.EventID ||
			head.EventRawSHA256 != decision.Event.RawSHA256 ||
			head.EventJCSSHA256 != decision.Event.JCSSHA256 {
			return result, ErrDCCutoffAlignment
		}
		seenHeads[head.FactID] = true
	}
	_, candidateSHA, err := jcs(candidate)
	if err != nil {
		return result, ErrDCCutoffAlignment
	}
	result = DCCurrentAlignment{ModuleID: req.ModuleID,
		FactSetRevisionID:   req.FactSetRevisionID,
		WikiRevisionID:      req.WikiRevisionID,
		SourceScopeRevision: req.SourceScopeRevision,
		SourceVersion:       version, CutoffOffset: req.CutoffOffset,
		DCIndexSHA256:         ack.DCIndexSHA256,
		ODSEvidenceSHA256:     ack.ODSEvidenceSHA256,
		RTWCandidateJCSSHA256: candidateSHA,
		RequiredHeads:         append([]RTWSourceVersionRequiredHead(nil), candidate.RequiredHeads...),
		SourceAvailable:       allAvailable && !moduleWithdrawn,
		QualityState:          "not_evaluable"}
	if !result.SourceAvailable {
		result.UnavailableReason = "RTW Wiki, Source or module withdrawn at aligned cutoff"
	}
	return result, nil
}
