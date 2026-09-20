package wikiqualitysource

import (
	"context"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
)

type fixedV1QualityAuthority struct {
	eventID string
	raw     []byte
}

func (a fixedV1QualityAuthority) ReadQualityEvent(_ context.Context, eventID string) ([]byte, string, error) {
	if eventID != a.eventID {
		return nil, "", ErrContract
	}
	return a.raw, digest(a.raw), nil
}

// A default-off Catalog cannot make the old verified single-Fact consumer
// stop serving an unmigrated, complete v1 ODS. Upgrade is a distinct command.
func TestDisabledFactSetConsumerStillCommitsOnUnmigratedV1PG(t *testing.T) {
	ctx, db := v2DisposablePool(t)
	v2InstallOriginalV1(t, ctx, db)
	if err := Initialize(ctx, db); err == nil {
		t.Fatal("fresh v2 schema initializer modified the old v1 source catalog")
	}
	batch, receipts, _ := testBatch(t)
	quality, original := v1Golden(t)
	oldID := batch.Events[1].Event.EventID
	batch.Events[1].Event = quality
	encoded, err := canonical(jsonEvent(t, quality))
	if err != nil {
		t.Fatal(err)
	}
	batch.Events[1].InputHash = digest(encoded)
	delete(receipts, oldID)
	receipts[quality.EventID] = eventing.Receipt{EventID: quality.EventID, Producer: Producer,
		TechnicalStatus: "accepted", ReceiptID: "receipt-" + quality.EventID,
		InputHash: batch.Events[1].InputHash, Offset: 2, ReceivedAt: "2026-09-16T00:05:00Z"}
	resetBatchHash(t, &batch)
	source := &testSource{batch: batch, receipts: receipts}
	consumer := &Consumer{DB: db, Source: source, Authority: fixedV1QualityAuthority{
		eventID: quality.EventID, raw: original}, Verifier: V1Verifier{}, Consumer: DefaultConsumer}
	first, err := consumer.RunOnce(ctx)
	if err != nil || first.Read != 3 || first.QualityVerified != 1 ||
		first.TechnicalSkips != 2 || first.Catalogs != 0 ||
		first.CommittedOffset != 3 || first.AcknowledgedOffset != 3 {
		t.Fatalf("default-off Catalog broke old verified v1 ODS SQL/ACK: %+v %v", first, err)
	}
	var cursor, qualityCount, skipCount int
	if err := db.QueryRow(ctx, `SELECT committed_offset FROM warehouse_wiki_quality.consumer_cursor
 WHERE consumer=$1`, DefaultConsumer).Scan(&cursor); err != nil || cursor != 3 {
		t.Fatalf("old v1 continuous cursor not advanced after PG commit: %d %v", cursor, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='quality_verified'),
 count(*) FILTER (WHERE status='technical_skip') FROM warehouse_wiki_quality.ods_event`).Scan(
		&qualityCount, &skipCount); err != nil || qualityCount != 1 || skipCount != 2 {
		t.Fatalf("old v1 immutable quality/technical evidence missing: %d/%d %v", qualityCount, skipCount, err)
	}
	var sidecarExists bool
	if err := db.QueryRow(ctx, `SELECT to_regclass('warehouse_wiki_quality.ods_fact_set') IS NOT NULL`).Scan(
		&sidecarExists); err != nil || sidecarExists {
		t.Fatalf("fresh initializer constructed half-v2 sidecar before migration: %t %v", sidecarExists, err)
	}
	if err := CheckFactSetSchema(ctx, db); err == nil {
		t.Fatal("old v1 ODS accepted a FactSet-enabled reader before explicit migration")
	}
	if err := ApplyFactSetMigration(ctx, db); err != nil {
		t.Fatalf("explicit upgrade after old verified v1 producer was blocked: %v", err)
	}
	if err := CheckFactSetSchema(ctx, db); err != nil {
		t.Fatalf("FactSet v2 probe rejected fully migrated old v1 source: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT committed_offset FROM warehouse_wiki_quality.consumer_cursor
 WHERE consumer=$1`, DefaultConsumer).Scan(&cursor); err != nil || cursor != 3 {
		t.Fatalf("v1→v2 migration fabricated a DC ACK or lost prefix: %d %v", cursor, err)
	}
	replay, err := consumer.RunOnce(ctx)
	if err != nil || replay.Read != 0 {
		t.Fatalf("ACKed default-off old v1 source redelivered after explicit v2 migration: %+v %v", replay, err)
	}
}
