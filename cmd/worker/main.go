// Command worker runs one explicitly selected local content job or user fact
// consumer through tRPC-Agent-Go. Each mode uses a separate process.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
	btwRuntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	contentmigration "github.com/Sea-Go/Sea-BreakTheWaves/migrations/content"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"trpc.group/trpc-go/trpc-agent-go/agent"
)

func main() { os.Exit(run()) }

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		event := "content.worker.configuration_failed"
		if isFactJobType(cfg.JobType) {
			event = "usermodel.worker.configuration_failed"
		}
		bootstrapLogger(os.Stderr, cfg).ErrorContext(ctx, "worker configuration rejected",
			"event", event, "outcome", "failed", "error_code", "WORKER_CONFIG_INVALID",
			"error_type", "config", "error_message", err.Error())
		return 2
	}
	serveMode := serve
	if isFactJobType(cfg.JobType) {
		serveMode = serveFactConsumer
	}
	if err := serveMode(ctx, cfg, os.Stderr); err != nil {
		return 1
	}
	return 0
}

func bootstrapLogger(output io.Writer, cfg config) *slog.Logger {
	known := func(value string) string {
		if value == "" {
			return "unresolved"
		}
		return value
	}
	handler := slog.NewJSONHandler(output, &slog.HandlerOptions{Level: slog.LevelInfo, ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
		switch attr.Key {
		case slog.TimeKey:
			return slog.Time("timestamp", attr.Value.Time().UTC())
		case slog.LevelKey:
			return slog.String("level", strings.ToLower(attr.Value.String()))
		case slog.MessageKey:
			return slog.String("message", attr.Value.String())
		}
		return attr
	}})
	component := "content"
	if isFactJobType(cfg.JobType) {
		component = "usermodel"
	}
	return slog.New(handler).With("service", workerService(cfg), "environment", known(cfg.Environment),
		"service_version", known(cfg.Version), "instance_id", known(cfg.InstanceID),
		"component", component, "log_source", "application")
}

func workerService(cfg config) string {
	switch cfg.JobType {
	case favoriteFactJobType:
		return "sea-btw-favorite-fact-worker"
	case commentFactJobType:
		return "sea-btw-comment-fact-worker"
	case likeFactJobType:
		return "sea-btw-like-fact-worker"
	}
	if cfg.JobType == app.IndexJobType {
		return "sea-btw-index-worker"
	}
	return "sea-btw-prepare-worker"
}

func serve(ctx context.Context, cfg config, output io.Writer) (resultErr error) {
	bundle, err := telemetry.New(ctx, telemetry.Config{Service: workerService(cfg), Environment: cfg.Environment,
		Version: cfg.Version, InstanceID: cfg.InstanceID, Output: output, Level: slog.LevelInfo,
		OTLPEndpoint: cfg.OTLPTracesURL, SampleRatio: 1})
	if err != nil {
		bootstrapLogger(output, cfg).ErrorContext(ctx, "worker telemetry initialization failed",
			"event", "content.worker.start_failed", "outcome", "failed", "error_code", "TELEMETRY_INIT_FAILED",
			"error_type", fmt.Sprintf("%T", err), "error_message", err.Error())
		return err
	}
	logger, err := bundle.Logger("content", "application")
	if err != nil {
		_ = bundle.Close(context.Background())
		return err
	}
	var runner *btwRuntime.Runtime
	var pool *pgxpool.Pool
	var metrics *http.Server
	var dcTransport, rtwTransport *http.Transport
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if metrics != nil {
			if err := metrics.Shutdown(shutdownCtx); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("stop metrics server: %w", err))
				_ = metrics.Close()
			}
		}
		if runner != nil {
			resultErr = errors.Join(resultErr, runner.Close()) // also closes the owned framework Postgres Session
		}
		if dcTransport != nil {
			dcTransport.CloseIdleConnections()
		}
		if rtwTransport != nil {
			rtwTransport.CloseIdleConnections()
		}
		if pool != nil {
			pool.Close()
		}
		resultErr = errors.Join(resultErr, bundle.Close(shutdownCtx))
		if resultErr != nil {
			logger.ErrorContext(context.Background(), "worker stopped with error", "event", "content.worker.stopped",
				"outcome", "failed", "error_code", "WORKER_STOPPED", "error_type", fmt.Sprintf("%T", resultErr),
				"error_message", safeError(resultErr, cfg))
		} else {
			logger.InfoContext(context.Background(), "worker stopped", "event", "content.worker.stopped", "outcome", "succeeded")
		}
	}()
	if err := bundle.InstallGlobals(); err != nil {
		return fmt.Errorf("install framework telemetry: %w", err)
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return errors.New("default HTTP transport does not support isolated provider clients")
	}
	dcTransport, rtwTransport = base.Clone(), base.Clone()
	client := &http.Client{Timeout: cfg.HTTPTimeout}
	dcClient := *client
	dcClient.Transport = clientTransport("datacenter", dcTransport)
	rtwClient := *client
	rtwClient.Transport = clientTransport("ridethewind", rtwTransport)
	dc, err := datacenter.New(httpclient.Config{BaseURL: cfg.DCURL, Token: cfg.DCToken, HTTPClient: &dcClient})
	if err != nil {
		return fmt.Errorf("construct DataCenter client: %w", err)
	}
	rtw, err := ridethewind.New(httpclient.Config{BaseURL: cfg.RTWURL, Token: cfg.RTWToken, HTTPClient: &rtwClient})
	if err != nil {
		return fmt.Errorf("construct RideTheWind client: %w", err)
	}
	pgCfg, err := pgxpool.ParseConfig(cfg.ContentDSN)
	if err != nil {
		return errors.New("invalid content Postgres DSN") // never write credentials to logs
	}
	if pgCfg.ConnConfig.RuntimeParams == nil {
		pgCfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	pgCfg.ConnConfig.RuntimeParams["search_path"] = cfg.ContentSchema
	pool, err = pgxpool.NewWithConfig(ctx, pgCfg)
	if err != nil {
		return fmt.Errorf("open content Postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping content Postgres: %w", err)
	}
	if cfg.ContentMigrate {
		if _, err := pool.Exec(ctx, contentmigration.SQL); err != nil {
			return fmt.Errorf("apply content schema migration: %w", err)
		}
		logger.InfoContext(ctx, "content schema migration applied", "event", "content.worker.migration_applied", "outcome", "succeeded")
	}
	objects, err := artifacts.NewLocal(cfg.ArtifactDir)
	if err != nil {
		return fmt.Errorf("open local artifact directory: %w", err)
	}
	store := content.NewStore(pool)
	var graph agent.Agent
	if cfg.JobType == app.IndexJobType {
		graph, err = newIndexGraph(cfg.IndexSettings, dc, rtw, objects, store, bundle)
		if err != nil {
			return fmt.Errorf("construct framework index graph: %w", err)
		}
	} else {
		var chunker *content.Chunker
		chunker, err = content.NewChunker(content.ChunkConfig{ID: cfg.ChunkProfile, Size: cfg.ChunkSize, Overlap: cfg.ChunkOverlap})
		if err != nil {
			return fmt.Errorf("construct chunker: %w", err)
		}
		var preparer *content.Preparer
		preparer, err = content.NewPreparer(rtw, objects, store, chunker, bundle)
		if err != nil {
			return fmt.Errorf("construct content preparer: %w", err)
		}
		graph, err = content.NewPrepareGraphAgent(preparer)
		if err != nil {
			return fmt.Errorf("construct framework prepare graph: %w", err)
		}
	}
	appName := "sea-btw-content-prepare"
	if cfg.JobType == app.IndexJobType {
		appName = "sea-btw-content-index"
	}
	runner, err = btwRuntime.OpenPostgres(appName, graph, btwRuntime.PostgresConfig{DSN: cfg.SessionDSN,
		Schema: cfg.SessionSchema, TablePrefix: cfg.SessionPrefix, Initialize: cfg.SessionInit}, bundle)
	if err != nil {
		return fmt.Errorf("open framework Postgres Runner: %w", err)
	}
	var worker poller
	if cfg.JobType == app.IndexJobType {
		worker, err = app.NewIndexWorker(app.IndexWorkerConfig{WorkerID: cfg.WorkerID,
			ResourceProfile: cfg.Resource, LeaseSeconds: cfg.LeaseSeconds}, dc, rtw, runner, store, objects, bundle)
	} else {
		worker, err = app.NewPrepareWorker(app.PrepareWorkerConfig{WorkerID: cfg.WorkerID,
			ResourceProfile: cfg.Resource, LeaseSeconds: cfg.LeaseSeconds}, dc, rtw, runner, store, objects, bundle)
	}
	if err != nil {
		return fmt.Errorf("construct %s worker: %w", cfg.JobType, err)
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", bundle.MetricsHandler())
	metrics = &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second,
		ErrorLog: slog.NewLogLogger(logger.With("event", "content.worker.metrics_server").Handler(), slog.LevelError)}
	listener, err := net.Listen("tcp", cfg.MetricsAddr)
	if err != nil {
		return fmt.Errorf("listen metrics: %w", err)
	}
	metricsErrors := make(chan error, 1)
	go func() { metricsErrors <- metrics.Serve(listener) }()
	logger.InfoContext(ctx, "content worker started", "event", "content.worker.started", "outcome", "succeeded",
		"worker_id", cfg.WorkerID, "resource_profile", cfg.Resource, "metrics_addr", listener.Addr().String(),
		"artifact_store", "local", "index_backend", cfg.IndexBackend, "job_type", cfg.JobType)
	return poll(ctx, worker, cfg.PollInterval, metricsErrors)
}

func safeError(err error, cfg config) string {
	message := err.Error()
	secrets := []string{cfg.DCToken, cfg.RTWToken, cfg.ContentDSN, cfg.SessionDSN, cfg.AuthorityToken, cfg.FactDSN}
	for _, dsn := range []string{cfg.ContentDSN, cfg.SessionDSN, cfg.FactDSN} {
		if parsed, parseErr := url.Parse(dsn); parseErr == nil && parsed.User != nil {
			if password, ok := parsed.User.Password(); ok {
				secrets = append(secrets, password)
			}
		}
	}
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	return message
}

type poller interface {
	RunOnce(context.Context) (bool, error)
}

func poll(ctx context.Context, worker poller, interval time.Duration, metricsErrors <-chan error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		_, err := worker.RunOnce(ctx)
		if err != nil && ctx.Err() != nil {
			return nil
		}
		// The application stage records claim/processing failures once. A later
		// fixed attempt may be claimable, so poll again after the bounded interval.
		select {
		case <-ctx.Done():
			return nil
		case serverErr := <-metricsErrors:
			if errors.Is(serverErr, http.ErrServerClosed) {
				return errors.New("metrics server stopped before worker cancellation")
			}
			return fmt.Errorf("metrics server stopped: %w", serverErr)
		case <-ticker.C:
		}
	}
}

// The upstream transport owns the client Span and injects that child context.
// Its name is bounded by the fixed provider, never by URL, ID or query text.
func clientTransport(service string, base *http.Transport) http.RoundTripper {
	return otelhttp.NewTransport(base, otelhttp.WithSpanNameFormatter(
		func(_ string, _ *http.Request) string { return "http.client " + service }))
}
