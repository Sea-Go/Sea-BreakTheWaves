package infra

import (
	"context"
	"time"

	"sea/config"
	"sea/zlog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap"
)

// OtelInit 初始化 OpenTelemetry Trace，使 Jaeger 能捕捉到完整链路。
//
// docker-compose 里 Jaeger 使用 OTLP 端口：34317 -> 4317（gRPC）
// 配置在 config.yaml 的 otel.otlp_grpc_address。
func OtelInit() (func(ctx context.Context) error, error) {
	if !config.Cfg.Otel.Enable {
		zlog.L().Warn("OpenTelemetry 未启用")
		return func(ctx context.Context) error { return nil }, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(config.Cfg.Otel.OtlpGrpcAddress),
	}
	if config.Cfg.Otel.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}

	exp, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		zlog.L().Error("初始化 OTLP Trace exporter 失败", zap.Error(err))
		return nil, err
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", config.Cfg.Otel.ServiceName),
		),
	)
	if err != nil {
		zlog.L().Warn("创建 OTel resource 失败（继续启动）", zap.Error(err))
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	zlog.L().Info("OpenTelemetry 初始化完成", zap.String("otlp_grpc", config.Cfg.Otel.OtlpGrpcAddress))
	return tp.Shutdown, nil
}
