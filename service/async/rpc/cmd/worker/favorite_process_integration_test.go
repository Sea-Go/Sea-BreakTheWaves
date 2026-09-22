package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func factProcessPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	dsn := os.Getenv("USERMODEL_TEST_POSTGRES_DSN")
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "favorite_process_" + hex.EncodeToString(nonce[:])
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := usermodel.MigratePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	})
	return pool, schema
}

func freeLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

type factWorkerProcess struct {
	command *exec.Cmd
	logFile *os.File
	logPath string
}

func startFactWorkerProcess(t *testing.T, dcURL, metricsAddr, schema string) *factWorkerProcess {
	t.Helper()
	logFile, err := os.CreateTemp(t.TempDir(), "favorite-worker-*.log")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Getenv("BTW_WORKER_BIN"))
	command.Env = append(os.Environ(),
		"BTW_MODE=local", "BTW_JOB_TYPE="+favoriteFactJobType,
		"BTW_DC_URL="+dcURL, "BTW_DC_TOKEN="+os.Getenv("SEA_FACT_DC_TOKEN"),
		"BTW_FAVORITE_AUTHORITY_URL="+os.Getenv("SEA_FACT_AUTHORITY_URL"),
		"BTW_FAVORITE_AUTHORITY_TOKEN="+os.Getenv("SEA_FACT_AUTHORITY_TOKEN"),
		"BTW_FACT_POSTGRES_DSN="+os.Getenv("USERMODEL_TEST_POSTGRES_DSN"),
		"BTW_FACT_SCHEMA="+schema, "BTW_FACT_CONSUMER=btw-favorite-process",
		"BTW_FACT_BATCH_LIMIT=10", "BTW_POLL_INTERVAL=100ms", "BTW_HTTP_TIMEOUT=5s",
		"BTW_OTLP_TRACES_URL="+os.Getenv("SEA_FACT_OTLP_URL"),
		"BTW_METRICS_ADDR="+metricsAddr, "BTW_SERVICE_VERSION="+os.Getenv("BTW_WORKER_VERSION"),
		"BTW_ENVIRONMENT=test", "BTW_INSTANCE_ID=process-test")
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	process := &factWorkerProcess{command: command, logFile: logFile, logPath: logFile.Name()}
	t.Cleanup(func() {
		if process.command.ProcessState == nil {
			_ = process.command.Process.Kill()
			_ = process.command.Wait()
			_ = process.logFile.Close()
		}
	})
	return process
}

func (p *factWorkerProcess) logs(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(p.logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (p *factWorkerProcess) stop(t *testing.T) {
	t.Helper()
	if err := p.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.command.Wait() }()
	select {
	case err := <-done:
		p.logFile.Close()
		if err != nil {
			t.Fatalf("favorite worker exit: %v\n%s", err, p.logs(t))
		}
	case <-time.After(15 * time.Second):
		_ = p.command.Process.Kill()
		<-done
		p.logFile.Close()
		t.Fatalf("favorite worker did not stop after SIGTERM\n%s", p.logs(t))
	}
	if !strings.Contains(p.logs(t), `"event":"usermodel.worker.stopped"`) {
		t.Fatalf("favorite worker missing structured stop log\n%s", p.logs(t))
	}
}

func awaitFactProcess(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("favorite worker did not reach expected process state")
}

func TestFavoriteFactWorkerProcessReal(t *testing.T) {
	for _, key := range []string{"BTW_WORKER_BIN", "BTW_WORKER_VERSION", "USERMODEL_TEST_POSTGRES_DSN",
		"SEA_FACT_DC_URL", "SEA_FACT_DC_TOKEN", "SEA_FACT_AUTHORITY_URL", "SEA_FACT_AUTHORITY_TOKEN",
		"SEA_FACT_ASSERT_EVENT_ID", "SEA_FACT_RETRACT_EVENT_ID"} {
		if os.Getenv(key) == "" {
			t.Skip("run through favorite_acceptance.sh with real RTW/DC and race-built cmd/worker")
		}
	}
	ctx := context.Background()
	pool, schema := factProcessPool(t)
	store := usermodel.NewStore(pool, nil)
	subject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	var tracesMu sync.Mutex
	var traceBodies [][]byte
	otlp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		tracesMu.Lock()
		traceBodies = append(traceBodies, body)
		tracesMu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer otlp.Close()
	t.Setenv("SEA_FACT_OTLP_URL", otlp.URL+"/v1/traces")
	emptySchema := schema + "_empty"
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{emptySchema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{emptySchema}.Sanitize()+" CASCADE")
	unmigrated := startFactWorkerProcess(t, os.Getenv("SEA_FACT_DC_URL"), freeLoopbackAddress(t), emptySchema)
	failed := make(chan error, 1)
	go func() { failed <- unmigrated.command.Wait() }()
	select {
	case err := <-failed:
		unmigrated.logFile.Close()
		if err == nil || !strings.Contains(unmigrated.logs(t), "user fact schema must be migrated before worker start") {
			t.Fatalf("worker auto-migrated or accepted empty schema: err=%v\n%s", err, unmigrated.logs(t))
		}
	case <-time.After(10 * time.Second):
		_ = unmigrated.command.Process.Kill()
		<-failed
		t.Fatal("worker did not reject empty fact schema")
	}
	var createdTable *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, emptySchema+".usermodel_events").Scan(&createdTable); err != nil || createdTable != nil {
		t.Fatalf("worker wrote schema without migration approval: table=%v err=%v", createdTable, err)
	}
	client, err := datacenter.New(httpclient.Config{BaseURL: os.Getenv("SEA_FACT_DC_URL"), Token: os.Getenv("SEA_FACT_DC_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := client.ReadEvents(ctx, "btw-favorite-process", "rtw.community.favorite", 10)
	if err != nil || len(initial.Events) != 2 || initial.Events[0].Event.EventID != os.Getenv("SEA_FACT_ASSERT_EVENT_ID") ||
		initial.Events[1].Event.EventID != os.Getenv("SEA_FACT_RETRACT_EVENT_ID") {
		t.Fatalf("RTW high-ID source not ready for process: %+v %v", initial, err)
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/ack") {
			http.Error(w, "test ACK response unavailable", http.StatusServiceUnavailable)
			return
		}
		upstream, err := http.NewRequestWithContext(r.Context(), r.Method, os.Getenv("SEA_FACT_DC_URL")+r.URL.RequestURI(), r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		upstream.Header = r.Header.Clone()
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(upstream)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	defer proxy.Close()
	firstMetrics := freeLoopbackAddress(t)
	first := startFactWorkerProcess(t, proxy.URL, firstMetrics, schema)
	awaitFactProcess(t, func() bool {
		current, err := store.Current(ctx, subject)
		if err != nil || current.StateVersion != 2 || len(current.Active) != 0 {
			return false
		}
		outbox, err := store.OutboxAfter(ctx, subject, 0, 10)
		logs := first.logs(t)
		return err == nil && len(outbox) == 2 && strings.Contains(logs, `"event":"usermodel.worker.started"`) &&
			strings.Contains(logs, `"event":"usermodel.worker.batch_deferred"`)
	})
	before, err := client.ReadEvents(ctx, "btw-favorite-process", "rtw.community.favorite", 10)
	if err != nil || before.FromOffset != initial.FromOffset || before.ToOffset != initial.ToOffset {
		t.Fatalf("DC cursor advanced through failed ACK: %+v %v", before, err)
	}
	metricsResponse, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + firstMetrics + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	metricsBody, readErr := io.ReadAll(io.LimitReader(metricsResponse.Body, 2<<20))
	metricsResponse.Body.Close()
	if readErr != nil || metricsResponse.StatusCode != http.StatusOK ||
		!bytes.Contains(metricsBody, []byte("sea_btw_operations_total")) ||
		!bytes.Contains(metricsBody, []byte("trpc_agent_go_agent_")) {
		t.Fatalf("favorite worker native/application metrics missing: status=%d err=%v", metricsResponse.StatusCode, readErr)
	}
	first.stop(t)
	if !strings.Contains(first.logs(t), `"error_code":"FACT_DELIVERY_RETRY"`) &&
		!strings.Contains(first.logs(t), `"event":"usermodel.worker.batch_deferred"`) {
		t.Fatalf("failed ACK did not emit bounded structured retry log\n%s", first.logs(t))
	}
	second := startFactWorkerProcess(t, os.Getenv("SEA_FACT_DC_URL"), freeLoopbackAddress(t), schema)
	awaitFactProcess(t, func() bool {
		batch, err := client.ReadEvents(ctx, "btw-favorite-process", "rtw.community.favorite", 10)
		return err == nil && len(batch.Events) == 0 && batch.FromOffset == initial.ToOffset+1
	})
	second.stop(t)
	outbox, err := store.OutboxAfter(ctx, subject, 0, 10)
	if err != nil || len(outbox) != 2 || !strings.Contains(second.logs(t), `"replayed_count":2`) ||
		!strings.Contains(second.logs(t), fmt.Sprintf(`"acknowledged_offset":%d`, initial.ToOffset)) {
		t.Fatalf("restart replay/Outbox/ACK mismatch: outbox=%+v err=%v\n%s", outbox, err, second.logs(t))
	}
	var values []string
	rows, err := pool.Query(ctx, `SELECT event_body->>'value_ref',status,accepted_version,supersedes_event_id FROM usermodel_events
		WHERE producer='rtw.community.favorite' AND event_id=ANY($1) ORDER BY event_id`,
		[]string{os.Getenv("SEA_FACT_ASSERT_EVENT_ID"), os.Getenv("SEA_FACT_RETRACT_EVENT_ID")})
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var value, status string
		var version *int64
		var predecessor *string
		if err := rows.Scan(&value, &status, &version, &predecessor); err != nil {
			t.Fatal(err)
		}
		if status != "accepted" || version == nil || *version != int64(len(values)+1) {
			t.Fatalf("fact lacks sequential Graph/PG acceptance: status=%q version=%v", status, version)
		}
		if len(values) == 0 && predecessor != nil || len(values) == 1 &&
			(predecessor == nil || *predecessor != os.Getenv("SEA_FACT_ASSERT_EVENT_ID")) {
			t.Fatalf("retract supersedes wrong frozen assertion: %v", predecessor)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	expected := "article/article-shared-authority"
	if revision := os.Getenv("SEA_EXPECT_FAVORITE_REVISION"); revision != "" {
		expected += "/revision/" + revision
	}
	if len(values) != 2 || values[0] != expected || values[1] != expected {
		t.Fatalf("frozen favorite revision changed across assert/retract: values=%v expected=%q", values, expected)
	}
	tracesMu.Lock()
	joined := bytes.Join(traceBodies, nil)
	tracesMu.Unlock()
	if !bytes.Contains(joined, []byte("trpc.agent.go")) || !bytes.Contains(joined, []byte("usermodel_fact")) {
		t.Fatal("race-built worker did not export native tRPC-Agent-Go Graph span")
	}
	for _, secret := range []string{os.Getenv("SEA_FACT_DC_TOKEN"), os.Getenv("SEA_FACT_AUTHORITY_TOKEN")} {
		if strings.Contains(first.logs(t), secret) || strings.Contains(second.logs(t), secret) {
			t.Fatal("worker structured logs exposed a service token")
		}
	}
}
