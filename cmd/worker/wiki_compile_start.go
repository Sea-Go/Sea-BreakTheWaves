package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
)

var errWikiCompileStartup = errors.New("Wiki compile worker lacks signed model, shared objects or result reference")

// The same run factory that opens the DC-authorized per-Compile model must
// supply current native app_user proof. A jobs service token cannot satisfy it.
type wikiCompileAuthorizedRuns interface {
	app.WikiCompileRunFactory
	CurrentNativeSession(context.Context) (app.WikiCompileModelSession, error)
	BindTelemetry(*telemetry.Bundle) error
	Close() error
}

// The caller owns borrowed provider clients and closes their transports after
// this worker stops. This entry owns Runs.Close; Open owns each Runner/Session.
type wikiCompileStartDeps struct {
	Jobs                app.WikiCompileJobClient
	Owner               app.WikiCompileOwner
	Runs                wikiCompileAuthorizedRuns
	Objects             artifacts.Store
	ResultRefContractID string
	ObjectBackend       string
	RTWObjectBucket     string
	SharedObjectRoot    string
	RTWObjectRoot       string
}

type wikiBucketStore interface {
	artifacts.Store
	Bucket() string
}

func wikiNilDependency(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// wikiCompileStartupGate runs before telemetry, PG, job Claim or model access.
// A syntactically valid enable flag cannot bypass missing owned capabilities.
func wikiCompileStartupGate(ctx context.Context, cfg config, d wikiCompileStartDeps) error {
	_, err := wikiCompileStartupSession(ctx, cfg, d)
	return err
}

// wikiCompileStartupSession freezes exactly one validated native session from
// the same factory that later opens the request-specific official DC model.
func wikiCompileStartupSession(ctx context.Context, cfg config,
	d wikiCompileStartDeps) (app.WikiCompileModelSession, error) {
	if ctx == nil || cfg.JobType != app.WikiCompileJobType || cfg.Wiki == nil ||
		!cfg.Wiki.Enabled || cfg.Wiki.NativeSessionSource != wikiCompileNativeSessionSource ||
		cfg.Wiki.ModelCallpoint != wikiCompileModelCallpoint ||
		wikiNilDependency(d.Jobs) || wikiNilDependency(d.Owner) ||
		wikiNilDependency(d.Runs) || wikiNilDependency(d.Objects) ||
		d.ResultRefContractID != app.WikiCompileResultRefContractID ||
		cfg.Wiki.ResultRefContractID != app.WikiCompileResultRefContractID ||
		d.ObjectBackend != cfg.Wiki.ObjectBackend {
		return app.WikiCompileModelSession{}, errWikiCompileStartup
	}
	switch cfg.Wiki.ObjectBackend {
	case "s3":
		store, ok := d.Objects.(wikiBucketStore)
		if !ok || store.Bucket() == "" || store.Bucket() != cfg.Wiki.ObjectBucket ||
			d.RTWObjectBucket != store.Bucket() || d.RTWObjectBucket != cfg.Wiki.RTWObjectBucket {
			return app.WikiCompileModelSession{}, errWikiCompileStartup
		}
	case "shared-local":
		if d.SharedObjectRoot == "" || d.SharedObjectRoot != cfg.Wiki.SharedObjectRoot ||
			d.RTWObjectRoot != d.SharedObjectRoot || d.RTWObjectRoot != cfg.Wiki.RTWObjectRoot {
			return app.WikiCompileModelSession{}, errWikiCompileStartup
		}
	default:
		return app.WikiCompileModelSession{}, errWikiCompileStartup
	}
	current, err := d.Runs.CurrentNativeSession(ctx)
	if err != nil {
		return app.WikiCompileModelSession{}, errWikiCompileStartup
	}
	frozen, err := app.NewWikiCompileModelSession(current.AccountID, current.NativeBearer())
	if err != nil || frozen.Proof() != current.Proof() {
		return app.WikiCompileModelSession{}, errWikiCompileStartup
	}
	return frozen, nil
}

// The main binary deliberately passes no provider here until the platform
// owner supplies a real native session and versioned DC ResultRef. It exits
// before claiming a job, instead of routing this job into prepare/index.
func serveWikiCompile(ctx context.Context, cfg config, output io.Writer) error {
	return serveWikiCompileWithDeps(ctx, cfg, output, wikiCompileStartDeps{})
}

func wikiCloseRunsBeforeTelemetry(ctx context.Context, cfg config, output io.Writer,
	runs wikiCompileAuthorizedRuns) error {
	if wikiNilDependency(runs) {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := runs.Close()
	if err != nil {
		bootstrapLogger(output, cfg).ErrorContext(ctx, "Wiki native Factory close failed before telemetry",
			"event", "content.wiki_compile.factory_close_failed", "outcome", "failed",
			"error_code", "NATIVE_FACTORY_CLOSE_FAILED")
	}
	return err
}

func serveWikiCompileWithDeps(ctx context.Context, cfg config, output io.Writer,
	d wikiCompileStartDeps) (resultErr error) {
	frozen, err := wikiCompileStartupSession(ctx, cfg, d)
	if err != nil {
		bootstrapLogger(output, cfg).ErrorContext(ctx, "Wiki compile worker startup denied",
			"event", "content.wiki_compile.start_rejected", "outcome", "rejected",
			"error_code", "SIGNED_DEPENDENCIES_MISSING")
		return errors.Join(errWikiCompileStartup,
			wikiCloseRunsBeforeTelemetry(ctx, cfg, output, d.Runs))
	}
	bundle, err := telemetry.New(ctx, telemetry.Config{Service: workerService(cfg),
		Environment: cfg.Environment, Version: cfg.Version, InstanceID: cfg.InstanceID,
		Output: output, Level: slog.LevelInfo, OTLPEndpoint: cfg.OTLPTracesURL,
		SampleRatio: 1})
	if err != nil {
		bootstrapLogger(output, cfg).ErrorContext(ctx, "Wiki telemetry startup failed",
			"event", "content.wiki_compile.start_failed", "outcome", "failed",
			"error_code", "TELEMETRY_INIT_FAILED")
		return errors.Join(err,
			wikiCloseRunsBeforeTelemetry(ctx, cfg, output, d.Runs))
	}
	var logger *slog.Logger
	// Register final logging first so metric shutdown and Bundle.Close update
	// resultErr before the terminal record is written.
	defer func() {
		if logger == nil {
			return
		}
		if resultErr == nil {
			logger.InfoContext(context.Background(), "Wiki compile worker stopped",
				"event", "content.wiki_compile.stopped", "outcome", "succeeded")
		} else {
			logger.ErrorContext(context.Background(), "Wiki compile worker stopped with error",
				"event", "content.wiki_compile.stopped", "outcome", "failed",
				"error_code", "WORKER_STOPPED")
		}
	}()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, bundle.Close(shutdown))
	}()
	// Go defers run LIFO: metrics shutdown (registered later) -> Factory.Close
	// -> Bundle.Close -> final stopped JSON. Native runners/nodes/sinks cannot
	// outlive the framework telemetry they still use.
	defer func() {
		closeCtx, stage, stageErr := bundle.Begin(context.Background(), "content",
			"content.wiki_compile.factory_close")
		closeErr := d.Runs.Close()
		if stageErr == nil {
			if closeErr == nil {
				stage.End(closeCtx, "succeeded", "", nil)
			} else {
				stage.End(closeCtx, "failed", "NATIVE_FACTORY_CLOSE_FAILED", closeErr)
			}
		}
		resultErr = errors.Join(resultErr, stageErr, closeErr)
	}()
	logger, err = bundle.Logger("content", "application")
	if err != nil {
		bootstrapLogger(output, cfg).ErrorContext(ctx, "Wiki logger startup failed",
			"event", "content.wiki_compile.start_failed", "outcome", "failed",
			"error_code", "LOGGER_INIT_FAILED")
		return err
	}
	if err := bundle.InstallGlobals(); err != nil {
		logger.ErrorContext(ctx, "Wiki framework telemetry startup failed",
			"event", "content.wiki_compile.start_failed", "outcome", "failed",
			"error_code", "FRAMEWORK_TELEMETRY_FAILED")
		return err
	}
	bindCtx, bindStage, err := bundle.Begin(ctx, "content", "content.wiki_compile.factory_bind")
	if err != nil {
		logger.ErrorContext(ctx, "Wiki native Factory bind stage failed",
			"event", "content.wiki_compile.start_failed", "outcome", "failed",
			"error_code", "NATIVE_FACTORY_BIND_FAILED")
		return err
	}
	if err := d.Runs.BindTelemetry(bundle); err != nil {
		bindStage.End(bindCtx, "failed", "NATIVE_FACTORY_BIND_FAILED", err)
		logger.ErrorContext(bindCtx, "Wiki native Factory bind failed",
			"event", "content.wiki_compile.start_failed", "outcome", "failed",
			"error_code", "NATIVE_FACTORY_BIND_FAILED")
		return err
	}
	bindStage.End(bindCtx, "succeeded", "", nil)
	worker, err := app.NewWikiCompileWorker(app.WikiCompileWorkerConfig{
		WorkerID: cfg.WorkerID, LeaseSeconds: cfg.LeaseSeconds,
		CompletionRef:       app.BuildWikiCompileResultRef,
		ResultRefContractID: app.WikiCompileResultRefContractID, ModelSession: frozen},
		d.Jobs, d.Owner, d.Runs, d.Objects, bundle)
	if err != nil {
		logger.ErrorContext(ctx, "Wiki worker assembly failed",
			"event", "content.wiki_compile.start_failed", "outcome", "failed",
			"error_code", "ASSEMBLY_FAILED")
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", bundle.MetricsHandler())
	metrics := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second,
		ErrorLog: slog.NewLogLogger(logger.With("event", "content.wiki_compile.metrics_server").Handler(), slog.LevelError)}
	listener, err := net.Listen("tcp", cfg.MetricsAddr)
	if err != nil {
		logger.ErrorContext(ctx, "Wiki metrics listener failed",
			"event", "content.wiki_compile.start_failed", "outcome", "failed",
			"error_code", "METRICS_LISTEN_FAILED")
		return fmt.Errorf("listen Wiki metrics: %w", err)
	}
	defer listener.Close()
	errorsFromMetrics := make(chan error, 1)
	go func() { errorsFromMetrics <- metrics.Serve(listener) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := metrics.Shutdown(shutdown); err != nil {
			resultErr = errors.Join(resultErr, err)
			_ = metrics.Close()
		}
	}()
	logger.InfoContext(ctx, "Wiki compile worker started", "event", "content.wiki_compile.started",
		"job_type", app.WikiCompileJobType, "worker_id", cfg.WorkerID,
		"object_backend", cfg.Wiki.ObjectBackend, "result_ref_contract", cfg.Wiki.ResultRefContractID)
	return poll(ctx, worker, cfg.PollInterval, errorsFromMetrics)
}
