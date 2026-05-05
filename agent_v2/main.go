package main

import (
	"agent_v2/agent"
	"agent_v2/config"
	"context"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	oteltrace "go.opentelemetry.io/otel/trace"

	"trpc.group/trpc-go/trpc-agent-go/log"
	ametric "trpc.group/trpc-go/trpc-agent-go/telemetry/metric"
	atrace "trpc.group/trpc-go/trpc-agent-go/telemetry/trace"
)

func main() {
	ctx := context.Background()

	// 1. Logging
	log.SetLevel(log.LevelInfo)
	log.Info("agent app starting")

	// 2. Metrics：OTLP HTTP → OpenTelemetry Collector
	mp, err := ametric.NewMeterProvider(
		ctx,
		ametric.WithEndpoint("localhost:4318"),
		ametric.WithProtocol("http"),
		ametric.WithServiceNamespace("your-namespace"),
		ametric.WithServiceName("your-agent-app"),
		ametric.WithServiceVersion("v0.1.0"),
	)
	if err != nil {
		log.Fatalf("failed to create meter provider: %v", err)
	}
	if err := ametric.InitMeterProvider(mp); err != nil {
		log.Fatalf("failed to init meter provider: %v", err)
	}
	defer func() {
		if err := mp.Shutdown(context.Background()); err != nil {
			log.Errorf("failed to shutdown meter provider: %v", err)
		}
	}()

	// 3. Tracing：OTLP gRPC → OpenTelemetry Collector
	traceClean, err := atrace.Start(
		ctx,
		atrace.WithEndpoint("localhost:4317"),
		atrace.WithProtocol("grpc"),
		atrace.WithServiceNamespace("your-namespace"),
		atrace.WithServiceName("your-agent-app"),
		atrace.WithServiceVersion("v0.1.0"),
	)
	if err != nil {
		log.Fatalf("failed to start trace telemetry: %v", err)
	}
	defer func() {
		if err := traceClean(); err != nil {
			log.Errorf("failed to clean trace telemetry: %v", err)
		}
	}()

	// 4. 给 main 启动过程打一个 span 和一个 counter
	ctx, span := atrace.Tracer.Start(
		ctx,
		"app.main",
		oteltrace.WithAttributes(
			attribute.String("component", "main"),
			attribute.String("app", "your-agent-app"),
		),
	)
	defer span.End()

	meter := mp.Meter("your-agent-app")
	startCounter, err := meter.Int64Counter(
		"app.starts.total",
		otelmetric.WithDescription("Total number of application starts"),
		otelmetric.WithUnit("1"),
	)
	if err != nil {
		log.Fatalf("failed to create app start counter: %v", err)
	}
	startCounter.Add(ctx, 1)

	if err := config.Init(); err != nil {
		log.Fatalf("初始化配置失败: %v", err)
	}
	handler, cleanup, err := agent.NewTravelPlanningAGUIHandler()
	if err != nil {
		log.Fatalf("create amap agui handler failed: %v", err)
	}
	defer cleanup()

	addr := "127.0.0.1:8080"

	log.Info("Amap AG-UI server listening on http://%s/agui", addr)

	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server stopped with error: %v", err)
	}
}
