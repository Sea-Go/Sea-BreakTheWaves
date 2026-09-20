package wikiqualitysource

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
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
	DB               *pgxpool.Pool
	Source           Source
	Authority        Authority
	Verifier         QualityVerifier
	FactSetAuthority FactSetAuthority
	FactSetVerifier  FactSetVerifier
	Consumer         string
}

type Result struct {
	Read               int
	QualityVerified    int
	Catalogs           int
	TechnicalSkips     int
	CommittedOffset    int64
	AcknowledgedOffset int64
}

type row struct {
	item              eventing.Item
	eventSpec         []byte
	receipt           []byte
	receivedAt        time.Time
	authorityRaw      []byte
	authoritySHA      string
	factSet           *FactSetV1
	factSetPayloadJCS []byte
	factSetPayloadSHA string
}

func Initialize(ctx context.Context, db *pgxpool.Pool) error {
	if db == nil {
		return ErrContract
	}
	// schema.sql creates a fresh v2 database only. Running CREATE IF NOT
	// EXISTS against v1 would add a sidecar while leaving the parent v1 CHECK
	// constraints unchanged, blocking the explicit reentrant migration.
	var parent, cursor, sidecar bool
	if err := db.QueryRow(ctx, `SELECT
 to_regclass('warehouse_wiki_quality.ods_event') IS NOT NULL,
 to_regclass('warehouse_wiki_quality.consumer_cursor') IS NOT NULL,
 to_regclass('warehouse_wiki_quality.ods_fact_set') IS NOT NULL`).Scan(
		&parent, &cursor, &sidecar); err != nil {
		return err
	}
	if parent {
		return CheckFactSetSchema(ctx, db) // old/partial schema fails before DDL
	}
	if cursor || sidecar {
		return ErrContract // a partial catalog cannot be repaired by init
	}
	if _, err := db.Exec(ctx, schema); err != nil {
		return err
	}
	return CheckFactSetSchema(ctx, db)
}

// RunOnce stores the complete DC producer prefix and its technical receipts
// in a separate ODS cursor. An unfrozen Wiki quality event stops the whole
// batch before its PG transaction and before DC ACK; it is never a skip.
func (c *Consumer) RunOnce(ctx context.Context) (Result, error) {
	var out Result
	if c == nil || c.DB == nil || c.Source == nil || c.Consumer != DefaultConsumer {
		return out, ErrContract
	}
	factSetEnabled := c.FactSetAuthority != nil || c.FactSetVerifier != nil
	if factSetEnabled {
		if c.FactSetAuthority == nil || c.FactSetVerifier == nil {
			return out, ErrFactSetUnconfigured
		}
		// An existing v1 database must be migrated explicitly before this
		// consumer is allowed to read or ACK any DC source offset.
		if err := CheckFactSetSchema(ctx, c.DB); err != nil {
			return out, fmt.Errorf("verify Wiki FactSet ODS schema before DC read: %w", err)
		}
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
	if err := validateBatchWithFactSet(batch, factSetEnabled); err != nil {
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
		} else if factSetEvent(item.Event.EventType) {
			if !factSetEnabled {
				return out, ErrFactSetUnconfigured
			}
			proof, err := c.FactSetAuthority.ReadFactSetEvent(ctx, item.Event.EventID)
			if err != nil {
				return out, fmt.Errorf("read RTW original FactSet event: %w", err)
			}
			if err := verifyFactSetDC(item.Event, proof, item.InputHash); err != nil {
				return out, err
			}
			catalog, err := ParseFactSetV1(item.Event, proof.EventJSON, proof.FactSetJCSSHA256)
			if err != nil {
				return out, err
			}
			if err := c.FactSetVerifier.VerifyFactSetEvent(ctx, item.Event, proof); err != nil {
				return out, fmt.Errorf("verify RTW FactSet source: %w", err)
			}
			payloadJCS, err := canonical(item.Event.Payload)
			if err != nil || digest(payloadJCS) != proof.FactSetJCSSHA256 {
				return out, ErrContract
			}
			entry.authorityRaw, entry.authoritySHA = proof.EventJSON, proof.EventRawSHA256
			entry.factSet, entry.factSetPayloadJCS, entry.factSetPayloadSHA =
				&catalog, payloadJCS, proof.FactSetJCSSHA256
			out.Catalogs++
		} else {
			out.TechnicalSkips++
		}
		rows = append(rows, entry)
	}
	committed, err := c.commit(ctx, batch, rows, factSetEnabled)
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

func (c *Consumer) commit(ctx context.Context, batch eventing.Batch, rows []row,
	factSetEnabled bool) (int64, error) {
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
		var originalSHA, factSetPayloadSHA *string
		if qualityEvent(r.item.Event.EventType) {
			status, original, originalSHA = "quality_verified", r.authorityRaw, &r.authoritySHA
		} else if r.factSet != nil {
			status, original, originalSHA = "fact_set_verified", r.authorityRaw, &r.authoritySHA
			factSetPayloadSHA = &r.factSetPayloadSHA
		}
		if r.item.Offset <= last {
			var oldID, oldHash, oldStatus string
			var oldSpec, oldReceipt, oldOriginal []byte
			var oldOriginalSHA, oldFactSetPayloadSHA *string
			query := `SELECT event_id,dc_input_hash,status,event_spec,dc_receipt,
 authority_event_json,authority_event_sha256 FROM warehouse_wiki_quality.ods_event
 WHERE producer=$1 AND source_offset=$2`
			fields := []any{&oldID, &oldHash, &oldStatus, &oldSpec, &oldReceipt,
				&oldOriginal, &oldOriginalSHA}
			if factSetEnabled {
				query = `SELECT event_id,dc_input_hash,status,event_spec,dc_receipt,
 authority_event_json,authority_event_sha256,fact_set_payload_jcs_sha256
 FROM warehouse_wiki_quality.ods_event WHERE producer=$1 AND source_offset=$2`
				fields = append(fields, &oldFactSetPayloadSHA)
			}
			if err := tx.QueryRow(ctx, query, Producer, r.item.Offset).Scan(fields...); err != nil ||
				oldID != r.item.Event.EventID || oldHash != r.item.InputHash || oldStatus != status ||
				!bytes.Equal(oldSpec, r.eventSpec) || !bytes.Equal(oldReceipt, r.receipt) ||
				!bytes.Equal(oldOriginal, original) || !equalOptional(oldOriginalSHA, originalSHA) ||
				!equalOptional(oldFactSetPayloadSHA, factSetPayloadSHA) {
				return 0, ErrContract
			}
			if r.factSet != nil {
				var oldSetID, oldRevisionID, oldBaseID, oldWikiID, oldScope, oldPayloadSHA string
				var oldRevision int64
				var oldPayload []byte
				if err := tx.QueryRow(ctx, `SELECT fact_set_id,revision_id,revision,base_revision_id,
 wiki_revision_id,source_scope_revision,payload_jcs,payload_jcs_sha256
 FROM warehouse_wiki_quality.ods_fact_set WHERE producer=$1 AND source_offset=$2`,
					Producer, r.item.Offset).Scan(&oldSetID, &oldRevisionID, &oldRevision,
					&oldBaseID, &oldWikiID, &oldScope, &oldPayload, &oldPayloadSHA); err != nil ||
					oldSetID != r.factSet.FactSetID || oldRevisionID != r.factSet.FactSetRevisionID ||
					oldBaseID != r.factSet.BaseFactSetRevisionID ||
					oldWikiID != r.factSet.WikiRevisionID ||
					oldScope != r.factSet.SourceScopeRevision ||
					oldRevision != factSetRevision(r.factSet) ||
					oldPayloadSHA != r.factSetPayloadSHA ||
					!bytes.Equal(oldPayload, r.factSetPayloadJCS) {
					return 0, ErrContract
				}
			}
			continue
		}
		if r.item.Offset != last+1 {
			return 0, fmt.Errorf("%w: noncontiguous Wiki quality source offset %d", ErrContract, r.item.Offset)
		}
		if r.factSet != nil {
			var priorID, priorScope string
			var priorRevision int64
			err := tx.QueryRow(ctx, `SELECT revision_id,revision,source_scope_revision
 FROM warehouse_wiki_quality.ods_fact_set
 WHERE producer=$1 AND fact_set_id=$2 ORDER BY source_offset DESC LIMIT 1`,
				Producer, r.factSet.FactSetID).Scan(&priorID, &priorRevision, &priorScope)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return 0, err
			}
			if (errors.Is(err, pgx.ErrNoRows) &&
				(r.factSet.BaseFactSetRevisionID != "" || factSetRevision(r.factSet) != 1)) ||
				(err == nil && (r.factSet.BaseFactSetRevisionID != priorID ||
					factSetRevision(r.factSet) != priorRevision+1 ||
					r.factSet.SourceScopeRevision != priorScope)) {
				return 0, fmt.Errorf("%w: FactSet revision chain at source offset %d", ErrContract, r.item.Offset)
			}
		}
		insert := `INSERT INTO warehouse_wiki_quality.ods_event
 (producer,source_offset,event_id,event_type,event_spec,dc_input_hash,dc_receipt,
  dc_received_at,status,authority_event_json,authority_event_sha256)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`
		args := []any{Producer, r.item.Offset, r.item.Event.EventID, r.item.Event.EventType,
			r.eventSpec, r.item.InputHash, r.receipt, r.receivedAt, status, original, originalSHA}
		if factSetEnabled {
			insert = `INSERT INTO warehouse_wiki_quality.ods_event
 (producer,source_offset,event_id,event_type,event_spec,dc_input_hash,dc_receipt,
  dc_received_at,status,authority_event_json,authority_event_sha256,fact_set_payload_jcs_sha256)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`
			args = append(args, factSetPayloadSHA)
		}
		if _, err := tx.Exec(ctx, insert, args...); err != nil {
			return 0, err
		}
		if r.factSet != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO warehouse_wiki_quality.ods_fact_set
 (producer,source_offset,fact_set_id,revision_id,revision,base_revision_id,
  wiki_revision_id,source_scope_revision,payload_jcs,payload_jcs_sha256)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, Producer, r.item.Offset,
				r.factSet.FactSetID, r.factSet.FactSetRevisionID, factSetRevision(r.factSet),
				r.factSet.BaseFactSetRevisionID, r.factSet.WikiRevisionID,
				r.factSet.SourceScopeRevision, r.factSetPayloadJCS, r.factSetPayloadSHA); err != nil {
				return 0, err
			}
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

// ParseFactSetV1 already accepts only canonical positive decimal revisions.
// This projection cannot invent a different ODS revision from the source.
func factSetRevision(value *FactSetV1) int64 {
	if value == nil {
		return 0
	}
	revision, ok := canonicalDecimal(value.FactSetRevision, true)
	if !ok {
		return 0
	}
	return revision
}

func equalOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
