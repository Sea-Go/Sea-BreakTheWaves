package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"agent_v3/internal/app/delivery/agui"
	"agent_v3/internal/app/delivery/stream"
	"agent_v3/internal/config"
	"agent_v3/internal/graph"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	oteltrace "go.opentelemetry.io/otel/trace"

	"trpc.group/trpc-go/trpc-agent-go/log"
	ametric "trpc.group/trpc-go/trpc-agent-go/telemetry/metric"
	atrace "trpc.group/trpc-go/trpc-agent-go/telemetry/trace"
)

const (
	serviceName    = "your-agent-app"
	serviceVersion = "v0.1.0"
	listenAddr     = "127.0.0.1:8088"
)

func RunServer(ctx context.Context) error {
	log.SetLevel(log.LevelInfo)
	log.Info("agent app starting")

	mp, err := ametric.NewMeterProvider(
		ctx,
		ametric.WithEndpoint("localhost:4318"),
		ametric.WithProtocol("http"),
		ametric.WithServiceNamespace("your-namespace"),
		ametric.WithServiceName(serviceName),
		ametric.WithServiceVersion(serviceVersion),
	)
	if err != nil {
		return fmt.Errorf("create meter provider: %w", err)
	}
	if err := ametric.InitMeterProvider(mp); err != nil {
		return fmt.Errorf("init meter provider: %w", err)
	}
	defer func() {
		if err := mp.Shutdown(context.Background()); err != nil {
			log.Errorf("failed to shutdown meter provider: %v", err)
		}
	}()

	traceClean, err := atrace.Start(
		ctx,
		atrace.WithEndpoint("localhost:4317"),
		atrace.WithProtocol("grpc"),
		atrace.WithServiceNamespace("your-namespace"),
		atrace.WithServiceName(serviceName),
		atrace.WithServiceVersion(serviceVersion),
	)
	if err != nil {
		return fmt.Errorf("start trace telemetry: %w", err)
	}
	defer func() {
		if err := traceClean(); err != nil {
			log.Errorf("failed to clean trace telemetry: %v", err)
		}
	}()

	ctx, span := atrace.Tracer.Start(
		ctx,
		"app.main",
		oteltrace.WithAttributes(
			attribute.String("component", "main"),
			attribute.String("app", serviceName),
		),
	)
	defer span.End()

	meter := mp.Meter(serviceName)
	startCounter, err := meter.Int64Counter(
		"app.starts.total",
		otelmetric.WithDescription("Total number of application starts"),
		otelmetric.WithUnit("1"),
	)
	if err != nil {
		return fmt.Errorf("create app start counter: %w", err)
	}
	startCounter.Add(ctx, 1)

	if err := config.Init(); err != nil {
		return fmt.Errorf("init config: %w", err)
	}
	graph.Init()
	exportZhihuEnvironment()

	aguiHandler, cleanup, err := agui.NewHandler()
	if err != nil {
		return fmt.Errorf("create ag-ui handler: %w", err)
	}
	defer cleanup()

	mux := http.NewServeMux()
	mux.Handle("/agui", aguiHandler)
	mux.Handle("/agui/", aguiHandler)
	mux.Handle("/travel/", stream.NewHandler())

	log.Info("Travel planning server listening on http://%s/agui and http://%s/travel/stream", listenAddr, listenAddr)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		return fmt.Errorf("server stopped: %w", err)
	}
	return nil
}

func exportZhihuEnvironment() {
	if secret := strings.TrimSpace(config.Cfg.Zhihu.AccessSecret); secret != "" {
		os.Setenv("ZHIHU_ACCESS_SECRET", secret)
	}
	if baseURL := strings.TrimSpace(config.Cfg.Zhihu.OpenAPIBaseURL); baseURL != "" {
		os.Setenv("ZHIHU_OPENAPI_BASE_URL", baseURL)
	}
	if searchURL := strings.TrimSpace(config.Cfg.Zhihu.ZhihuSearchURL); searchURL != "" {
		os.Setenv("ZHIHU_ZHIHU_SEARCH_URL", searchURL)
	}
	if searchURL := strings.TrimSpace(config.Cfg.Zhihu.GlobalSearchURL); searchURL != "" {
		os.Setenv("ZHIHU_GLOBAL_SEARCH_URL", searchURL)
	}
}
