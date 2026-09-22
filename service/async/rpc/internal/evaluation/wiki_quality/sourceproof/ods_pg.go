package sourceproof

import (
	"context"
	"errors"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/warehouse/wikiqualitysource"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGODSReader reads only the independent, append-only BTW Wiki quality ODS.
// A repeatable-read transaction pins its committed cursor and every row from
// one through cutoff in the same snapshot. The DC ACK watermark is separate.
type PGODSReader struct{ DB *pgxpool.Pool }

func (r PGODSReader) ReadPrefix(ctx context.Context, cutoff int64) (ODSSnapshot, error) {
	var out ODSSnapshot
	if ctx == nil || ctx.Err() != nil || r.DB == nil || cutoff < 1 || cutoff > 4096 {
		return out, ErrPrefix
	}
	if err := wikiqualitysource.CheckFactSetSchema(ctx, r.DB); err != nil {
		return out, err
	}
	tx, err := r.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	out.Producer, out.Consumer = Producer, wikiqualitysource.DefaultConsumer
	var producer string
	if err := tx.QueryRow(ctx, `SELECT producer,committed_offset
 FROM warehouse_wiki_quality.consumer_cursor WHERE consumer=$1`, out.Consumer).
		Scan(&producer, &out.CommittedOffset); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ODSSnapshot{}, ErrPrefix
		}
		return ODSSnapshot{}, err
	}
	if producer != Producer || out.CommittedOffset < cutoff {
		return ODSSnapshot{}, ErrPrefix
	}
	rows, err := tx.Query(ctx, `SELECT e.source_offset,e.event_id,e.event_type,
 e.event_spec,e.dc_input_hash,e.dc_receipt,e.status,
 e.authority_event_json,COALESCE(e.authority_event_sha256,''),
 COALESCE(e.fact_set_payload_jcs_sha256,''),s.payload_jcs,
 COALESCE(s.revision_id,''),COALESCE(s.wiki_revision_id,''),
 COALESCE(s.source_scope_revision,'')
 FROM warehouse_wiki_quality.ods_event e
 LEFT JOIN warehouse_wiki_quality.ods_fact_set s
   ON s.producer=e.producer AND s.source_offset=e.source_offset
 WHERE e.producer=$1 AND e.source_offset BETWEEN 1 AND $2
 ORDER BY e.source_offset`, Producer, cutoff)
	if err != nil {
		return ODSSnapshot{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var row ODSRow
		if err := rows.Scan(&row.Offset, &row.EventID, &row.EventType,
			&row.EventSpec, &row.DCInputHash, &row.DCReceipt, &row.Status,
			&row.AuthorityRaw, &row.AuthorityRawSHA256,
			&row.FactSetPayloadJCSSHA256, &row.FactSetPayloadJCS,
			&row.SidecarRevisionID, &row.SidecarWikiRevisionID,
			&row.SidecarSourceScope); err != nil {
			return ODSSnapshot{}, err
		}
		if row.Offset != int64(len(out.Rows))+1 {
			return ODSSnapshot{}, ErrPrefix
		}
		out.Rows = append(out.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return ODSSnapshot{}, err
	}
	if int64(len(out.Rows)) != cutoff {
		return ODSSnapshot{}, ErrPrefix
	}
	return out, tx.Commit(ctx)
}
