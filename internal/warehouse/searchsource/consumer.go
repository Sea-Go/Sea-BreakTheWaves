package searchsource

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
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

// Authority returns RTW's original immutable Outbox Event JSON and the SHA
// over those exact bytes. Only judgment event IDs have this read surface.
type Authority interface {
	ReadJudgmentEvent(context.Context, string) (eventJSON []byte, eventSHA256 string, err error)
}

type Consumer struct {
	DB        *pgxpool.Pool
	Source    Source
	Authority Authority
	Consumer  string
}

type Result struct {
	Read               int
	Judgments          int
	TechnicalSkips     int
	CommittedOffset    int64
	AcknowledgedOffset int64
}

type row struct {
	item         eventing.Item
	receipt      eventing.Receipt
	receivedAt   time.Time
	eventSpec    []byte
	authorityRaw []byte
	authoritySHA string
	judgment     *Judgment
}

func Initialize(ctx context.Context, db *pgxpool.Pool) error {
	if db == nil {
		return ErrContract
	}
	_, err := db.Exec(ctx, schema)
	return err
}

// RunOnce owns a cursor separate from BTW Search, usermodel, and other
// warehouse consumers. RTW Knowledge shares its producer with non-qrel events;
// those offsets are recorded as technical skips, never mislabeled qrels.
func (c *Consumer) RunOnce(ctx context.Context) (Result, error) {
	var out Result
	if c == nil || c.DB == nil || c.Source == nil || c.Authority == nil || c.Consumer != DefaultConsumer {
		return out, ErrContract
	}
	batch, err := c.Source.ReadEvents(ctx, c.Consumer, Producer, 128)
	if err != nil {
		return out, fmt.Errorf("read independent DC qrel cursor: %w", err)
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
			return out, fmt.Errorf("read DC judgment source receipt: %w", err)
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
		eventSpec, err := json.Marshal(item.Event)
		if err != nil {
			return out, err
		}
		entry := row{item: item, receipt: receipt, receivedAt: receivedAt, eventSpec: eventSpec}
		if isJudgment(item.Event.EventType) {
			raw, sha, err := c.Authority.ReadJudgmentEvent(ctx, item.Event.EventID)
			if err != nil {
				return out, fmt.Errorf("read RTW frozen judgment source: %w", err)
			}
			judgment, err := parseJudgment(item.Event, raw, sha, item.InputHash)
			if err != nil {
				return out, err
			}
			judgedAt, _ := time.Parse(time.RFC3339Nano, judgment.JudgedAt)
			if judgedAt.After(receivedAt) {
				return out, ErrContract
			}
			entry.authorityRaw, entry.authoritySHA, entry.judgment = raw, sha, &judgment
			out.Judgments++
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
		return out, fmt.Errorf("ACK independent DC qrel cursor after PG commit: %w", err)
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
	_, err = tx.Exec(ctx, `INSERT INTO warehouse_search_source.consumer_cursor(consumer,producer)
 VALUES($1,$2) ON CONFLICT DO NOTHING`, c.Consumer, Producer)
	if err != nil {
		return 0, err
	}
	var producer string
	var last int64
	if err := tx.QueryRow(ctx, `SELECT producer,committed_offset FROM warehouse_search_source.consumer_cursor
 WHERE consumer=$1 FOR UPDATE`, c.Consumer).Scan(&producer, &last); err != nil {
		return 0, err
	}
	if producer != Producer || batch.FromOffset > last+1 {
		return 0, fmt.Errorf("%w: independent cursor producer or offset mismatch", ErrContract)
	}
	for _, r := range rows {
		if r.item.Offset <= last {
			var oldEventID, oldHash string
			if err := tx.QueryRow(ctx, `SELECT event_id,dc_input_hash FROM warehouse_search_source.ods_event
 WHERE producer=$1 AND source_offset=$2`, Producer, r.item.Offset).Scan(&oldEventID, &oldHash); err != nil ||
				oldEventID != r.item.Event.EventID || oldHash != r.item.InputHash {
				return 0, ErrContract
			}
			continue
		}
		if r.item.Offset != last+1 {
			return 0, fmt.Errorf("%w: noncontiguous ODS source offset %d", ErrContract, r.item.Offset)
		}
		status := "technical_skip"
		var authorityRaw []byte
		var authoritySHA, judgmentID, revisionID, baseID, judgmentState *string
		var revisionNumber *int
		var payload []byte
		if r.judgment != nil {
			status = "qrel_revision"
			j := r.judgment
			var prior string
			var priorNumber int
			err := tx.QueryRow(ctx, `SELECT judgment_revision_id,judgment_revision FROM warehouse_search_source.ods_event
 WHERE producer=$1 AND judgment_id=$2 AND status='qrel_revision'
	ORDER BY source_offset DESC LIMIT 1`, Producer, j.JudgmentID).Scan(&prior, &priorNumber)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return 0, err
			}
			if (errors.Is(err, pgx.ErrNoRows) && (j.BaseRevisionID != "" || j.JudgmentRevision != 1)) ||
				(err == nil && (j.BaseRevisionID != prior || j.JudgmentRevision != priorNumber+1)) {
				return 0, fmt.Errorf("%w: judgment revision chain at source offset %d", ErrContract, r.item.Offset)
			}
			authorityRaw, payload = r.authorityRaw, r.item.Event.Payload
			authoritySHA, judgmentID, revisionID = &r.authoritySHA, &j.JudgmentID, &j.RevisionID
			revisionNumber = &j.JudgmentRevision
			baseID, judgmentState = &j.BaseRevisionID, &j.State
		}
		receipt, err := json.Marshal(r.receipt)
		if err != nil {
			return 0, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO warehouse_search_source.ods_event
 (producer,source_offset,event_id,event_type,event_spec,dc_input_hash,dc_receipt,dc_received_at,
  status,authority_event_json,authority_event_sha256,judgment_id,judgment_revision_id,
  judgment_revision,base_revision_id,judgment_state,judgment_payload)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
			Producer, r.item.Offset, r.item.Event.EventID, r.item.Event.EventType, r.eventSpec,
			r.item.InputHash, receipt, r.receivedAt, status, authorityRaw, authoritySHA,
			judgmentID, revisionID, revisionNumber, baseID, judgmentState, payload)
		if err != nil {
			return 0, err
		}
		last++
	}
	if _, err := tx.Exec(ctx, `UPDATE warehouse_search_source.consumer_cursor
 SET committed_offset=$2 WHERE consumer=$1`, c.Consumer, last); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return last, nil
}
