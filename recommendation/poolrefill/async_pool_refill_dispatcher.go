package poolrefill

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"sea/config"
	"sea/metrics"
	"sea/zlog"

	types "sea/type"

	"go.uber.org/zap"
)

type AsyncPoolRefillDispatcher struct {
	baseCtx     context.Context
	runner      PoolRefillRunner
	taskTimeout time.Duration
	queryFanout int
	enabled     bool

	queue chan string

	mu     sync.Mutex
	states map[string]*asyncPoolRefillState
}

type asyncPoolRefillState struct {
	job     types.PoolRefillJob
	queued  bool
	running bool
	dirty   bool
}

func NewAsyncPoolRefillDispatcher(baseCtx context.Context, runner PoolRefillRunner, cfg config.AsyncPoolConfig) *AsyncPoolRefillDispatcher {
	if baseCtx == nil {
		baseCtx = context.Background()
	}

	dispatcher := &AsyncPoolRefillDispatcher{
		baseCtx:     baseCtx,
		runner:      runner,
		taskTimeout: time.Duration(cfg.TaskTimeoutSecondsValue()) * time.Second,
		queryFanout: cfg.QueryFanoutValue(),
		enabled:     cfg.EnabledValue() && runner != nil,
		states:      map[string]*asyncPoolRefillState{},
	}
	if !dispatcher.enabled {
		return dispatcher
	}

	dispatcher.queue = make(chan string, cfg.QueueSizeValue())
	for workerIdx := 0; workerIdx < cfg.WorkersValue(); workerIdx++ {
		go dispatcher.worker()
	}
	return dispatcher
}

func (d *AsyncPoolRefillDispatcher) Enqueue(job types.PoolRefillJob) types.PoolRefillEnqueueResult {
	normalized := normalizePoolRefillJob(job, d.queryFanout)
	result := types.PoolRefillEnqueueResult{
		Key:      poolRefillJobKey(normalized),
		PoolType: normalized.PoolType,
	}

	switch {
	case d == nil || !d.enabled:
		result.QueueResult = "disabled"
	case normalized.UserID == "" || normalized.PoolType == "" || len(normalized.QueryTexts) == 0:
		result.QueueResult = "dropped_invalid"
		result.Dropped = true
	default:
		result = d.enqueueNormalized(normalized)
	}

	metrics.PoolRefillEnqueueTotal.WithLabelValues(result.QueueResult, result.PoolType).Inc()
	return result
}

func (d *AsyncPoolRefillDispatcher) enqueueNormalized(job types.PoolRefillJob) types.PoolRefillEnqueueResult {
	result := types.PoolRefillEnqueueResult{
		Key:      poolRefillJobKey(job),
		PoolType: job.PoolType,
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if state, ok := d.states[result.Key]; ok {
		mergedQueries := mergeQueryTexts(state.job.QueryTexts, job.QueryTexts, d.queryFanout)
		changed := !sameQueryTexts(state.job.QueryTexts, mergedQueries)
		state.job = types.PoolRefillJob{
			UserID:       job.UserID,
			PoolType:     job.PoolType,
			PeriodBucket: job.PeriodBucket,
			QueryTexts:   mergedQueries,
		}

		if state.running {
			if changed {
				state.dirty = true
			}
			result.QueueResult = "deduped"
			result.Deduped = true
			return result
		}
		if state.queued {
			result.QueueResult = "deduped"
			result.Deduped = true
			return result
		}

		state.queued = true
		select {
		case d.queue <- result.Key:
			result.QueueResult = "queued"
			result.Enqueued = true
			return result
		default:
			delete(d.states, result.Key)
			result.QueueResult = "dropped_queue_full"
			result.Dropped = true
			return result
		}
	}

	state := &asyncPoolRefillState{
		job:    job,
		queued: true,
	}
	d.states[result.Key] = state

	select {
	case d.queue <- result.Key:
		result.QueueResult = "queued"
		result.Enqueued = true
		return result
	default:
		delete(d.states, result.Key)
		result.QueueResult = "dropped_queue_full"
		result.Dropped = true
		return result
	}
}

func (d *AsyncPoolRefillDispatcher) worker() {
	for {
		select {
		case <-d.baseCtx.Done():
			return
		case key := <-d.queue:
			if key == "" {
				continue
			}
			d.runKey(key)
		}
	}
}

func (d *AsyncPoolRefillDispatcher) runKey(key string) {
	d.mu.Lock()
	state, ok := d.states[key]
	if !ok || !state.queued {
		d.mu.Unlock()
		return
	}

	job := state.job
	state.queued = false
	state.running = true
	state.dirty = false
	d.mu.Unlock()

	for {
		runCtx, cancel := context.WithTimeout(d.baseCtx, d.taskTimeout)
		runCtx = zlog.NewTrace(runCtx, "pool_refill_"+randHex(8), "pool_refill_async", "pool_refill_dispatcher", job.UserID, "", nil)
		runCtx, sp := zlog.StartSpan(runCtx, "pool_refill.async."+job.PoolType)
		metrics.PoolRefillInflight.WithLabelValues(job.PoolType).Inc()

		result, err := d.runner.Run(runCtx, job)

		metrics.PoolRefillInflight.WithLabelValues(job.PoolType).Dec()
		cancel()

		runStatus := "ok"
		if err != nil {
			runStatus = "error"
		} else if result.Empty {
			runStatus = "empty"
		}
		metrics.PoolRefillRunsTotal.WithLabelValues(runStatus, job.PoolType).Inc()

		if err != nil {
			sp.End(zlog.StatusError, err, zap.Any("job", map[string]any{
				"user_id":        job.UserID,
				"pool_type":      job.PoolType,
				"period_bucket":  job.PeriodBucket,
				"query_count":    len(job.QueryTexts),
				"successful":     result.SuccessfulQueries,
				"failed_queries": result.FailedQueries,
			}))
			zlog.L().Warn("pool refill run failed",
				zap.String("user_id", job.UserID),
				zap.String("pool_type", job.PoolType),
				zap.String("period_bucket", job.PeriodBucket),
				zap.Strings("queries", job.QueryTexts),
				zap.Error(err),
			)
		} else {
			status := zlog.StatusOK
			if result.Empty {
				status = zlog.StatusFallback
			}
			sp.End(status, nil, zap.Any("pool_refill", map[string]any{
				"user_id":         job.UserID,
				"pool_type":       job.PoolType,
				"period_bucket":   job.PeriodBucket,
				"query_count":     len(job.QueryTexts),
				"inserted":        result.Inserted,
				"considered":      result.Considered,
				"pool_size_after": result.PoolSizeAfter,
				"coverage_score":  result.CoverageScore,
				"successful":      result.SuccessfulQueries,
				"failed_queries":  result.FailedQueries,
			}))
		}

		d.mu.Lock()
		state, ok = d.states[key]
		if !ok {
			d.mu.Unlock()
			return
		}
		if state.dirty {
			job = state.job
			state.dirty = false
			d.mu.Unlock()
			continue
		}
		state.running = false
		delete(d.states, key)
		d.mu.Unlock()
		return
	}
}

func (d *AsyncPoolRefillDispatcher) InflightCount(key string) (bool, error) {
	if d == nil {
		return false, errors.New("dispatcher is nil")
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	state, ok := d.states[key]
	if !ok {
		return false, nil
	}
	return state.running || state.queued, nil
}

func randHex(nBytes int) string {
	buffer := make([]byte, nBytes)
	_, err := rand.Read(buffer)
	if err != nil {
		return "poolrefill"
	}
	return hex.EncodeToString(buffer)
}
