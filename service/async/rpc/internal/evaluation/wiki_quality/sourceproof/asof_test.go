package sourceproof

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/warehouse/wikiqualitysource"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
)

type fixtureVersionWitness struct {
	Candidate RTWSourceVersionCandidate
	Requested int64
}

func (r *fixtureVersionWitness) ReadWikiQualitySourceVersionCandidate(_ context.Context,
	_ PinnedRequest, version int64) (RTWSourceVersionCandidate, error) {
	r.Requested = version
	return r.Candidate, nil
}

// The original two-fact fixture has per-Fact judge revisions, not module
// sequence numbers. Give each actual DC Event a distinct RTW module version
// while preserving its original FactSet/quality payload and source quote.
func alignedVersionFixture(t *testing.T) (AuthorityReader, PinnedRequest,
	fixtureDCAck, RTWSourceVersionCandidate) {
	t.Helper()
	reader, req := sourceReaderFixture(t)
	ods := reader.ODS.(fixtureODS)
	rtw := reader.RTW.(fixtureRTW)
	for i, original := range ods.snapshot.Rows {
		var event eventing.Event
		if json.Unmarshal(original.AuthorityRaw, &event) != nil {
			t.Fatal("frozen test Event could not decode")
		}
		event.AggregateVersion = int64(i + 1)
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := canonicalRaw(raw)
		if err != nil {
			t.Fatal(err)
		}
		ods.snapshot.Rows[i].AuthorityRaw = raw
		ods.snapshot.Rows[i].AuthorityRawSHA256 = digest(raw)
		ods.snapshot.Rows[i].EventSpec = canonical
		ods.snapshot.Rows[i].DCInputHash = digest(canonical)
		var receipt eventing.Receipt
		if json.Unmarshal(original.DCReceipt, &receipt) != nil {
			t.Fatal("frozen DC technical receipt could not decode")
		}
		receipt.InputHash = digest(canonical)
		ods.snapshot.Rows[i].DCReceipt, err = json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		rtw.events[event.EventID] = raw
		if i == 0 {
			rtw.history.EventRawSHA256 = digest(raw)
			rtw.history.EventJCSSHA256 = digest(canonical)
		}
	}
	reader.ODS, reader.RTW = ods, rtw
	req.CutoffOffset = 4
	dc := fixtureDCAck{rows: ods.snapshot.Rows, chunk: 2}
	ack, err := reader.ReadAcknowledgedPinnedSource(context.Background(), req, dc)
	if err != nil {
		t.Fatalf("version-normalized original RTW/DC/ODS source fixture rejected: %v", err)
	}
	version := RTWSourceVersionCandidate{
		SchemaVersion:                 sourceVersionSchema,
		SourceVersion:                 4,
		EventSequenceAtRead:           4,
		EventCount:                    4,
		ModuleLifecycleAtRead:         "ENABLED",
		WikiAndSourcesAvailableAtRead: true,
		AllRequiredHaveHead:           true,
		Catalog: RTWSourceVersionCatalog{
			FactSetRevisionID:   req.FactSetRevisionID,
			WikiRevisionID:      req.WikiRevisionID,
			SourceScopeRevision: req.SourceScopeRevision,
			EventID:             ack.Catalog.Event.EventID,
			EventRawSHA256:      ack.Catalog.Event.RawSHA256,
			EventJCSSHA256:      ack.Catalog.Event.JCSSHA256,
			FactSetJCSSHA256:    ack.Catalog.FactSetPayloadJCSSHA256,
		},
	}
	for _, source := range ack.Catalog.Sources {
		version.Sources = append(version.Sources, RTWSourceVersionSource{
			RevisionID: source.RevisionID, ContentSHA256: source.ContentSHA256})
	}
	byFact := map[string]CatalogFact{}
	for _, fact := range ack.Catalog.Facts {
		if fact.Required {
			byFact[fact.FactID] = fact
		}
	}
	for _, head := range ack.Transported {
		fact := byFact[head.FactID]
		version.RequiredHeads = append(version.RequiredHeads, RTWSourceVersionRequiredHead{
			FactID: head.FactID, SourceRevisionID: fact.SourceRevisionID,
			SourceContentSHA256: fact.SourceContentSHA256, Present: true,
			JudgeRevisionID: head.JudgeRevisionID, EventID: head.Event.EventID,
			EventRawSHA256: head.Event.RawSHA256, EventJCSSHA256: head.Event.JCSSHA256})
	}
	page := RTWSourceVersionPage{FromVersion: 1, ToVersion: 4}
	for i, row := range ods.snapshot.Rows {
		jcsSource := "original_quality_event_sidecar"
		if row.EventType == wikiqualitysource.FactSetFrozenV1 {
			jcsSource = "original_fact_set_event_sidecar"
		}
		page.Events = append(page.Events, RTWSourceVersionEvent{
			AggregateVersion: int64(i + 1), EventID: row.EventID,
			EventType: row.EventType, EventJCSSHA256: row.DCInputHash,
			JCSSource: jcsSource, DeliveredAt: "2026-09-16T00:00:00Z"})
	}
	version.Pages = []RTWSourceVersionPage{page}
	return reader, req, dc, version
}

func TestDCCutoffAlignmentNeedsWholeSourceVersionAndCurrentRequiredHeads(t *testing.T) {
	reader, req, dc, candidate := alignedVersionFixture(t)
	port := &fixtureVersionWitness{Candidate: candidate}
	aligned, err := reader.ReadDCCurrentAlignment(context.Background(), req, dc, port)
	if err != nil || port.Requested != 4 || aligned.SourceVersion != 4 ||
		aligned.CutoffOffset != 4 || !aligned.SourceAvailable ||
		aligned.QualityState != "not_evaluable" || len(aligned.RequiredHeads) != 2 {
		t.Fatalf("current RTW source V did not align with acknowledged DC C: %+v err=%v requested=%d", aligned, err, port.Requested)
	}
	for _, trial := range []struct {
		name string
		edit func(*RTWSourceVersionCandidate)
	}{
		{"rtw_advanced_after_cutoff", func(v *RTWSourceVersionCandidate) {
			v.SourceVersion = 5
			v.EventSequenceAtRead = 5
		}},
		{"one_source_event_missing", func(v *RTWSourceVersionCandidate) {
			v.Pages[0].Events = v.Pages[0].Events[:3]
		}},
		{"wrong_dc_whole_event_jcs", func(v *RTWSourceVersionCandidate) {
			v.Pages[0].Events[1].EventJCSSHA256 = digest([]byte("different source Event"))
		}},
		{"head_created_but_not_transport_current", func(v *RTWSourceVersionCandidate) {
			v.RequiredHeads[0].JudgeRevisionID = "judge_revision_after_dc_cutoff"
		}},
		{"source_withdrawn_without_event", func(v *RTWSourceVersionCandidate) {
			v.Sources[0].WithdrawnAtRead = true
			v.RequiredHeads[0].SourceWithdrawnAtRead = true
			v.WikiAndSourcesAvailableAtRead = false
		}},
		{"module_withdrawn_without_event", func(v *RTWSourceVersionCandidate) {
			v.ModuleLifecycleAtRead = "WITHDRAWN"
		}},
		{"old_incomplete_admin_grade_set", func(v *RTWSourceVersionCandidate) {
			v.AllRequiredHaveHead = false
		}},
	} {
		t.Run(trial.name, func(t *testing.T) {
			reader, req, dc, witness := alignedVersionFixture(t)
			trial.edit(&witness)
			port := &fixtureVersionWitness{Candidate: witness}
			if _, err := reader.ReadDCCurrentAlignment(context.Background(), req, dc, port); !errors.Is(err, ErrDCCutoffAlignment) {
				t.Fatalf("%s crossed RTW source V/DC ACK C qualification: %v", trial.name, err)
			}
		})
	}
}

func TestDCCutoffAlignmentCannotReadASecondODSIdentityUnderSameCutoff(t *testing.T) {
	reader, req, dc, candidate := alignedVersionFixture(t)
	first := reader.ODS.(fixtureODS)
	reader.ODS = &changedReceiptODS{fixtureODS: first}
	port := &fixtureVersionWitness{Candidate: candidate}
	if _, err := reader.ReadDCCurrentAlignment(context.Background(), req, dc, port); !errors.Is(err, ErrPrefix) {
		t.Fatalf("changed ODS original receipt qualified against DC ACK cutoff: %v", err)
	}
}

func TestSourceVersionCandidateCannotClaimASyntheticHigherOffset(t *testing.T) {
	reader, req, dc, candidate := alignedVersionFixture(t)
	candidate.Pages[0].Events[3].AggregateVersion = 8
	port := &fixtureVersionWitness{Candidate: candidate}
	_, err := reader.ReadDCCurrentAlignment(context.Background(), req, dc, port)
	if !errors.Is(err, ErrDCCutoffAlignment) || port.Requested != 4 {
		t.Fatalf("RTW Event aggregate version %s escaped fixed DC source V: %v",
			strconv.FormatInt(candidate.Pages[0].Events[3].AggregateVersion, 10), err)
	}
}
