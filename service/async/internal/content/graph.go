package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/graphagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/graph"
)

const (
	prepareGraphAgentName  = "content_prepare"
	prepareGraphNodeName   = "prepare_chunks"
	prepareGraphRequestKey = "content_prepare_request"
	prepareGraphReceiptKey = "content_prepare_receipt"
)

var ErrPrepareGraphOutput = errors.New("content prepare graph output missing or invalid")

// PrepareService is the deterministic content operation borrowed by the graph.
// The graph does not own its source, object store, database pool or lifecycle.
type PrepareService interface {
	Prepare(context.Context, BuildInput, Fence) (Prepared, error)
}

// PrepareGraphRequest is one fixed DC attempt and immutable RTW build input.
// The caller must not mutate its Revisions after passing it to the graph.
type PrepareGraphRequest struct {
	Input BuildInput `json:"input"`
	Fence Fence      `json:"fence"`
}

// PrepareGraphReceipt is the bounded graph output. It is evidence that chunks
// were prepared and recorded under the fixed attempt, not that lanes are READY
// or that a release was published. Consumers verify Ref against authoritative
// Store/object state before proceeding. Chunk text never enters Graph state.
type PrepareGraphReceipt struct {
	BuildID       string     `json:"build_id"`
	ReleaseID     string     `json:"release_id"`
	Generation    int64      `json:"generation"`
	OperationID   string     `json:"operation_id"`
	ChunkManifest corpus.Ref `json:"chunk_manifest"`
	ChunkCount    int        `json:"chunk_count"`
	State         string     `json:"state"`
	AttemptID     string     `json:"attempt_id"`
	LeaseEpoch    int64      `json:"lease_epoch"`
	CancelVersion int64      `json:"cancel_version"`
}

// PrepareGraphRunOption injects a per-run typed request using the framework's
// public RuntimeState API. It copies and normalizes Revisions before returning;
// MergeRuntimeState preserves other run state installed by the caller.
func PrepareGraphRunOption(input BuildInput, fence Fence) (agent.RunOption, error) {
	input, err := normalizeInput(input)
	if err != nil {
		return nil, fmt.Errorf("prepare graph input: %w", err)
	}
	if fence.BuildID != input.BuildID || fence.AttemptID == "" || fence.LeaseEpoch <= 0 ||
		fence.CancelVersion < 0 || fence.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("prepare graph fence: %w", ErrInvalid)
	}
	request := PrepareGraphRequest{Input: input, Fence: fence}
	return agent.MergeRuntimeState(map[string]any{prepareGraphRequestKey: request}), nil
}

// NewPrepareGraphAgent compiles a real tRPC-Agent-Go GraphAgent with one
// deterministic node. The caller owns Runner and provides one request per Run;
// neither GraphAgent nor this constructor starts a worker or publishes a build.
func NewPrepareGraphAgent(preparer PrepareService) (*graphagent.GraphAgent, error) {
	if preparer == nil {
		return nil, fmt.Errorf("prepare graph service: %w", ErrInvalid)
	}
	value := reflect.ValueOf(preparer)
	if (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) && value.IsNil() {
		return nil, fmt.Errorf("prepare graph service: %w", ErrInvalid)
	}
	schema := graph.NewStateSchema().
		AddField(prepareGraphRequestKey, graph.StateField{
			Type: reflect.TypeOf(PrepareGraphRequest{}), Reducer: graph.DefaultReducer,
		}).
		AddField(prepareGraphReceiptKey, graph.StateField{
			Type: reflect.TypeOf(PrepareGraphReceipt{}), Reducer: graph.DefaultReducer,
		})
	compiled, err := graph.NewStateGraph(schema).
		AddNode(prepareGraphNodeName, func(ctx context.Context, state graph.State) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			request, ok := graph.GetStateValue[PrepareGraphRequest](state, prepareGraphRequestKey)
			if !ok {
				return nil, fmt.Errorf("prepare graph state %q: %w", prepareGraphRequestKey, ErrInvalid)
			}
			input, err := normalizeInput(request.Input)
			if err != nil {
				return nil, fmt.Errorf("prepare graph input: %w", err)
			}
			fence := request.Fence
			if fence.BuildID != input.BuildID || fence.AttemptID == "" || fence.LeaseEpoch <= 0 ||
				fence.CancelVersion < 0 || fence.ExpiresAt.IsZero() {
				return nil, fmt.Errorf("prepare graph fence: %w", ErrInvalid)
			}
			prepared, err := preparer.Prepare(ctx, input, fence)
			if err != nil {
				return nil, fmt.Errorf("prepare chunks: %w", err)
			}
			if !validRef(prepared.Ref) || prepared.Build.Chunks == nil || *prepared.Build.Chunks != prepared.Ref ||
				prepared.Build.State != "BUILDING" || prepared.Build.BuildInput.BuildID != input.BuildID ||
				prepared.Build.ModuleID != input.ModuleID || prepared.Build.ReleaseID != input.ReleaseID ||
				prepared.Build.Generation != input.Generation ||
				prepared.Build.OperationID != input.OperationID || prepared.Build.InputHash != input.InputHash ||
				!reflect.DeepEqual(prepared.Build.Revisions, input.Revisions) || prepared.Build.Fence.BuildID != fence.BuildID ||
				prepared.Build.AttemptID != fence.AttemptID || prepared.Build.LeaseEpoch != fence.LeaseEpoch ||
				prepared.Build.CancelVersion != fence.CancelVersion || !prepared.Build.ExpiresAt.Equal(fence.ExpiresAt) ||
				prepared.Manifest.ModuleID != input.ModuleID || prepared.Manifest.ReleaseID != input.ReleaseID ||
				prepared.Manifest.InputManifestHash != input.InputHash || len(prepared.Manifest.Chunks) == 0 {
				return nil, fmt.Errorf("prepare graph receipt differs from fixed attempt: %w", ErrConflict)
			}
			receipt := PrepareGraphReceipt{BuildID: input.BuildID, ReleaseID: input.ReleaseID,
				Generation: input.Generation, OperationID: input.OperationID, ChunkManifest: prepared.Ref,
				ChunkCount: len(prepared.Manifest.Chunks), State: prepared.Build.State,
				AttemptID: fence.AttemptID, LeaseEpoch: fence.LeaseEpoch, CancelVersion: fence.CancelVersion}
			return graph.State{prepareGraphReceiptKey: receipt}, nil
		}).
		SetEntryPoint(prepareGraphNodeName).
		SetFinishPoint(prepareGraphNodeName).
		Compile()
	if err != nil {
		return nil, fmt.Errorf("compile content prepare graph: %w", err)
	}
	ag, err := graphagent.New(prepareGraphAgentName, compiled,
		graphagent.WithDescription("Prepare fixed content chunks under one DC lease"))
	if err != nil {
		return nil, fmt.Errorf("construct content prepare graph agent: %w", err)
	}
	return ag, nil
}

// PrepareGraphReceiptFromCompletion extracts the bounded result only from a
// real Graph completion event. The caller must still consume through Runner
// completion and reject any terminal error; Graph completion alone is not a
// successful run receipt.
func PrepareGraphReceiptFromCompletion(e *event.Event) (PrepareGraphReceipt, bool, error) {
	if !graph.IsGraphCompletionEvent(e) {
		return PrepareGraphReceipt{}, false, nil
	}
	raw, ok := e.StateDelta[prepareGraphReceiptKey]
	if !ok {
		return PrepareGraphReceipt{}, true, ErrPrepareGraphOutput
	}
	var receipt PrepareGraphReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return PrepareGraphReceipt{}, true, fmt.Errorf("decode prepare graph receipt: %w", err)
	}
	if receipt.BuildID == "" || receipt.ReleaseID == "" || receipt.Generation <= 0 ||
		receipt.OperationID == "" || !validRef(receipt.ChunkManifest) || receipt.ChunkCount <= 0 ||
		receipt.State != "BUILDING" || receipt.AttemptID == "" || receipt.LeaseEpoch <= 0 || receipt.CancelVersion < 0 {
		return PrepareGraphReceipt{}, true, ErrPrepareGraphOutput
	}
	return receipt, true, nil
}
