package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/database"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"gorm.io/gorm"

	"github.com/jackc/pgx/v5/pgxpool"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

// serveFactConsumer is selected only by an explicit user fact job type. It
// uses a GORM-managed user fact schema; framework session storage remains separate.
func serveFactConsumer(ctx context.Context, cfg config, output io.Writer) (resultErr error) {
	bundle, err := telemetry.New(ctx, telemetry.Config{Service: workerService(cfg), Environment: cfg.Environment,
		Version: cfg.Version, InstanceID: cfg.InstanceID, Output: output, Level: slog.LevelInfo,
		OTLPEndpoint: cfg.OTLPTracesURL, SampleRatio: 1})
	if err != nil {
		bootstrapLogger(output, cfg).ErrorContext(ctx, "user fact telemetry initialization failed",
			"event", "usermodel.worker.start_failed", "outcome", "failed", "error_code", "TELEMETRY_INIT_FAILED",
			"error_type", fmt.Sprintf("%T", err), "error_message", safeError(err, cfg))
		return err
	}
	logger, err := bundle.Logger("usermodel", "application")
	if err != nil {
		_ = bundle.Close(context.Background())
		return err
	}
	var pool *pgxpool.Pool
	var db *gorm.DB
	var graph *usermodel.FactGraphRuntime
	var metrics *http.Server
	var dcTransport, rtwTransport *http.Transport
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if metrics != nil {
			if err := metrics.Shutdown(shutdown); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("stop metrics server: %w", err))
				_ = metrics.Close()
			}
		}
		if graph != nil {
			resultErr = errors.Join(resultErr, graph.Close())
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
		if db != nil {
			if sqlDB, err := db.DB(); err == nil {
				_ = sqlDB.Close()
			}
		}
		resultErr = errors.Join(resultErr, bundle.Close(shutdown))
		if resultErr != nil {
			logger.ErrorContext(context.Background(), "user fact worker stopped with error",
				"event", "usermodel.worker.stopped", "outcome", "failed", "error_code", "WORKER_STOPPED",
				"error_type", fmt.Sprintf("%T", resultErr), "error_message", safeError(resultErr, cfg))
		} else {
			logger.InfoContext(context.Background(), "user fact worker stopped",
				"event", "usermodel.worker.stopped", "outcome", "succeeded")
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
	dcClient := &http.Client{Timeout: cfg.HTTPTimeout, Transport: clientTransport("datacenter", dcTransport)}
	rtwClient := &http.Client{Timeout: cfg.HTTPTimeout, Transport: clientTransport("ridethewind", rtwTransport)}
	dc, err := datacenter.New(httpclient.Config{BaseURL: cfg.DCURL, Token: cfg.DCToken, HTTPClient: dcClient})
	if err != nil {
		return fmt.Errorf("construct DataCenter client: %w", err)
	}
	bindings, err := factBindings(cfg, rtwClient)
	if err != nil {
		return fmt.Errorf("construct RTW fact authority binder: %w", err)
	}
	manager, err := database.Open(ctx, database.Config{DSN: cfg.FactDSN, Schema: cfg.FactSchema,
		MaxOpenConns: 4, MaxIdleConns: 2})
	if err != nil {
		return fmt.Errorf("open user fact database manager: %w", err)
	}
	db = manager
	if err := usermodel.MigrateSchema(ctx, manager); err != nil {
		return fmt.Errorf("apply user fact schema with GORM: %w", err)
	}
	pgCfg, err := pgxpool.ParseConfig(cfg.FactDSN)
	if err != nil {
		return errors.New("invalid user fact Postgres DSN")
	}
	if pgCfg.ConnConfig.RuntimeParams == nil {
		pgCfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	pgCfg.ConnConfig.RuntimeParams["search_path"] = cfg.FactSchema
	pool, err = pgxpool.NewWithConfig(ctx, pgCfg)
	if err != nil {
		return fmt.Errorf("open user fact Postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping user fact Postgres: %w", err)
	}
	var eventsTable, outboxTable *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('usermodel_events')::text,
		to_regclass('usermodel_outbox')::text`).Scan(&eventsTable, &outboxTable); err != nil ||
		eventsTable == nil || outboxTable == nil {
		return errors.New("user fact schema must be migrated before worker start")
	}
	store := usermodel.NewStore(pool, bundle)
	graph, err = usermodel.NewFactGraphRuntime(workerService(cfg), store, inmemory.NewSessionService(), bundle)
	if err != nil {
		return fmt.Errorf("construct tRPC fact Graph/Runner: %w", err)
	}
	worker, err := app.NewFactWorker(app.FactWorkerConfig{Consumer: cfg.FactConsumer, Producer: cfg.FactProducer,
		BatchLimit: cfg.FactBatchLimit, Bindings: bindings}, dc, nil, graph, store, bundle)
	if err != nil {
		return fmt.Errorf("construct user fact consumer: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", bundle.MetricsHandler())
	metrics = &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second,
		ErrorLog: slog.NewLogLogger(logger.With("event", "usermodel.worker.metrics_server").Handler(), slog.LevelError)}
	listener, err := net.Listen("tcp", cfg.MetricsAddr)
	if err != nil {
		return fmt.Errorf("listen metrics: %w", err)
	}
	metricsErrors := make(chan error, 1)
	go func() { metricsErrors <- metrics.Serve(listener) }()
	logger.InfoContext(ctx, "user fact worker started", "event", "usermodel.worker.started", "outcome", "succeeded",
		"consumer", cfg.FactConsumer, "producer", cfg.FactProducer, "batch_limit", cfg.FactBatchLimit,
		"metrics_addr", listener.Addr().String())
	return pollFavoriteFacts(ctx, worker, cfg, logger, metricsErrors)
}

func factBindings(cfg config, rtwClient *http.Client) ([]app.FactEventBinding, error) {
	switch cfg.JobType {
	case favoriteFactJobType:
		authority, err := app.NewFavoriteAuthorityBinder(app.FavoriteAuthorityBinderConfig{
			BaseURL: cfg.AuthorityURL, Token: cfg.AuthorityToken, Client: rtwClient})
		if err != nil {
			return nil, err
		}
		return []app.FactEventBinding{
			{EventType: "rtw.favorite.assert", SchemaVersion: 1, Action: usermodel.Assert, EvidenceBinder: authority},
			{EventType: "rtw.favorite.retract", SchemaVersion: 1, Action: usermodel.Retract, EvidenceBinder: authority},
			{EventType: "rtw.favorite.assert", SchemaVersion: 2, Action: usermodel.Assert, EvidenceBinder: authority},
			{EventType: "rtw.favorite.retract", SchemaVersion: 2, Action: usermodel.Retract, EvidenceBinder: authority},
		}, nil
	case commentFactJobType:
		authority, err := app.NewCommunityAuthorityBinder(app.CommunityAuthorityBinderConfig{
			BaseURL: cfg.AuthorityURL, Token: cfg.AuthorityToken, Producer: cfg.FactProducer, Client: rtwClient})
		if err != nil {
			return nil, err
		}
		return []app.FactEventBinding{
			{EventType: "community.comment.created", SchemaVersion: 1, Action: usermodel.Assert, EvidenceBinder: authority},
			{EventType: "community.comment.deleted", SchemaVersion: 1, Action: usermodel.Retract, EvidenceBinder: authority},
			{EventType: "community.comment.interaction", SchemaVersion: 1,
				AllowedActions: []usermodel.Action{usermodel.Assert, usermodel.Correct, usermodel.Retract}, EvidenceBinder: authority},
		}, nil
	case likeFactJobType:
		authority, err := app.NewCommunityAuthorityBinder(app.CommunityAuthorityBinderConfig{
			BaseURL: cfg.AuthorityURL, Token: cfg.AuthorityToken, Producer: cfg.FactProducer, Client: rtwClient})
		if err != nil {
			return nil, err
		}
		return []app.FactEventBinding{{EventType: "community.target.interaction", SchemaVersion: 1,
			AllowedActions: []usermodel.Action{usermodel.Assert, usermodel.Correct, usermodel.Retract}, EvidenceBinder: authority}}, nil
	default:
		return nil, app.ErrFactDeliveryContract
	}
}

func pollFavoriteFacts(ctx context.Context, worker *app.FactWorker, cfg config, logger *slog.Logger,
	metricsErrors <-chan error) error {
	backoff := cfg.PollInterval
	for {
		if ctx.Err() != nil {
			return nil
		}
		select {
		case serverErr := <-metricsErrors:
			if errors.Is(serverErr, http.ErrServerClosed) {
				return errors.New("metrics server stopped before worker cancellation")
			}
			return fmt.Errorf("metrics server stopped: %w", serverErr)
		default:
		}
		result, err := worker.RunOnce(ctx)
		if err != nil && ctx.Err() != nil {
			return nil
		}
		if err != nil {
			outcome := "retryable"
			if errors.Is(err, app.ErrFactDeliveryContract) || errors.Is(err, app.ErrFactDeliveryPending) {
				outcome = "blocked"
			}
			logger.WarnContext(ctx, "user fact batch deferred", "event", "usermodel.worker.batch_deferred",
				"outcome", outcome, "error_code", favoritePollErrorCode(err),
				"error_type", fmt.Sprintf("%T", err), "error_message", safeError(err, cfg),
				"retry_after_ms", backoff.Milliseconds())
		} else {
			backoff = cfg.PollInterval
			if !result.Empty {
				continue // bounded batch, then drain the next contiguous DC window
			}
		}
		wait := backoff
		if err != nil {
			backoff = min(backoff*2, max(cfg.PollInterval, 30*time.Second))
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case serverErr := <-metricsErrors:
			timer.Stop()
			if errors.Is(serverErr, http.ErrServerClosed) {
				return errors.New("metrics server stopped before worker cancellation")
			}
			return fmt.Errorf("metrics server stopped: %w", serverErr)
		case <-timer.C:
		}
	}
}

func favoritePollErrorCode(err error) string {
	switch {
	case errors.Is(err, app.ErrFactDeliveryPending):
		return "FACT_PENDING"
	case errors.Is(err, app.ErrFactDeliveryContract):
		return "CONTRACT_MISMATCH"
	case errors.Is(err, app.ErrFactAuthorityUnavailable):
		return "AUTHORITY_UNAVAILABLE"
	default:
		return "FACT_DELIVERY_RETRY"
	}
}
