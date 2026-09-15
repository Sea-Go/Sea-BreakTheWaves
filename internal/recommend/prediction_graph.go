package recommend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/graphagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
)

const (
	predictionGraphAgentName  = "recommend_prediction_candidate"
	predictionGraphNodeName   = "build_prediction_candidate"
	predictionGraphRequestKey = "recommend_prediction_request"
	predictionGraphResultKey  = "recommend_prediction_candidate"
	predictionGraphFailureKey = "recommend_prediction_failure"
)

var ErrPredictionGraphOutput = errors.New("recommend prediction graph output is invalid")

type PredictionCandidateBuilder interface {
	BuildCandidate(context.Context, PredictionCandidateRequest) (PredictionCandidate, error)
}

type predictionGraphFailure struct {
	Code string `json:"error_code"`
}

func predictionFailureState(err error) predictionGraphFailure {
	_, code := predictionCandidateOutcome(err)
	if code == "" {
		code = "PREDICTION_GRAPH_FAILED"
	}
	return predictionGraphFailure{Code: code}
}

func (f predictionGraphFailure) err() error {
	switch f.Code {
	case "PREDICTION_DISABLED":
		return ErrPredictionDisabled
	case "PREDICTION_CONTRACT":
		return ErrPredictionContract
	case "CANCELLED":
		return context.Canceled
	case "TIMEOUT":
		return context.DeadlineExceeded
	case "DC_PREDICTION_UNKNOWN":
		return datacenter.ErrPredictionOutcomeUnknown
	case "DC_PREDICTION_FAILED", "PREDICTION_GRAPH_FAILED":
		return ErrPredictionGraphOutput
	default:
		return ErrPredictionGraphOutput
	}
}

func PredictionGraphRunOption(request PredictionCandidateRequest) (agent.RunOption, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	return agent.MergeRuntimeState(map[string]any{predictionGraphRequestKey: request}), nil
}

func NewPredictionGraphAgent(builder PredictionCandidateBuilder) (*graphagent.GraphAgent, error) {
	if nilPredictionBuilder(builder) {
		return nil, ErrInvalid
	}
	schema := graph.NewStateSchema().
		AddField(predictionGraphRequestKey, graph.StateField{Type: reflect.TypeOf(PredictionCandidateRequest{}), Reducer: graph.DefaultReducer}).
		AddField(predictionGraphResultKey, graph.StateField{Type: reflect.TypeOf(PredictionCandidate{}), Reducer: graph.DefaultReducer}).
		AddField(predictionGraphFailureKey, graph.StateField{Type: reflect.TypeOf(predictionGraphFailure{}), Reducer: graph.DefaultReducer})
	compiled, err := graph.NewStateGraph(schema).
		AddNode(predictionGraphNodeName, func(ctx context.Context, state graph.State) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			request, ok := graph.GetStateValue[PredictionCandidateRequest](state, predictionGraphRequestKey)
			if !ok || request.validate() != nil {
				return graph.State{predictionGraphFailureKey: predictionFailureState(ErrInvalid)}, nil
			}
			candidate, err := builder.BuildCandidate(ctx, request)
			if err != nil {
				return graph.State{predictionGraphFailureKey: predictionFailureState(err)}, nil
			}
			if !validPredictionCandidate(candidate) || candidate.Subject != request.Subject ||
				candidate.Status != "candidate_default_off" || candidate.BusinessActivation != "none" {
				return graph.State{predictionGraphFailureKey: predictionFailureState(ErrPredictionContract)}, nil
			}
			return graph.State{predictionGraphResultKey: candidate}, nil
		}).
		SetEntryPoint(predictionGraphNodeName).
		SetFinishPoint(predictionGraphNodeName).
		Compile()
	if err != nil {
		return nil, fmt.Errorf("compile prediction candidate graph: %w", err)
	}
	ag, err := graphagent.New(predictionGraphAgentName, compiled,
		graphagent.WithDescription("Build one default-off recommendation prediction candidate"))
	if err != nil {
		return nil, fmt.Errorf("construct prediction candidate GraphAgent: %w", err)
	}
	return ag, nil
}

func nilPredictionBuilder(builder PredictionCandidateBuilder) bool {
	if builder == nil {
		return true
	}
	value := reflect.ValueOf(builder)
	return (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface || value.Kind() == reflect.Func) && value.IsNil()
}

func predictionCandidateFromCompletion(e *event.Event) (PredictionCandidate, bool, error) {
	if !graph.IsGraphCompletionEvent(e) {
		return PredictionCandidate{}, false, nil
	}
	if raw, ok := e.StateDelta[predictionGraphFailureKey]; ok {
		var failure predictionGraphFailure
		if err := json.Unmarshal(raw, &failure); err != nil {
			return PredictionCandidate{}, true, ErrPredictionGraphOutput
		}
		if failure.Code != "" {
			return PredictionCandidate{}, true, failure.err()
		}
	}
	raw, ok := e.StateDelta[predictionGraphResultKey]
	if !ok {
		return PredictionCandidate{}, true, ErrPredictionGraphOutput
	}
	var candidate PredictionCandidate
	if err := json.Unmarshal(raw, &candidate); err != nil || !validPredictionCandidate(candidate) {
		return PredictionCandidate{}, true, ErrPredictionGraphOutput
	}
	return candidate, true, nil
}

type PredictionGraphRuntime struct{ runtime *btwruntime.Runtime }

func NewPredictionGraphRuntime(app string, builder PredictionCandidateBuilder, sessions session.Service,
	observed *telemetry.Bundle) (*PredictionGraphRuntime, error) {
	ag, err := NewPredictionGraphAgent(builder)
	if err != nil {
		return nil, err
	}
	runtime, err := btwruntime.New(app, ag, sessions, observed)
	if err != nil {
		return nil, err
	}
	return &PredictionGraphRuntime{runtime: runtime}, nil
}

type PredictionGraphRequest struct {
	Candidate PredictionCandidateRequest
	SessionID string
	RunID     string
}

// Run consumes the tRPC-Agent-Go event stream through Runner completion. A
// Graph result is only a default-off candidate and never mutates recommend or
// usermodel business pointers.
func (r *PredictionGraphRuntime) Run(ctx context.Context, request PredictionGraphRequest) (PredictionCandidate, error) {
	if r == nil || r.runtime == nil || request.SessionID == "" || request.RunID == "" {
		return PredictionCandidate{}, ErrInvalid
	}
	option, err := PredictionGraphRunOption(request.Candidate)
	if err != nil {
		return PredictionCandidate{}, err
	}
	var candidate PredictionCandidate
	var completions int
	result, err := r.runtime.Run(ctx, btwruntime.Request{
		Subject: btwruntime.SubjectRef{AuthorityID: request.Candidate.Subject.Issuer,
			TenantID: request.Candidate.Subject.Realm, SubjectID: request.Candidate.Subject.UID},
		SessionID: request.SessionID, RunID: request.RunID,
		Message: model.NewUserMessage("build_prediction_candidate"), Options: []agent.RunOption{option},
	}, func(_ context.Context, event *event.Event) error {
		got, complete, extractErr := predictionCandidateFromCompletion(event)
		if extractErr != nil {
			return extractErr
		}
		if complete {
			completions++
			candidate = got
		}
		return nil
	})
	if err != nil {
		return PredictionCandidate{}, err
	}
	if !result.Completed || completions != 1 || !validPredictionCandidate(candidate) ||
		candidate.Subject != request.Candidate.Subject || candidate.BusinessActivation != "none" {
		return PredictionCandidate{}, ErrPredictionGraphOutput
	}
	return candidate, nil
}

func (r *PredictionGraphRuntime) Close() error {
	if r == nil || r.runtime == nil {
		return nil
	}
	return r.runtime.Close()
}
