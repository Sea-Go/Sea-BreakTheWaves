// Package communitysource owns independent warehouse cursors and immutable ODS
// for RTW comment and target-reaction producers. It never writes features,
// samples, recommendation labels, or impression facts.
package communitysource

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind/communityauthority"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	CommentProducer = "rtw.comment-rpc"
	LikeProducer    = "rtw.like-mq"
	CommentConsumer = "btw-warehouse-comment"
	LikeConsumer    = "btw-warehouse-like"
)

var (
	ErrContract = errors.New("warehouse community source contract mismatch")
	digest      = regexp.MustCompile(`^[a-f0-9]{64}$`)
	identifier  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@-]{0,191}$`)
)

type Source interface {
	ReadEvents(context.Context, string, string, int) (eventing.Batch, error)
	EventReceipt(context.Context, string, string) (eventing.Receipt, error)
	AcknowledgeEvents(context.Context, string, eventing.Acknowledge) (eventing.DeliveryReceipt, error)
}

type Authority interface {
	Lookup(context.Context, communityauthority.Evidence) (communityauthority.Fact, error)
}

type Stream struct {
	Producer string
	Consumer string
}

func CommentStream() Stream { return Stream{Producer: CommentProducer, Consumer: CommentConsumer} }
func LikeStream() Stream    { return Stream{Producer: LikeProducer, Consumer: LikeConsumer} }

type Consumer struct {
	DB        *pgxpool.Pool
	Source    Source
	Authority Authority
	Stream    Stream
	Logger    *slog.Logger
	Limit     int
}

//go:embed schema.sql
var schema string

//go:embed 002_batch_window_evidence.sql
var batchWindowMigration string

func Initialize(ctx context.Context, db *pgxpool.Pool) error {
	if db == nil {
		return ErrContract
	}
	if _, err := db.Exec(ctx, schema); err != nil {
		return err
	}
	_, err := db.Exec(ctx, batchWindowMigration)
	return err
}

type Result struct {
	Read               int
	NewRows            int
	ReplayedRows       int
	CommittedOffset    int64
	AcknowledgedOffset int64
}

type sourcePayload struct {
	SchemaVersion    string  `json:"schema_version"`
	EventID          string  `json:"event_id"`
	EventType        string  `json:"event_type"`
	Producer         string  `json:"producer"`
	AggregateID      string  `json:"aggregate_id"`
	AggregateVersion *int64  `json:"aggregate_version"`
	OperationID      string  `json:"operation_id"`
	SubjectRef       string  `json:"subject_ref"`
	OperatorRef      string  `json:"operator_ref,omitempty"`
	TargetType       string  `json:"target_type"`
	TargetID         string  `json:"target_id"`
	TargetRevision   *string `json:"target_revision"`
	RevisionStatus   string  `json:"revision_status"`
	Operation        string  `json:"operation"`
	SourceRef        string  `json:"source_ref"`
	CommentID        string  `json:"comment_id,omitempty"`
	ParentCommentID  string  `json:"parent_comment_id,omitempty"`
	OldState         *int16  `json:"old_state,omitempty"`
	NewState         *int16  `json:"new_state,omitempty"`
	VisibilityState  *int16  `json:"visibility_state,omitempty"`
	SearchEvidence   *bool   `json:"search_evidence,omitempty"`
	EventTime        string  `json:"event_time"`
	OccurredAt       string  `json:"occurred_at"`
	AvailableAt      string  `json:"available_at"`
}

type admittedRow struct {
	Item             eventing.Item
	Receipt          eventing.Receipt
	Issuer           string
	SubjectID        string
	TargetType       string
	TargetID         string
	TargetRevision   *string
	RevisionStatus   string
	Operation        string
	SourceRef        string
	CommentID        *string
	ParentCommentID  *string
	OldState         *int16
	NewState         *int16
	VisibilityState  *int16
	SearchEvidence   *bool
	Predecessor      *string
	OccurredAt       time.Time
	EventTime        time.Time
	AvailableAt      time.Time
	ReceivedAt       time.Time
	EventSpec        []byte
	TechnicalReceipt []byte
}

func (c *Consumer) RunOnce(ctx context.Context) (result Result, resultErr error) {
	started := time.Now()
	if err := c.validate(); err != nil {
		return result, err
	}
	c.Logger.InfoContext(ctx, "community warehouse batch started", "event", "warehouse.community.batch.started",
		"producer", c.Stream.Producer, "consumer", c.Stream.Consumer)
	defer func() {
		outcome, code, level := "succeeded", "", slog.LevelInfo
		if resultErr != nil {
			outcome, code, level = "failed", "WAREHOUSE_SOURCE_FAILED", slog.LevelError
			if errors.Is(resultErr, ErrContract) {
				outcome, code, level = "rejected", "SOURCE_CONTRACT_MISMATCH", slog.LevelWarn
			}
		}
		attrs := []slog.Attr{
			slog.String("event", "warehouse.community.batch.finished"), slog.String("outcome", outcome),
			slog.Float64("duration_ms", float64(time.Since(started).Microseconds())/1000),
			slog.String("producer", c.Stream.Producer),
			slog.String("consumer", c.Stream.Consumer), slog.Int("read", result.Read),
			slog.Int("new_rows", result.NewRows), slog.Int("replayed_rows", result.ReplayedRows),
			slog.Int64("committed_offset", result.CommittedOffset),
			slog.Int64("acknowledged_offset", result.AcknowledgedOffset),
		}
		if resultErr != nil {
			attrs = append(attrs, slog.String("error_code", code), slog.String("error_type", fmt.Sprintf("%T", resultErr)),
				slog.String("error_message", resultErr.Error()))
		}
		c.Logger.LogAttrs(ctx, level, "community warehouse batch finished", attrs...)
	}()
	batch, err := c.Source.ReadEvents(ctx, c.Stream.Consumer, c.Stream.Producer, c.limit())
	if err != nil {
		return result, fmt.Errorf("read DataCenter community batch: %w", err)
	}
	if batch.Consumer != c.Stream.Consumer || batch.Producer != c.Stream.Producer {
		return result, ErrContract
	}
	if len(batch.Events) == 0 {
		return result, nil
	}
	if err := validateBatch(batch, c.Stream, c.limit()); err != nil {
		return result, err
	}
	rows := make([]admittedRow, 0, len(batch.Events))
	for _, item := range batch.Events {
		row, err := c.admit(ctx, item)
		if err != nil {
			return result, err
		}
		rows = append(rows, row)
	}
	newRows, replayed, committed, err := c.commit(ctx, batch, rows)
	if err != nil {
		return result, err
	}
	result.Read, result.NewRows, result.ReplayedRows, result.CommittedOffset = len(rows), newRows, replayed, committed
	ack, err := c.Source.AcknowledgeEvents(ctx, c.Stream.Consumer, eventing.Acknowledge{Producer: batch.Producer,
		FromOffset: batch.FromOffset, ToOffset: batch.ToOffset, BatchHash: batch.BatchHash})
	if err != nil {
		return result, fmt.Errorf("ACK community warehouse cursor after PG commit: %w", err)
	}
	if ack.Consumer != c.Stream.Consumer || ack.Producer != c.Stream.Producer ||
		ack.AcknowledgedOffset != batch.ToOffset || ack.TechnicalStatus != "delivered" {
		return result, ErrContract
	}
	result.AcknowledgedOffset = ack.AcknowledgedOffset
	return result, nil
}

func (c *Consumer) validate() error {
	if c == nil || c.DB == nil || c.Source == nil || c.Authority == nil || c.Logger == nil {
		return ErrContract
	}
	if c.Stream != CommentStream() && c.Stream != LikeStream() {
		return ErrContract
	}
	if c.Limit != 0 && (c.Limit < 1 || c.Limit > 128) {
		return ErrContract
	}
	return nil
}

func (c *Consumer) limit() int {
	if c.Limit == 0 {
		return 128
	}
	return c.Limit
}

func validateBatch(batch eventing.Batch, stream Stream, limit int) error {
	if batch.FromOffset < 1 || len(batch.Events) < 1 || len(batch.Events) > limit ||
		batch.ToOffset != batch.FromOffset+int64(len(batch.Events))-1 || !digest.MatchString(batch.BatchHash) {
		return ErrContract
	}
	for index, item := range batch.Events {
		if item.Offset != batch.FromOffset+int64(index) || item.Event.Producer != stream.Producer ||
			item.Event.SchemaVersion != 1 || !digest.MatchString(item.InputHash) ||
			!identifier.MatchString(item.Event.EventID) || !identifier.MatchString(item.Event.OperationID) ||
			item.Event.AggregateID == "" || item.Event.AggregateVersion < 1 || item.Event.AggregateVersion > 9007199254740991 ||
			!allowedEvent(stream.Producer, item.Event.EventType) {
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

func allowedEvent(producer, eventType string) bool {
	if producer == CommentProducer {
		return eventType == "community.comment.created" || eventType == "community.comment.deleted" ||
			eventType == "community.comment.interaction"
	}
	return producer == LikeProducer && eventType == "community.target.interaction"
}

func (c *Consumer) admit(ctx context.Context, item eventing.Item) (admittedRow, error) {
	receipt, err := c.Source.EventReceipt(ctx, c.Stream.Producer, item.Event.EventID)
	if err != nil {
		return admittedRow{}, fmt.Errorf("read DataCenter community receipt: %w", err)
	}
	if receipt.Offset != item.Offset || receipt.InputHash != item.InputHash || receipt.TechnicalStatus != "accepted" ||
		receipt.ReceiptID == "" || receipt.Producer != c.Stream.Producer || receipt.EventID != item.Event.EventID {
		return admittedRow{}, ErrContract
	}
	fact, err := c.Authority.Lookup(ctx, communityauthority.Evidence{Event: item.Event,
		InputHash: item.InputHash, Offset: item.Offset, Receipt: receipt})
	if err != nil {
		return admittedRow{}, fmt.Errorf("verify RTW community authority: %w", err)
	}
	var payload sourcePayload
	if err := strictJSON(bytes.NewReader(item.Event.Payload), &payload); err != nil {
		return admittedRow{}, ErrContract
	}
	if !explicitNullSourceFields(item.Event.Payload) {
		return admittedRow{}, ErrContract
	}
	row, err := sourceRow(item, receipt, fact, payload)
	if err != nil {
		return admittedRow{}, err
	}
	row.EventSpec, err = json.Marshal(item.Event)
	if err != nil {
		return admittedRow{}, err
	}
	row.TechnicalReceipt, err = json.Marshal(receipt)
	if err != nil {
		return admittedRow{}, err
	}
	canonical, err := jsoncanonicalizer.Transform(row.EventSpec)
	if err != nil {
		return admittedRow{}, ErrContract
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != item.InputHash {
		return admittedRow{}, ErrContract
	}
	return row, nil
}

func sourceRow(item eventing.Item, receipt eventing.Receipt, fact communityauthority.Fact, payload sourcePayload) (admittedRow, error) {
	event := item.Event
	if fact.SubjectRef.Issuer != "rtw.identity" || !positiveID(fact.SubjectRef.SubjectID) ||
		payload.SubjectRef != "rtw.identity/platform/"+fact.SubjectRef.SubjectID ||
		payload.SchemaVersion != "rtw.community-fact.v1" || payload.EventID != event.EventID ||
		payload.EventType != event.EventType || payload.Producer != event.Producer || payload.OperationID != event.OperationID ||
		payload.AggregateVersion != nil || payload.TargetRevision != nil || payload.RevisionStatus != "unknown" ||
		payload.TargetType == "" || payload.TargetID == "" || payload.EventTime != event.OccurredAt || payload.OccurredAt != event.OccurredAt {
		return admittedRow{}, ErrContract
	}
	occurred, err := time.Parse(time.RFC3339Nano, event.OccurredAt)
	if err != nil {
		return admittedRow{}, ErrContract
	}
	eventTime, err := time.Parse(time.RFC3339Nano, payload.EventTime)
	if err != nil {
		return admittedRow{}, ErrContract
	}
	available, err := time.Parse(time.RFC3339Nano, payload.AvailableAt)
	if err != nil {
		return admittedRow{}, ErrContract
	}
	received, err := time.Parse(time.RFC3339Nano, receipt.ReceivedAt)
	if err != nil {
		return admittedRow{}, ErrContract
	}
	row := admittedRow{Item: item, Receipt: receipt, Issuer: fact.SubjectRef.Issuer,
		SubjectID: fact.SubjectRef.SubjectID, TargetType: payload.TargetType, TargetID: payload.TargetID,
		TargetRevision: payload.TargetRevision, RevisionStatus: payload.RevisionStatus, Operation: payload.Operation,
		SourceRef: payload.SourceRef, OldState: payload.OldState, NewState: payload.NewState,
		VisibilityState: payload.VisibilityState, SearchEvidence: payload.SearchEvidence,
		OccurredAt: occurred.UTC(), EventTime: eventTime.UTC(), AvailableAt: available.UTC(), ReceivedAt: received.UTC()}
	if fact.PredecessorEventID != "" {
		if !identifier.MatchString(fact.PredecessorEventID) {
			return admittedRow{}, ErrContract
		}
		value := fact.PredecessorEventID
		row.Predecessor = &value
	}
	if event.Producer == CommentProducer {
		if !positiveID(payload.CommentID) || payload.AggregateID != payload.CommentID || event.AggregateID != payload.CommentID ||
			payload.SearchEvidence == nil || *payload.SearchEvidence || payload.VisibilityState == nil {
			return admittedRow{}, ErrContract
		}
		row.CommentID = optional(payload.CommentID)
		row.ParentCommentID = optional(payload.ParentCommentID)
		if err := validateCommentSemantic(event, payload, row.Predecessor); err != nil {
			return admittedRow{}, err
		}
	} else {
		if payload.CommentID != "" || payload.SearchEvidence != nil || payload.VisibilityState != nil ||
			payload.AggregateID != payload.TargetType+"/"+payload.TargetID ||
			event.AggregateID != "like-state/"+fact.SubjectRef.SubjectID+"/"+payload.TargetType+"/"+payload.TargetID ||
			payload.EventID != "rtw.like."+payload.OperationID || payload.SourceRef != payload.EventID ||
			validateTransition(payload, row.Predecessor) != nil {
			return admittedRow{}, ErrContract
		}
	}
	return row, nil
}

func validateCommentSemantic(event eventing.Event, payload sourcePayload, predecessor *string) error {
	source := "rtw.comment/" + payload.CommentID
	switch event.EventType {
	case "community.comment.created":
		if payload.Operation != "create" || payload.SourceRef != source || event.EventID != "rtw.comment."+payload.CommentID+".created" ||
			payload.OldState != nil || payload.NewState != nil || predecessor != nil {
			return ErrContract
		}
	case "community.comment.deleted":
		expected := "rtw.comment." + payload.CommentID + ".created"
		if payload.Operation != "retract" || payload.SourceRef != source || event.EventID != "rtw.comment."+payload.CommentID+".deleted" ||
			payload.OldState != nil || payload.NewState != nil || predecessor == nil || *predecessor != expected {
			return ErrContract
		}
	case "community.comment.interaction":
		if payload.SourceRef != event.EventID || !strings.HasPrefix(event.EventID, "rtw.comment.interaction.") ||
			validateTransition(payload, predecessor) != nil {
			return ErrContract
		}
	default:
		return ErrContract
	}
	return nil
}

func validateTransition(payload sourcePayload, predecessor *string) error {
	if payload.OldState == nil || payload.NewState == nil || *payload.OldState == *payload.NewState ||
		*payload.OldState < 0 || *payload.OldState > 2 || *payload.NewState < 0 || *payload.NewState > 2 {
		return ErrContract
	}
	valid := map[string]bool{
		"like":      *payload.NewState == 1 && *payload.OldState != 1,
		"unlike":    *payload.OldState == 1 && *payload.NewState == 0,
		"dislike":   *payload.NewState == 2 && *payload.OldState != 2,
		"undislike": *payload.OldState == 2 && *payload.NewState == 0,
	}[payload.Operation]
	if !valid {
		return ErrContract
	}
	if *payload.OldState == 0 {
		if predecessor != nil {
			return ErrContract
		}
		return nil
	}
	if predecessor == nil {
		return ErrContract
	}
	return nil
}

func positiveID(value string) bool {
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func jsonNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func explicitNullSourceFields(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	return json.Unmarshal(raw, &fields) == nil && jsonNull(fields["target_revision"]) && jsonNull(fields["aggregate_version"])
}

func strictJSON(reader io.Reader, value any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrContract
	}
	return nil
}

func (c *Consumer) commit(ctx context.Context, batch eventing.Batch, rows []admittedRow) (int, int, int64, error) {
	tx, err := c.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, 0, 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO warehouse_community.consumer_cursor(consumer,producer) VALUES($1,$2) ON CONFLICT DO NOTHING`,
		c.Stream.Consumer, c.Stream.Producer); err != nil {
		return 0, 0, 0, err
	}
	var last int64
	if err := tx.QueryRow(ctx, `SELECT committed_offset FROM warehouse_community.consumer_cursor
		WHERE consumer=$1 AND producer=$2 FOR UPDATE`, c.Stream.Consumer, c.Stream.Producer).Scan(&last); err != nil {
		return 0, 0, 0, err
	}
	if batch.FromOffset > last+1 {
		return 0, 0, 0, ErrContract
	}
	if err := recordBatchEvidence(ctx, tx, c.Stream, batch); err != nil {
		return 0, 0, 0, err
	}
	newRows, replayed := 0, 0
	for _, row := range rows {
		if row.Item.Offset <= last {
			var eventID, inputHash, receiptID string
			if err := tx.QueryRow(ctx, `SELECT event_id,source_event_hash,receipt_id FROM warehouse_community.ods_event
				WHERE producer=$1 AND source_offset=$2`, c.Stream.Producer, row.Item.Offset).
				Scan(&eventID, &inputHash, &receiptID); err != nil || eventID != row.Item.Event.EventID ||
				inputHash != row.Item.InputHash || receiptID != row.Receipt.ReceiptID {
				return 0, 0, 0, ErrContract
			}
			replayed++
			continue
		}
		if row.Item.Offset != last+1 {
			return 0, 0, 0, ErrContract
		}
		if err := verifyPredecessor(ctx, tx, row); err != nil {
			return 0, 0, 0, err
		}
		_, err := tx.Exec(ctx, `INSERT INTO warehouse_community.ods_event
			(producer,source_offset,event_id,event_type,aggregate_id,aggregate_version,operation_id,occurred_at,
			 event_spec,source_event_hash,technical_receipt,receipt_id,dc_received_at,issuer,subject_id,target_type,
			 target_id,target_revision,revision_status,operation,source_ref,comment_id,parent_comment_id,old_state,new_state,
			 visibility_state,search_evidence,predecessor_event_id,event_time,available_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30)`,
			row.Item.Event.Producer, row.Item.Offset, row.Item.Event.EventID, row.Item.Event.EventType,
			row.Item.Event.AggregateID, row.Item.Event.AggregateVersion, row.Item.Event.OperationID, row.OccurredAt,
			row.EventSpec, row.Item.InputHash, row.TechnicalReceipt, row.Receipt.ReceiptID, row.ReceivedAt,
			row.Issuer, row.SubjectID, row.TargetType, row.TargetID, row.TargetRevision, row.RevisionStatus,
			row.Operation, row.SourceRef, row.CommentID, row.ParentCommentID, row.OldState, row.NewState,
			row.VisibilityState, row.SearchEvidence, row.Predecessor, row.EventTime, row.AvailableAt)
		if err != nil {
			return 0, 0, 0, err
		}
		last++
		newRows++
	}
	if _, err := tx.Exec(ctx, `UPDATE warehouse_community.consumer_cursor SET committed_offset=$3
		WHERE consumer=$1 AND producer=$2`, c.Stream.Consumer, c.Stream.Producer, last); err != nil {
		return 0, 0, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, 0, err
	}
	return newRows, replayed, last, nil
}

func recordBatchEvidence(ctx context.Context, tx pgx.Tx, stream Stream, batch eventing.Batch) error {
	// Every actual DC read window is distinct provenance. A lost ACK may yield
	// another valid window with the same start after the source grows or the
	// configured limit changes. ODS identity and cursor still decide admission.
	if _, err := tx.Exec(ctx, `INSERT INTO warehouse_community.read_batch_evidence
		(consumer,producer,from_offset,to_offset,batch_hash) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
		stream.Consumer, stream.Producer, batch.FromOffset, batch.ToOffset, batch.BatchHash); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM warehouse_community.read_batch_evidence
		WHERE consumer=$1 AND producer=$2 AND from_offset=$3 AND to_offset=$4 AND batch_hash=$5)`,
		stream.Consumer, stream.Producer, batch.FromOffset, batch.ToOffset, batch.BatchHash).Scan(&exists); err != nil || !exists {
		return ErrContract
	}
	return nil
}

func verifyPredecessor(ctx context.Context, tx pgx.Tx, row admittedRow) error {
	if row.Predecessor == nil {
		return nil
	}
	var offset int64
	var issuer, subjectID, targetType, targetID string
	var commentID *string
	if err := tx.QueryRow(ctx, `SELECT source_offset,issuer,subject_id,target_type,target_id,comment_id
		FROM warehouse_community.ods_event WHERE producer=$1 AND event_id=$2`, row.Item.Event.Producer, *row.Predecessor).
		Scan(&offset, &issuer, &subjectID, &targetType, &targetID, &commentID); err != nil || offset >= row.Item.Offset ||
		issuer != row.Issuer || subjectID != row.SubjectID || targetType != row.TargetType || targetID != row.TargetID ||
		!sameOptional(commentID, row.CommentID) {
		return ErrContract
	}
	return nil
}

func sameOptional(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

type ODSRow struct {
	Producer           string  `json:"producer"`
	SourceOffset       int64   `json:"source_offset"`
	EventID            string  `json:"event_id"`
	EventType          string  `json:"event_type"`
	AggregateID        string  `json:"aggregate_id"`
	AggregateVersion   int64   `json:"aggregate_version"`
	OperationID        string  `json:"operation_id"`
	OccurredAt         string  `json:"occurred_at"`
	Issuer             string  `json:"issuer"`
	SubjectID          string  `json:"subject_id"`
	TargetType         string  `json:"target_type"`
	TargetID           string  `json:"target_id"`
	TargetRevision     *string `json:"target_revision"`
	RevisionStatus     string  `json:"revision_status"`
	Operation          string  `json:"operation"`
	SourceRef          string  `json:"source_ref"`
	CommentID          *string `json:"comment_id"`
	ParentCommentID    *string `json:"parent_comment_id"`
	OldState           *int16  `json:"old_state"`
	NewState           *int16  `json:"new_state"`
	VisibilityState    *int16  `json:"visibility_state"`
	SearchEvidence     *bool   `json:"search_evidence"`
	PredecessorEventID *string `json:"predecessor_event_id"`
	EventTime          string  `json:"event_time"`
	AvailableAt        string  `json:"available_at"`
	DCReceivedAt       string  `json:"dc_received_at"`
	SourceEventHash    string  `json:"source_event_hash"`
	TechnicalReceipt   string  `json:"technical_receipt"`
	EventSpec          string  `json:"event_spec"`
}

func ExportODS(ctx context.Context, db *pgxpool.Pool) ([]byte, error) {
	if db == nil {
		return nil, ErrContract
	}
	rows, err := db.Query(ctx, `SELECT producer,source_offset,event_id,event_type,aggregate_id,aggregate_version,
		operation_id,occurred_at,issuer,subject_id,target_type,target_id,target_revision,revision_status,operation,
		source_ref,comment_id,parent_comment_id,old_state,new_state,visibility_state,search_evidence,predecessor_event_id,
		event_time,available_at,dc_received_at,source_event_hash,technical_receipt,event_spec
		FROM warehouse_community.ods_event ORDER BY producer,source_offset`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var output bytes.Buffer
	for rows.Next() {
		var row ODSRow
		var occurredAt, eventTime, availableAt, receivedAt time.Time
		if err := rows.Scan(&row.Producer, &row.SourceOffset, &row.EventID, &row.EventType, &row.AggregateID,
			&row.AggregateVersion, &row.OperationID, &occurredAt, &row.Issuer, &row.SubjectID, &row.TargetType,
			&row.TargetID, &row.TargetRevision, &row.RevisionStatus, &row.Operation, &row.SourceRef, &row.CommentID,
			&row.ParentCommentID, &row.OldState, &row.NewState, &row.VisibilityState, &row.SearchEvidence,
			&row.PredecessorEventID, &eventTime, &availableAt, &receivedAt, &row.SourceEventHash,
			&row.TechnicalReceipt, &row.EventSpec); err != nil {
			return nil, err
		}
		row.OccurredAt, row.EventTime = occurredAt.UTC().Format(time.RFC3339Nano), eventTime.UTC().Format(time.RFC3339Nano)
		row.AvailableAt, row.DCReceivedAt = availableAt.UTC().Format(time.RFC3339Nano), receivedAt.UTC().Format(time.RFC3339Nano)
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		output.Write(encoded)
		output.WriteByte('\n')
	}
	return output.Bytes(), rows.Err()
}
