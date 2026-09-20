package wikiqualitysource

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/jackc/pgx/v5/pgxpool"
)

type realQualityFixture struct {
	RTWURL          string   `json:"rtw_url"`
	RTWToken        string   `json:"rtw_token"`
	DCURL           string   `json:"dc_url"`
	DCToken         string   `json:"dc_token"`
	ExpectedEvents  int      `json:"expected_events"`
	QualityEventIDs []string `json:"quality_event_ids"`
	ResultPath      string   `json:"result_path"`
}

func realLoopback(t *testing.T, address string) {
	t.Helper()
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" ||
		u.RawQuery != "" || u.Fragment != "" || net.ParseIP(u.Hostname()) == nil ||
		!net.ParseIP(u.Hostname()).IsLoopback() || u.Port() == "" {
		t.Fatal("real Wiki quality source requires task-owned loopback service URL")
	}
}

func readPrivateQualityFixture(t *testing.T, path string) realQualityFixture {
	t.Helper()
	var receipt realQualityFixture
	if !filepath.IsAbs(path) {
		t.Fatal("RTW real Wiki quality fixture requires absolute task-owned path")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("RTW real Wiki quality fixture must be a private regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) < 2 || len(raw) > 32<<10 {
		t.Fatal("RTW real Wiki quality fixture unavailable or unbounded")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatal("RTW real Wiki quality fixture has unknown or trailing fields")
	}
	realLoopback(t, receipt.RTWURL)
	realLoopback(t, receipt.DCURL)
	if receipt.RTWToken == "" || receipt.DCToken == "" ||
		receipt.ExpectedEvents < 3 || receipt.ExpectedEvents > 128 ||
		len(receipt.QualityEventIDs) != 2 || receipt.QualityEventIDs[0] == receipt.QualityEventIDs[1] ||
		!eventIDPattern.MatchString(receipt.QualityEventIDs[0]) ||
		!eventIDPattern.MatchString(receipt.QualityEventIDs[1]) ||
		!filepath.IsAbs(receipt.ResultPath) ||
		filepath.Dir(receipt.ResultPath) != filepath.Dir(path) {
		t.Fatal("RTW real Wiki quality source/consumer scope is incomplete")
	}
	return receipt
}

// RTW owns admin-created immutable Wiki fact judgments and its private
// original-event reader. DC owns the actual shared producer position/receipt.
// BTW alone commits/ACKs its independent PG Wiki quality ODS prefix.
func TestRTWRealWikiQualitySource(t *testing.T) {
	path := os.Getenv("SEA_RTW_REAL_WIKI_QUALITY_FIXTURE")
	if path == "" {
		t.Skip("set RTW-owned real Wiki quality source fixture with DC eventing and RTW HTTP")
	}
	dsn := os.Getenv("WIKI_QUALITY_SOURCE_TEST_DSN")
	if dsn == "" {
		t.Fatal("use acceptance.sh for disposable independent BTW Wiki quality PG")
	}
	fixture := readPrivateQualityFixture(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Initialize(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `TRUNCATE warehouse_wiki_quality.ods_fact_set,
 warehouse_wiki_quality.ods_event,
 warehouse_wiki_quality.consumer_cursor`); err != nil {
		t.Fatal(err)
	}
	dc, err := datacenter.New(httpclient.Config{BaseURL: fixture.DCURL, Token: fixture.DCToken,
		HTTPClient: &http.Client{Timeout: 15 * time.Second}})
	if err != nil {
		t.Fatal("DC Wiki quality event client is not configured")
	}
	authority, err := NewHTTPAuthority(fixture.RTWURL, fixture.RTWToken)
	if err != nil {
		t.Fatal("RTW original Wiki quality Event reader is not configured")
	}
	for _, id := range fixture.QualityEventIDs {
		original, sha, err := authority.ReadQualityEvent(ctx, id)
		if err != nil || digest(original) != sha {
			t.Fatalf("RTW original Wiki quality Event unavailable: event=%s err=%v", id, err)
		}
	}
	consumer := &Consumer{DB: db, Source: dc, Authority: authority,
		Verifier: V1Verifier{}, Consumer: DefaultConsumer}
	first, err := consumer.RunOnce(ctx)
	if err != nil || first.Read != fixture.ExpectedEvents || first.QualityVerified != 2 ||
		first.TechnicalSkips != fixture.ExpectedEvents-2 ||
		first.CommittedOffset != int64(fixture.ExpectedEvents) ||
		first.AcknowledgedOffset != int64(fixture.ExpectedEvents) {
		t.Fatalf("RTW/DC real Wiki quality source ODS/ACK differs: %+v %v", first, err)
	}
	var verified, other int
	if err := db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='quality_verified'),
 count(*) FILTER (WHERE status='technical_skip') FROM warehouse_wiki_quality.ods_event`).Scan(
		&verified, &other); err != nil || verified != 2 || other != first.TechnicalSkips {
		t.Fatalf("actual mixed RTW producer quality ODS rows=%d skips=%d %v", verified, other, err)
	}
	for _, id := range fixture.QualityEventIDs {
		var original []byte
		var originalSHA, dcInputHash string
		if err := db.QueryRow(ctx, `SELECT authority_event_json,authority_event_sha256,
 dc_input_hash FROM warehouse_wiki_quality.ods_event
 WHERE producer=$1 AND event_id=$2 AND status='quality_verified'`, Producer, id).Scan(
			&original, &originalSHA, &dcInputHash); err != nil || digest(original) != originalSHA {
			t.Fatalf("actual RTW/DC original/JCS domains missing: event=%s err=%v", id, err)
		}
		canonicalEvent, err := canonical(original)
		var frozen eventing.Event
		if err != nil || digest(canonicalEvent) != dcInputHash ||
			json.Unmarshal(original, &frozen) != nil || frozen.EventID != id {
			t.Fatalf("actual RTW/DC quality Event proof changed: event=%s err=%v", id, err)
		}
		if _, err := ParseJudgmentV1(frozen, original); err != nil {
			t.Fatalf("actual RTW single-fact rubric/locator changed: event=%s err=%v", id, err)
		}
	}
	replay, err := consumer.RunOnce(ctx)
	if err != nil || replay.Read != 0 {
		t.Fatalf("DC acknowledged Wiki quality prefix redelivered: %+v %v", replay, err)
	}
	result, err := json.Marshal(struct {
		Committed       int64    `json:"committed_offset"`
		Acknowledged    int64    `json:"acknowledged_offset"`
		Judgments       int      `json:"judgments"`
		TechnicalSkips  int      `json:"technical_skips"`
		QualityEventIDs []string `json:"quality_event_ids"`
	}{first.CommittedOffset, first.AcknowledgedOffset, first.QualityVerified,
		first.TechnicalSkips, fixture.QualityEventIDs})
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(fixture.ResultPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("create task-owned Wiki quality ODS receipt")
	}
	if _, err := output.Write(result); err != nil {
		output.Close()
		t.Fatal("write Wiki quality ODS receipt")
	}
	if err := output.Close(); err != nil {
		t.Fatal("close Wiki quality ODS receipt")
	}
}
