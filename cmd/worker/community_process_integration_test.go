package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/usermodel"
	"github.com/jackc/pgx/v5/pgxpool"
)

func startCommunityFactWorkerProcess(t *testing.T, jobType, producer, authorityURL, authorityToken,
	consumer, dcURL, metricsAddr, schema string) *factWorkerProcess {
	t.Helper()
	logDir := os.Getenv("SEA_COMMUNITY_EVIDENCE_DIR")
	if logDir == "" {
		logDir = t.TempDir()
	}
	logFile, err := os.CreateTemp(logDir, "community-worker-*.log")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Getenv("BTW_WORKER_BIN"))
	command.Env = append(os.Environ(),
		"BTW_MODE=local", "BTW_JOB_TYPE="+jobType,
		"BTW_DC_URL="+dcURL, "BTW_DC_TOKEN="+os.Getenv("SEA_COMMUNITY_DC_TOKEN"),
		"BTW_COMMUNITY_AUTHORITY_URL="+authorityURL, "BTW_COMMUNITY_AUTHORITY_TOKEN="+authorityToken,
		"BTW_FACT_POSTGRES_DSN="+os.Getenv("USERMODEL_TEST_POSTGRES_DSN"),
		"BTW_FACT_SCHEMA="+schema, "BTW_FACT_CONSUMER="+consumer,
		"BTW_FACT_BATCH_LIMIT=10", "BTW_POLL_INTERVAL=100ms", "BTW_HTTP_TIMEOUT=5s",
		"BTW_OTLP_TRACES_URL="+os.Getenv("SEA_FACT_OTLP_URL"), "BTW_METRICS_ADDR="+metricsAddr,
		"BTW_SERVICE_VERSION="+os.Getenv("BTW_WORKER_VERSION"), "BTW_ENVIRONMENT=test",
		"BTW_INSTANCE_ID=community-"+producer)
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

type communityProcessCase struct {
	name, jobType, producer, authorityURL, authorityToken, consumer     string
	eventIDs, eventTypes                                                []string
	subjectIDs, actions, predicates, valueRefs, itemIDs, predecessorIDs []string
	subjects                                                            []usermodel.SubjectRef
}

func TestCommunityFactWorkerProcessesReal(t *testing.T) {
	required := []string{"BTW_WORKER_BIN", "BTW_WORKER_VERSION", "USERMODEL_TEST_POSTGRES_DSN",
		"SEA_COMMUNITY_DC_URL", "SEA_COMMUNITY_DC_TOKEN", "SEA_COMMENT_AUTHORITY_URL",
		"SEA_COMMENT_AUTHORITY_TOKEN", "SEA_COMMENT_EVENT_IDS", "SEA_LIKE_AUTHORITY_URL",
		"SEA_LIKE_AUTHORITY_TOKEN", "SEA_LIKE_EVENT_IDS"}
	for _, key := range required {
		if os.Getenv(key) == "" {
			t.Skip("run through community_fact_acceptance.sh with real RTW/DC processes")
		}
	}
	ctx := context.Background()
	pool, schema := factProcessPool(t)
	store := usermodel.NewStore(pool, nil)
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
	cases := []communityProcessCase{
		{name: "comment", jobType: commentFactJobType, producer: "rtw.comment-rpc",
			authorityURL: os.Getenv("SEA_COMMENT_AUTHORITY_URL"), authorityToken: os.Getenv("SEA_COMMENT_AUTHORITY_TOKEN"),
			consumer: "btw-comment-community", eventIDs: splitEventIDs(t, os.Getenv("SEA_COMMENT_EVENT_IDS"), 4),
			eventTypes: []string{"community.comment.created", "community.comment.interaction", "community.comment.interaction", "community.comment.deleted"},
			subjectIDs: []string{"1101", "1301", "1301", "1101"}, actions: []string{"assert", "assert", "retract", "retract"},
			predicates: []string{"comment", "comment_like", "comment_like", "comment"},
			valueRefs:  []string{"comment/9101", "comment/9101", "comment/9101", "comment/9101"},
			itemIDs:    []string{"article-community", "article-community", "article-community", "article-community"},
			subjects: []usermodel.SubjectRef{{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1101"},
				{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1301"}}},
		{name: "like", jobType: likeFactJobType, producer: "rtw.like-mq",
			authorityURL: os.Getenv("SEA_LIKE_AUTHORITY_URL"), authorityToken: os.Getenv("SEA_LIKE_AUTHORITY_TOKEN"),
			consumer: "btw-like-community", eventIDs: splitEventIDs(t, os.Getenv("SEA_LIKE_EVENT_IDS"), 2),
			eventTypes: []string{"community.target.interaction", "community.target.interaction"},
			subjectIDs: []string{"1401", "1401"}, actions: []string{"assert", "retract"},
			predicates: []string{"like", "like"}, valueRefs: []string{"article/article-community", "article/article-community"},
			itemIDs:  []string{"article-community", "article-community"},
			subjects: []usermodel.SubjectRef{{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1401"}}},
	}
	cases[0].predecessorIDs = []string{"", "", cases[0].eventIDs[1], cases[0].eventIDs[0]}
	cases[1].predecessorIDs = []string{"", cases[1].eventIDs[0]}
	client, err := datacenter.New(httpclient.Config{BaseURL: os.Getenv("SEA_COMMUNITY_DC_URL"), Token: os.Getenv("SEA_COMMUNITY_DC_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			assertCommunityProcessCase(t, ctx, client, pool, store, schema, test)
		})
	}
	tracesMu.Lock()
	joined := bytes.Join(traceBodies, nil)
	tracesMu.Unlock()
	if !bytes.Contains(joined, []byte("trpc.agent.go")) || !bytes.Contains(joined, []byte("usermodel_fact")) {
		t.Fatal("community workers did not export native tRPC-Agent-Go Graph spans")
	}
}

func assertCommunityProcessCase(t *testing.T, ctx context.Context, client *datacenter.Client,
	pool *pgxpool.Pool, store *usermodel.Store, schema string, test communityProcessCase) {
	t.Helper()
	initial, err := client.ReadEvents(ctx, test.consumer, test.producer, 10)
	if err != nil || initial.FromOffset != 1 || initial.ToOffset != int64(len(test.eventIDs)) || len(initial.Events) != len(test.eventIDs) {
		t.Fatalf("real DC initial batch: %+v %v", initial, err)
	}
	for i, item := range initial.Events {
		if item.Offset != int64(i+1) || item.Event.EventID != test.eventIDs[i] || item.Event.EventType != test.eventTypes[i] {
			t.Fatalf("shared producer order at %d: %+v", i, item)
		}
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/ack") {
			http.Error(w, "test ACK response unavailable", http.StatusServiceUnavailable)
			return
		}
		upstream, err := http.NewRequestWithContext(r.Context(), r.Method, os.Getenv("SEA_COMMUNITY_DC_URL")+r.URL.RequestURI(), r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		upstream.Header = r.Header.Clone()
		response, err := (&http.Client{}).Do(upstream)
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
	first := startCommunityFactWorkerProcess(t, test.jobType, test.producer, test.authorityURL, test.authorityToken,
		test.consumer, proxy.URL, freeLoopbackAddress(t), schema)
	awaitCommunityProcess(t, first, func() bool {
		for _, subject := range test.subjects {
			current, err := store.Current(ctx, subject)
			if err != nil || current.StateVersion != 2 || len(current.Active) != 0 {
				return false
			}
			outbox, err := store.OutboxAfter(ctx, subject, 0, 10)
			if err != nil || len(outbox) != 2 {
				return false
			}
		}
		return strings.Contains(first.logs(t), `"event":"usermodel.worker.batch_deferred"`)
	})
	before, err := client.ReadEvents(ctx, test.consumer, test.producer, 10)
	if err != nil || before.FromOffset != 1 || before.ToOffset != int64(len(test.eventIDs)) {
		t.Fatalf("DC cursor advanced despite lost ACK: %+v %v", before, err)
	}
	first.stop(t)
	second := startCommunityFactWorkerProcess(t, test.jobType, test.producer, test.authorityURL, test.authorityToken,
		test.consumer, os.Getenv("SEA_COMMUNITY_DC_URL"), freeLoopbackAddress(t), schema)
	awaitCommunityProcess(t, second, func() bool {
		batch, err := client.ReadEvents(ctx, test.consumer, test.producer, 10)
		return err == nil && len(batch.Events) == 0 && batch.FromOffset == int64(len(test.eventIDs)+1)
	})
	second.stop(t)
	if !strings.Contains(second.logs(t), fmt.Sprintf(`"replayed_count":%d`, len(test.eventIDs))) ||
		!strings.Contains(second.logs(t), fmt.Sprintf(`"acknowledged_offset":%d`, len(test.eventIDs))) {
		t.Fatalf("restart replay/ACK absent\n%s", second.logs(t))
	}
	for _, secret := range []string{os.Getenv("SEA_COMMUNITY_DC_TOKEN"), test.authorityToken} {
		if strings.Contains(first.logs(t), secret) || strings.Contains(second.logs(t), secret) {
			t.Fatal("community worker logs exposed a service token")
		}
	}
	rows, err := storeEvents(ctx, pool, test.producer)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(test.eventIDs) {
		t.Fatalf("committed facts=%d want=%d", len(rows), len(test.eventIDs))
	}
	for i, row := range rows {
		if row.AuthorityID != "rtw.identity" || row.CompatibilityRealm != "platform" || row.SubjectID != test.subjectIDs[i] ||
			row.EventID != test.eventIDs[i] || row.SourceSequence != int64(i+1) || row.Action != test.actions[i] ||
			row.Predicate != test.predicates[i] || row.ValueRef != test.valueRefs[i] || row.ItemID != test.itemIDs[i] ||
			row.Kind != string(usermodel.ProductAction) || row.ImpressionID != nil || row.Status != "accepted" ||
			row.AcceptedVersion == nil || row.HasRawTargetRevision || row.HasRawRevisionStatus || row.HasRawSearchEvidence {
			t.Fatalf("committed community fact at %d: %+v", i, row)
		}
		if test.predecessorIDs[i] == "" && (row.SupersedesProducer != nil || row.SupersedesEventID != nil) ||
			test.predecessorIDs[i] != "" && (row.SupersedesProducer == nil || *row.SupersedesProducer != test.producer ||
				row.SupersedesEventID == nil || *row.SupersedesEventID != test.predecessorIDs[i]) {
			t.Fatalf("community predecessor at %d: %+v", i, row)
		}
	}
}

func awaitCommunityProcess(t *testing.T, process *factWorkerProcess, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		if err := process.command.Process.Signal(syscall.Signal(0)); err != nil {
			t.Fatalf("community worker exited before expected state: %v\n%s", err, process.logs(t))
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("community worker did not reach expected state\n%s", process.logs(t))
}

type storedCommunityFact struct {
	AuthorityID, CompatibilityRealm, SubjectID   string
	EventID, Action, Predicate, ValueRef, ItemID string
	SourceSequence                               int64
	Kind                                         string
	ImpressionID                                 *string
	SupersedesProducer, SupersedesEventID        *string
	Status                                       string
	AcceptedVersion                              *int64
	HasRawTargetRevision, HasRawRevisionStatus   bool
	HasRawSearchEvidence                         bool
}

func storeEvents(ctx context.Context, pool *pgxpool.Pool, producer string) ([]storedCommunityFact, error) {
	rows, err := pool.Query(ctx, `SELECT authority_id,tenant_id,subject_id,event_id,action,event_body->>'predicate',
		COALESCE(event_body->>'value_ref',''),COALESCE(event_body->>'item_id',''),source_sequence,semantic_kind,
		event_body->>'impression_id',supersedes_producer,supersedes_event_id,status,accepted_version,
		event_body ? 'target_revision',event_body ? 'revision_status',event_body ? 'search_evidence'
		FROM usermodel_events WHERE producer=$1 ORDER BY source_sequence`, producer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []storedCommunityFact{}
	for rows.Next() {
		var row storedCommunityFact
		if err := rows.Scan(&row.AuthorityID, &row.CompatibilityRealm, &row.SubjectID, &row.EventID, &row.Action,
			&row.Predicate, &row.ValueRef, &row.ItemID, &row.SourceSequence, &row.Kind, &row.ImpressionID,
			&row.SupersedesProducer, &row.SupersedesEventID, &row.Status, &row.AcceptedVersion,
			&row.HasRawTargetRevision, &row.HasRawRevisionStatus, &row.HasRawSearchEvidence); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func splitEventIDs(t *testing.T, raw string, count int) []string {
	t.Helper()
	ids := strings.Split(raw, ",")
	if len(ids) != count {
		t.Fatalf("event IDs=%d want=%d", len(ids), count)
	}
	for _, id := range ids {
		if id == "" {
			t.Fatal("empty event ID")
		}
	}
	return ids
}
