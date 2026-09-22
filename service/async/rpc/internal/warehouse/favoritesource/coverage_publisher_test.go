package favoritesource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/sourcecoverage"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCoveragePublisherRealRTW(t *testing.T) {
	for _, key := range []string{"COVERAGE_REAL_DSN", "COVERAGE_S3_PREFIX", "SEA_FACT_DC_URL", "SEA_FACT_DC_TOKEN",
		"SEA_FACT_AUTHORITY_URL", "SEA_FACT_AUTHORITY_TOKEN"} {
		if os.Getenv(key) == "" {
			t.Skip("run warehouse/coverage/acceptance.sh with isolated RTW/DC/PG/S3")
		}
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("COVERAGE_REAL_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Initialize(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := InitializeCoverage(ctx, pool); err != nil {
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
	source := &failACK{Client: client, fail: true}
	consumer := CoverageConsumer{DB: pool, Source: source, Binder: binder, Limit: 1}
	publisher := CoveragePublisher{DB: pool, Source: client, Binder: binder, S3Prefix: os.Getenv("COVERAGE_S3_PREFIX")}
	first, err := consumer.RunOnce(ctx)
	if err == nil || first.Read != 1 || first.CommittedOffset != 1 || first.AcknowledgedOffset != 0 {
		t.Fatalf("first PG commit + lost ACK: %+v %v", first, err)
	}
	g1, err := publisher.PublishPrefix(ctx, 1, "coverage_real_g1")
	if err != nil || g1.Ref.ThroughOffset != "1" || g1.Ref.BindingPolicyID != CoverageBindingPolicyID {
		t.Fatalf("real W1 after lost ACK: %+v %v", g1, err)
	}
	replay, err := consumer.RunOnce(ctx)
	if err != nil || replay.Read != 1 || replay.CommittedOffset != 1 || replay.AcknowledgedOffset != 1 {
		t.Fatalf("lost ACK replay: %+v %v", replay, err)
	}
	second, err := consumer.RunOnce(ctx)
	if err != nil || second.Read != 1 || second.CommittedOffset != 2 || second.AcknowledgedOffset != 2 {
		t.Fatalf("real retract: %+v %v", second, err)
	}
	g2, err := publisher.PublishPrefix(ctx, 2, "coverage_real_g2")
	if err != nil || g2.Ref.ThroughOffset != "2" || g2.Ref.EventIndexSHA256 == g1.Ref.EventIndexSHA256 {
		t.Fatalf("real W2: %+v %v", g2, err)
	}
	if again, err := publisher.PublishPrefix(ctx, 2, "coverage_real_g2"); err != nil || again.Ref != g2.Ref {
		t.Fatalf("global replay changed root: %+v %v", again, err)
	}
	u1 := sourcecoverage.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	u2 := sourcecoverage.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1002"}
	s1, err := publisher.PublishSubject(ctx, g2.Ref, u1)
	if err != nil || s1.Ref.EventCount != 2 {
		t.Fatalf("real RTW user slice: %+v %v", s1, err)
	}
	s2, err := publisher.PublishSubject(ctx, g2.Ref, u2)
	if err != nil || s2.Ref.EventCount != 0 || s2.Ref.SparseIndexSHA256 != coverageHash(nil) {
		t.Fatalf("proved empty subject: %+v %v", s2, err)
	}
	if again, err := publisher.PublishSubject(ctx, g2.Ref, u1); err != nil || again.Ref != s1.Ref {
		t.Fatalf("subject replay changed receipt: %+v %v", again, err)
	}
	other, err := client.ReadEvents(ctx, "btw-favorite-authority", Producer, 10)
	if err != nil || other.FromOffset != 1 || other.ToOffset != 2 {
		t.Fatalf("warehouse ACK moved Graph consumer: %+v %v", other, err)
	}
	if path := os.Getenv("COVERAGE_REAL_ODS_OUTPUT"); path != "" {
		archive, err := ExportODS(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, archive, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// A later malformed source cannot be called a complete prefix.
	if _, err := pool.Exec(ctx, `DELETE FROM warehouse_favorite.ods_event WHERE producer=$1 AND source_offset=2`, Producer); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.PublishPrefix(ctx, 2, "coverage_real_missing"); !errors.Is(err, ErrCoveragePending) {
		t.Fatalf("missing ODS offset published: %v", err)
	}
	// The already-published S3 bytes remain immutable and independently readable.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, g2.Ref.EventIndexURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal(response.Status)
	}
	var indexed []sourcecoverage.EventIndexRow
	decoder := json.NewDecoder(response.Body)
	for {
		var row sourcecoverage.EventIndexRow
		err := decoder.Decode(&row)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		indexed = append(indexed, row)
	}
	if len(indexed) != 2 || indexed[0].Offset != "1" || indexed[1].Offset != "2" {
		t.Fatalf("frozen source changed: %+v", indexed)
	}
	if path := os.Getenv("COVERAGE_REAL_REPORT"); path != "" {
		body, _ := json.Marshal(struct {
			G1, G2 sourcecoverage.GlobalPrefixRef
			U1, U2 sourcecoverage.SubjectCoverageRef
		}{g1.Ref, g2.Ref, s1.Ref, s2.Ref})
		if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("real RTW/DC coverage W1=%s W2=%s u1=%s empty=%s", g1.Ref.EventIndexSHA256, g2.Ref.EventIndexSHA256, s1.Ref.ReceiptSHA256, s2.Ref.ReceiptSHA256)
}
