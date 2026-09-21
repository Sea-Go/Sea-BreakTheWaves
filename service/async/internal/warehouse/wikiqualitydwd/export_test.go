package wikiqualitydwd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/evaluation/wiki_quality/sourceproof"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/warehouse/wikiqualitysource"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
)

type odsFixture struct {
	rows       []sourceproof.ODSRow
	reads      int
	changeLast bool
}

func (f *odsFixture) ReadPrefix(_ context.Context, cutoff int64) (sourceproof.ODSSnapshot, error) {
	if cutoff < 1 || cutoff > int64(len(f.rows)) {
		return sourceproof.ODSSnapshot{}, sourceproof.ErrPrefix
	}
	f.reads++
	rows := append([]sourceproof.ODSRow(nil), f.rows[:cutoff]...)
	if f.changeLast && f.reads == 3 {
		rows[0].DCReceipt = append(append([]byte(nil), rows[0].DCReceipt...), ' ')
	}
	return sourceproof.ODSSnapshot{Producer: sourceproof.Producer,
		Consumer:        wikiqualitysource.DefaultConsumer,
		CommittedOffset: int64(len(f.rows)), Rows: rows}, nil
}

type rtwFixture struct {
	events  map[string][]byte
	history sourceproof.HistoricalFactSet
}

func (f rtwFixture) ReadFactSetEvent(_ context.Context, id string) (
	wikiqualitysource.FactSetEventProof, error) {
	raw := f.events[id]
	if len(raw) == 0 {
		return wikiqualitysource.FactSetEventProof{}, sourceproof.ErrHistoricalAuthority
	}
	var event eventing.Event
	if json.Unmarshal(raw, &event) != nil {
		return wikiqualitysource.FactSetEventProof{}, ErrSource
	}
	eventJCS, _ := jcs(raw)
	payloadJCS, _ := jcs(event.Payload)
	return wikiqualitysource.FactSetEventProof{EventJSON: raw,
		EventRawSHA256: hash(raw), EventJCSSHA256: hash(eventJCS),
		FactSetJCSSHA256: hash(payloadJCS)}, nil
}

func (f rtwFixture) ReadQualityEvent(_ context.Context, id string) ([]byte, string, error) {
	raw := f.events[id]
	if len(raw) == 0 {
		return nil, "", sourceproof.ErrHistoricalAuthority
	}
	return raw, hash(raw), nil
}

func (f rtwFixture) ReadHistoricalFactSet(_ context.Context, _, _, _, _ string) (
	sourceproof.HistoricalFactSet, error) {
	return f.history, nil
}

type dcFixture struct {
	rows   []sourceproof.ODSRow
	badACK bool
}

func (f dcFixture) ReadAcknowledgedPrefix(_ context.Context, consumer, producer string,
	cutoff, from int64, _ int) (sourceproof.DCAckPage, error) {
	to := cutoff
	page := sourceproof.DCAckPage{Consumer: consumer, Producer: producer,
		CutoffOffset: cutoff, AcknowledgedOffset: int64(len(f.rows)),
		ProducerOffset: int64(len(f.rows)), FromOffset: from, ToOffset: to,
		PrefixVerified: true}
	if f.badACK {
		page.AcknowledgedOffset = cutoff - 1
	}
	items := make([]eventing.Item, 0, len(f.rows))
	for i, row := range f.rows {
		var event eventing.Event
		var receipt eventing.Receipt
		if json.Unmarshal(row.EventSpec, &event) != nil ||
			json.Unmarshal(row.DCReceipt, &receipt) != nil {
			return sourceproof.DCAckPage{}, ErrSource
		}
		items = append(items, eventing.Item{Offset: int64(i) + 1,
			InputHash: row.DCInputHash, Event: event})
		if int64(i)+1 >= from && int64(i)+1 <= to {
			page.Events = append(page.Events, sourceproof.DCAckItem{
				Offset: row.Offset, EventID: row.EventID,
				InputHash: row.DCInputHash, ReceiptID: receipt.ReceiptID,
				ReceivedAt: receipt.ReceivedAt, Event: event})
		}
	}
	batchJCS, _ := jcsValue(items)
	page.DeliveryReceipts = []sourceproof.DCBatchReceipt{{FromOffset: 1,
		ToOffset: int64(len(f.rows)), BatchHash: hash(batchJCS),
		AcceptedAt: "2026-09-16T00:00:02Z"}}
	return page, nil
}

func readFixtureEvent(t *testing.T, name string) ([]byte, eventing.Event) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "wikiqualitysource", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var event eventing.Event
	if json.Unmarshal(raw, &event) != nil {
		t.Fatal("fixed RTW event fixture cannot decode")
	}
	return bytes.TrimSpace(raw), event
}

func makeODSRow(t *testing.T, offset int64, raw []byte, event eventing.Event) sourceproof.ODSRow {
	t.Helper()
	spec, err := jcs(raw)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := json.Marshal(eventing.Receipt{EventID: event.EventID,
		Producer: sourceproof.Producer, TechnicalStatus: "accepted",
		ReceiptID: "receipt-" + strconv.FormatInt(offset, 10),
		InputHash: hash(spec), Offset: offset,
		ReceivedAt: "2026-09-16T00:00:01Z"})
	if err != nil {
		t.Fatal(err)
	}
	row := sourceproof.ODSRow{Offset: offset, EventID: event.EventID,
		EventType: event.EventType, EventSpec: spec, DCInputHash: hash(spec),
		DCReceipt: receipt}
	switch event.EventType {
	case wikiqualitysource.FactSetFrozenV1:
		row.Status = "fact_set_verified"
		row.FactSetPayloadJCS, _ = jcs(event.Payload)
		row.FactSetPayloadJCSSHA256 = hash(row.FactSetPayloadJCS)
		var id struct {
			Revision string `json:"fact_set_revision_id"`
			Wiki     string `json:"wiki_revision_id"`
			Scope    string `json:"source_scope_revision"`
		}
		if json.Unmarshal(event.Payload, &id) != nil {
			t.Fatal("frozen FactSet fixture identity")
		}
		row.SidecarRevisionID, row.SidecarWikiRevisionID, row.SidecarSourceScope =
			id.Revision, id.Wiki, id.Scope
		row.AuthorityRaw, row.AuthorityRawSHA256 = raw, hash(raw)
	case wikiqualitysource.Judged:
		row.Status = "quality_verified"
		row.AuthorityRaw, row.AuthorityRawSHA256 = raw, hash(raw)
	default:
		row.Status = "technical_skip"
	}
	return row
}

func makeJudgment(t *testing.T, template eventing.Event,
	catalog wikiqualitysource.FactSetV1, fact wikiqualitysource.FactSetFactV1,
	ordinal, revision int, previous string) ([]byte, eventing.Event) {
	t.Helper()
	var value wikiqualitysource.JudgmentV1
	if json.Unmarshal(template.Payload, &value) != nil {
		t.Fatal("quality template fixture")
	}
	value.ModuleID, value.PageID = catalog.ModuleID, catalog.PageID
	value.WikiRevisionID, value.WikiContentSHA256 =
		catalog.WikiRevisionID, catalog.WikiContentSHA256
	value.WikiOriginKind, value.BaseWikiRevisionID =
		catalog.WikiOriginKind, catalog.BaseWikiRevisionID
	value.JudgmentID = "wiki_judgment_dwd_" + strconv.Itoa(ordinal)
	value.JudgeRevisionID = "judge_revision_dwd_" + strconv.Itoa(ordinal) + "_" + strconv.Itoa(revision)
	value.BaseJudgeRevisionID = previous
	value.JudgeRevision = strconv.Itoa(revision)
	value.FactID = fact.FactID
	value.SourceRevisionID, value.SourceContentSHA256 =
		fact.SourceRevisionID, fact.SourceContentSHA256
	value.Locator, value.SourceByteStart, value.SourceByteEnd =
		fact.Locator, fact.SourceByteStart, fact.SourceByteEnd
	value.SourceQuote, value.SourceQuoteSHA256 =
		fact.SourceQuote, fact.SourceQuoteSHA256
	value.WikiClaimText, value.WikiClaimSHA256 =
		fact.SourceQuote, fact.SourceQuoteSHA256
	value.ActorID = "test-admin"
	value.CitationPresent, value.Assessment = true, "covered"
	grade := 3
	value.Grade = &grade
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	event := template
	event.EventID = "evt_" + value.JudgeRevisionID
	event.AggregateID = catalog.ModuleID
	event.AggregateVersion = int64(revision)
	event.Payload = payload
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wikiqualitysource.ParseJudgmentV1(event, raw); err != nil {
		t.Fatalf("constructed source judgment violates frozen v1: %v", err)
	}
	return raw, event
}

func buildFixture(t *testing.T) (sourceproof.AuthorityReader,
	sourceproof.PinnedRequest, dcFixture) {
	t.Helper()
	catalogRaw, catalogEvent := readFixtureEvent(t, "wiki-fact-set-event-v1.json")
	payloadJCS, _ := jcs(catalogEvent.Payload)
	catalog, err := wikiqualitysource.ParseFactSetV1(catalogEvent,
		catalogRaw, hash(payloadJCS))
	if err != nil {
		t.Fatal(err)
	}
	_, template := readFixtureEvent(t, "wiki-quality-event-v1.json")
	rows := []sourceproof.ODSRow{makeODSRow(t, 1, catalogRaw, catalogEvent)}
	events := map[string][]byte{catalogEvent.EventID: catalogRaw}
	for i, fact := range catalog.Facts {
		raw, event := makeJudgment(t, template, catalog, fact, i+1, 1, "")
		rows = append(rows, makeODSRow(t, int64(i)+2, raw, event))
		events[event.EventID] = raw
	}
	firstRevision := "judge_revision_dwd_1_1"
	raw, event := makeJudgment(t, template, catalog, catalog.Facts[0], 1, 2, firstRevision)
	rows = append(rows, makeODSRow(t, 4, raw, event))
	events[event.EventID] = raw
	technical := eventing.Event{EventID: "evt_dwd_technical_5",
		EventType: "knowledge.wiki.page.revised.v1", SchemaVersion: 1,
		Producer: sourceproof.Producer, AggregateID: catalog.ModuleID,
		AggregateVersion: 3, OperationID: "command:dwd-technical-5",
		OccurredAt: "2026-09-16T00:00:03Z", Payload: json.RawMessage(`{}`)}
	technicalRaw, _ := json.Marshal(technical)
	rows = append(rows, makeODSRow(t, 5, technicalRaw, technical))
	catalogJCS, _ := jcs(catalogRaw)
	reader := sourceproof.AuthorityReader{ODS: &odsFixture{rows: rows},
		RTW: rtwFixture{events: events,
			history: sourceproof.HistoricalFactSet{Record: catalog,
				EventID:          catalogEvent.EventID,
				EventRawSHA256:   hash(catalogRaw),
				EventJCSSHA256:   hash(catalogJCS),
				FactSetJCSSHA256: hash(payloadJCS)}}}
	req := sourceproof.PinnedRequest{ModuleID: catalog.ModuleID,
		PageID: catalog.PageID, WikiRevisionID: catalog.WikiRevisionID,
		SourceScopeRevision: catalog.SourceScopeRevision,
		FactSetRevisionID:   catalog.FactSetRevisionID, CutoffOffset: 3}
	return reader, req, dcFixture{rows: rows}
}

func TestFreezePinsLastTransportedFactAndKeepsTechnicalSkipOutOfQuality(t *testing.T) {
	reader, req, dc := buildFixture(t)
	first, err := Freeze(context.Background(), reader, req, dc)
	if err != nil || first.Manifest.PrefixRows != 3 ||
		first.Manifest.CatalogFacts != 2 || first.Manifest.JudgmentRevisions != 2 ||
		first.Manifest.TransportedFactCount != 2 ||
		first.Manifest.QualityState != NotEvaluable ||
		first.Manifest.Activation != "none" ||
		first.Judgments[0].JudgeRevision != "1" {
		t.Fatalf("cutoff3 offline provenance lost Fact sources: %+v %v", first.Manifest, err)
	}
	reader, req, dc = buildFixture(t)
	req.CutoffOffset = 5
	second, err := Freeze(context.Background(), reader, req, dc)
	if err != nil || second.Manifest.PrefixRows != 5 ||
		second.Manifest.TechnicalSkips != 1 ||
		second.Manifest.JudgmentRevisions != 3 ||
		second.Judgments[2].JudgeRevision != "2" ||
		second.PrefixRows[4].Status != "technical_skip" ||
		second.PrefixRows[4].RTWOriginalEvent != "" ||
		bytes.Contains(second.JudgmentJSONL, []byte("technical_skip")) ||
		bytes.Contains(second.CatalogJSONL, []byte("observed")) {
		t.Fatalf("cutoff5 rejudgment or technical prefix was promoted: %+v %v", second.Manifest, err)
	}
	if first.Manifest.CatalogJSONLSHA256 == second.Manifest.CatalogJSONLSHA256 ||
		first.Manifest.JudgmentJSONLSHA256 == second.Manifest.JudgmentJSONLSHA256 {
		t.Fatal("two acknowledged cutoff generations collapsed to one source stream")
	}
	reader, req, dc = buildFixture(t)
	req.CutoffOffset = 5
	replay, err := Freeze(context.Background(), reader, req, dc)
	if err != nil || !bytes.Equal(second.PrefixJSONL, replay.PrefixJSONL) ||
		!bytes.Equal(second.CatalogJSONL, replay.CatalogJSONL) ||
		!bytes.Equal(second.JudgmentJSONL, replay.JudgmentJSONL) {
		t.Fatal("same frozen cutoff changed append-only landing bytes")
	}
}

func TestFreezeRejectsUnacknowledgedOrChangedThirdODSSnapshot(t *testing.T) {
	reader, req, dc := buildFixture(t)
	dc.badACK = true
	if _, err := Freeze(context.Background(), reader, req, dc); !errors.Is(err, sourceproof.ErrDCAckEvidence) {
		t.Fatalf("unacknowledged cutoff crossed PG to offline landing: %v", err)
	}
	reader, req, dc = buildFixture(t)
	reader.ODS.(*odsFixture).changeLast = true
	if _, err := Freeze(context.Background(), reader, req, dc); !errors.Is(err, ErrSource) {
		t.Fatalf("third ODS snapshot changed original receipt yet passed: %v", err)
	}
}

func TestSyntheticOfflineGoldenRebuildsExactSourcesAtBothCutoffs(t *testing.T) {
	for _, item := range []struct {
		cutoff int64
		sha    string
	}{{3, "9f79112c2271c1209b89fa0f7a07a80760e09efb0705f80f21cd804314649735"},
		{5, "6697b6fc2fcb08f245ba65393f108a7e9002733c355448e96f6912bc34ea29e9"}} {
		reader, req, dc := buildFixture(t)
		req.CutoffOffset = item.cutoff
		frozen, err := Freeze(context.Background(), reader, req, dc)
		if err != nil || frozen.ManifestSHA != item.sha {
			t.Fatalf("cutoff%d synthetic source Golden changed: %s %v",
				item.cutoff, frozen.ManifestSHA, err)
		}
		dir := filepath.Join("testdata", "fixtures", "cutoff-"+strconv.FormatInt(item.cutoff, 10))
		for _, artifact := range []struct {
			name string
			body []byte
		}{{"prefix.jsonl", frozen.PrefixJSONL},
			{"catalog-facts.jsonl", frozen.CatalogJSONL},
			{"judgments.jsonl", frozen.JudgmentJSONL},
			{"manifest.json", frozen.ManifestJCS}} {
			fixed, err := os.ReadFile(filepath.Join(dir, artifact.name))
			if err != nil || !bytes.Equal(fixed, artifact.body) {
				t.Fatalf("cutoff%d Golden %s differs from frozen Go output: %v",
					item.cutoff, artifact.name, err)
			}
		}
	}
}

// This optional generator writes fixed SYNTHETIC fixtures once for isolated
// ClickHouse/dbt tests. It never reads a live RTW/DC/PG service.
func TestGenerateSyntheticWikiWarehouseFixtures(t *testing.T) {
	root := os.Getenv("SEA_WIKI_QUALITY_SYNTHETIC_FIXTURE_OUTPUT")
	if root == "" {
		t.Skip("explicit synthetic fixture output directory not provided")
	}
	for _, cutoff := range []int64{3, 5} {
		reader, req, dc := buildFixture(t)
		req.CutoffOffset = cutoff
		frozen, err := Freeze(context.Background(), reader, req, dc)
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, "cutoff-"+strconv.FormatInt(cutoff, 10))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := frozen.Write(dir); err != nil {
			t.Fatal(err)
		}
		if err := frozen.Write(dir); !errors.Is(err, os.ErrExist) {
			t.Fatalf("append-only evidence file was overwritten: %v", err)
		}
	}
}
