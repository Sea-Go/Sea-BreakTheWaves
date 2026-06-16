package service

import (
	"context"
	"strings"
	"time"

	"sea/metrics"
	"sea/storage"
	types "sea/type"

	"github.com/prometheus/client_golang/prometheus"
)

const defaultRecoEvalSurface = "dashboard_recommend"

type RecoEvaluationService struct {
	repo *storage.RecoEvaluationRepo
}

func NewRecoEvaluationService(repo *storage.RecoEvaluationRepo) *RecoEvaluationService {
	return &RecoEvaluationService{repo: repo}
}

func (s *RecoEvaluationService) RecordRecommendation(ctx context.Context, req types.RecommendRequest, resp types.RecommendResponse) error {
	if s == nil || s.repo == nil {
		return nil
	}

	surface := normalizeSurface(req.Surface)
	ids := resp.ArticleIDs
	if len(ids) == 0 {
		ids = resp.IDs
	}

	requestLog := types.RecoRequestLog{
		RecRequestID:  resp.RecRequestID,
		UserID:        req.UserID,
		SessionID:     req.SessionID,
		Surface:       surface,
		Query:         req.Query,
		Status:        resp.Status,
		ReturnedCount: len(ids),
		CreatedAt:     time.Now(),
	}
	if err := s.repo.RecordRequest(ctx, requestLog); err != nil {
		return err
	}

	metrics.RecoRequestsTotal.WithLabelValues(surface, normalizeStatus(resp.Status)).Inc()
	metrics.RecoReturnedItems.WithLabelValues(surface, normalizeStatus(resp.Status)).Observe(float64(len(ids)))

	events := make([]types.RecoEventLog, 0, len(ids))
	for index, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		events = append(events, types.RecoEventLog{
			RecRequestID: resp.RecRequestID,
			UserID:       req.UserID,
			SessionID:    req.SessionID,
			Surface:      surface,
			ArticleID:    id,
			Rank:         index + 1,
			EventType:    types.RecoEventImpression,
			EventTS:      time.Now(),
			Metadata: map[string]any{
				"status": resp.Status,
			},
		})
	}
	if len(events) == 0 {
		return nil
	}

	accepted, err := s.repo.RecordEvents(ctx, events)
	if err != nil {
		return err
	}
	metrics.RecoEventsTotal.WithLabelValues(surface, types.RecoEventImpression).Add(float64(accepted))
	return nil
}

func (s *RecoEvaluationService) RecordEvents(ctx context.Context, request types.RecoEventRequest) (types.RecoEventResponse, error) {
	if s == nil || s.repo == nil {
		return types.RecoEventResponse{}, nil
	}

	accepted, err := s.repo.RecordEvents(ctx, request.Events)
	if err != nil {
		return types.RecoEventResponse{Accepted: accepted}, err
	}
	for _, event := range request.Events {
		eventType := strings.TrimSpace(strings.ToLower(event.EventType))
		if eventType == "" {
			continue
		}
		metrics.RecoEventsTotal.WithLabelValues(normalizeSurface(event.Surface), eventType).Inc()
	}
	return types.RecoEventResponse{Accepted: accepted}, nil
}

func (s *RecoEvaluationService) Summary(ctx context.Context, surface string, window string) (types.RecoEvaluationSummary, error) {
	if s == nil || s.repo == nil {
		return types.RecoEvaluationSummary{
			Surface:     normalizeSurface(surface),
			Window:      window,
			GeneratedAt: time.Now(),
		}, nil
	}
	return s.repo.Summary(ctx, normalizeSurface(surface), ParseRecoEvaluationWindow(window))
}

func ParseRecoEvaluationWindow(value string) time.Duration {
	value = strings.TrimSpace(strings.ToLower(value))
	switch value {
	case "1h", "hour":
		return time.Hour
	case "6h":
		return 6 * time.Hour
	case "7d", "week":
		return 7 * 24 * time.Hour
	case "30d":
		return 30 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

type RecoEvaluationCollector struct {
	service *RecoEvaluationService
	desc    *prometheus.Desc
}

func NewRecoEvaluationCollector(service *RecoEvaluationService) *RecoEvaluationCollector {
	return &RecoEvaluationCollector{
		service: service,
		desc: prometheus.NewDesc(
			"genrec_eval_metric_value",
			"Recommendation evaluation metric value computed from online implicit behavior.",
			[]string{"metric", "source", "surface", "window"},
			nil,
		),
	}
}

func (c *RecoEvaluationCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

func (c *RecoEvaluationCollector) Collect(ch chan<- prometheus.Metric) {
	if c == nil || c.service == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for _, window := range []string{"1h", "24h", "7d"} {
		summary, err := c.service.Summary(ctx, defaultRecoEvalSurface, window)
		if err != nil {
			continue
		}
		for _, item := range summary.MetricValues {
			ch <- prometheus.MustNewConstMetric(
				c.desc,
				prometheus.GaugeValue,
				item.Value,
				item.Key,
				item.Source,
				summary.Surface,
				summary.Window,
			)
		}
	}
}

func normalizeSurface(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultRecoEvalSurface
	}
	return value
}

func normalizeStatus(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "unknown"
	}
	return value
}
