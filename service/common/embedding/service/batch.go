package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/openai/openai-go/v3"
)

const (
	defaultEmbeddingWorkerCount   = 6
	defaultEmbeddingRateLimitPerS = 30
)

type BatchTask struct {
	ID    string
	Kind  string // text / image / multi_images
	Input string
}

type batchResult struct {
	id     string
	vector []float32
	err    error
}

func BatchVectors(ctx context.Context, tasks []BatchTask) (map[string][]float32, error) {
	if len(tasks) == 0 {
		return map[string][]float32{}, nil
	}

	workerCount := defaultEmbeddingWorkerCount
	if len(tasks) < workerCount {
		workerCount = len(tasks)
	}
	if workerCount <= 0 {
		workerCount = 1
	}

	taskCh := make(chan BatchTask)
	resultCh := make(chan batchResult, len(tasks))
	ticker := time.NewTicker(time.Second / defaultEmbeddingRateLimitPerS)
	defer ticker.Stop()

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range taskCh {
				select {
				case <-ctx.Done():
					resultCh <- batchResult{id: task.ID, err: ctx.Err()}
					continue
				case <-ticker.C:
				}

				vector, err := vectorForTask(ctx, task)
				resultCh <- batchResult{id: task.ID, vector: vector, err: err}
			}
		}()
	}

	go func() {
		defer close(taskCh)
		for _, task := range tasks {
			select {
			case <-ctx.Done():
				return
			case taskCh <- task:
			}
		}
	}()

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	vectors := make(map[string][]float32, len(tasks))
	var firstErr error
	for result := range resultCh {
		if result.err != nil && firstErr == nil {
			firstErr = result.err
		}
		if result.err == nil {
			vectors[result.id] = result.vector
		}
	}

	if firstErr != nil {
		return nil, firstErr
	}
	return vectors, nil
}

func vectorForTask(ctx context.Context, task BatchTask) ([]float32, error) {
	switch task.Kind {
	case "", "text":
		return TextVector(ctx, task.Input)
	case "image", "multi_images":
		return GraphVector(ctx, task.Kind, task.Input)
	default:
		return nil, fmt.Errorf("unsupported embedding task kind: %s", task.Kind)
	}
}

func GraphVector(ctx context.Context, kind string, input string) ([]float32, error) {
	res, err := EmbeddingGraph(kind, input)
	if err != nil {
		return nil, err
	}
	return embeddingResponseToFloat32(res)
}

func embeddingResponseToFloat32(res *openai.CreateEmbeddingResponse) ([]float32, error) {
	if res == nil || len(res.Data) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}

	vec64 := res.Data[0].Embedding
	vec32 := make([]float32, 0, len(vec64))
	for _, value := range vec64 {
		vec32 = append(vec32, float32(value))
	}
	return vec32, nil
}
