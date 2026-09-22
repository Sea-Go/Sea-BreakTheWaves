package sourceproof

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/warehouse/wikiqualitysource"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

// ErrHistoricalAuthority is distinct from ErrPrefix: verified ODS rows cannot
// stand in for RTW's immutable FactSet revision or original Event read.
var ErrHistoricalAuthority = errors.New("Wiki quality historical RTW authority missing")

// ODSRow is an immutable position on the independent Wiki quality consumer.
// EventSpec is DC's whole-Event JCS; AuthorityRaw preserves RTW's emitted bytes.
type ODSRow struct {
	Offset                  int64
	EventID                 string
	EventType               string
	EventSpec               []byte
	DCInputHash             string
	DCReceipt               []byte
	Status                  string
	AuthorityRaw            []byte
	AuthorityRawSHA256      string
	FactSetPayloadJCSSHA256 string
	FactSetPayloadJCS       []byte
	SidecarRevisionID       string
	SidecarWikiRevisionID   string
	SidecarSourceScope      string
}

type ODSSnapshot struct {
	Producer        string
	Consumer        string
	CommittedOffset int64
	Rows            []ODSRow // exactly the continuous prefix [1, cutoff]
}

type ODSReader interface {
	ReadPrefix(context.Context, int64) (ODSSnapshot, error)
}

// HistoricalFactSet is returned by an explicit RTW revision GET. A current
// scope head or an ODS sidecar alone cannot substitute for this revision.
type HistoricalFactSet struct {
	Record           wikiqualitysource.FactSetV1
	EventID          string
	EventRawSHA256   string
	EventJCSSHA256   string
	FactSetJCSSHA256 string
}

type RTWReader interface {
	ReadFactSetEvent(context.Context, string) (wikiqualitysource.FactSetEventProof, error)
	ReadQualityEvent(context.Context, string) ([]byte, string, error)
	ReadHistoricalFactSet(context.Context, string, string, string, string) (HistoricalFactSet, error)
}

type PinnedRequest struct {
	ModuleID            string
	PageID              string
	WikiRevisionID      string
	SourceScopeRevision string
	FactSetRevisionID   string
	CutoffOffset        int64
}

// TransportedJudgment is only the latest judgment found in the DC/ODS prefix
// by cutoff. It is not an RTW as-of head assertion: an RTW decision can exist
// before its transport, and the current RTW head can have moved since cutoff.
type TransportedJudgment struct {
	WikiRevisionID      string
	FactID              string
	JudgeRevisionID     string
	SourceScopeRevision string // derived after exact match to the pinned Catalog
	RTWActorID          string
	Event               EventProof
}

// SourceProjection proves actual RTW/ODS selected rows and the full committed
// ODS prefix at cutoff. It has no DC ACK/batch or RTW as-of-head/withdrawal
// witness, and cannot be passed to Freeze as authority.
type SourceProjection struct {
	Catalog           CatalogProof
	Transported       []TransportedJudgment
	Selected          []PrefixEvent
	CutoffOffset      int64
	CommittedOffset   int64
	ODSPrefixSHA256   string
	ODSEvidenceSHA256 string // all committed raw Event/receipt/Catalog bytes at cutoff
}

type AuthorityReader struct {
	ODS ODSReader
	RTW RTWReader
}

type verifiedODSRow struct {
	ODSRow
	Event eventing.Event
}

func verifyODSRow(row ODSRow, expectedOffset int64) (verifiedODSRow, error) {
	var result verifiedODSRow
	if row.Offset != expectedOffset || !idPattern.MatchString(row.EventID) ||
		!validSHA(row.DCInputHash) || len(row.EventSpec) < 2 ||
		len(row.EventSpec) > 2<<20 || len(row.DCReceipt) < 2 ||
		len(row.DCReceipt) > 1<<20 {
		return result, ErrProof
	}
	eventJCS, err := canonicalRaw(row.EventSpec)
	if err != nil || !bytes.Equal(row.EventSpec, eventJCS) || digest(eventJCS) != row.DCInputHash {
		return result, ErrProof
	}
	var event eventing.Event
	var receipt eventing.Receipt
	if json.Unmarshal(row.EventSpec, &event) != nil ||
		json.Unmarshal(row.DCReceipt, &receipt) != nil ||
		event.EventID != row.EventID || event.EventType != row.EventType ||
		event.Producer != Producer || event.SchemaVersion != 1 ||
		receipt.EventID != row.EventID || receipt.Producer != Producer ||
		receipt.Offset != row.Offset || receipt.InputHash != row.DCInputHash ||
		receipt.ReceiptID == "" || receipt.TechnicalStatus != "accepted" {
		return result, ErrProof
	}
	switch row.Status {
	case "technical_skip":
		if row.AuthorityRaw != nil || row.AuthorityRawSHA256 != "" ||
			row.FactSetPayloadJCSSHA256 != "" || row.FactSetPayloadJCS != nil ||
			row.SidecarRevisionID != "" || row.SidecarWikiRevisionID != "" ||
			row.SidecarSourceScope != "" ||
			strings.HasPrefix(row.EventType, "knowledge.wiki.quality.") ||
			strings.HasPrefix(row.EventType, "knowledge.wiki.fact-set.") ||
			strings.HasPrefix(row.EventType, "knowledge.wiki.fact-catalog.") {
			return result, ErrProof
		}
	case "quality_verified":
		if row.EventType != wikiqualitysource.Judged ||
			row.FactSetPayloadJCSSHA256 != "" || row.FactSetPayloadJCS != nil ||
			row.SidecarRevisionID != "" || row.SidecarWikiRevisionID != "" ||
			row.SidecarSourceScope != "" {
			return result, ErrProof
		}
	case "fact_set_verified":
		if row.EventType != wikiqualitysource.FactSetFrozenV1 ||
			!validSHA(row.FactSetPayloadJCSSHA256) ||
			len(row.FactSetPayloadJCS) < 2 ||
			!idPattern.MatchString(row.SidecarRevisionID) ||
			!idPattern.MatchString(row.SidecarWikiRevisionID) ||
			len(row.SidecarSourceScope) != len("scope_")+64 ||
			!validSHA(row.SidecarSourceScope[len("scope_"):]) {
			return result, ErrProof
		}
		payloadJCS, err := canonicalRaw(event.Payload)
		if err != nil || !bytes.Equal(row.FactSetPayloadJCS, payloadJCS) ||
			digest(payloadJCS) != row.FactSetPayloadJCSSHA256 {
			return result, ErrProof
		}
		var identity struct {
			RevisionID string `json:"fact_set_revision_id"`
			WikiID     string `json:"wiki_revision_id"`
			Scope      string `json:"source_scope_revision"`
		}
		if json.Unmarshal(event.Payload, &identity) != nil ||
			identity.RevisionID != row.SidecarRevisionID ||
			identity.WikiID != row.SidecarWikiRevisionID ||
			identity.Scope != row.SidecarSourceScope {
			return result, ErrProof
		}
	default:
		return result, ErrProof
	}
	if row.Status != "technical_skip" {
		if !validSHA(row.AuthorityRawSHA256) ||
			len(row.AuthorityRaw) < 2 || len(row.AuthorityRaw) > 2<<20 ||
			digest(row.AuthorityRaw) != row.AuthorityRawSHA256 {
			return result, ErrProof
		}
		originalJCS, err := canonicalRaw(row.AuthorityRaw)
		if err != nil || !bytes.Equal(originalJCS, row.EventSpec) {
			return result, ErrProof
		}
	}
	return verifiedODSRow{ODSRow: row, Event: event}, nil
}

func canonicalRaw(raw []byte) ([]byte, error) {
	return jsoncanonicalizer.Transform(raw)
}

// ReadPinnedSource reconstructs the last TRANSPORTED judgment per Fact at the
// explicit DC cutoff from the complete ODS event history. It rejects a gap in
// a revision chain and any selected Event whose RTW original bytes differ
// from the ODS row. It does not claim actual DC ACK or RTW as-of head.
func (r AuthorityReader) ReadPinnedSource(ctx context.Context, req PinnedRequest) (SourceProjection, error) {
	var out SourceProjection
	if ctx == nil || ctx.Err() != nil || r.ODS == nil || r.RTW == nil ||
		!idPattern.MatchString(req.ModuleID) || req.PageID == "" ||
		!idPattern.MatchString(req.WikiRevisionID) ||
		!idPattern.MatchString(req.FactSetRevisionID) ||
		len(req.SourceScopeRevision) != len("scope_")+64 ||
		!validSHA(req.SourceScopeRevision[len("scope_"):]) ||
		req.CutoffOffset < 1 || req.CutoffOffset > 4096 {
		return out, ErrProof
	}
	snapshot, err := r.ODS.ReadPrefix(ctx, req.CutoffOffset)
	if err != nil {
		return out, fmt.Errorf("read committed Wiki ODS prefix: %w", err)
	}
	if snapshot.Producer != Producer || snapshot.Consumer != wikiqualitysource.DefaultConsumer ||
		snapshot.CommittedOffset < req.CutoffOffset ||
		int64(len(snapshot.Rows)) != req.CutoffOffset {
		return out, ErrPrefix
	}
	rows := make([]verifiedODSRow, 0, len(snapshot.Rows))
	var catalog *verifiedODSRow
	for i, row := range snapshot.Rows {
		verified, err := verifyODSRow(row, int64(i)+1)
		if err != nil {
			return out, err
		}
		if verified.Status == "fact_set_verified" {
			var payload struct {
				RevisionID string `json:"fact_set_revision_id"`
			}
			if json.Unmarshal(verified.Event.Payload, &payload) != nil {
				return out, ErrProof
			}
			if payload.RevisionID == req.FactSetRevisionID {
				if catalog != nil {
					return out, ErrProof
				}
				catalog = &verified
			}
		}
		rows = append(rows, verified)
	}
	if catalog == nil {
		return out, ErrHistoricalAuthority
	}
	private, err := r.RTW.ReadFactSetEvent(ctx, catalog.EventID)
	if err != nil || !bytes.Equal(private.EventJSON, catalog.AuthorityRaw) ||
		private.EventRawSHA256 != catalog.AuthorityRawSHA256 ||
		private.EventJCSSHA256 != catalog.DCInputHash ||
		private.FactSetJCSSHA256 != catalog.FactSetPayloadJCSSHA256 {
		return out, ErrHistoricalAuthority
	}
	frozen, err := wikiqualitysource.ParseFactSetV1(catalog.Event, private.EventJSON,
		private.FactSetJCSSHA256)
	if err != nil || frozen.ModuleID != req.ModuleID || frozen.PageID != req.PageID ||
		frozen.WikiRevisionID != req.WikiRevisionID ||
		frozen.SourceScopeRevision != req.SourceScopeRevision ||
		frozen.FactSetRevisionID != req.FactSetRevisionID {
		return out, ErrHistoricalAuthority
	}
	history, err := r.RTW.ReadHistoricalFactSet(ctx, req.ModuleID, req.PageID,
		req.FactSetRevisionID, req.WikiRevisionID)
	if err != nil || history.EventID != catalog.EventID ||
		history.EventRawSHA256 != catalog.AuthorityRawSHA256 ||
		history.EventJCSSHA256 != catalog.DCInputHash ||
		history.FactSetJCSSHA256 != catalog.FactSetPayloadJCSSHA256 {
		return out, ErrHistoricalAuthority
	}
	historyPayloadJCS, _, err := jcs(history.Record)
	if err != nil || !bytes.Equal(historyPayloadJCS, catalog.FactSetPayloadJCS) {
		return out, ErrHistoricalAuthority
	}
	proof := CatalogProof{ModuleID: frozen.ModuleID, PageID: frozen.PageID,
		WikiRevisionID: frozen.WikiRevisionID, SourceScopeRevision: frozen.SourceScopeRevision,
		FactSetRevisionID:       frozen.FactSetRevisionID,
		FactSetPayloadJCSSHA256: private.FactSetJCSSHA256,
		FactSetPayloadJCS:       append([]byte(nil), catalog.FactSetPayloadJCS...),
		Event: EventProof{EventID: catalog.EventID, DCOffset: strconv.FormatInt(catalog.Offset, 10),
			OriginalRaw: append([]byte(nil), private.EventJSON...),
			RawSHA256:   private.EventRawSHA256, JCSSHA256: private.EventJCSSHA256},
		FactsCompleteDeclared: frozen.FactsComplete,
		DeclarationSource:     frozen.DeclarationSource, RTWActorID: frozen.ActorID}
	for _, source := range frozen.SourceRevisions {
		proof.Sources = append(proof.Sources, CatalogSource{RevisionID: source.RevisionID,
			ContentSHA256: source.ContentSHA256})
	}
	facts := map[string]wikiqualitysource.FactSetFactV1{}
	for _, fact := range frozen.Facts {
		proof.Facts = append(proof.Facts, CatalogFact{FactID: fact.FactID,
			SourceRevisionID: fact.SourceRevisionID, Locator: fact.Locator,
			SourceContentSHA256: fact.SourceContentSHA256,
			SourceQuoteSHA256:   fact.SourceQuoteSHA256, Required: fact.Required})
		facts[fact.FactID] = fact
	}
	// The original Wiki quality v1 payload does not carry a FactSetRevisionID
	// or SourceScopeRevision. Both are derived ONLY after matching its FactID,
	// target Wiki and exact source bytes against this pinned Catalog.
	type current struct {
		Judgment wikiqualitysource.JudgmentV1
		Proof    TransportedJudgment
		Revision int64
	}
	heads := map[string]current{}
	for _, row := range rows {
		if row.Status != "quality_verified" {
			continue
		}
		var hinted struct {
			WikiRevisionID string `json:"wiki_revision_id"`
			FactID         string `json:"fact_id"`
		}
		if json.Unmarshal(row.Event.Payload, &hinted) != nil {
			return out, ErrProof
		}
		fact, relevant := facts[hinted.FactID]
		if !relevant || hinted.WikiRevisionID != req.WikiRevisionID {
			continue
		}
		raw, rawSHA, err := r.RTW.ReadQualityEvent(ctx, row.EventID)
		if err != nil || !bytes.Equal(raw, row.AuthorityRaw) ||
			rawSHA != row.AuthorityRawSHA256 {
			return out, ErrHistoricalAuthority
		}
		judgment, err := wikiqualitysource.ParseJudgmentV1(row.Event, raw)
		if err != nil || judgment.ModuleID != req.ModuleID || judgment.PageID != req.PageID ||
			judgment.WikiRevisionID != req.WikiRevisionID ||
			judgment.WikiContentSHA256 != frozen.WikiContentSHA256 ||
			judgment.FactID != fact.FactID ||
			judgment.SourceRevisionID != fact.SourceRevisionID ||
			judgment.SourceContentSHA256 != fact.SourceContentSHA256 ||
			judgment.Locator != fact.Locator ||
			judgment.SourceByteStart != fact.SourceByteStart ||
			judgment.SourceByteEnd != fact.SourceByteEnd ||
			judgment.SourceQuote != fact.SourceQuote ||
			judgment.SourceQuoteSHA256 != fact.SourceQuoteSHA256 {
			return out, ErrProof
		}
		revision, ok := decimal(judgment.JudgeRevision, true)
		if !ok {
			return out, ErrProof
		}
		prior := heads[judgment.FactID]
		if (revision == 1 && (prior.Revision != 0 || judgment.BaseJudgeRevisionID != "")) ||
			(revision > 1 && (prior.Revision+1 != revision ||
				judgment.BaseJudgeRevisionID != prior.Judgment.JudgeRevisionID ||
				judgment.JudgmentID != prior.Judgment.JudgmentID)) {
			return out, ErrProof
		}
		heads[judgment.FactID] = current{Judgment: judgment, Revision: revision,
			Proof: TransportedJudgment{WikiRevisionID: judgment.WikiRevisionID,
				FactID: judgment.FactID, JudgeRevisionID: judgment.JudgeRevisionID,
				SourceScopeRevision: frozen.SourceScopeRevision,
				RTWActorID:          judgment.ActorID,
				Event: EventProof{EventID: row.EventID, DCOffset: strconv.FormatInt(row.Offset, 10),
					OriginalRaw: append([]byte(nil), raw...), RawSHA256: rawSHA,
					JCSSHA256: row.DCInputHash}}}
	}
	for _, fact := range frozen.Facts {
		if fact.Required && heads[fact.FactID].Revision == 0 {
			return out, ErrProof
		}
	}
	for _, head := range heads {
		out.Transported = append(out.Transported, head.Proof)
	}
	sort.Slice(out.Transported, func(i, j int) bool {
		return out.Transported[i].FactID < out.Transported[j].FactID
	})
	_, required, _, err := validateCatalog(proof)
	if err != nil || len(required) == 0 {
		return SourceProjection{}, ErrProof
	}
	index := make([]PrefixEvent, 0, len(rows))
	selected := map[int64]bool{catalog.Offset: true}
	for _, head := range out.Transported {
		offset, _ := decimal(head.Event.DCOffset, true)
		selected[offset] = true
	}
	for _, row := range rows {
		index = append(index, PrefixEvent{DCOffset: strconv.FormatInt(row.Offset, 10),
			EventID: row.EventID, JCSSHA256: row.DCInputHash})
		if selected[row.Offset] {
			out.Selected = append(out.Selected, index[len(index)-1])
		}
	}
	_, indexSHA, err := jcs(index)
	if err != nil {
		return SourceProjection{}, ErrProof
	}
	_, evidenceSHA, err := jcs(snapshot.Rows)
	if err != nil {
		return SourceProjection{}, ErrProof
	}
	out.Catalog, out.CutoffOffset, out.CommittedOffset, out.ODSPrefixSHA256 =
		proof, req.CutoffOffset, snapshot.CommittedOffset, indexSHA
	out.ODSEvidenceSHA256 = evidenceSHA
	return out, nil
}
