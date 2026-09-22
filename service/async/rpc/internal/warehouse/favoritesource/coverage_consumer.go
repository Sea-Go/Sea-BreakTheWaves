package favoritesource

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sync"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed coverage_schema.sql
var coverageSchema string

func InitializeCoverage(ctx context.Context, db *pgxpool.Pool) error {
	if db == nil {
		return ErrContract
	}
	_, err := db.Exec(ctx, coverageSchema)
	return err
}

// CoverageConsumer uses the existing warehouse ODS transaction and DC cursor.
// It records the exact read-window evidence after ODS commit but before DC ACK.
// If recording fails, ACK is withheld and the unchanged DC batch can replay.
type CoverageConsumer struct {
	DB          *pgxpool.Pool
	Source      Source
	Binder      *app.FavoriteAuthorityBinder
	Limit       int
	V2Candidate *SubjectRefV2StorageCandidate
}

func (c *CoverageConsumer) RunOnce(ctx context.Context) (Result, error) {
	if c == nil || c.DB == nil || c.Source == nil || c.Binder == nil || c.Limit < 1 || c.Limit > 128 {
		return Result{}, ErrContract
	}
	adapter := &coverageReadAdapter{source: c.Source, db: c.DB, limit: c.Limit}
	return (&Consumer{DB: c.DB, Source: adapter, Binder: c.Binder, Consumer: DefaultConsumer,
		V2Candidate: c.V2Candidate}).RunOnce(ctx)
}

type coverageReadAdapter struct {
	source Source
	db     *pgxpool.Pool
	limit  int
	mu     sync.Mutex
	batch  eventing.Batch
}

func (a *coverageReadAdapter) ReadEvents(ctx context.Context, consumer, producer string, _ int) (eventing.Batch, error) {
	b, err := a.source.ReadEvents(ctx, consumer, producer, a.limit)
	if err == nil {
		a.mu.Lock()
		a.batch = b
		a.mu.Unlock()
	}
	return b, err
}
func (a *coverageReadAdapter) EventReceipt(ctx context.Context, producer, id string) (eventing.Receipt, error) {
	return a.source.EventReceipt(ctx, producer, id)
}
func (a *coverageReadAdapter) AcknowledgeEvents(ctx context.Context, consumer string, q eventing.Acknowledge) (eventing.DeliveryReceipt, error) {
	a.mu.Lock()
	b := a.batch
	a.mu.Unlock()
	if consumer != DefaultConsumer || q.Producer != Producer || b.Consumer != consumer || b.Producer != q.Producer ||
		b.FromOffset != q.FromOffset || b.ToOffset != q.ToOffset || b.BatchHash != q.BatchHash || len(b.Events) == 0 {
		return eventing.DeliveryReceipt{}, ErrContract
	}
	if err := a.recordBatch(ctx, b); err != nil {
		return eventing.DeliveryReceipt{}, err
	}
	return a.source.AcknowledgeEvents(ctx, consumer, q)
}

func (a *coverageReadAdapter) recordBatch(ctx context.Context, b eventing.Batch) error {
	if err := validateBatch(b); err != nil {
		return err
	}
	var committed int64
	if err := a.db.QueryRow(ctx, `SELECT committed_offset FROM warehouse_favorite.consumer_cursor WHERE consumer=$1 AND producer=$2`, DefaultConsumer, Producer).Scan(&committed); err != nil || committed < b.ToOffset {
		return fmt.Errorf("coverage batch lacks committed warehouse ODS: %w", ErrContract)
	}
	_, err := a.db.Exec(ctx, `INSERT INTO warehouse_favorite.coverage_batch_evidence(consumer,producer,from_offset,to_offset,batch_hash)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, DefaultConsumer, Producer, b.FromOffset, b.ToOffset, b.BatchHash)
	if err != nil {
		return err
	}
	var to int64
	var oldHash string
	err = a.db.QueryRow(ctx, `SELECT to_offset,batch_hash FROM warehouse_favorite.coverage_batch_evidence
		WHERE consumer=$1 AND producer=$2 AND from_offset=$3`, DefaultConsumer, Producer, b.FromOffset).Scan(&to, &oldHash)
	if err != nil || to != b.ToOffset || oldHash != b.BatchHash {
		return errors.Join(ErrContract, err)
	}
	return nil
}
