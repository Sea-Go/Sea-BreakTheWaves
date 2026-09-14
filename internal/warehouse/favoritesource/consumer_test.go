package favoritesource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPositiveDecimal(t *testing.T) {
	for _, raw := range []string{`"9007199254741993"`, `662`} {
		if _, err := positiveDecimal(json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`9007199254741993`, `"01"`, `0`, `-1`, `"x"`, `null`} {
		if _, err := positiveDecimal(json.RawMessage(raw)); !errors.Is(err, ErrContract) {
			t.Fatalf("%s: %v", raw, err)
		}
	}
}

func TestBatchGapAndHash(t *testing.T) {
	batch := eventing.Batch{Consumer: DefaultConsumer, Producer: Producer, FromOffset: 1, ToOffset: 1, Events: []eventing.Item{
		{Offset: 2, InputHash: strings.Repeat("a", 64), Event: eventing.Event{Producer: Producer, EventType: "rtw.favorite.assert", SchemaVersion: 1}},
	}, BatchHash: strings.Repeat("b", 64)}
	if !errors.Is(validateBatch(batch), ErrContract) {
		t.Fatal("offset gap accepted")
	}
	batch.Events[0].Offset = 1
	if !errors.Is(validateBatch(batch), ErrContract) {
		t.Fatal("wrong DC batch hash accepted")
	}
}

type failACK struct {
	*datacenter.Client
	fail bool
}

func (s *failACK) AcknowledgeEvents(ctx context.Context, consumer string, ack eventing.Acknowledge) (eventing.DeliveryReceipt, error) {
	if s.fail {
		s.fail = false
		return eventing.DeliveryReceipt{}, errors.New("isolated ACK transport loss")
	}
	return s.Client.AcknowledgeEvents(ctx, consumer, ack)
}

type alteredBatch struct{ *datacenter.Client }

func (s alteredBatch) ReadEvents(ctx context.Context, consumer, producer string, limit int) (eventing.Batch, error) {
	b, err := s.Client.ReadEvents(ctx, consumer, producer, limit)
	if err == nil && len(b.Events) > 0 {
		b.Events[0].Offset++
	}
	return b, err
}

func TestRealRTWFavoriteWarehouse(t *testing.T) {
	if os.Getenv("SEA_FACT_DC_URL") == "" || os.Getenv("SEA_FACT_AUTHORITY_URL") == "" || os.Getenv("FAVORITE_TEST_DSN") == "" {
		t.Skip("requires isolated RTW/DC/PG fixture")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("FAVORITE_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Initialize(ctx, pool); err != nil {
		t.Fatal(err)
	}
	client, err := datacenter.New(httpclient.Config{BaseURL: os.Getenv("SEA_FACT_DC_URL"), Token: os.Getenv("SEA_FACT_DC_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	binder, err := app.NewFavoriteAuthorityBinder(app.FavoriteAuthorityBinderConfig{BaseURL: os.Getenv("SEA_FACT_AUTHORITY_URL"), Token: os.Getenv("SEA_FACT_AUTHORITY_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	worker := &Consumer{DB: pool, Source: alteredBatch{client}, Binder: binder, Consumer: DefaultConsumer}
	if _, err := worker.RunOnce(ctx); !errors.Is(err, ErrContract) {
		t.Fatalf("gap was accepted: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM warehouse_favorite.ods_event`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("gap wrote ODS: %d %v", count, err)
	}
	worker.Source = &failACK{Client: client, fail: true}
	first, err := worker.RunOnce(ctx)
	if err == nil || first.Read != 2 || first.CommittedOffset != 2 || first.AcknowledgedOffset != 0 {
		t.Fatalf("PG commit and lost ACK: %+v %v", first, err)
	}
	batch, err := client.ReadEvents(ctx, DefaultConsumer, Producer, 10)
	if err != nil || batch.FromOffset != 1 || batch.ToOffset != 2 {
		t.Fatalf("own DC cursor advanced on lost ACK: %+v %v", batch, err)
	}
	second, err := worker.RunOnce(ctx)
	if err != nil || second.Read != 2 || second.CommittedOffset != 2 || second.AcknowledgedOffset != 2 {
		t.Fatalf("replay/ACK: %+v %v", second, err)
	}
	third, err := worker.RunOnce(ctx)
	if err != nil || third.Read != 0 {
		t.Fatalf("ACKed source redelivered: %+v %v", third, err)
	}
	other, err := client.ReadEvents(ctx, "btw-favorite-authority", Producer, 10)
	if err != nil || other.FromOffset != 1 || other.ToOffset != 2 {
		t.Fatalf("warehouse ACK changed graph consumer cursor: %+v %v", other, err)
	}
	var versions []int64
	rows, err := pool.Query(ctx, `SELECT aggregate_version FROM warehouse_favorite.ods_event ORDER BY source_offset`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, version)
	}
	rows.Close()
	if len(versions) != 2 || versions[0] != 1 || versions[1] != 2 {
		t.Fatalf("source versions: %v", versions)
	}
	// A later retract whose asserted predecessor was never admitted cannot
	// silently become a negative label or advance the independent cursor.
	orphan := row{item: eventing.Item{Offset: 3, Event: eventing.Event{EventID: "favorite.999.v2"}},
		operation: "retract", predecessor: "favorite.999.v1"}
	if _, err := worker.commit(ctx, eventing.Batch{FromOffset: 3}, []row{orphan}); !errors.Is(err, ErrContract) {
		t.Fatalf("orphan retract admitted: %v", err)
	}
	var committed int64
	if err := pool.QueryRow(ctx, `SELECT committed_offset FROM warehouse_favorite.consumer_cursor WHERE consumer=$1`, DefaultConsumer).Scan(&committed); err != nil || committed != 2 {
		t.Fatalf("orphan advanced cursor: %d %v", committed, err)
	}
	archive, err := ExportODS(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(archive)), "\n")
	if len(lines) != 2 {
		t.Fatalf("ODS rows=%d", len(lines))
	}
	for i, line := range lines {
		var value map[string]any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatal(err)
		}
		if value["favorite_id"] != "9007199254741993" || value["folder_id"] != "9007199254741991" || value["target_revision"] != "article-shared-authority:r1" || value["subject_id"] != "1001" {
			t.Fatalf("immutable source identity/revision lost: %v", value)
		}
		want := "assert"
		if i == 1 {
			want = "retract"
		}
		if value["operation"] != want {
			t.Fatalf("operation %d: %v", i, value)
		}
	}
	path := os.Getenv("WAREHOUSE_FAVORITE_ODS_OUTPUT")
	if path != "" {
		if err := os.WriteFile(path, archive, 0600); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256(archive)
	t.Logf("isolated RTW/DC/PG ODS rows=2 offsets=1..2 sha256=%s", hex.EncodeToString(sum[:]))
}
