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
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/sparse"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	searchhttp "github.com/Sea-Go/Sea-BreakTheWaves/internal/transport/http/search"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
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
	for _, secret := range []string{cfg.ScopeKey, cfg.ToolsScopeKey, cfg.RTWToken,
		cfg.DCToken, cfg.ModelKey, cfg.MilvusAPIKey} {
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
	var nativeClient *milvusclient.Client
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
		if nativeClient != nil {
			resultErr = errors.Join(resultErr, nativeClient.Close(shutdown))
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
	current, err := app.NewRTWSearchSnapshotProvider(rtw)
	if err != nil {
		return err
	}
	var denseReader searchdomain.DenseReader
	var sparseReader searchdomain.SparseReader
	var multiReader searchdomain.MultiVectorReader
	var native *nativeBackend
	if cfg.Mode == "native-milvus" || cfg.Mode == "native-hybrid" {
		if cfg.Native == nil {
			return errors.New("native Milvus mode has no fixed physical settings")
		}
		nativeClient, err = milvusclient.New(ctx, &milvusclient.ClientConfig{
			Address: cfg.MilvusAddress, APIKey: cfg.MilvusAPIKey})
		if err != nil {
			return fmt.Errorf("connect native Milvus backend: %w", err)
		}
		physical := cfg.Native
		denseLane, denseErr := dense.NewMilvus(objects, representations, cfg.Indexes.Dense,
			nativeClient, dense.MilvusConfig{Namespace: physical.Namespace, Engine: physical.Engine,
				M: physical.Dense.M, EFConstruction: physical.Dense.EFConstruction,
				EFSearch: physical.Dense.EFSearch})
		if denseErr != nil {
			return fmt.Errorf("construct native dense lane: %w", denseErr)
		}
		var sparseLane *sparse.Service
		var sparseErr error
		if physical.SparseBackend == "frozen_ip_postings" {
			// Lite 3.2.1's SPARSE_INVERTED_INDEX scores BM25 even when the
			// caller requests IP. The immutable lane shard already contains a
			// complete learned-weight posting index; use its actual dot-product
			// candidate generation rather than pruning by BM25 first.
			sparseLane, sparseErr = sparse.New(objects, representations, cfg.Indexes.Sparse)
		} else {
			sparseLane, sparseErr = sparse.NewMilvus(objects, representations,
				cfg.Indexes.Sparse, nativeClient,
				sparse.MilvusConfig{Namespace: physical.Namespace, Engine: "milvus"})
		}
		if sparseErr != nil {
			return fmt.Errorf("construct physical learned sparse lane: %w", sparseErr)
		}
		multiLane, multiErr := multivector.NewMilvus(objects, representations,
			cfg.Indexes.MultiVector, nativeClient, multivector.MilvusConfig{
				Namespace: physical.Namespace, Engine: physical.Engine,
				M:              physical.MultiVector.M,
				EFConstruction: physical.MultiVector.EFConstruction,
				EFSearch:       physical.MultiVector.EFSearch}, multivector.WithTelemetry(bundle))
		if multiErr != nil {
			return fmt.Errorf("construct native token multivector lane: %w", multiErr)
		}
		native, err = newNativeBackend(current, objects, cfg.Indexes, nativeLaneLoads{
			Dense: func(ctx context.Context, ref corpus.Ref) (searchdomain.DenseReader, error) {
				return denseLane.Load(ctx, ref)
			},
			Sparse: func(ctx context.Context, ref corpus.Ref) (searchdomain.SparseReader, error) {
				return sparseLane.Load(ctx, ref)
			},
			MultiVector: func(ctx context.Context, ref corpus.Ref) (searchdomain.MultiVectorReader, error) {
				return multiLane.Load(ctx, ref)
			},
			ProbeDense: func(ctx context.Context, index corpus.LaneIndex, manifest corpus.ChunkManifest,
				probes []corpus.Chunk) error {
				result, err := denseLane.VerifyAndProbe(ctx, index, manifest, probes)
				if err != nil || len(result) != len(probes) {
					return errNativeProjection
				}
				return nil
			},
			ProbeSparse: func(ctx context.Context, index corpus.LaneIndex, manifest corpus.ChunkManifest,
				probes []corpus.Chunk) error {
				result, err := sparseLane.VerifyAndProbe(ctx, index, manifest, probes)
				if err != nil || len(result) != len(probes) {
					return errNativeProjection
				}
				return nil
			},
			ProbeMulti: func(ctx context.Context, index corpus.LaneIndex, manifest corpus.ChunkManifest,
				probes []corpus.Chunk) error {
				result, err := multiLane.VerifyAndProbe(ctx, index, manifest, probes)
				if err != nil || len(result) != len(probes) {
					return errNativeProjection
				}
				return nil
			},
		}, bundle)
		if err != nil {
			return fmt.Errorf("construct native three-lane publication loader: %w", err)
		}
		denseReader, sparseReader, multiReader =
			nativeDenseReader{native}, nativeSparseReader{native}, nativeMultiReader{native}
	} else {
		denseLane, denseErr := dense.New(objects, representations, cfg.Indexes.Dense)
		if denseErr != nil {
			return fmt.Errorf("construct dense lane: %w", denseErr)
		}
		sparseLane, sparseErr := sparse.New(objects, representations, cfg.Indexes.Sparse)
		if sparseErr != nil {
			return fmt.Errorf("construct sparse lane: %w", sparseErr)
		}
		multiLane, multiErr := multivector.New(objects, representations, cfg.Indexes.MultiVector)
		if multiErr != nil {
			return fmt.Errorf("construct multivector lane: %w", multiErr)
		}
		denseReader, sparseReader, multiReader = denseLane, sparseLane, multiLane
	}
	checker, err := app.NewRTWEffectiveRevisionChecker(current)
	if err != nil {
		return err
	}
	// Low keeps the original one-query contract. Medium can run only after the
	// optional root Graph's native planning Agent has validated new queries.
	planner := searchdomain.PlanFunc(func(ctx context.Context, in searchdomain.PlanInput) ([]string, error) {
		if in.Round != 1 || in.Depth != searchdomain.Fast {
			return nil, searchdomain.ErrUnavailable
		}
		switch in.Intelligence {
		case searchdomain.Low:
			return []string{in.Query}, nil
		case searchdomain.Medium:
			if cfg.FastMedium == nil {
				return nil, searchdomain.ErrUnavailable
			}
			return searchdomain.PlanFastMedium(ctx, in)
		default:
			return nil, searchdomain.ErrUnavailable
		}
	})
	searcher, err := searchdomain.New(denseReader, sparseReader, multiReader, planner, checker, cfg.Policy)
	if err != nil {
		return fmt.Errorf("construct fixed search policy: %w", err)
	}
	citations, err := app.NewRTWSearchCitationAdapter(rtw)
	if err != nil {
		return err
	}
	maxReads := cfg.Policy.Profiles[searchdomain.Fast][searchdomain.Low].MaxEvidence
	if cfg.FastMedium != nil && cfg.Policy.Profiles[searchdomain.Fast][searchdomain.Medium].MaxEvidence > maxReads {
		maxReads = cfg.Policy.Profiles[searchdomain.Fast][searchdomain.Medium].MaxEvidence
	}
	delivery, err := searchdomain.NewDelivery(searcher, checker, citations, citations,
		searchdomain.EvidenceLimits{MaxReads: maxReads,
			MaxQuoteRunes: cfg.MaxQuoteRunes})
	if err != nil {
		return err
	}
	history, err := app.NewRTWAcceptedRootHistory(rtw)
	if err != nil {
		return err
	}
	model := gatewayModel{name: cfg.ModelName, url: cfg.ModelURL, key: cfg.ModelKey}
	// The final no-tool summary remains capped at 512 output tokens. Medium
	// additionally bounds its own one-call planning stage and total wall time.
	// An explicit history budget opts every new attempt into accepted-turn
	// injection; the zero budget keeps the original no-injection boundary.
	if cfg.FastMedium == nil {
		if cfg.History.Valid() {
			boundary, err = searchdomain.NewRootSessionBoundaryWithHistorySeed(delivery, model, history, bundle,
				cfg.History, searchdomain.SummaryModelLimits{MaxOutputTokens: 512})
		} else {
			boundary, err = searchdomain.NewRootSessionBoundary(delivery, model, history, bundle,
				searchdomain.SummaryModelLimits{MaxOutputTokens: 512})
		}
	} else {
		plannerModel := gatewayModel{name: cfg.ModelName, url: cfg.ModelURL, key: cfg.ModelKey, stage: "plan"}
		if cfg.History.Valid() {
			boundary, err = searchdomain.NewRootSessionBoundaryWithFastMediumAndHistorySeed(delivery, model, plannerModel, history, bundle,
				searchdomain.FastMediumModelLimits{MaxOutputTokens: cfg.FastMedium.PlannerMaxOutputTokens,
					WallTime: cfg.FastMedium.WallTime}, cfg.History,
				searchdomain.SummaryModelLimits{MaxOutputTokens: 512})
		} else {
			boundary, err = searchdomain.NewRootSessionBoundaryWithFastMedium(delivery, model, plannerModel, history, bundle,
				searchdomain.FastMediumModelLimits{MaxOutputTokens: cfg.FastMedium.PlannerMaxOutputTokens,
					WallTime: cfg.FastMedium.WallTime}, searchdomain.SummaryModelLimits{MaxOutputTokens: 512})
		}
	}
	if err != nil {
		return fmt.Errorf("construct root search session: %w", err)
	}
	resolver, err := searchhttp.NewSignedScopeResolver([]byte(cfg.ScopeKey))
	if err != nil {
		return err
	}
	var summaryResolver searchhttp.ScopeResolver = resolver
	if native != nil {
		summaryResolver = nativeSummaryScope{inner: resolver, backend: native, policy: cfg.Policy}
	}
	handler, err := searchhttp.NewHandler(summaryResolver, boundary, bundle)
	if err != nil {
		return err
	}
	if cfg.FastMediumTools {
		plannerModel := gatewayModel{name: cfg.ModelName, url: cfg.ModelURL, key: cfg.ModelKey, stage: "plan"}
		toolsBoundary, err = searchdomain.NewToolRunBoundaryWithFastMedium(delivery, plannerModel, bundle,
			searchdomain.FastMediumModelLimits{MaxOutputTokens: cfg.FastMedium.PlannerMaxOutputTokens,
				WallTime: cfg.FastMedium.WallTime})
	} else {
		toolsBoundary, err = searchdomain.NewToolRunBoundary(delivery, bundle)
	}
	if err != nil {
		return fmt.Errorf("construct tool search graph: %w", err)
	}
	toolsResolver, err := searchhttp.NewSignedToolsScopeResolver([]byte(cfg.ToolsScopeKey))
	if err != nil {
		return err
	}
	var toolsHandler http.Handler
	var scopedTools searchhttp.ToolsScopeResolver = toolsResolver
	if native != nil {
		scopedTools = nativeToolsScope{inner: toolsResolver, backend: native,
			mediumEnabled: cfg.FastMediumTools}
	}
	if cfg.FastMediumTools {
		toolsHandler, err = searchhttp.NewToolsHandlerWithFastMedium(scopedTools, toolsBoundary, bundle)
	} else {
		toolsHandler, err = searchhttp.NewToolsHandler(scopedTools, toolsBoundary, bundle)
	}
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
	supportedProfile := "fast.low"
	if cfg.FastMedium != nil {
		supportedProfile = "fast.low,fast.medium.summary"
	}
	toolsSupportedProfile := "fast.low"
	if cfg.FastMediumTools {
		toolsSupportedProfile = "fast.low,fast.medium.tools"
	}
	logger.InfoContext(ctx, "search API started", "event", "search.api.started", "outcome", "succeeded",
		"api_addr", apiListener.Addr().String(), "metrics_addr", metricsListener.Addr().String(),
		"backend", cfg.Mode, "supported_profile", supportedProfile,
		"representation_max_in_flight", cfg.RepresentationMaxInFlight,
		"tools_supported_profile", toolsSupportedProfile,
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
