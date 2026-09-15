package search

import (
	"context"
	"encoding/json"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
)

type modelInvocationKey struct{}

// ModelInvocationRef is fixed by RTW's signed product operation, never by the
// model prompt. A physical model call and its retry share this exact scope.
type ModelInvocationRef struct {
	AuthorityID string `json:"authority_id"`
	TenantID    string `json:"tenant_id"`
	SubjectID   string `json:"subject_id"`
	SearchID    string `json:"search_id"`
	AnswerID    string `json:"answer_id"`
	SnapshotSHA string `json:"snapshot_sha256"`
}

func (r ModelInvocationRef) valid() bool {
	return r.AuthorityID != "" && r.TenantID != "" && r.SubjectID != "" &&
		r.SearchID != "" && r.AnswerID != "" && artifacts.ValidHash(r.SnapshotSHA)
}

// WithModelInvocationRef is the typed request-context seam used by the root
// Graph and by the cmd/api DataCenter model adapter. Missing scope fails
// before any external model request.
func WithModelInvocationRef(ctx context.Context, ref ModelInvocationRef) (context.Context, error) {
	if ctx == nil || !ref.valid() {
		return nil, ErrInvalid
	}
	return context.WithValue(ctx, modelInvocationKey{}, ref), nil
}

func ModelInvocationFromContext(ctx context.Context) (ModelInvocationRef, bool) {
	if ctx == nil {
		return ModelInvocationRef{}, false
	}
	ref, ok := ctx.Value(modelInvocationKey{}).(ModelInvocationRef)
	return ref, ok && ref.valid()
}

func invocationForSummary(q SummaryRequest) (ModelInvocationRef, error) {
	raw, err := json.Marshal(q.Search.Snapshot)
	if err != nil {
		return ModelInvocationRef{}, err
	}
	return ModelInvocationRef{AuthorityID: q.Subject.AuthorityID,
		TenantID: q.Subject.TenantID, SubjectID: q.Subject.SubjectID,
		SearchID: q.SearchID, AnswerID: q.AnswerID,
		SnapshotSHA: artifacts.Hash(raw)}, nil
}

// invocationForTool binds one signed child search and its durable parent
// operation to the same typed model seam used by Summary. The legacy
// TenantID field is the fixed v1 compatibility slot, not a product tenant.
func invocationForTool(q ToolRunRequest) (ModelInvocationRef, error) {
	if !validToolRunRequest(q) {
		return ModelInvocationRef{}, ErrInvalid
	}
	raw, err := json.Marshal(q.Search.Snapshot)
	if err != nil {
		return ModelInvocationRef{}, err
	}
	return ModelInvocationRef{AuthorityID: q.Subject.AuthorityID,
		TenantID: q.Subject.TenantID, SubjectID: q.Subject.SubjectID,
		SearchID: q.SearchID, AnswerID: q.OperationID,
		SnapshotSHA: artifacts.Hash(raw)}, nil
}
