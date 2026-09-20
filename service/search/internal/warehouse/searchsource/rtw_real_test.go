package searchsource

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/jackc/pgx/v5/pgxpool"
)

type realQrelFixture struct {
	RTWURL           string   `json:"rtw_url"`
	RTWToken         string   `json:"rtw_token"`
	DCURL            string   `json:"dc_url"`
	DCToken          string   `json:"dc_token"`
	ExpectedEvents   int      `json:"expected_events"`
	JudgmentEventIDs []string `json:"judgment_event_ids"`
	ResultPath       string   `json:"result_path"`
}

func realQrelLoopback(t *testing.T, address string) {
	t.Helper()
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil ||
		!net.ParseIP(u.Hostname()).IsLoopback() || u.Path != "" || u.RawQuery != "" {
		t.Fatal("real qrel source requires task-owned loopback URLs")
	}
}

// RTW owns actual admin-written, frozen Outbox events. DC owns the shared
// producer and independent technical cursor. BTW owns its ODS transaction.
func TestRTWRealSearchJudgmentSource(t *testing.T) {
	path := os.Getenv("SEA_RTW_REAL_QREL_FIXTURE")
	if path == "" {
		t.Skip("set RTW-owned real search judgment source fixture")
	}
	dsn := os.Getenv("SEARCH_QREL_SOURCE_TEST_DSN")
	if dsn == "" {
		t.Fatal("run through searchsource/acceptance.sh for disposable ODS PostgreSQL")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture realQrelFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	realQrelLoopback(t, fixture.RTWURL)
	realQrelLoopback(t, fixture.DCURL)
	if fixture.RTWToken == "" || fixture.DCToken == "" || fixture.ExpectedEvents < 3 ||
		len(fixture.JudgmentEventIDs) != 2 || fixture.ResultPath == "" {
		t.Fatal("real qrel source fixture is incomplete")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Initialize(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// The package also runs a synthetic cursor test against this disposable PG.
	// The live RTW/DC scenario owns a fresh cursor and must start from offset 1.
	if _, err := pool.Exec(ctx, `TRUNCATE warehouse_search_source.ods_event,
 warehouse_search_source.consumer_cursor`); err != nil {
		t.Fatal(err)
	}
	dc, err := datacenter.New(httpclient.Config{BaseURL: fixture.DCURL, Token: fixture.DCToken,
		HTTPClient: &http.Client{Timeout: 15 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewHTTPAuthority(fixture.RTWURL, fixture.RTWToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range fixture.JudgmentEventIDs {
		frozen, hash, err := authority.ReadJudgmentEvent(ctx, id)
		if err != nil || digest(frozen) != hash {
			t.Fatalf("RTW original judgment event is unavailable: id=%s err=%v", id, err)
		}
	}
	consumer := &Consumer{DB: pool, Source: dc, Authority: authority, Consumer: DefaultConsumer}
	first, err := consumer.RunOnce(ctx)
	if err != nil {
		t.Fatalf("actual DC/RTW qrel source rejected: %v", err)
	}
	if first.Read != fixture.ExpectedEvents || first.Judgments != 2 ||
		first.TechnicalSkips != fixture.ExpectedEvents-2 || first.CommittedOffset != int64(fixture.ExpectedEvents) ||
		first.AcknowledgedOffset != int64(fixture.ExpectedEvents) {
		t.Fatalf("actual shared producer ODS/ACK mismatch: %+v", first)
	}
	var revisions, skipped int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='qrel_revision'),
 count(*) FILTER (WHERE status='technical_skip') FROM warehouse_search_source.ods_event`).Scan(&revisions, &skipped); err != nil ||
		revisions != 2 || skipped != first.TechnicalSkips {
		t.Fatalf("independent ODS rows mismatch: revisions=%d other=%d err=%v", revisions, skipped, err)
	}
	var state string
	var grade *int
	if err := pool.QueryRow(ctx, `SELECT judgment_state,(judgment_payload->>'grade')::int
 FROM warehouse_search_source.ods_event WHERE event_id=$1`, fixture.JudgmentEventIDs[1]).Scan(&state, &grade); err != nil ||
		state != "withdrawn" || grade != nil {
		t.Fatalf("withdrawn judgment entered ODS incorrectly: state=%s grade=%v err=%v", state, grade, err)
	}
	replay, err := consumer.RunOnce(ctx)
	if err != nil || replay.Read != 0 {
		t.Fatalf("DC acknowledged offset replayed or changed ODS: %+v err=%v", replay, err)
	}
	report, err := json.Marshal(struct {
		Committed   int64 `json:"committed_offset"`
		Acked       int64 `json:"acknowledged_offset"`
		Judgments   int   `json:"judgments"`
		OtherEvents int   `json:"technical_skips"`
	}{first.CommittedOffset, first.AcknowledgedOffset, first.Judgments, first.TechnicalSkips})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.ResultPath, report, 0600); err != nil {
		t.Fatal(err)
	}
}
