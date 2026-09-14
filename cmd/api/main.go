// Command api serves the RTW-signed search summary handoff in explicit local
// exact-index mode. It does not publish indexes or accept client identity.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/sparse"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	searchhttp "github.com/Sea-Go/Sea-BreakTheWaves/internal/transport/http/search"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"
)

const serviceName = "sea-btw-search-api"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		bootstrap(os.Stderr).ErrorContext(ctx, "search API configuration rejected",
			"event", "search.api.configuration_failed", "outcome", "failed", "error_code", "SEARCH_CONFIG_INVALID",
			"error_message", err.Error())
		os.Exit(2)
	}
	if err := serve(ctx, cfg, os.Stderr); err != nil {
		bootstrap(os.Stderr).ErrorContext(context.Background(), "search API stopped with error",
			"event", "search.api.stopped", "outcome", "failed", "error_code", "SEARCH_API_FAILED",
			"error_message", redacted(err, cfg))
		os.Exit(1)
	}
}

func bootstrap(output io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: slog.LevelInfo})).With(
		"service", serviceName, "component", "search", "log_source", "application")
}

func redacted(err error, cfg config) string {
	message := err.Error()
	for _, secret := range []string{cfg.ScopeKey, cfg.ToolsScopeKey, cfg.RTWToken, cfg.DCToken, cfg.ModelKey} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	return message
}

func serve(ctx context.Context, cfg config, output io.Writer) (resultErr error) {
	bundle, err := telemetry.New(ctx, telemetry.Config{Service: serviceName, Environment: cfg.Environment,
		Version: cfg.Version, InstanceID: cfg.InstanceID, Output: output, Level: slog.LevelInfo,
		OTLPEndpoint: cfg.OTLPTracesURL, SampleRatio: 1})
	if err != nil {
		return fmt.Errorf("initialize search telemetry: %w", err)
	}
	logger, err := bundle.Logger("search", "application")
	if err != nil {
		_ = bundle.Close(context.Background())
		return err
	}
	var boundary *searchdomain.RootSessionBoundary
	var toolsBoundary *searchdomain.ToolRunBoundary
	var apiServer, metricsServer *http.Server
	var rtwTransport, dcTransport *http.Transport
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, server := range []*http.Server{apiServer, metricsServer} {
			if server != nil {
				if err := server.Shutdown(shutdown); err != nil {
					resultErr = errors.Join(resultErr, err)
					_ = server.Close()
				}
			}
		}
		if boundary != nil {
			resultErr = errors.Join(resultErr, boundary.Close())
		}
		if toolsBoundary != nil {
			resultErr = errors.Join(resultErr, toolsBoundary.Close())
		}
		if rtwTransport != nil {
			rtwTransport.CloseIdleConnections()
		}
		if dcTransport != nil {
			dcTransport.CloseIdleConnections()
		}
		resultErr = errors.Join(resultErr, bundle.Close(shutdown))
		if resultErr == nil {
			logger.InfoContext(context.Background(), "search API stopped", "event", "search.api.stopped", "outcome", "succeeded")
		}
	}()
	if err := bundle.InstallGlobals(); err != nil {
		return fmt.Errorf("install framework telemetry: %w", err)
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return errors.New("default HTTP transport unavailable")
	}
	rtwTransport, dcTransport = base.Clone(), base.Clone()
	rtwHTTP := &http.Client{Timeout: cfg.HTTPTimeout, Transport: otelhttp.NewTransport(rtwTransport)}
	dcHTTP := &http.Client{Timeout: cfg.HTTPTimeout, Transport: otelhttp.NewTransport(dcTransport)}
	rtw, err := ridethewind.New(httpclient.Config{BaseURL: cfg.RTWURL, Token: cfg.RTWToken, HTTPClient: rtwHTTP})
	if err != nil {
		return fmt.Errorf("construct RTW client: %w", err)
	}
	dc, err := datacenter.New(httpclient.Config{BaseURL: cfg.DCURL, Token: cfg.DCToken, HTTPClient: dcHTTP})
	if err != nil {
		return fmt.Errorf("construct DataCenter client: %w", err)
	}
	// Dense, sparse and ColBERT query encoding can start concurrently. Bound
	// their shared physical provider to the explicitly configured capacity.
	representations, err := datacenter.NewRepresentationGate(dc, cfg.RepresentationMaxInFlight)
	if err != nil {
		return fmt.Errorf("construct representation capacity gate: %w", err)
	}
	objects, err := artifacts.NewLocal(cfg.ArtifactDir)
	if err != nil {
		return fmt.Errorf("open local exact artifacts: %w", err)
	}
	denseLane, err := dense.New(objects, representations, cfg.Indexes.Dense)
	if err != nil {
		return fmt.Errorf("construct dense lane: %w", err)
	}
	sparseLane, err := sparse.New(objects, representations, cfg.Indexes.Sparse)
	if err != nil {
		return fmt.Errorf("construct sparse lane: %w", err)
	}
	multiLane, err := multivector.New(objects, representations, cfg.Indexes.MultiVector)
	if err != nil {
		return fmt.Errorf("construct multivector lane: %w", err)
	}
	current, err := app.NewRTWSearchSnapshotProvider(rtw)
	if err != nil {
		return err
	}
	checker, err := app.NewRTWEffectiveRevisionChecker(current)
	if err != nil {
		return err
	}
	// This one-query planner is the only advertised profile in local-exact
	// mode. Detailed and model-planned tiers require a separate implementation.
	planner := searchdomain.PlanFunc(func(_ context.Context, in searchdomain.PlanInput) ([]string, error) {
		if in.Round != 1 || in.Depth != searchdomain.Fast || in.Intelligence != searchdomain.Low {
			return nil, searchdomain.ErrUnavailable
		}
		return []string{in.Query}, nil
	})
	searcher, err := searchdomain.New(denseLane, sparseLane, multiLane, planner, checker, cfg.Policy)
	if err != nil {
		return fmt.Errorf("construct fixed search policy: %w", err)
	}
	citations, err := app.NewRTWSearchCitationAdapter(rtw)
	if err != nil {
		return err
	}
	delivery, err := searchdomain.NewDelivery(searcher, checker, citations, citations,
		searchdomain.EvidenceLimits{MaxReads: cfg.Policy.Profiles[searchdomain.Fast][searchdomain.Low].MaxEvidence,
			MaxQuoteRunes: cfg.MaxQuoteRunes})
	if err != nil {
		return err
	}
	history, err := app.NewRTWAcceptedRootHistory(rtw)
	if err != nil {
		return err
	}
	model := openai.New(cfg.ModelName, openai.WithBaseURL(cfg.ModelURL), openai.WithAPIKey(cfg.ModelKey))
	boundary, err = searchdomain.NewRootSessionBoundary(delivery, model, history, bundle)
	if err != nil {
		return fmt.Errorf("construct root search session: %w", err)
	}
	resolver, err := searchhttp.NewSignedScopeResolver([]byte(cfg.ScopeKey))
	if err != nil {
		return err
	}
	handler, err := searchhttp.NewHandler(resolver, boundary, bundle)
	if err != nil {
		return err
	}
	toolsBoundary, err = searchdomain.NewToolRunBoundary(delivery, bundle)
	if err != nil {
		return fmt.Errorf("construct tool search graph: %w", err)
	}
	toolsResolver, err := searchhttp.NewSignedToolsScopeResolver([]byte(cfg.ToolsScopeKey))
	if err != nil {
		return err
	}
	toolsHandler, err := searchhttp.NewToolsHandler(toolsResolver, toolsBoundary, bundle)
	if err != nil {
		return err
	}
	apiMux := http.NewServeMux()
	apiMux.Handle(searchhttp.Route, handler)
	apiMux.Handle(searchhttp.ToolsRoute, toolsHandler)
	apiMux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", bundle.MetricsHandler())
	apiServer = &http.Server{Handler: apiMux, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 95 * time.Second,
		ErrorLog: slog.NewLogLogger(logger.With("event", "search.api.http_server").Handler(), slog.LevelError)}
	metricsServer = &http.Server{Handler: metricsMux, ReadHeaderTimeout: 3 * time.Second,
		ErrorLog: slog.NewLogLogger(logger.With("event", "search.api.metrics_server").Handler(), slog.LevelError)}
	apiListener, err := net.Listen("tcp", cfg.APIAddr)
	if err != nil {
		return fmt.Errorf("listen search API: %w", err)
	}
	metricsListener, err := net.Listen("tcp", cfg.MetricsAddr)
	if err != nil {
		_ = apiListener.Close()
		return fmt.Errorf("listen search metrics: %w", err)
	}
	stopped := make(chan error, 2)
	go func() { stopped <- apiServer.Serve(apiListener) }()
	go func() { stopped <- metricsServer.Serve(metricsListener) }()
	logger.InfoContext(ctx, "search API started", "event", "search.api.started", "outcome", "succeeded",
		"api_addr", apiListener.Addr().String(), "metrics_addr", metricsListener.Addr().String(),
		"backend", "local-exact", "supported_profile", "fast.low",
		"representation_max_in_flight", cfg.RepresentationMaxInFlight,
		"tools_route", searchhttp.ToolsRoute)
	select {
	case <-ctx.Done():
		return nil
	case err := <-stopped:
		if errors.Is(err, http.ErrServerClosed) {
			return errors.New("search server stopped before process cancellation")
		}
		return fmt.Errorf("search server stopped: %w", err)
	}
}
