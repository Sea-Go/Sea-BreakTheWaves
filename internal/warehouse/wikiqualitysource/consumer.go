package wikiqualitysource

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

type Source interface {
	ReadEvents(context.Context, string, string, int) (eventing.Batch, error)
	EventReceipt(context.Context, string, string) (eventing.Receipt, error)
	AcknowledgeEvents(context.Context, string, eventing.Acknowledge) (eventing.DeliveryReceipt, error)
}

// Authority returns the exact immutable RTW Event JSON and its SHA over the
// original bytes; a DC copy or JSONB reserialization is not an authority.
type Authority interface {
	ReadQualityEvent(context.Context, string) (eventJSON []byte, eventSHA256 string, err error)
}

// QualityVerifier is supplied only after RTW freezes its v1 Event payload,
// source revision proof, human rubric and replay contract. Nil is fail-closed.
type QualityVerifier interface {
	VerifyQualityEvent(context.Context, eventing.Event, []byte) error
}

type Consumer struct {
	DB        *pgxpool.Pool
	Source    Source
	Authority Authority
	Verifier  QualityVerifier
	Consumer  string
}

type Result struct {
	Read               int
	QualityVerified    int
	TechnicalSkips     int
	CommittedOffset    int64
	AcknowledgedOffset int64
}

type row struct {
	item         eventing.Item
	eventSpec    []byte
	receipt      []byte
	receivedAt   time.Time
	authorityRaw []byte
	authoritySHA string
}

func Initialize(ctx context.Context, db *pgxpool.Pool) error {
	if db == nil {
		return ErrContract
	}
	_, err := db.Exec(ctx, schema)
	return err
}

// RunOnce stores the complete DC producer prefix and its technical receipts
// in a separate ODS cursor. An unfrozen Wiki quality event stops the whole
// batch before its PG transaction and before DC ACK; it is never a skip.
func (c *Consumer) RunOnce(ctx context.Context) (Result, error) {
	var out Result
	if c == nil || c.DB == nil || c.Source == nil || c.Consumer != DefaultConsumer {
		return out, ErrContract
	}
	batch, err := c.Source.ReadEvents(ctx, c.Consumer, Producer, 128)
	if err != nil {
		return out, fmt.Errorf("read independent DC Wiki quality cursor: %w", err)
	}
	if batch.Consumer != c.Consumer || batch.Producer != Producer {
		return out, ErrContract
	}
	if len(batch.Events) == 0 {
		return out, nil
	}
	if err := validateBatch(batch); err != nil {
		return out, err
	}
	rows := make([]row, 0, len(batch.Events))
	for _, item := range batch.Events {
		receipt, err := c.Source.EventReceipt(ctx, Producer, item.Event.EventID)
		if err != nil {
			return out, fmt.Errorf("read DC Wiki quality source receipt: %w", err)
		}
		if receipt.Producer != Producer || receipt.EventID != item.Event.EventID ||
			receipt.Offset != item.Offset || receipt.InputHash != item.InputHash ||
			receipt.ReceiptID == "" || receipt.TechnicalStatus != "accepted" {
			return out, ErrContract
		}
		receivedAt, err := time.Parse(time.RFC3339Nano, receipt.ReceivedAt)
		if err != nil {
			return out, ErrContract
		}
		eventRaw, err := json.Marshal(item.Event)
		if err != nil {
			return out, err
		}
		eventSpec, err := canonical(eventRaw)
		if err != nil {
			return out, err
		}
		receiptRaw, err := json.Marshal(receipt)
		if err != nil {
			return out, err
		}
		entry := row{item: item, eventSpec: eventSpec, receipt: receiptRaw, receivedAt: receivedAt}
		if qualityEvent(item.Event.EventType) {
			if c.Authority == nil || c.Verifier == nil {
				return out, ErrQualityUnconfigured
			}
			raw, sha, err := c.Authority.ReadQualityEvent(ctx, item.Event.EventID)
			if err != nil {
				return out, fmt.Errorf("read RTW original Wiki quality event: %w", err)
			}
			if err := proveOriginalEvent(item.Event, raw, sha, item.InputHash); err != nil {
				return out, err
			}
			if err := c.Verifier.VerifyQualityEvent(ctx, item.Event, raw); err != nil {
				return out, fmt.Errorf("verify RTW Wiki quality source: %w", err)
			}
			entry.authorityRaw, entry.authoritySHA = raw, sha
			out.QualityVerified++
		} else {
			out.TechnicalSkips++
		}
		rows = append(rows, entry)
	}
	committed, err := c.commit(ctx, batch, rows)
	if err != nil {
		return out, err
	}
	out.Read, out.CommittedOffset = len(rows), committed
	ack, err := c.Source.AcknowledgeEvents(ctx, c.Consumer, eventing.Acknowledge{
		Producer: Producer, FromOffset: batch.FromOffset, ToOffset: batch.ToOffset, BatchHash: batch.BatchHash})
	if err != nil {
		return out, fmt.Errorf("ACK independent DC Wiki quality cursor after PG commit: %w", err)
	}
	if ack.Consumer != c.Consumer || ack.Producer != Producer || ack.AcknowledgedOffset != batch.ToOffset ||
		ack.TechnicalStatus != "delivered" {
		return out, ErrContract
	}
	out.AcknowledgedOffset = ack.AcknowledgedOffset
	return out, nil
}

func (c *Consumer) commit(ctx context.Context, batch eventing.Batch, rows []row) (int64, error) {
	tx, err := c.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO warehouse_wiki_quality.consumer_cursor(consumer,producer)
 VALUES($1,$2) ON CONFLICT DO NOTHING`, c.Consumer, Producer); err != nil {
		return 0, err
	}
	var producer string
	var last int64
	if err := tx.QueryRow(ctx, `SELECT producer,committed_offset FROM warehouse_wiki_quality.consumer_cursor
 WHERE consumer=$1 FOR UPDATE`, c.Consumer).Scan(&producer, &last); err != nil {
		return 0, err
	}
	if producer != Producer || batch.FromOffset > last+1 {
		return 0, fmt.Errorf("%w: Wiki quality consumer offset or producer mismatch", ErrContract)
	}
	for _, r := range rows {
		status := "technical_skip"
		var original []byte
		var originalSHA *string
		if qualityEvent(r.item.Event.EventType) {
			status, original, originalSHA = "quality_verified", r.authorityRaw, &r.authoritySHA
		}
		if r.item.Offset <= last {
			var oldID, oldHash, oldStatus string
			var oldSpec, oldReceipt, oldOriginal []byte
			var oldOriginalSHA *string
			if err := tx.QueryRow(ctx, `SELECT event_id,dc_input_hash,status,event_spec,dc_receipt,
 authority_event_json,authority_event_sha256 FROM warehouse_wiki_quality.ods_event
 WHERE producer=$1 AND source_offset=$2`, Producer, r.item.Offset).Scan(
				&oldID, &oldHash, &oldStatus, &oldSpec, &oldReceipt, &oldOriginal, &oldOriginalSHA); err != nil ||
				oldID != r.item.Event.EventID || oldHash != r.item.InputHash || oldStatus != status ||
				!bytes.Equal(oldSpec, r.eventSpec) || !bytes.Equal(oldReceipt, r.receipt) ||
				!bytes.Equal(oldOriginal, original) || !equalOptional(oldOriginalSHA, originalSHA) {
				return 0, ErrContract
			}
			continue
		}
		if r.item.Offset != last+1 {
			return 0, fmt.Errorf("%w: noncontiguous Wiki quality source offset %d", ErrContract, r.item.Offset)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO warehouse_wiki_quality.ods_event
 (producer,source_offset,event_id,event_type,event_spec,dc_input_hash,dc_receipt,
  dc_received_at,status,authority_event_json,authority_event_sha256)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, Producer, r.item.Offset,
			r.item.Event.EventID, r.item.Event.EventType, r.eventSpec, r.item.InputHash,
			r.receipt, r.receivedAt, status, original, originalSHA); err != nil {
			return 0, err
		}
		last++
	}
	if _, err := tx.Exec(ctx, `UPDATE warehouse_wiki_quality.consumer_cursor
 SET committed_offset=$2 WHERE consumer=$1`, c.Consumer, last); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return last, nil
}

func equalOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
