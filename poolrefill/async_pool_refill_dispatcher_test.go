package poolrefill

import (
	"context"
	"sync"
	"testing"
	"time"

	"sea/config"

	types "sea/type"
)

type fakePoolRefillRunner struct {
	mu      sync.Mutex
	jobs    []types.PoolRefillJob
	started chan types.PoolRefillJob
	blocks  []chan struct{}
}

func (r *fakePoolRefillRunner) Run(ctx context.Context, job types.PoolRefillJob) (types.PoolRefillRunResult, error) {
	r.mu.Lock()
	index := len(r.jobs)
	r.jobs = append(r.jobs, job)
	var wait chan struct{}
	if index < len(r.blocks) {
		wait = r.blocks[index]
	}
	r.mu.Unlock()

	if r.started != nil {
		r.started <- job
	}

	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return types.PoolRefillRunResult{PoolType: job.PoolType}, ctx.Err()
		}
	}

	return types.PoolRefillRunResult{
		PoolType:          job.PoolType,
		PeriodBucket:      job.PeriodBucket,
		SuccessfulQueries: len(job.QueryTexts),
	}, nil
}

func (r *fakePoolRefillRunner) snapshotJobs() []types.PoolRefillJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]types.PoolRefillJob, len(r.jobs))
	copy(out, r.jobs)
	return out
}

func TestAsyncPoolRefillDispatcherDedupesSameJobWhileRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	block := make(chan struct{})
	runner := &fakePoolRefillRunner{
		started: make(chan types.PoolRefillJob, 4),
		blocks:  []chan struct{}{block},
	}
	dispatcher := NewAsyncPoolRefillDispatcher(ctx, runner, config.AsyncPoolConfig{
		Workers:     1,
		QueueSize:   4,
		QueryFanout: 3,
	})

	job := types.PoolRefillJob{
		UserID:     "u-1",
		PoolType:   "short_term",
		QueryTexts: []string{"护肤"},
	}

	first := dispatcher.Enqueue(job)
	if !first.Enqueued || first.QueueResult != "queued" {
		t.Fatalf("expected first enqueue to be queued, got %+v", first)
	}

	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first run to start")
	}

	for idx := 0; idx < 3; idx++ {
		result := dispatcher.Enqueue(job)
		if !result.Deduped || result.QueueResult != "deduped" {
			t.Fatalf("expected duplicate enqueue to be deduped, got %+v", result)
		}
	}

	close(block)
	waitForCondition(t, 2*time.Second, func() bool {
		return len(runner.snapshotJobs()) == 1
	})
}

func TestAsyncPoolRefillDispatcherReplaysMergedQueries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	block := make(chan struct{})
	runner := &fakePoolRefillRunner{
		started: make(chan types.PoolRefillJob, 4),
		blocks:  []chan struct{}{block},
	}
	dispatcher := NewAsyncPoolRefillDispatcher(ctx, runner, config.AsyncPoolConfig{
		Workers:     1,
		QueueSize:   4,
		QueryFanout: 3,
	})

	firstJob := types.PoolRefillJob{
		UserID:     "u-2",
		PoolType:   "long_term",
		QueryTexts: []string{"日语"},
	}
	if result := dispatcher.Enqueue(firstJob); !result.Enqueued {
		t.Fatalf("expected first enqueue to be queued, got %+v", result)
	}

	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first run to start")
	}

	secondResult := dispatcher.Enqueue(types.PoolRefillJob{
		UserID:     "u-2",
		PoolType:   "long_term",
		QueryTexts: []string{"N2 语法"},
	})
	if !secondResult.Deduped || secondResult.QueueResult != "deduped" {
		t.Fatalf("expected running duplicate to be deduped, got %+v", secondResult)
	}

	close(block)

	select {
	case job := <-runner.started:
		if len(job.QueryTexts) != 2 || job.QueryTexts[0] != "日语" || job.QueryTexts[1] != "N2 语法" {
			t.Fatalf("expected replay job to merge latest queries, got %+v", job.QueryTexts)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for replay run")
	}

	waitForCondition(t, 2*time.Second, func() bool {
		return len(runner.snapshotJobs()) == 2
	})
}

func TestAsyncPoolRefillDispatcherDropsWhenQueueIsFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	block := make(chan struct{})
	runner := &fakePoolRefillRunner{
		started: make(chan types.PoolRefillJob, 4),
		blocks:  []chan struct{}{block},
	}
	dispatcher := NewAsyncPoolRefillDispatcher(ctx, runner, config.AsyncPoolConfig{
		Workers:     1,
		QueueSize:   1,
		QueryFanout: 3,
	})

	if result := dispatcher.Enqueue(types.PoolRefillJob{UserID: "u-1", PoolType: "short_term", QueryTexts: []string{"a"}}); !result.Enqueued {
		t.Fatalf("expected first job to be queued, got %+v", result)
	}
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first run to start")
	}

	second := dispatcher.Enqueue(types.PoolRefillJob{UserID: "u-2", PoolType: "short_term", QueryTexts: []string{"b"}})
	if !second.Enqueued || second.QueueResult != "queued" {
		t.Fatalf("expected second job to occupy queue, got %+v", second)
	}

	third := dispatcher.Enqueue(types.PoolRefillJob{UserID: "u-3", PoolType: "short_term", QueryTexts: []string{"c"}})
	if !third.Dropped || third.QueueResult != "dropped_queue_full" {
		t.Fatalf("expected third job to be dropped, got %+v", third)
	}

	close(block)
}

func waitForCondition(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
