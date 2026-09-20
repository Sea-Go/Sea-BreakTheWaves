package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/app"
)

type wikiFactoryLifeLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *wikiFactoryLifeLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
func (w *wikiFactoryLifeLog) snapshot() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf.Bytes()...)
}

func wikiFactoryLifeRecords(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var records []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("Factory lifecycle lost structured JSON: %v", err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}

func wikiFactoryLifeRecord(records []map[string]any, event, code, outcome string) bool {
	for _, record := range records {
		if record["event"] == event && (code == "" || record["error_code"] == code) &&
			(outcome == "" || record["outcome"] == outcome) {
			return true
		}
	}
	return false
}

// Each mode is a child process because tRPC InstallGlobals is process-wide.
// Closed/duplicate bind and shutdown errors are exercised at the cmd owner,
// while the separate native-start fixture proves a full official Graph run.
func TestWikiCompileCommandFactoryBindAndCloseLifecycle(t *testing.T) {
	mode := os.Getenv("SEA_WIKI_CMD_FACTORY_LIFECYCLE_MODE")
	if mode == "" {
		for _, mode := range []string{"preclosed", "doublebind", "closefail"} {
			t.Run(mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWikiCompileCommandFactoryBindAndCloseLifecycle$", "-test.v")
				cmd.Env = append(os.Environ(), "SEA_WIKI_CMD_FACTORY_LIFECYCLE_MODE="+mode)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("isolated Wiki Factory lifecycle %s failed: %v\n%s", mode, err, output)
				}
			})
		}
		return
	}
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer collector.Close()
	v := wikiCompileConfigFixture(t)
	cfg, err := loadConfig(func(key string) string { return v[key] })
	if err != nil {
		t.Fatal(err)
	}
	cfg.OTLPTracesURL, cfg.MetricsAddr = collector.URL+"/v1/traces", "127.0.0.1:0"
	deps, jobsClient, runs := wikiGateFixtureDeps(t, cfg)
	var expected error
	switch mode {
	case "preclosed":
		runs.err = app.ErrWikiCompileNativeFactory
	case "doublebind":
		runs.preBound = true
	case "closefail":
		expected = errors.New("fixture request-owned Factory Close failed")
		runs.closeErr = expected
	default:
		t.Fatal("unknown Factory lifecycle mode")
	}
	ctx := context.Background()
	if mode == "closefail" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
	}
	var output wikiFactoryLifeLog
	err = serveWikiCompileWithDeps(ctx, cfg, &output, deps)
	records := wikiFactoryLifeRecords(t, output.snapshot())
	if runs.closes != 1 || runs.opens != 0 || runs.closedAfterBundle {
		t.Fatalf("Factory was not closed before Bundle or opened on a rejected path: mode=%s binds=%d closes=%d opened=%d",
			mode, runs.binds, runs.closes, runs.opens)
	}
	switch mode {
	case "preclosed":
		if !errors.Is(err, errWikiCompileStartup) || runs.binds != 0 || jobsClient.claims != 0 ||
			!wikiFactoryLifeRecord(records, "content.wiki_compile.start_rejected", "SIGNED_DEPENDENCIES_MISSING", "rejected") {
			t.Fatalf("preclosed Factory crossed the initial identity gate: err=%v binds=%d claims=%d", err, runs.binds, jobsClient.claims)
		}
	case "doublebind":
		if !errors.Is(err, app.ErrWikiCompileNativeFactory) || runs.binds != 1 || jobsClient.claims != 0 ||
			!wikiFactoryLifeRecord(records, "content.wiki_compile.start_failed", "NATIVE_FACTORY_BIND_FAILED", "failed") ||
			!wikiFactoryLifeRecord(records, "content.wiki_compile.factory_bind.finished", "NATIVE_FACTORY_BIND_FAILED", "failed") ||
			!wikiFactoryLifeRecord(records, "content.wiki_compile.stopped", "WORKER_STOPPED", "failed") {
			t.Fatalf("duplicate bind reached DC Claim/model or hid failure: err=%v binds=%d claims=%d", err, runs.binds, jobsClient.claims)
		}
	case "closefail":
		if !errors.Is(err, expected) || runs.binds != 1 || jobsClient.claims < 1 ||
			!wikiFactoryLifeRecord(records, "content.wiki_compile.factory_close.finished", "NATIVE_FACTORY_CLOSE_FAILED", "failed") ||
			!wikiFactoryLifeRecord(records, "content.wiki_compile.stopped", "WORKER_STOPPED", "failed") {
			t.Fatalf("Factory close error was lost after poll: err=%v binds=%d claims=%d", err, runs.binds, jobsClient.claims)
		}
	}
}
