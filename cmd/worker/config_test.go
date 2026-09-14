package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func validEnvironment() map[string]string {
	return map[string]string{
		"BTW_MODE": "local", "BTW_ARTIFACT_STORE": "local",
		"BTW_WORKER_ID": "local-worker-1", "BTW_RESOURCE_PROFILE": "cpu",
		"BTW_LEASE_SECONDS": "60", "BTW_POLL_INTERVAL": "200ms", "BTW_HTTP_TIMEOUT": "5s",
		"BTW_DC_URL": "http://127.0.0.1:8081", "BTW_DC_TOKEN": "secret-dc",
		"BTW_RTW_URL": "http://127.0.0.1:8082", "BTW_RTW_TOKEN": "secret-rtw",
		"BTW_CONTENT_POSTGRES_DSN": "postgres://user:secret@127.0.0.1:5432/content",
		"BTW_CONTENT_SCHEMA":       "public", "BTW_CONTENT_MIGRATE": "false",
		"BTW_ARTIFACT_DIR": "/tmp/sea-worker-artifacts", "BTW_CHUNK_PROFILE_ID": "paragraph-v1",
		"BTW_CHUNK_SIZE": "512", "BTW_CHUNK_OVERLAP": "64",
		"BTW_SESSION_POSTGRES_DSN": "postgres://user:secret@127.0.0.1:5432/sessions",
		"BTW_SESSION_SCHEMA":       "public", "BTW_SESSION_TABLE_PREFIX": "content_prepare_",
		"BTW_SESSION_INITIALIZE": "false", "BTW_OTLP_TRACES_URL": "http://127.0.0.1:4318/v1/traces",
		"BTW_METRICS_ADDR": "127.0.0.1:9091", "BTW_SERVICE_VERSION": strings.Repeat("a", 40),
		"BTW_ENVIRONMENT": "local", "BTW_INSTANCE_ID": "local-worker-1",
	}
}

func TestLoadConfigRequiresExplicitLocalAndDDLControls(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
		want             string
	}{
		{"valid", "", "", ""},
		{"storage", "BTW_ARTIFACT_STORE", "s3", "BTW_ARTIFACT_STORE"},
		{"migration", "BTW_CONTENT_MIGRATE", "", "BTW_CONTENT_MIGRATE"},
		{"session initialization", "BTW_SESSION_INITIALIZE", "", "BTW_SESSION_INITIALIZE"},
		{"lease", "BTW_LEASE_SECONDS", "4", "BTW_LEASE_SECONDS"},
		{"schema injection", "BTW_CONTENT_SCHEMA", "public;DROP SCHEMA public", "schema"},
		{"metrics external bind", "BTW_METRICS_ADDR", "0.0.0.0:9091", "loopback"},
		{"trace credentials", "BTW_OTLP_TRACES_URL", "http://user:secret@127.0.0.1:4318/v1/traces", "BTW_OTLP_TRACES_URL"},
		{"commit version", "BTW_SERVICE_VERSION", "latest", "BTW_SERVICE_VERSION"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := validEnvironment()
			if tc.key != "" {
				values[tc.key] = tc.value
			}
			cfg, err := loadConfig(func(key string) string { return values[key] })
			if tc.want == "" {
				if err != nil || cfg.WorkerID != values["BTW_WORKER_ID"] || cfg.ContentMigrate || cfg.SessionInit {
					t.Fatalf("valid config rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unexpected error: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatalf("configuration error exposed credential: %v", err)
			}
		})
	}
}

type pollingStub struct {
	calls atomic.Int64
	err   error
}

func (p *pollingStub) RunOnce(context.Context) (bool, error) {
	p.calls.Add(1)
	return false, p.err
}

func TestPollRetriesTransientFailureAndStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Millisecond)
	defer cancel()
	p := &pollingStub{err: errors.New("temporary provider failure")}
	if err := poll(ctx, p, 20*time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() < 2 {
		t.Fatalf("transient failure ended polling after %d calls", p.calls.Load())
	}
}

func TestPollStopsWhenMetricsServerDies(t *testing.T) {
	server := make(chan error, 1)
	server <- http.ErrServerClosed
	err := poll(context.Background(), &pollingStub{}, time.Hour, server)
	if err == nil || !strings.Contains(err.Error(), "metrics server") {
		t.Fatalf("unexpected metrics error: %v", err)
	}
}

func TestBootstrapFailureKeepsCanonicalJSONAndRedactsDSNPassword(t *testing.T) {
	var output bytes.Buffer
	bootstrapLogger(&output, config{}).Error("configuration rejected", "event", "content.worker.configuration_failed",
		"outcome", "failed", "error_code", "WORKER_CONFIG_INVALID", "error_type", "config", "error_message", "missing field")
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"timestamp", "level", "message", "service", "environment", "service_version", "instance_id", "component", "log_source", "event"} {
		if record[field] == nil {
			t.Fatalf("bootstrap record lacks %s", field)
		}
	}
	cfg := config{ContentDSN: "postgres://user:db-secret@localhost/content", SessionDSN: "postgres://user:session-secret@localhost/session"}
	got := safeError(errors.New("connection failed with db-secret and session-secret"), cfg)
	if strings.Contains(got, "db-secret") || strings.Contains(got, "session-secret") || !strings.Contains(got, "[REDACTED]") {
		t.Fatal("worker error redaction failed")
	}
}
