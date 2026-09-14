package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/usermodel"
)

var (
	ErrFactDeliveryContract = errors.New("user fact delivery contract mismatch")
	ErrFactDeliveryPending  = errors.New("user fact has no accepted version and Outbox")
	factDeliveryHash        = regexp.MustCompile(`^[a-f0-9]{64}$`)
	factDeliveryToken       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/@-]{0,191}$`)
)

// FactEventSource is the DataCenter H04 read/receipt/ACK contract. The
// generated SDK Client implements it. A read never advances the DC cursor.
type FactEventSource interface {
	ReadEvents(context.Context, string, string, int) (eventing.Batch, error)
	EventReceipt(context.Context, string, string) (eventing.Receipt, error)
	AcknowledgeEvents(context.Context, string, eventing.Acknowledge) (eventing.DeliveryReceipt, error)
}

// TrustedFactBinder is owned by the RTW/WhaleHall source adapter. It must
// resolve the RTW-issued SubjectRef and apply the source EventSpec before
// returning semantic fields. Decoding a client-supplied subject from Payload
// is not a trusted implementation. The worker replaces all transport
// provenance with the immutable DC event receipt.
type TrustedFactBinder interface {
	BindFact(context.Context, eventing.Event) (usermodel.Event, error)
}

type FactGraphCommitter interface {
	Append(context.Context, usermodel.FactGraphRequest) (usermodel.FactGraphReceipt, error)
}

type FactWorkerConfig struct {
	Consumer      string
	Producer      string
	EventType     string
	SchemaVersion int
	BatchLimit    int
}

// FactWorker borrows the one process telemetry Bundle, DataCenter SDK,
// trusted source binder, and a real tRPC-Agent-Go FactGraphRuntime. It does
// not own their shutdown. One call handles at most one fixed-producer batch.
type FactWorker struct {
	config   FactWorkerConfig
	source   FactEventSource
	binder   TrustedFactBinder
	graph    FactGraphCommitter
	observed *telemetry.Bundle
}

func NewFactWorker(cfg FactWorkerConfig, source FactEventSource, binder TrustedFactBinder,
	graph FactGraphCommitter, observed *telemetry.Bundle) (*FactWorker, error) {
	if cfg.Consumer == "" || !factDeliveryToken.MatchString(cfg.Producer) ||
		len("dc:event:")+len(cfg.Producer)+1+20 > 192 || cfg.EventType == "" || cfg.SchemaVersion < 1 ||
		cfg.BatchLimit < 1 || cfg.BatchLimit > 128 || nilDependency(source) || nilDependency(binder) ||
		nilDependency(graph) || observed == nil || !observed.Installed() || observed.Closed() {
		return nil, fmt.Errorf("fact worker dependencies and fixed source contract: %w", ErrFactDeliveryContract)
	}
	return &FactWorker{config: cfg, source: source, binder: binder, graph: graph, observed: observed}, nil
}

type FactDeliveryResult struct {
	Count       int
	NewFacts    int
	Replayed    int
	AckedOffset int64
	Empty       bool
}

// RunOnce only ACKs after every Graph receipt proves a committed accepted
// version (and therefore its same-transaction Outbox). An uncertain ACK or
// later-item failure leaves the DC cursor untouched; the next read replays
// already committed facts by their fixed source event key/hash.
func (w *FactWorker) RunOnce(parent context.Context) (result FactDeliveryResult, resultErr error) {
	if w == nil {
		return result, ErrFactDeliveryContract
	}
	ctx, stage, err := w.observed.Begin(parent, "usermodel", "usermodel.worker.batch",
		slog.String("consumer", w.config.Consumer), slog.String("producer", w.config.Producer))
	if err != nil {
		return result, err
	}
	defer func() {
		outcome, code := factWorkerOutcome(resultErr)
		stage.End(ctx, outcome, code, resultErr, slog.Int("event_count", result.Count),
			slog.Int("new_fact_count", result.NewFacts), slog.Int("replayed_count", result.Replayed),
			slog.Int64("acknowledged_offset", result.AckedOffset))
	}()
	batch, err := w.source.ReadEvents(ctx, w.config.Consumer, w.config.Producer, w.config.BatchLimit)
	if err != nil {
		return result, fmt.Errorf("read DC fact events: %w", err)
	}
	if err := w.validateBatch(batch); err != nil {
		return result, err
	}
	if len(batch.Events) == 0 {
		result.Empty = true
		return result, nil
	}
	for _, item := range batch.Events {
		receipt, err := w.processItem(ctx, item)
		if err != nil {
			return result, err
		}
		result.Count++
		if receipt.Replay {
			result.Replayed++
		} else {
			result.NewFacts++
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	ack, err := w.source.AcknowledgeEvents(ctx, w.config.Consumer, eventing.Acknowledge{
		Producer: batch.Producer, FromOffset: batch.FromOffset, ToOffset: batch.ToOffset, BatchHash: batch.BatchHash})
	if err != nil {
		return result, fmt.Errorf("ACK committed DC fact batch: %w", err)
	}
	if ack.Consumer != batch.Consumer || ack.Producer != batch.Producer ||
		ack.AcknowledgedOffset != batch.ToOffset || ack.TechnicalStatus != "delivered" {
		return result, fmt.Errorf("DC fact ACK receipt: %w", ErrFactDeliveryContract)
	}
	result.AckedOffset = ack.AcknowledgedOffset
	return result, nil
}

func (w *FactWorker) validateBatch(batch eventing.Batch) error {
	if batch.Consumer != w.config.Consumer || batch.Producer != w.config.Producer ||
		batch.FromOffset < 1 || len(batch.Events) > w.config.BatchLimit || !factDeliveryHash.MatchString(batch.BatchHash) ||
		batch.ToOffset != batch.FromOffset+int64(len(batch.Events))-1 {
		return fmt.Errorf("DC fact batch scope or cursor: %w", ErrFactDeliveryContract)
	}
	for index, item := range batch.Events {
		if item.Offset != batch.FromOffset+int64(index) || !factDeliveryHash.MatchString(item.InputHash) ||
			item.Event.Producer != batch.Producer || !factDeliveryToken.MatchString(item.Event.EventID) ||
			item.Event.EventType != w.config.EventType || item.Event.SchemaVersion != w.config.SchemaVersion ||
			item.Event.OperationID == "" || item.Event.AggregateID == "" || item.Event.AggregateVersion < 1 {
			return fmt.Errorf("DC fact event envelope at offset %d: %w", item.Offset, ErrFactDeliveryContract)
		}
		if _, err := time.Parse(time.RFC3339Nano, item.Event.OccurredAt); err != nil {
			return fmt.Errorf("DC fact event time at offset %d: %w", item.Offset, ErrFactDeliveryContract)
		}
	}
	return nil
}

func (w *FactWorker) processItem(parent context.Context, item eventing.Item) (receipt usermodel.FactGraphReceipt, resultErr error) {
	runID := fmt.Sprintf("dc-fact:%s:%d", w.config.Producer, item.Offset)
	ctx, stage, err := w.observed.Begin(parent, "usermodel", "usermodel.worker.event",
		slog.String("operation_id", item.Event.OperationID), slog.String("run_id", runID),
		slog.String("event_id", item.Event.EventID), slog.Int64("source_offset", item.Offset))
	if err != nil {
		return receipt, err
	}
	defer func() {
		outcome, code := factWorkerOutcome(resultErr)
		stage.End(ctx, outcome, code, resultErr, slog.String("domain_status", receipt.Status),
			slog.Int64("accepted_version", receipt.AcceptedVersion), slog.Bool("replay", receipt.Replay))
	}()
	dcReceipt, err := w.source.EventReceipt(ctx, w.config.Producer, item.Event.EventID)
	if err != nil {
		return receipt, fmt.Errorf("read DC immutable event receipt: %w", err)
	}
	if dcReceipt.EventID != item.Event.EventID || dcReceipt.Producer != w.config.Producer ||
		dcReceipt.Offset != item.Offset || dcReceipt.InputHash != item.InputHash ||
		dcReceipt.TechnicalStatus != "accepted" || dcReceipt.ReceiptID == "" {
		return receipt, fmt.Errorf("DC event receipt and batch mismatch: %w", ErrFactDeliveryContract)
	}
	observedAt, err := time.Parse(time.RFC3339Nano, dcReceipt.ReceivedAt)
	if err != nil {
		return receipt, fmt.Errorf("DC event received time: %w", ErrFactDeliveryContract)
	}
	bound, err := w.binder.BindFact(ctx, item.Event)
	if err != nil {
		return receipt, fmt.Errorf("bind RTW-issued fact subject and EventSpec: %w", err)
	}
	occurredAt, _ := time.Parse(time.RFC3339Nano, item.Event.OccurredAt)
	bound.EventKey = usermodel.EventKey{Producer: w.config.Producer, EventID: item.Event.EventID}
	bound.EvidenceRef = fmt.Sprintf("dc:event:%s:%d", w.config.Producer, item.Offset)
	bound.EvidenceHash = item.InputHash
	bound.OccurredAt = occurredAt.UTC()
	bound.ObservedAt = observedAt.UTC()
	bound.SourcePartition = "dc:" + w.config.Producer
	bound.SourceSequence = item.Offset
	receipt, err = w.graph.Append(ctx, usermodel.FactGraphRequest{Event: bound, SessionID: runID, RunID: runID})
	if err != nil {
		return usermodel.FactGraphReceipt{}, fmt.Errorf("commit fact through tRPC Runner/Graph: %w", err)
	}
	if receipt.Subject != bound.Subject || receipt.EventKey != bound.EventKey ||
		receipt.Status != "accepted" || receipt.AcceptedVersion <= 0 ||
		!factDeliveryHash.MatchString(receipt.NormalizedHash) {
		if receipt.Status == "pending_dependency" && receipt.AcceptedVersion == 0 {
			return receipt, ErrFactDeliveryPending
		}
		return usermodel.FactGraphReceipt{}, fmt.Errorf("fact Graph receipt before DC ACK: %w", ErrFactDeliveryContract)
	}
	return receipt, nil
}

func factWorkerOutcome(err error) (string, string) {
	switch {
	case err == nil:
		return "succeeded", ""
	case errors.Is(err, context.Canceled):
		return "cancelled", "CANCELLED"
	case errors.Is(err, context.DeadlineExceeded):
		return "failed", "DEADLINE_EXCEEDED"
	case errors.Is(err, ErrFactDeliveryPending):
		return "rejected", "FACT_PENDING"
	case errors.Is(err, ErrFactDeliveryContract):
		return "rejected", "CONTRACT_MISMATCH"
	case errors.Is(err, usermodel.ErrConflict):
		return "rejected", "FACT_CONFLICT"
	default:
		return "failed", "UPSTREAM_OR_STORE_ERROR"
	}
}
