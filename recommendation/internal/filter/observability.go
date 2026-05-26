package filter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	v1 "sea/api/recommendation/v1"
	"sea/metrics"
	"sea/zlog"

	"go.opentelemetry.io/otel"
	"go.uber.org/zap"
	"trpc.group/trpc-go/trpc-go/filter"
)

func init() {
	filter.Register("observability", ObservabilityFilter(), nil)
}

func ObservabilityFilter() filter.ServerFilter {
	return func(ctx context.Context, req interface{}, next filter.ServerHandleFunc) (interface{}, error) {
		start := time.Now()

		agentName := "reco_agent"
		surface, userID, sessionID, recReqID := extractMeta(req, &agentName)

		tracer := otel.Tracer("sea/reco_agent")
		ctx, root := tracer.Start(ctx, "invoke_agent "+agentName)
		defer root.End()

		ctx = zlog.NewTrace(ctx, recReqID, surface, agentName, userID, sessionID, nil)

		base, _ := zlog.BaseFrom(ctx)
		zlog.L().Info("invoke_agent",
			zap.String("event_type", "invoke_agent"),
			zap.String("trace_id", base.TraceID),
			zap.String("rec_request_id", recReqID),
			zap.String("surface", surface),
			zap.String("agent", agentName),
			zap.String("status", "OK"),
		)

		rsp, err := next(ctx, req)

		statusLabel := "ok"
		if err != nil {
			statusLabel = "error"
		}
		metrics.GenRecAgentRequestsTotalMetric.WithLabelValues(agentName, surface, statusLabel).Inc()
		metrics.GenRecAgentE2ELatencySecondsMetric.WithLabelValues(agentName, surface, statusLabel).
			Observe(time.Since(start).Seconds())

		return rsp, err
	}
}

func extractMeta(req interface{}, agentName *string) (surface, userID, sessionID, reqID string) {
	switch r := req.(type) {
	case *v1.RecommendRequest:
		surface = r.Surface
		if surface == "" {
			surface = "home_feed"
			r.Surface = surface
		}
		userID = r.UserId
		sessionID = r.SessionId
		reqID = r.RecRequestId
		if reqID == "" {
			reqID = "rec_" + randID()
			r.RecRequestId = reqID
		}
		*agentName = "reco_agent"
	case *v1.ContentSearchRequest:
		reqID = r.SearchRequestId
		if reqID == "" {
			reqID = "cs_" + randID()
			r.SearchRequestId = reqID
		}
		surface = "content_search"
		*agentName = "search_agent"
	case *v1.IngestRequest:
		reqID = "ingest_" + r.ArticleId
		surface = "doc_ingest"
		*agentName = "ingest_agent"
	default:
		reqID = "req_" + randID()
	}
	return
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
