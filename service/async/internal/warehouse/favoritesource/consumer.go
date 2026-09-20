// Package favoritesource owns the warehouse's independent DC favorite cursor
// and immutable ODS. It does not write recommendation exposure or label facts.
package favoritesource

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const Producer = "rtw.community.favorite"
const DefaultConsumer = "btw-warehouse-favorite"

var ErrContract = errors.New("warehouse favorite source contract mismatch")
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Source interface {
	ReadEvents(context.Context, string, string, int) (eventing.Batch, error)
	EventReceipt(context.Context, string, string) (eventing.Receipt, error)
	AcknowledgeEvents(context.Context, string, eventing.Acknowledge) (eventing.DeliveryReceipt, error)
}

type Consumer struct {
	DB          *pgxpool.Pool
	Source      Source
	Binder      *app.FavoriteAuthorityBinder
	Consumer    string
	V2Candidate *SubjectRefV2StorageCandidate
}

//go:embed schema.sql
var schema string

func Initialize(ctx context.Context, db *pgxpool.Pool) error {
	if db == nil {
		return ErrContract
	}
	_, err := db.Exec(ctx, schema)
	return err
}

type Result struct {
	Read               int
	CommittedOffset    int64
	AcknowledgedOffset int64
}

type payload struct {
	FavoriteID     json.RawMessage `json:"favorite_id"`
	FolderID       json.RawMessage `json:"folder_id"`
	TargetType     string          `json:"target_type"`
	TargetID       string          `json:"target_id"`
	TargetRevision *string         `json:"target_revision"`
	Operation      string          `json:"operation"`
	EventTime      string          `json:"event_time"`
	AvailableAt    string          `json:"available_at"`
}

type row struct {
	item                               eventing.Item
	receipt                            eventing.Receipt
	authorityID, tenantID, subjectID   string
	favoriteID, folderID               string
	targetType, targetID               string
	targetRevision                     *string
	operation, predecessor             string
	eventTime, availableAt, receivedAt time.Time
	eventSpec                          []byte
}

func (c *Consumer) RunOnce(ctx context.Context) (Result, error) {
	var result Result
	if c == nil || c.DB == nil || c.Source == nil || c.Binder == nil || c.Consumer != DefaultConsumer {
		return result, ErrContract
	}
	if c.V2Candidate != nil && !c.V2Candidate.readyFor(c.DB) {
		return result, ErrContract
	}
	batch, err := c.Source.ReadEvents(ctx, c.Consumer, Producer, 128)
	if err != nil {
		return result, fmt.Errorf("read DC warehouse cursor: %w", err)
	}
	if batch.Consumer != c.Consumer || batch.Producer != Producer {
		return result, ErrContract
	}
	if len(batch.Events) == 0 {
		return result, nil
	}
	if err := validateBatch(batch); err != nil {
		return result, err
	}
	rows := make([]row, 0, len(batch.Events))
	for _, item := range batch.Events {
		receipt, err := c.Source.EventReceipt(ctx, Producer, item.Event.EventID)
		if err != nil {
			return result, fmt.Errorf("read DC receipt: %w", err)
		}
		if receipt.Offset != item.Offset || receipt.InputHash != item.InputHash || receipt.TechnicalStatus != "accepted" || receipt.ReceiptID == "" || receipt.Producer != Producer || receipt.EventID != item.Event.EventID {
			return result, ErrContract
		}
		fact, err := c.Binder.BindFactWithEvidence(ctx, app.FactSourceEvidence{Event: item.Event, InputHash: item.InputHash, Offset: item.Offset, Receipt: receipt})
		if err != nil {
			return result, fmt.Errorf("verify RTW frozen source: %w", err)
		}
		var body payload
		if err := json.Unmarshal(item.Event.Payload, &body); err != nil {
			return result, ErrContract
		}
		favoriteID, err := positiveDecimal(body.FavoriteID)
		if err != nil || favoriteID != item.Event.AggregateID {
			return result, ErrContract
		}
		folderID, err := positiveDecimal(body.FolderID)
		if err != nil {
			return result, ErrContract
		}
		eventTime, err := time.Parse(time.RFC3339Nano, body.EventTime)
		if err != nil {
			return result, ErrContract
		}
		availableAt, err := time.Parse(time.RFC3339Nano, body.AvailableAt)
		if err != nil {
			return result, ErrContract
		}
		receivedAt, err := time.Parse(time.RFC3339Nano, receipt.ReceivedAt)
		if err != nil {
			return result, ErrContract
		}
		spec, err := json.Marshal(item.Event)
		if err != nil {
			return result, err
		}
		predecessor := ""
		if item.Event.EventType == "rtw.favorite.retract" {
			predecessor = "favorite." + favoriteID + ".v1"
		}
		rows = append(rows, row{item: item, receipt: receipt, authorityID: fact.Subject.AuthorityID,
			tenantID: fact.Subject.TenantID, subjectID: fact.Subject.SubjectID, favoriteID: favoriteID, folderID: folderID,
			targetType: body.TargetType, targetID: body.TargetID, targetRevision: body.TargetRevision,
			operation: body.Operation, predecessor: predecessor, eventTime: eventTime, availableAt: availableAt,
			receivedAt: receivedAt, eventSpec: spec})
	}
	committed, err := c.commit(ctx, batch, rows)
	if err != nil {
		return result, err
	}
	result.Read, result.CommittedOffset = len(rows), committed
	ack, err := c.Source.AcknowledgeEvents(ctx, c.Consumer, eventing.Acknowledge{Producer: Producer,
		FromOffset: batch.FromOffset, ToOffset: batch.ToOffset, BatchHash: batch.BatchHash})
	if err != nil {
		return result, fmt.Errorf("ACK warehouse-only DC cursor after PG commit: %w", err)
	}
	if ack.Consumer != c.Consumer || ack.Producer != Producer || ack.AcknowledgedOffset != batch.ToOffset || ack.TechnicalStatus != "delivered" {
		return result, ErrContract
	}
	result.AcknowledgedOffset = ack.AcknowledgedOffset
	return result, nil
}

func validateBatch(batch eventing.Batch) error {
	if batch.FromOffset < 1 || len(batch.Events) < 1 || len(batch.Events) > 128 ||
		batch.ToOffset != batch.FromOffset+int64(len(batch.Events))-1 || !hashPattern.MatchString(batch.BatchHash) {
		return ErrContract
	}
	for i, item := range batch.Events {
		if item.Offset != batch.FromOffset+int64(i) || item.Event.Producer != Producer ||
			!hashPattern.MatchString(item.InputHash) || (item.Event.EventType != "rtw.favorite.assert" && item.Event.EventType != "rtw.favorite.retract") || item.Event.SchemaVersion != 1 {
			return ErrContract
		}
	}
	raw, err := json.Marshal(batch.Events)
	if err != nil {
		return err
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		return ErrContract
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != batch.BatchHash {
		return ErrContract
	}
	return nil
}

func positiveDecimal(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", ErrContract
	}
	value := string(raw)
	quoted := raw[0] == '"'
	if quoted && json.Unmarshal(raw, &value) != nil {
		return "", ErrContract
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 1 || strconv.FormatInt(n, 10) != value || (!quoted && n > 9007199254740991) {
		return "", ErrContract
	}
	return value, nil
}

func (c *Consumer) commit(ctx context.Context, batch eventing.Batch, rows []row) (committed int64, err error) {
	if c.V2Candidate != nil {
		ctx, stage, beginErr := c.V2Candidate.begin(ctx, "warehouse.favorite.subjectref_v2.ods",
			"source_offset", batch.FromOffset)
		if beginErr != nil {
			return 0, beginErr
		}
		defer func() { c.V2Candidate.end(ctx, stage, err, committed) }()
	}
	tx, err := c.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO warehouse_favorite.consumer_cursor(consumer,producer) VALUES($1,$2) ON CONFLICT DO NOTHING`, c.Consumer, Producer)
	if err != nil {
		return 0, err
	}
	var last int64
	var producer string
	if err := tx.QueryRow(ctx, `SELECT producer,committed_offset FROM warehouse_favorite.consumer_cursor WHERE consumer=$1 FOR UPDATE`, c.Consumer).Scan(&producer, &last); err != nil {
		return 0, err
	}
	if producer != Producer || batch.FromOffset > last+1 {
		return 0, ErrContract
	}
	for _, r := range rows {
		if r.item.Offset <= last {
			var oldHash, oldEventID, aid, tid, sid string
			if err := tx.QueryRow(ctx, `SELECT source_event_hash,event_id,authority_id,tenant_id,subject_id
				FROM warehouse_favorite.ods_event WHERE producer=$1 AND source_offset=$2 FOR SHARE`, Producer, r.item.Offset).
				Scan(&oldHash, &oldEventID, &aid, &tid, &sid); err != nil || oldHash != r.item.InputHash || oldEventID != r.item.Event.EventID {
				return 0, ErrContract
			}
			if c.V2Candidate != nil {
				if aid != r.authorityID || tid != r.tenantID || sid != r.subjectID {
					return 0, ErrContract
				}
				if err = c.V2Candidate.projectODS(ctx, tx, Producer, r.item.Offset, oldEventID, aid, tid, sid); err != nil {
					return 0, err
				}
			}
			continue
		}
		if r.item.Offset != last+1 {
			return 0, ErrContract
		}
		if r.operation == "retract" {
			var aid, tid, sid, targetType, targetID string
			var revision *string
			if err := tx.QueryRow(ctx, `SELECT authority_id,tenant_id,subject_id,target_type,target_id,target_revision FROM warehouse_favorite.ods_event WHERE producer=$1 AND event_id=$2 AND operation='assert'`, Producer, r.predecessor).Scan(&aid, &tid, &sid, &targetType, &targetID, &revision); err != nil ||
				aid != r.authorityID || tid != r.tenantID || sid != r.subjectID || targetType != r.targetType || targetID != r.targetID || !sameRevision(revision, r.targetRevision) {
				return 0, ErrContract
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO warehouse_favorite.ods_event
			(producer,source_offset,event_id,event_type,aggregate_id,aggregate_version,event_spec,source_event_hash,technical_receipt,authority_id,tenant_id,subject_id,favorite_id,folder_id,target_type,target_id,target_revision,operation,predecessor_event_id,event_time,available_at,dc_received_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`,
			Producer, r.item.Offset, r.item.Event.EventID, r.item.Event.EventType, r.item.Event.AggregateID, r.item.Event.AggregateVersion,
			r.eventSpec, r.item.InputHash, mustJSON(r.receipt), r.authorityID, r.tenantID, r.subjectID, r.favoriteID, r.folderID,
			r.targetType, r.targetID, r.targetRevision, r.operation, r.predecessor, r.eventTime, r.availableAt, r.receivedAt)
		if err != nil {
			return 0, err
		}
		if c.V2Candidate != nil {
			if err = c.V2Candidate.projectODS(ctx, tx, Producer, r.item.Offset, r.item.Event.EventID,
				r.authorityID, r.tenantID, r.subjectID); err != nil {
				return 0, err
			}
		}
		last++
	}
	_, err = tx.Exec(ctx, `UPDATE warehouse_favorite.consumer_cursor SET committed_offset=$2 WHERE consumer=$1`, c.Consumer, last)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return last, nil
}

func mustJSON(value any) []byte { raw, _ := json.Marshal(value); return raw }

func sameRevision(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ExportODS freezes the independently admitted event rows for CH/S3. The
// caller owns the output path and source-generation receipt.
func ExportODS(ctx context.Context, db *pgxpool.Pool) ([]byte, error) {
	rows, err := db.Query(ctx, `SELECT producer,source_offset,event_id,event_type,authority_id,tenant_id,subject_id,favorite_id,folder_id,target_type,target_id,target_revision,operation,predecessor_event_id,event_time,available_at,dc_received_at,source_event_hash,technical_receipt,event_spec FROM warehouse_favorite.ods_event ORDER BY producer,source_offset`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out bytes.Buffer
	for rows.Next() {
		var producer, eventID, eventType, aid, tid, sid, fid, folder, targetType, targetID, op, prev, hash string
		var revision *string
		var offset int64
		var eventTime, availableAt, receivedAt time.Time
		var receipt, spec []byte
		if err := rows.Scan(&producer, &offset, &eventID, &eventType, &aid, &tid, &sid, &fid, &folder, &targetType, &targetID, &revision, &op, &prev, &eventTime, &availableAt, &receivedAt, &hash, &receipt, &spec); err != nil {
			return nil, err
		}
		value := map[string]any{"producer": producer, "source_offset": offset, "event_id": eventID, "event_type": eventType,
			"authority_id": aid, "tenant_id": tid, "subject_id": sid, "favorite_id": fid, "folder_id": folder, "target_type": targetType,
			"target_id": targetID, "target_revision": revision, "operation": op, "predecessor_event_id": prev,
			"event_time": eventTime.UTC().Format(time.RFC3339Nano), "available_at": availableAt.UTC().Format(time.RFC3339Nano),
			"dc_received_at": receivedAt.UTC().Format(time.RFC3339Nano), "source_event_hash": hash,
			"technical_receipt": string(receipt), "event_spec": string(spec)}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		out.Write(encoded)
		out.WriteByte('\n')
	}
	return out.Bytes(), rows.Err()
}
