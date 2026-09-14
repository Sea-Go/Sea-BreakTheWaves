package usermodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

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
	factGraphAgentName  = "usermodel_fact"
	factGraphNodeName   = "commit_fact"
	factGraphRequestKey = "usermodel_fact_request"
	factGraphReceiptKey = "usermodel_fact_receipt"
	factGraphFailureKey = "usermodel_fact_failure"
)

var ErrFactGraphOutput = errors.New("user fact graph receipt missing or invalid")
var ErrFactGraphCommit = errors.New("user fact domain commit failed")

// FactGraphFailure is a bounded Graph outcome, not an accepted receipt. The
// locked framework mutates error events concurrently on its traced error path;
// domain failures therefore travel in state and are rejected by Runtime's
// event sink. Store's own stage still records the precise failure cause.
type FactGraphFailure struct {
	Code string `json:"error_code"`
}

func factGraphFailure(err error) FactGraphFailure {
	switch {
	case errors.Is(err, ErrConflict):
		return FactGraphFailure{Code: "FACT_CONFLICT"}
	case errors.Is(err, ErrInvalid):
		return FactGraphFailure{Code: "INVALID_FACT"}
	case errors.Is(err, ErrPending):
		return FactGraphFailure{Code: "EVIDENCE_PENDING"}
	case errors.Is(err, context.Canceled):
		return FactGraphFailure{Code: "CANCELLED"}
	case errors.Is(err, context.DeadlineExceeded):
		return FactGraphFailure{Code: "DEADLINE_EXCEEDED"}
	default:
		return FactGraphFailure{Code: "STORE_ERROR"}
	}
}

func (f FactGraphFailure) err() error {
	switch f.Code {
	case "FACT_CONFLICT":
		return ErrConflict
	case "INVALID_FACT":
		return ErrInvalid
	case "EVIDENCE_PENDING":
		return ErrPending
	case "CANCELLED":
		return context.Canceled
	case "DEADLINE_EXCEEDED":
		return context.DeadlineExceeded
	case "STORE_ERROR":
		return ErrFactGraphCommit
	default:
		return ErrFactGraphOutput
	}
}

// FactAppender is the single domain commit point borrowed by the Graph node.
// The production assembly supplies a Store, whose Append transaction includes
// the accepted version and Outbox. Graph execution does not own the PG pool.
type FactAppender interface {
	Append(context.Context, Event) (Receipt, error)
}

// FactGraphReceipt is a bounded result of the committed domain operation.
// Pending dependencies have no accepted version, even if the subject already
// has an unrelated current state version. A replay retains the original status.
type FactGraphReceipt struct {
	Subject SubjectRef `json:"subject_ref"`
	EventKey
	NormalizedHash  string `json:"normalized_hash"`
	Status          string `json:"status"`
	AcceptedVersion int64  `json:"accepted_version,omitempty"`
	Replay          bool   `json:"replay"`
}

// FactGraphRunOption copies a trusted, producer-bound event into this Run's
// framework RuntimeState. The caller must bind SubjectRef and Producer before
// calling it; neither a model message nor a client can select those fields.
func FactGraphRunOption(input Event) (agent.RunOption, error) {
	e, _, _, err := normalized(input, true)
	if err != nil {
		return nil, fmt.Errorf("fact graph input: %w", err)
	}
	if e.Supersedes != nil {
		key := *e.Supersedes
		e.Supersedes = &key
	}
	return agent.MergeRuntimeState(map[string]any{factGraphRequestKey: e}), nil
}

// NewFactGraphAgent compiles the locked tRPC-Agent-Go GraphAgent. The only node
// delegates to the authoritative transaction; it never synthesizes acceptance
// from a model response or an application telemetry stage.
func NewFactGraphAgent(writer FactAppender) (*graphagent.GraphAgent, error) {
	if writer == nil {
		return nil, fmt.Errorf("fact graph writer: %w", ErrInvalid)
	}
	value := reflect.ValueOf(writer)
	if (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface || value.Kind() == reflect.Func) && value.IsNil() {
		return nil, fmt.Errorf("fact graph writer: %w", ErrInvalid)
	}
	schema := graph.NewStateSchema().
		AddField(factGraphRequestKey, graph.StateField{Type: reflect.TypeOf(Event{}), Reducer: graph.DefaultReducer}).
		AddField(factGraphReceiptKey, graph.StateField{Type: reflect.TypeOf(FactGraphReceipt{}), Reducer: graph.DefaultReducer}).
		AddField(factGraphFailureKey, graph.StateField{Type: reflect.TypeOf(FactGraphFailure{}), Reducer: graph.DefaultReducer})
	compiled, err := graph.NewStateGraph(schema).
		AddNode(factGraphNodeName, func(ctx context.Context, state graph.State) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			input, ok := graph.GetStateValue[Event](state, factGraphRequestKey)
			if !ok {
				return nil, fmt.Errorf("fact graph request state: %w", ErrInvalid)
			}
			e, hash, _, err := normalized(input, true)
			if err != nil {
				return nil, fmt.Errorf("fact graph input: %w", err)
			}
			committed, err := writer.Append(ctx, e)
			if err != nil {
				return graph.State{factGraphFailureKey: factGraphFailure(err)}, nil
			}
			if committed.Subject != e.Subject || committed.EventKey != e.EventKey || committed.NormalizedHash != hash {
				return graph.State{factGraphFailureKey: factGraphFailure(ErrConflict)}, nil
			}
			receipt := FactGraphReceipt{Subject: committed.Subject, EventKey: committed.EventKey,
				NormalizedHash: committed.NormalizedHash, Status: committed.Status, Replay: committed.Replay}
			switch committed.Status {
			case "accepted":
				if committed.StateVersion <= 0 {
					return graph.State{factGraphFailureKey: FactGraphFailure{Code: "INVALID_RECEIPT"}}, nil
				}
				receipt.AcceptedVersion = committed.StateVersion
			case "pending_dependency":
				// Store.StateVersion may be the version of an earlier fact.
			default:
				return graph.State{factGraphFailureKey: FactGraphFailure{Code: "INVALID_RECEIPT"}}, nil
			}
			return graph.State{factGraphReceiptKey: receipt}, nil
		}).
		SetEntryPoint(factGraphNodeName).
		SetFinishPoint(factGraphNodeName).
		Compile()
	if err != nil {
		return nil, fmt.Errorf("compile user fact graph: %w", err)
	}
	ag, err := graphagent.New(factGraphAgentName, compiled,
		graphagent.WithDescription("Commit one trusted user fact and its Outbox record"))
	if err != nil {
		return nil, fmt.Errorf("construct user fact graph agent: %w", err)
	}
	return ag, nil
}

// FactGraphReceiptFromCompletion reads only a genuine Graph completion. The
// caller must also see Runner completion without a terminal error before
// exposing this receipt; a Graph completion alone is not a run-level ACK.
func FactGraphReceiptFromCompletion(e *event.Event) (FactGraphReceipt, bool, error) {
	if !graph.IsGraphCompletionEvent(e) {
		return FactGraphReceipt{}, false, nil
	}
	if raw, ok := e.StateDelta[factGraphFailureKey]; ok {
		var failure FactGraphFailure
		if err := json.Unmarshal(raw, &failure); err != nil {
			return FactGraphReceipt{}, true, ErrFactGraphOutput
		}
		// Graph schema includes a zero-value state field at completion even
		// when the node wrote only a successful receipt.
		if failure.Code != "" {
			return FactGraphReceipt{}, true, failure.err()
		}
	}
	raw, ok := e.StateDelta[factGraphReceiptKey]
	if !ok {
		return FactGraphReceipt{}, true, ErrFactGraphOutput
	}
	var receipt FactGraphReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return FactGraphReceipt{}, true, fmt.Errorf("decode user fact receipt: %w", err)
	}
	if !receipt.Subject.valid() || !receipt.EventKey.valid() || !digest.MatchString(receipt.NormalizedHash) ||
		(receipt.Status != "accepted" && receipt.Status != "pending_dependency") ||
		(receipt.Status == "accepted" && receipt.AcceptedVersion <= 0) ||
		(receipt.Status == "pending_dependency" && receipt.AcceptedVersion != 0) {
		return FactGraphReceipt{}, true, ErrFactGraphOutput
	}
	return receipt, true, nil
}

// FactGraphRuntime owns a framework Runner, but borrows Store, Session service
// and process telemetry. Close it before closing those borrowed dependencies.
type FactGraphRuntime struct{ runtime *btwruntime.Runtime }

func NewFactGraphRuntime(app string, store *Store, sessions session.Service, observed *telemetry.Bundle) (*FactGraphRuntime, error) {
	if store == nil || store.db == nil || observed == nil || store.telemetry != observed {
		return nil, fmt.Errorf("fact graph store: %w", ErrInvalid)
	}
	ag, err := NewFactGraphAgent(store)
	if err != nil {
		return nil, err
	}
	r, err := btwruntime.New(app, ag, sessions, observed)
	if err != nil {
		return nil, err
	}
	return &FactGraphRuntime{runtime: r}, nil
}

type FactGraphRequest struct {
	Event     Event
	SessionID string
	RunID     string
}

// Append consumes the real Runner stream through EOF. A visible receipt needs
// exactly one Graph completion, Runner completion, no terminal/runtime error,
// and the same fixed SubjectRef/event/hash. On cancellation or uncertain stream
// completion it returns no ACK; the producer retries the same idempotent event.
func (g *FactGraphRuntime) Append(ctx context.Context, q FactGraphRequest) (FactGraphReceipt, error) {
	if g == nil || g.runtime == nil {
		return FactGraphReceipt{}, fmt.Errorf("fact graph runtime: %w", ErrInvalid)
	}
	_, hash, _, err := normalized(q.Event, true)
	if err != nil {
		return FactGraphReceipt{}, err
	}
	option, err := FactGraphRunOption(q.Event)
	if err != nil {
		return FactGraphReceipt{}, err
	}
	var receipt FactGraphReceipt
	var completions int
	result, err := g.runtime.Run(ctx, btwruntime.Request{
		Subject: btwruntime.SubjectRef{AuthorityID: q.Event.Subject.AuthorityID,
			TenantID: q.Event.Subject.TenantID, SubjectID: q.Event.Subject.SubjectID},
		SessionID: q.SessionID, RunID: q.RunID, Message: model.NewUserMessage("record_user_fact"),
		Options: []agent.RunOption{option},
	}, func(_ context.Context, e *event.Event) error {
		got, complete, decodeErr := FactGraphReceiptFromCompletion(e)
		if decodeErr != nil {
			return decodeErr
		}
		if complete {
			completions++
			receipt = got
		}
		return nil
	})
	if err != nil {
		return FactGraphReceipt{}, err
	}
	if !result.Completed || completions != 1 || receipt.Subject != q.Event.Subject ||
		receipt.EventKey != q.Event.EventKey || receipt.NormalizedHash != hash {
		return FactGraphReceipt{}, ErrFactGraphOutput
	}
	return receipt, nil
}

func (g *FactGraphRuntime) Close() error {
	if g == nil || g.runtime == nil {
		return nil
	}
	return g.runtime.Close()
}
