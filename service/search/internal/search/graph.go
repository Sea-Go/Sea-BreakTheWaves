package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/graphagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/graph"
)

const (
	searchGraphRequestKey = "search_request"
	searchGraphResultKey  = "search_result"
	searchGraphNodeName   = "execute_search"
)

var ErrSearchGraphOutput = errors.New("search graph output missing or invalid")

// SearchGraphRunOption fixes one request to the framework run state. It does
// not authorize a release or resolve its current publication state; the caller
// must obtain the snapshot from the authoritative publication owner first.
func SearchGraphRunOption(request Request) (agent.RunOption, error) {
	if strings.TrimSpace(request.Query) == "" || (request.Depth != Fast && request.Depth != Detailed) ||
		(request.Intelligence != Low && request.Intelligence != Medium && request.Intelligence != High) ||
		!validSnapshot(request.Snapshot) {
		return nil, ErrInvalid
	}
	request.Snapshot = cloneSnapshot(request.Snapshot)
	return agent.MergeRuntimeState(map[string]any{searchGraphRequestKey: request}), nil
}

// NewSearchGraphAgent places the deterministic three-lane search operation in
// a real tRPC-Agent-Go GraphAgent. The service owns policy and retrieval; the
// caller owns Runner, Session, telemetry and request authentication.
func NewSearchGraphAgent(service *Service) (*graphagent.GraphAgent, error) {
	if service == nil {
		return nil, ErrInvalid
	}
	schema := graph.NewStateSchema().
		AddField(searchGraphRequestKey, graph.StateField{Type: reflect.TypeOf(Request{}), Reducer: graph.DefaultReducer}).
		AddField(searchGraphResultKey, graph.StateField{Type: reflect.TypeOf(Result{}), Reducer: graph.DefaultReducer})
	compiled, err := graph.NewStateGraph(schema).
		AddNode(searchGraphNodeName, func(ctx context.Context, state graph.State) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			request, ok := graph.GetStateValue[Request](state, searchGraphRequestKey)
			if !ok {
				return nil, fmt.Errorf("search graph request state: %w", ErrInvalid)
			}
			request.Snapshot = cloneSnapshot(request.Snapshot)
			result, err := service.Execute(ctx, request)
			if err != nil {
				return nil, fmt.Errorf("execute fixed search: %w", err)
			}
			if !validGraphResult(result, request) {
				return nil, fmt.Errorf("search graph result differs from fixed request: %w", ErrSearchGraphOutput)
			}
			return graph.State{searchGraphResultKey: result}, nil
		}).
		SetEntryPoint(searchGraphNodeName).
		SetFinishPoint(searchGraphNodeName).
		Compile()
	if err != nil {
		return nil, fmt.Errorf("compile search graph: %w", err)
	}
	return graphagent.New("search_execute", compiled, graphagent.WithDescription("Execute fixed-snapshot three-lane search"))
}

// SearchGraphResultFromCompletion only decodes a Graph completion. The caller
// must supply its original fixed request, consume the Runner completion, and
// reject terminal errors before delivering any public evidence.
func SearchGraphResultFromCompletion(e *event.Event, expected Request) (Result, bool, error) {
	if !graph.IsGraphCompletionEvent(e) {
		return Result{}, false, nil
	}
	raw, ok := e.StateDelta[searchGraphResultKey]
	if !ok {
		return Result{}, true, ErrSearchGraphOutput
	}
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return Result{}, true, fmt.Errorf("decode search graph result: %w", err)
	}
	if !validGraphResult(result, expected) {
		return Result{}, true, ErrSearchGraphOutput
	}
	return result, true, nil
}

func validGraphResult(result Result, expected Request) bool {
	if strings.TrimSpace(expected.Query) == "" || (expected.Depth != Fast && expected.Depth != Detailed) ||
		(expected.Intelligence != Low && expected.Intelligence != Medium && expected.Intelligence != High) ||
		(result.Status != "complete" && result.Status != "partial" && result.Status != "empty") ||
		result.StopReason == "" || !validSnapshot(expected.Snapshot) ||
		!reflect.DeepEqual(result.Snapshot, expected.Snapshot) || result.Profile.PolicyVersion == "" ||
		result.Profile.RequestedDepth != expected.Depth || result.Profile.EffectiveDepth != expected.Depth ||
		result.Profile.RequestedIntelligence != expected.Intelligence ||
		(result.Profile.EffectiveIntelligence != Low && result.Profile.EffectiveIntelligence != Medium && result.Profile.EffectiveIntelligence != High) ||
		result.UsedSubqueries < 0 || (result.Status == "empty" && len(result.Verified) != 0) ||
		(result.Status != "empty" && len(result.Verified) == 0) {
		return false
	}
	if result.Profile.EffectiveIntelligence != expected.Intelligence &&
		(!expected.AllowLowerIntelligence || result.Profile.ChangeReason == "") {
		return false
	}
	for _, candidate := range result.Candidates {
		if candidate.Chunk.Text != "" {
			return false
		}
	}
	for _, candidate := range result.Verified {
		if candidate.Chunk.Text != "" {
			return false
		}
	}
	return true
}
