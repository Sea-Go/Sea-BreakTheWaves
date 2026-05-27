// Package graphutil 提供 trpc-agent-go graph 的同步执行封装。
// graphutil 用来避免和框架的 graph 包产生命名冲突。
package graphutil

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	graph "trpc.group/trpc-go/trpc-agent-go/graph"
)

// RunGraph 同步编译后的 Graph，是 trpc-agent-go executor.Execute 的标准同步封装。
// 适合无需流式/checkpoint 的请求-响应场景。
func RunGraph(ctx context.Context, g *graph.Graph, initialState graph.State) (graph.State, error) {
	executor, err := graph.NewExecutor(g)
	if err != nil {
		return nil, fmt.Errorf("graph: create executor: %w", err)
	}

	inv := &agent.Invocation{
		InvocationID: uuid.NewString(),
	}
	eventChan, err := executor.Execute(ctx, initialState, inv)
	if err != nil {
		return nil, fmt.Errorf("graph: execute: %w", err)
	}

	finalState := make(graph.State)
	for evt := range eventChan {
		if evt.StateDelta == nil {
			continue
		}
		for key, valueBytes := range evt.StateDelta {
			switch key {
			case graph.MetadataKeyNode, graph.MetadataKeyPregel,
				graph.MetadataKeyChannel, graph.MetadataKeyState,
				graph.MetadataKeyCompletion:
				continue
			}
			var value any
			if err := json.Unmarshal(valueBytes, &value); err != nil {
				continue
			}
			finalState[key] = value
		}
		if evt.Done {
			break
		}
	}
	return finalState, nil
}
