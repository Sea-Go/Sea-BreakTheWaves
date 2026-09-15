package sourceproof

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/warehouse/wikiqualitysource"
)

type fixtureODS struct{ snapshot ODSSnapshot }

func (s fixtureODS) ReadPrefix(_ context.Context, cutoff int64) (ODSSnapshot, error) {
	if cutoff > int64(len(s.snapshot.Rows)) {
		return ODSSnapshot{}, ErrPrefix
	}
	copy := s.snapshot
	copy.Rows = append([]ODSRow(nil), copy.Rows[:cutoff]...)
	return copy, nil
}

type fixtureRTW struct {
	events  map[string][]byte
	history HistoricalFactSet
}

func (s fixtureRTW) ReadFactSetEvent(_ context.Context, id string) (
	wikiqualitysource.FactSetEventProof, error) {
	raw := s.events[id]
	if len(raw) == 0 {
		return wikiqualitysource.FactSetEventProof{}, ErrHistoricalAuthority
	}
	canonical, err := canonicalRaw(raw)
	if err != nil {
		return wikiqualitysource.FactSetEventProof{}, err
	}
	var event eventing.Event
	if json.Unmarshal(raw, &event) != nil {
		return wikiqualitysource.FactSetEventProof{}, ErrHistoricalAuthority
	}
	payload, err := canonicalRaw(event.Payload)
	if err != nil {
		return wikiqualitysource.FactSetEventProof{}, err
	}
	return wikiqualitysource.FactSetEventProof{EventJSON: raw,
		EventRawSHA256: digest(raw), EventJCSSHA256: digest(canonical),
		FactSetJCSSHA256: digest(payload)}, nil
}

func (s fixtureRTW) ReadQualityEvent(_ context.Context, id string) ([]byte, string, error) {
	raw := s.events[id]
	if len(raw) == 0 {
		return nil, "", ErrHistoricalAuthority
	}
	return raw, digest(raw), nil
}

func (s fixtureRTW) ReadHistoricalFactSet(_ context.Context, _, _, _, _ string) (
	HistoricalFactSet, error) {
	return s.history, nil
}

func fixtureReadEvent(t *testing.T, filename string) ([]byte, eventing.Event) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "warehouse",
		"wikiqualitysource", "testdata", filename))
	if err != nil {
		t.Fatal(err)
	}
	var event eventing.Event
	if json.Unmarshal(raw, &event) != nil {
		t.Fatal("fixed source Event fixture cannot decode")
	}
	return bytes.TrimSpace(raw), event
}

func fixtureODSRow(t *testing.T, offset int64, raw []byte,
	event eventing.Event, payloadJCS []byte) ODSRow {
	t.Helper()
	spec, err := canonicalRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := json.Marshal(eventing.Receipt{EventID: event.EventID,
		Producer: Producer, TechnicalStatus: "accepted", ReceiptID: "dc-receipt-" +
			strconv.FormatInt(offset, 10), InputHash: digest(spec), Offset: offset,
		ReceivedAt: "2026-09-16T00:00:01Z"})
	if err != nil {
		t.Fatal(err)
	}
	row := ODSRow{Offset: offset, EventID: event.EventID, EventType: event.EventType,
		EventSpec: spec, DCInputHash: digest(spec), DCReceipt: receipt,
		AuthorityRaw: raw, AuthorityRawSHA256: digest(raw)}
	if event.EventType == wikiqualitysource.FactSetFrozenV1 {
		row.Status, row.FactSetPayloadJCS, row.FactSetPayloadJCSSHA256 =
			"fact_set_verified", payloadJCS, digest(payloadJCS)
		var payload struct {
			RevisionID string `json:"fact_set_revision_id"`
			WikiID     string `json:"wiki_revision_id"`
			Scope      string `json:"source_scope_revision"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil {
			t.Fatal("fixed Catalog payload identity cannot decode")
		}
		row.SidecarRevisionID, row.SidecarWikiRevisionID, row.SidecarSourceScope =
			payload.RevisionID, payload.WikiID, payload.Scope
	} else {
		row.Status = "quality_verified"
	}
	return row
}

func fixtureJudgment(t *testing.T, template eventing.Event,
	frozen wikiqualitysource.FactSetV1, fact wikiqualitysource.FactSetFactV1,
	judgeID, revisionID, baseID string, revision int) ([]byte, eventing.Event) {
	t.Helper()
	var judgment wikiqualitysource.JudgmentV1
	if json.Unmarshal(template.Payload, &judgment) != nil {
		t.Fatal("fixed judgment fixture cannot decode")
	}
	judgment.ModuleID, judgment.PageID = frozen.ModuleID, frozen.PageID
	judgment.WikiRevisionID, judgment.WikiContentSHA256 =
		frozen.WikiRevisionID, frozen.WikiContentSHA256
	judgment.WikiOriginKind, judgment.BaseWikiRevisionID =
		frozen.WikiOriginKind, frozen.BaseWikiRevisionID
	judgment.JudgmentID, judgment.JudgeRevisionID = judgeID, revisionID
	judgment.BaseJudgeRevisionID, judgment.JudgeRevision = baseID, strconv.Itoa(revision)
	judgment.FactID = fact.FactID
	judgment.SourceRevisionID, judgment.SourceContentSHA256 =
		fact.SourceRevisionID, fact.SourceContentSHA256
	judgment.Locator, judgment.SourceByteStart, judgment.SourceByteEnd =
		fact.Locator, fact.SourceByteStart, fact.SourceByteEnd
	judgment.SourceQuote, judgment.SourceQuoteSHA256 =
		fact.SourceQuote, fact.SourceQuoteSHA256
	judgment.WikiClaimText, judgment.WikiClaimSHA256 =
		fact.SourceQuote, fact.SourceQuoteSHA256
	judgment.ActorID = "test-admin"
	grade := 3
	judgment.Grade = &grade
	judgment.Assessment, judgment.CitationPresent = "covered", true
	encoded, err := json.Marshal(judgment)
	if err != nil {
		t.Fatal(err)
	}
	event := template
	event.AggregateID, event.EventID = frozen.ModuleID, "evt_"+revisionID
	event.AggregateVersion = int64(revision)
	event.Payload = encoded
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wikiqualitysource.ParseJudgmentV1(event, raw); err != nil {
		t.Fatalf("synthetic transported RTW judgment does not meet fixed v1 wire: %v", err)
	}
	return raw, event
}

func sourceReaderFixture(t *testing.T) (AuthorityReader, PinnedRequest) {
	t.Helper()
	catalogRaw, catalogEvent := fixtureReadEvent(t, "wiki-fact-set-event-v1.json")
	payloadJCS, err := canonicalRaw(catalogEvent.Payload)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := wikiqualitysource.ParseFactSetV1(catalogEvent, catalogRaw, digest(payloadJCS))
	if err != nil {
		t.Fatal(err)
	}
	_, template := fixtureReadEvent(t, "wiki-quality-event-v1.json")
	rows := []ODSRow{fixtureODSRow(t, 1, catalogRaw, catalogEvent, payloadJCS)}
	events := map[string][]byte{catalogEvent.EventID: catalogRaw}
	for i, fact := range frozen.Facts {
		revisionID := "judge_revision_source_" + strconv.Itoa(i+1)
		raw, event := fixtureJudgment(t, template, frozen, fact,
			"wiki_judgment_source_"+strconv.Itoa(i+1), revisionID, "", 1)
		rows = append(rows, fixtureODSRow(t, int64(i+2), raw, event, nil))
		events[event.EventID] = raw
	}
	// A later rejudgment proves that cutoff3 pins revision1 while cutoff4
	// chooses revision2 only if its predecessor is present in the same prefix.
	raw, event := fixtureJudgment(t, template, frozen, frozen.Facts[0],
		"wiki_judgment_source_1", "judge_revision_source_1_v2",
		"judge_revision_source_1", 2)
	rows = append(rows, fixtureODSRow(t, 4, raw, event, nil))
	events[event.EventID] = raw
	catalogJCS, err := canonicalRaw(catalogRaw)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := ODSSnapshot{Producer: Producer, Consumer: wikiqualitysource.DefaultConsumer,
		CommittedOffset: 4, Rows: rows}
	rtw := fixtureRTW{events: events, history: HistoricalFactSet{Record: frozen,
		EventID: catalogEvent.EventID, EventRawSHA256: digest(catalogRaw),
		EventJCSSHA256: digest(catalogJCS), FactSetJCSSHA256: digest(payloadJCS)}}
	req := PinnedRequest{ModuleID: frozen.ModuleID, PageID: frozen.PageID,
		WikiRevisionID:      frozen.WikiRevisionID,
		SourceScopeRevision: frozen.SourceScopeRevision,
		FactSetRevisionID:   frozen.FactSetRevisionID, CutoffOffset: 3}
	return AuthorityReader{ODS: fixtureODS{snapshot}, RTW: rtw}, req
}

func TestPinnedReaderDistinguishesTransportedCutoffFromRTWHead(t *testing.T) {
	reader, req := sourceReaderFixture(t)
	first, err := reader.ReadPinnedSource(context.Background(), req)
	if err != nil || len(first.Catalog.Sources) != 2 || len(first.Catalog.Facts) != 2 ||
		len(first.Transported) != 2 || len(first.Selected) != 3 ||
		first.ODSPrefixSHA256 == first.Catalog.Event.RawSHA256 ||
		first.CommittedOffset != 4 || first.CutoffOffset != 3 {
		t.Fatalf("fixed source/ODS prefix lost selected Fact proofs: %+v %v", first, err)
	}
	for _, head := range first.Transported {
		if head.JudgeRevisionID == "judge_revision_source_1_v2" ||
			head.SourceScopeRevision != req.SourceScopeRevision ||
			len(head.Event.OriginalRaw) == 0 {
			t.Fatalf("cutoff3 accepted later RTW decision or lost original bytes: %+v", head)
		}
	}
	req.CutoffOffset = 4
	second, err := reader.ReadPinnedSource(context.Background(), req)
	if err != nil || len(second.Transported) != 2 || len(second.Selected) != 3 {
		t.Fatalf("cutoff4 could not rebuild distinct last transported Fact labels: %+v %v", second, err)
	}
	foundRevision2 := false
	for _, head := range second.Transported {
		foundRevision2 = foundRevision2 || head.JudgeRevisionID == "judge_revision_source_1_v2"
	}
	if !foundRevision2 || first.ODSPrefixSHA256 == second.ODSPrefixSHA256 {
		t.Fatal("read-through cutoff did not move the transported judgment/index")
	}
}

func TestPinnedReaderRejectsMissingPrefixOldHeadAndOriginalTampering(t *testing.T) {
	for _, trial := range []struct {
		name   string
		change func(*AuthorityReader, *PinnedRequest)
	}{
		{"missing_prefix_position", func(r *AuthorityReader, _ *PinnedRequest) {
			ods := r.ODS.(fixtureODS)
			ods.snapshot.Rows[1].Offset = 7
			r.ODS = ods
		}},
		{"forged_dc_input_hash", func(r *AuthorityReader, _ *PinnedRequest) {
			ods := r.ODS.(fixtureODS)
			ods.snapshot.Rows[0].DCInputHash = digest([]byte("wrong"))
			r.ODS = ods
		}},
		{"substituted_fact_set_sidecar_scope", func(r *AuthorityReader, _ *PinnedRequest) {
			ods := r.ODS.(fixtureODS)
			ods.snapshot.Rows[0].SidecarSourceScope = "scope_" +
				strings.Repeat("0", 64)
			r.ODS = ods
		}},
		{"old_or_substituted_historical_fact_set", func(r *AuthorityReader, _ *PinnedRequest) {
			rtw := r.RTW.(fixtureRTW)
			rtw.history.Record.WikiRevisionID = "another-wiki"
			r.RTW = rtw
		}},
		{"worker_original_bytes_substituted", func(r *AuthorityReader, _ *PinnedRequest) {
			rtw := r.RTW.(fixtureRTW)
			for id := range rtw.events {
				rtw.events[id] = []byte(`{"forged":true}`)
				break
			}
			r.RTW = rtw
		}},
		{"judgment_source_byte_span_differs_from_catalog", func(r *AuthorityReader, _ *PinnedRequest) {
			ods := r.ODS.(fixtureODS)
			var event eventing.Event
			if json.Unmarshal(ods.snapshot.Rows[1].AuthorityRaw, &event) != nil {
				panic("fixed judgment fixture cannot decode")
			}
			var judgment wikiqualitysource.JudgmentV1
			if json.Unmarshal(event.Payload, &judgment) != nil {
				panic("fixed judgment payload cannot decode")
			}
			start, _ := strconv.Atoi(judgment.SourceByteStart)
			end, _ := strconv.Atoi(judgment.SourceByteEnd)
			judgment.SourceByteStart = strconv.Itoa(start + 1)
			judgment.SourceByteEnd = strconv.Itoa(end + 1)
			event.Payload, _ = json.Marshal(judgment)
			raw, _ := json.Marshal(event)
			ods.snapshot.Rows[1] = fixtureODSRow(t, 2, raw, event, nil)
			r.ODS = ods
			rtw := r.RTW.(fixtureRTW)
			rtw.events[event.EventID] = raw
			r.RTW = rtw
		}},
		{"missing_predecessor_before_rejudge", func(r *AuthorityReader, req *PinnedRequest) {
			ods := r.ODS.(fixtureODS)
			var rejudge eventing.Event
			if json.Unmarshal(ods.snapshot.Rows[3].AuthorityRaw, &rejudge) != nil {
				panic("fixed rejudge fixture cannot decode")
			}
			ods.snapshot.Rows[1] = fixtureODSRow(t, 2,
				ods.snapshot.Rows[3].AuthorityRaw, rejudge, nil)
			r.ODS = ods
			req.CutoffOffset = 3
		}},
		{"missing_required_fact", func(r *AuthorityReader, req *PinnedRequest) {
			req.CutoffOffset = 2
		}},
	} {
		t.Run(trial.name, func(t *testing.T) {
			reader, req := sourceReaderFixture(t)
			trial.change(&reader, &req)
			if _, err := reader.ReadPinnedSource(context.Background(), req); err == nil ||
				(!errors.Is(err, ErrProof) && !errors.Is(err, ErrPrefix) &&
					!errors.Is(err, ErrHistoricalAuthority)) {
				t.Fatalf("tampered or incomplete source proof crossed cutoff: %v", err)
			}
		})
	}
}
