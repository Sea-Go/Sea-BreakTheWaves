package main

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	oteltrace "go.opentelemetry.io/otel/trace"

	"trpc.group/trpc-go/trpc-agent-go/log"
	ametric "trpc.group/trpc-go/trpc-agent-go/telemetry/metric"
	atrace "trpc.group/trpc-go/trpc-agent-go/telemetry/trace"
)

func main() {
	ctx := context.Background()

	// 1. Logging：先设置日志级别。
	log.SetLevel(log.LevelInfo)
	log.Info("agent app starting")

	// 2. Metrics：推荐用 OTLP HTTP，默认 Collector 端口通常是 4318。
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

	// 3. Tracing：推荐用 OTLP gRPC，默认 Collector 端口通常是 4317。
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

	// 4. 可选：给 main 启动过程打一个 span 和一个 counter，方便确认链路和指标已接通。
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

	// 5. 你的 Agent / Runner 初始化与执行代码放这里。
	//
	// ag := llmagent.New(...)
	// r := runner.NewRunner("your-agent-app", ag)
	// events, err := r.Run(ctx, userID, sessionID, model.NewUserMessage("..."))
	// ...
}
