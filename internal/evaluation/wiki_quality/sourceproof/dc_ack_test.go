package sourceproof

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
)

type fixtureDCAck struct {
	rows           []ODSRow
	chunk          int64
	badACK         bool
	badInputHash   bool
	badBatchHash   bool
	badProducer    bool
	badReceiptPage bool
}

// A separate ODS read can keep the same EventID/DC JCS index while changing
// raw receipt bytes. The full fixed-cutoff evidence must remain identical.
type changedReceiptODS struct {
	fixtureODS
	reads int
}

func (s *changedReceiptODS) ReadPrefix(ctx context.Context, cutoff int64) (ODSSnapshot, error) {
	snapshot, err := s.fixtureODS.ReadPrefix(ctx, cutoff)
	if err != nil {
		return snapshot, err
	}
	s.reads++
	if s.reads == 2 {
		snapshot.Rows[0].DCReceipt = append(append([]byte(nil), snapshot.Rows[0].DCReceipt...), ' ')
	}
	return snapshot, nil
}

func (f fixtureDCAck) ReadAcknowledgedPrefix(_ context.Context, consumer, producer string,
	cutoff, from int64, _ int) (DCAckPage, error) {
	to := from + f.chunk - 1
	if to > cutoff {
		to = cutoff
	}
	page := DCAckPage{Consumer: consumer, Producer: producer,
		CutoffOffset: cutoff, AcknowledgedOffset: int64(len(f.rows)),
		ProducerOffset: int64(len(f.rows)), FromOffset: from, ToOffset: to,
		PrefixVerified: true}
	if f.badACK {
		page.AcknowledgedOffset = cutoff - 1
	}
	if f.badProducer && from > 1 {
		page.Producer = "substituted-producer"
	}
	for offset := from; offset <= to; offset++ {
		row := f.rows[offset-1]
		var event eventing.Event
		var receipt eventing.Receipt
		if json.Unmarshal(row.EventSpec, &event) != nil ||
			json.Unmarshal(row.DCReceipt, &receipt) != nil {
			return DCAckPage{}, ErrDCAckEvidence
		}
		item := DCAckItem{Offset: offset, EventID: row.EventID,
			InputHash: row.DCInputHash, ReceiptID: receipt.ReceiptID,
			ReceivedAt: receipt.ReceivedAt, Event: event}
		if f.badInputHash && offset == 2 {
			item.InputHash = digest([]byte("substituted"))
		}
		page.Events = append(page.Events, item)
	}
	for _, bounds := range [][2]int64{{1, 3}, {4, 4}} {
		if bounds[0] > cutoff || bounds[0] > to || bounds[1] < from {
			continue
		}
		items := make([]eventing.Item, 0, bounds[1]-bounds[0]+1)
		for offset := bounds[0]; offset <= bounds[1]; offset++ {
			row := f.rows[offset-1]
			var event eventing.Event
			if json.Unmarshal(row.EventSpec, &event) != nil {
				return DCAckPage{}, ErrDCAckEvidence
			}
			items = append(items, eventing.Item{Offset: offset,
				InputHash: row.DCInputHash, Event: event})
		}
		_, hash, err := jcs(items)
		if err != nil {
			return DCAckPage{}, err
		}
		if f.badBatchHash && bounds[0] == 1 {
			hash = digest([]byte("wrong-batch"))
		}
		if f.badReceiptPage && from > 1 && bounds[0] == 1 {
			hash = digest([]byte("contradictory-page"))
		}
		page.DeliveryReceipts = append(page.DeliveryReceipts, DCBatchReceipt{
			FromOffset: bounds[0], ToOffset: bounds[1], BatchHash: hash,
			AcceptedAt: "2026-09-16T00:00:02Z"})
	}
	return page, nil
}

func TestAcknowledgedReaderJoinsDCPagesToImmutableODSPrefix(t *testing.T) {
	reader, req := sourceReaderFixture(t)
	dc := fixtureDCAck{rows: reader.ODS.(fixtureODS).snapshot.Rows, chunk: 2}
	ack, err := reader.ReadAcknowledgedPinnedSource(context.Background(), req, dc)
	if err != nil || ack.AcknowledgedAtLeast != 4 ||
		ack.DCIndexSHA256 == ack.ODSPrefixSHA256 ||
		len(ack.DCBatchReceipts) != 1 || len(ack.Transported) != 2 ||
		ack.DCBatchReceipts[0].FromOffset != 1 ||
		ack.DCBatchReceipts[0].ToOffset != 3 {
		t.Fatalf("DC ACK page/ODS/source proof did not join on one cutoff: %+v %v", ack, err)
	}
	req.CutoffOffset = 4
	ack, err = reader.ReadAcknowledgedPinnedSource(context.Background(), req, dc)
	if err != nil || len(ack.DCBatchReceipts) != 2 ||
		ack.DCBatchReceipts[1].FromOffset != 4 {
		t.Fatalf("second acknowledged batch was dropped at cutoff4: %+v %v", ack, err)
	}
}

func TestAcknowledgedReaderRejectsChangedOriginalReceiptAcrossODSSnapshots(t *testing.T) {
	reader, req := sourceReaderFixture(t)
	first := reader.ODS.(fixtureODS)
	reader.ODS = &changedReceiptODS{fixtureODS: first}
	dc := fixtureDCAck{rows: first.snapshot.Rows, chunk: 2}
	if _, err := reader.ReadAcknowledgedPinnedSource(context.Background(), req, dc); !errors.Is(err, ErrPrefix) {
		t.Fatalf("same EventID/JCS with changed original receipt qualified: %v", err)
	}
}

func TestAcknowledgedReaderRejectsUnackedAndCrossPageSubstitution(t *testing.T) {
	for _, trial := range []struct {
		name string
		bad  func(*fixtureDCAck)
	}{
		{"no_dc_port", nil},
		{"unacknowledged_cutoff", func(d *fixtureDCAck) { d.badACK = true }},
		{"substituted_dc_input_hash", func(d *fixtureDCAck) { d.badInputHash = true }},
		{"wrong_original_batch_hash", func(d *fixtureDCAck) { d.badBatchHash = true }},
		{"producer_changed_on_second_page", func(d *fixtureDCAck) { d.badProducer = true }},
		{"receipt_conflicts_across_pages", func(d *fixtureDCAck) { d.badReceiptPage = true }},
	} {
		t.Run(trial.name, func(t *testing.T) {
			reader, req := sourceReaderFixture(t)
			dc := fixtureDCAck{rows: reader.ODS.(fixtureODS).snapshot.Rows, chunk: 2}
			var port DCAckReader = dc
			if trial.bad == nil {
				port = nil
			} else {
				trial.bad(&dc)
				port = dc
			}
			if _, err := reader.ReadAcknowledgedPinnedSource(context.Background(), req, port); !errors.Is(err, ErrDCAckEvidence) {
				t.Fatalf("incomplete DC ACK witness crossed SourceProof: %v", err)
			}
		})
	}
}

func TestHTTPDCAckReaderUsesFixedServiceTokenReadSelector(t *testing.T) {
	reader, req := sourceReaderFixture(t)
	dc := fixtureDCAck{rows: reader.ODS.(fixtureODS).snapshot.Rows, chunk: 2}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path !=
			"/v1/event-consumers/btw-warehouse-wiki-quality/acknowledged-prefix" ||
			r.Header.Get("Authorization") != "Bearer fixture-platform-token" ||
			r.URL.Query().Get("producer") != Producer ||
			r.URL.Query().Get("cutoff") != strconv.FormatInt(req.CutoffOffset, 10) ||
			len(r.URL.Query()) != 4 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		from, _ := strconv.ParseInt(r.URL.Query().Get("from_offset"), 10, 64)
		page, err := dc.ReadAcknowledgedPrefix(r.Context(),
			"btw-warehouse-wiki-quality", Producer, req.CutoffOffset, from, 128)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()
	httpDC, err := NewHTTPDCAckReader(server.URL, "fixture-platform-token")
	if err != nil {
		t.Fatal(err)
	}
	ack, err := reader.ReadAcknowledgedPinnedSource(context.Background(), req, httpDC)
	if err != nil || ack.AcknowledgedAtLeast != 4 || len(ack.DCBatchReceipts) != 1 {
		t.Fatalf("fixed DC acknowledged-prefix HTTP selector did not join ODS: %+v %v", ack, err)
	}
	if _, err := NewHTTPDCAckReader(server.URL, "fixture-platform-token\nforged"); !errors.Is(err, ErrDCAckEvidence) {
		t.Fatalf("platform bearer controls were accepted: %v", err)
	}
}
