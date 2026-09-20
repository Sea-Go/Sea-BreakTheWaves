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
	indexGraphAgentName  = "content_index"
	indexGraphNodeName   = "index_fixed_generation"
	indexGraphRequestKey = "content_index_request"
	indexGraphReceiptKey = "content_index_receipt"
)

var ErrIndexGraphOutput = errors.New("content index graph output missing or invalid")

// IndexService is the already-fenced deterministic coordinator borrowed by the
// framework node. GraphAgent and Runner do not own its backends or PG pool.
type IndexService interface {
	Index(context.Context, string, Fence, map[string]corpus.Ref) (IndexBuildResult, error)
}

type IndexGraphRequest struct {
	BuildID       string                `json:"build_id"`
	Fence         Fence                 `json:"fence"`
	ResumeIndexes map[string]corpus.Ref `json:"resume_indexes,omitempty"`
}

// IndexGraphReceipt is a bounded local READY receipt. Neither this receipt nor
// a DC technical ACK changes RTW build acceptance or its publication pointer.
type IndexGraphReceipt struct {
	BuildID       string                `json:"build_id"`
	ReleaseID     string                `json:"release_id"`
	Generation    int64                 `json:"generation"`
	ChunkManifest corpus.Ref            `json:"chunk_manifest"`
	Lanes         map[string]corpus.Ref `json:"lanes"`
	IndexManifest corpus.Ref            `json:"index_manifest"`
	State         string                `json:"state"`
	AttemptID     string                `json:"attempt_id"`
	LeaseEpoch    int64                 `json:"lease_epoch"`
	CancelVersion int64                 `json:"cancel_version"`
}

func validIndexFence(buildID string, fence Fence) bool {
	return buildID != "" && fence.BuildID == buildID && fence.AttemptID != "" &&
		fence.LeaseEpoch > 0 && fence.CancelVersion >= 0 && !fence.ExpiresAt.IsZero()
}

func copyResumeIndexes(resume map[string]corpus.Ref) (map[string]corpus.Ref, error) {
	copy := make(map[string]corpus.Ref, len(resume))
	for lane, ref := range resume {
		if lane != "dense" && lane != "sparse" && lane != "multivector" || !validRef(ref) {
			return nil, ErrInvalid
		}
		copy[lane] = ref
	}
	return copy, nil
}

// IndexGraphRunOption puts one immutable request in framework RuntimeState.
func IndexGraphRunOption(buildID string, fence Fence, resume map[string]corpus.Ref) (agent.RunOption, error) {
	if !validIndexFence(buildID, fence) {
		return nil, ErrInvalid
	}
	copy, err := copyResumeIndexes(resume)
	if err != nil {
		return nil, err
	}
	return agent.MergeRuntimeState(map[string]any{indexGraphRequestKey: IndexGraphRequest{
		BuildID: buildID, Fence: fence, ResumeIndexes: copy,
	}}), nil
}

func validIndexReceipt(receipt IndexGraphReceipt) bool {
	if receipt.BuildID == "" || receipt.ReleaseID == "" || receipt.Generation <= 0 ||
		receipt.State != "READY" || receipt.AttemptID == "" || receipt.LeaseEpoch <= 0 || receipt.CancelVersion < 0 ||
		!validRef(receipt.ChunkManifest) || !validRef(receipt.IndexManifest) || len(receipt.Lanes) != 3 {
		return false
	}
	for _, lane := range []string{"dense", "sparse", "multivector"} {
		if !validRef(receipt.Lanes[lane]) {
			return false
		}
	}
	return true
}

// NewIndexGraphAgent uses the framework's typed state schema and GraphAgent.
// It performs no second write after IndexCoordinator's PG READY commit.
func NewIndexGraphAgent(indexer IndexService) (*graphagent.GraphAgent, error) {
	if isNilIndexer(indexer) {
		return nil, fmt.Errorf("index graph service: %w", ErrInvalid)
	}
	schema := graph.NewStateSchema().
		AddField(indexGraphRequestKey, graph.StateField{Type: reflect.TypeOf(IndexGraphRequest{}), Reducer: graph.DefaultReducer}).
		AddField(indexGraphReceiptKey, graph.StateField{Type: reflect.TypeOf(IndexGraphReceipt{}), Reducer: graph.DefaultReducer})
	compiled, err := graph.NewStateGraph(schema).
		AddNode(indexGraphNodeName, func(ctx context.Context, state graph.State) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			request, ok := graph.GetStateValue[IndexGraphRequest](state, indexGraphRequestKey)
			if !ok || !validIndexFence(request.BuildID, request.Fence) {
				return nil, fmt.Errorf("index graph request: %w", ErrInvalid)
			}
			resume, err := copyResumeIndexes(request.ResumeIndexes)
			if err != nil {
				return nil, err
			}
			result, err := indexer.Index(ctx, request.BuildID, request.Fence, resume)
			if err != nil {
				return nil, fmt.Errorf("index fixed generation: %w", err)
			}
			receipt := IndexGraphReceipt{BuildID: result.BuildID, ReleaseID: result.ReleaseID,
				Generation: result.Generation, ChunkManifest: result.ChunkManifest, Lanes: result.Lanes,
				IndexManifest: result.IndexManifest, State: result.State, AttemptID: request.Fence.AttemptID,
				LeaseEpoch: request.Fence.LeaseEpoch, CancelVersion: request.Fence.CancelVersion}
			if receipt.BuildID != request.BuildID || !validIndexReceipt(receipt) {
				return nil, fmt.Errorf("index graph result differs from fixed build: %w", ErrIndexGraphOutput)
			}
			return graph.State{indexGraphReceiptKey: receipt}, nil
		}).
		SetEntryPoint(indexGraphNodeName).
		SetFinishPoint(indexGraphNodeName).
		Compile()
	if err != nil {
		return nil, fmt.Errorf("compile content index graph: %w", err)
	}
	ag, err := graphagent.New(indexGraphAgentName, compiled,
		graphagent.WithDescription("Index and reconcile one fixed content generation under a DC lease"))
	if err != nil {
		return nil, fmt.Errorf("construct content index graph agent: %w", err)
	}
	return ag, nil
}

// IndexGraphReceiptFromCompletion accepts only an actual framework Graph
// completion. The caller must still consume Runner through EOF and reject its
// terminal errors before using the receipt.
func IndexGraphReceiptFromCompletion(e *event.Event) (IndexGraphReceipt, bool, error) {
	if !graph.IsGraphCompletionEvent(e) {
		return IndexGraphReceipt{}, false, nil
	}
	raw, ok := e.StateDelta[indexGraphReceiptKey]
	if !ok {
		return IndexGraphReceipt{}, true, ErrIndexGraphOutput
	}
	var receipt IndexGraphReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return IndexGraphReceipt{}, true, fmt.Errorf("decode index graph receipt: %w", err)
	}
	if !validIndexReceipt(receipt) {
		return IndexGraphReceipt{}, true, ErrIndexGraphOutput
	}
	return receipt, true, nil
}
