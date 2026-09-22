package favoritesource

import (
	"context"
	_ "embed"
	"errors"
	"log/slog"
	"regexp"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"github.com/jackc/pgx/v5"
)

//go:embed migrate_subjectref_v2_storage.sql
var favoriteV2MigrationBody string

var favoriteV2LocalNonce = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (p *SubjectRefV2Preflight) markedLocalTarget(ctx context.Context) bool {
	if p == nil || p.DB == nil || !favoriteV2LocalNonce.MatchString(p.LocalTestNonce) {
		return false
	}
	var database, user, server, client string
	var version int
	var marked bool
	err := p.DB.QueryRow(ctx, `SELECT current_database(),current_user,host(inet_server_addr()),
	 host(inet_client_addr()),current_setting('server_version_num')::integer,
	 EXISTS (SELECT 1 FROM public.favorite_subjectref_v2_local_test_gate
	  WHERE nonce=$1 AND created_at BETWEEN clock_timestamp()-interval '1 hour' AND clock_timestamp())`,
		p.LocalTestNonce).Scan(&database, &user, &server, &client, &version, &marked)
	return err == nil && marked && strings.HasPrefix(database, "favorite_v2_") &&
		user == "sea_btw_favorite_test" && server == "127.0.0.1" &&
		client == "127.0.0.1" && version >= 160000 && version < 170000
}

// ApplySubjectRefV2StorageCandidate is the only supported migration entry.
// It shares one locked repeatable-read transaction with the existing complete
// ODS/coverage preflight and the additive SQL body. The public SQL file alone
// is only a structural probe with a synthetic transaction marker.
func (p *SubjectRefV2Preflight) ApplySubjectRefV2StorageCandidate(ctx context.Context,
	observed *telemetry.Bundle) (report SubjectRefV2Report, err error) {
	if p == nil || p.DB == nil || observed == nil || observed.Closed() {
		return report, ErrContract
	}
	// This cut only admits the fresh local PG16 DB marked by the independent
	// test owner. Production needs a separate online expand/watermark contract.
	if !p.markedLocalTarget(ctx) {
		return report, ErrContract
	}
	ctx, stage, beginErr := observed.Begin(ctx, "warehouse", "warehouse.favorite.subjectref_v2.apply",
		slog.String("producer", Producer))
	if beginErr != nil {
		return report, beginErr
	}
	defer func() {
		if err == nil {
			stage.End(ctx, "succeeded", "", nil,
				slog.Int("ods_rows", report.ODSRows), slog.Int("subject_receipts", report.SubjectReceipts))
			return
		}
		outcome := "rejected"
		if errors.Is(err, context.Canceled) {
			outcome = "cancelled"
		}
		stage.End(ctx, outcome, "SUBJECTREF_V2_PREFLIGHT_REJECTED", ErrContract,
			slog.Int("finding_count", len(report.Findings)))
	}()
	tx, err := p.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return report, err
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(ctx, `SET LOCAL lock_timeout='10s'; SET LOCAL statement_timeout='5min';
	 LOCK TABLE warehouse_favorite.consumer_cursor,warehouse_favorite.ods_event,
	 warehouse_favorite.coverage_batch_evidence,warehouse_favorite.coverage_publication,
	 warehouse_favorite.coverage_subject_receipt IN SHARE ROW EXCLUSIVE MODE`, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		return report, err
	}
	snapshot, err := p.readSnapshotInTx(ctx, tx)
	if err != nil {
		return report, err
	}
	report = p.assessSnapshot(ctx, snapshot)
	if !report.Clear() || !hashPattern.MatchString(report.SnapshotSHA256) {
		return report, ErrContract
	}
	_, err = tx.Exec(ctx, `SELECT set_config('warehouse_favorite.subjectref_v2_locked_preflight',$1,true)`,
		report.SnapshotSHA256)
	if err != nil {
		return report, err
	}
	_, err = tx.Exec(ctx, favoriteV2MigrationBody, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		return report, favoriteV2ProjectionError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return report, err
	}
	return report, nil
}
