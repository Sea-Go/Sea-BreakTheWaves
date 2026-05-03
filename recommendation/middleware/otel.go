package middleware

import (
	"sea/metrics"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TraceMiddleware 是一个极简的 HTTP Trace 中间件：
// 1) 从 HTTP Header Extract 上游 traceparent（跨服务不断链）
// 2) 为每个请求创建一个 server span
// 这样你可以在 Jaeger 里看到 HTTP 入口，并把后续 agent/skills 的 span 串起来。
func TraceMiddleware() gin.HandlerFunc {
	tr := otel.Tracer("sea/http")
	prop := otel.GetTextMapPropagator()

	return func(c *gin.Context) {
		// 可观测性自检：统计服务侧输出的 trace/span（按 HTTP 入口请求计数）。
		// 避免 /metrics 等探针/抓取接口把计数刷爆。
		path := c.Request.URL.Path
		if path != "/metrics" && path != "/health" {
			metrics.GenRecAgentTracesTotalMetric.Inc()
		}

		parent := prop.Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))

		spanName := c.FullPath()
		if spanName == "" {
			spanName = c.Request.URL.Path
		}

		ctx, span := tr.Start(parent, spanName, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		c.Request = c.Request.WithContext(ctx)

		c.Next()

		span.SetAttributes(
			attribute.String("http.method", c.Request.Method),
			attribute.String("http.route", c.FullPath()),
			attribute.String("http.target", c.Request.URL.Path),
			attribute.Int("http.status_code", c.Writer.Status()),
		)
		if len(c.Errors) > 0 {
			span.SetStatus(codes.Error, c.Errors.String())
		} else {
			span.SetStatus(codes.Ok, "")
		}
	}
}
