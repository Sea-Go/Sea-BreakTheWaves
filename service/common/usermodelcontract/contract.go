// Package usermodelcontract holds the stable cross-service value types shared
// by Async's user-model owner and Recommend's read-side adapters. Store,
// event-ledger and projection implementations remain private to Async.
package usermodelcontract

import (
	"context"
	"errors"
	"regexp"
)

var (
	ErrInvalid  = errors.New("invalid user fact")
	ErrConflict = errors.New("user fact conflict")
	ErrNotFound = errors.New("user fact not found")
	ErrPending  = errors.New("user fact dependency pending")
)

var (
	token  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/@-]{0,191}$`)
	digest = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type SubjectRef struct {
	AuthorityID string `json:"authority_id"`
	TenantID    string `json:"tenant_id"`
	SubjectID   string `json:"subject_id"`
}

func (s SubjectRef) Valid() bool {
	return token.MatchString(s.AuthorityID) && token.MatchString(s.TenantID) && token.MatchString(s.SubjectID)
}

type EventKey struct {
	Producer string `json:"producer"`
	EventID  string `json:"event_id"`
}

func (k EventKey) Valid() bool { return token.MatchString(k.Producer) && token.MatchString(k.EventID) }

type Action string

const (
	Assert  Action = "assert"
	Correct Action = "correct"
	Retract Action = "retract"
)

type SemanticKind string

const (
	ProductAction SemanticKind = "product_action"
	Reading       SemanticKind = "reading"
	Impression    SemanticKind = "impression"
	LocalSemantic SemanticKind = "local_semantic"
	SelfReport    SemanticKind = "self_report"
)

type Watermark struct {
	Producer           string `json:"producer"`
	SourcePartition    string `json:"source_partition"`
	ContiguousSequence int64  `json:"contiguous_sequence"`
	MaxSeenSequence    int64  `json:"max_seen_sequence"`
	Complete           bool   `json:"complete"`
}

type PairRef struct {
	PairID             string `json:"pair_id"`
	SpaceID            string `json:"space_id"`
	EncoderID          string `json:"encoder_id"`
	Kind               string `json:"kind"`
	FeatureSpecVersion string `json:"feature_spec_version"`
	FeatureSpecHash    string `json:"feature_spec_hash"`
	Dimension          int    `json:"dimension"`
	Metric             string `json:"metric"`
}

func (p PairRef) Valid() bool {
	return token.MatchString(p.PairID) && token.MatchString(p.SpaceID) &&
		token.MatchString(p.EncoderID) && token.MatchString(p.FeatureSpecVersion) &&
		digest.MatchString(p.FeatureSpecHash) && p.Dimension > 0 && p.Dimension <= 4096 &&
		(p.Kind == "fixed_baseline" || p.Kind == "model") &&
		(p.Metric == "dot" || p.Metric == "cosine" || p.Metric == "l2")
}

type ServingBundle struct {
	ID                string      `json:"bundle_id"`
	Subject           SubjectRef  `json:"subject_ref"`
	Pair              PairRef     `json:"pair"`
	FeatureSnapshotID string      `json:"feature_snapshot_id"`
	StateVersion      int64       `json:"user_state_version"`
	OntologyVersion   int64       `json:"ontology_definition_version,omitempty"`
	BaselineState     string      `json:"baseline_state"`
	Generation        string      `json:"baseline_generation,omitempty"`
	Revision          int64       `json:"baseline_revision,omitempty"`
	Watermarks        []Watermark `json:"source_watermarks"`
	Tail              []EventKey  `json:"tail_event_keys"`
	EncodingSource    string      `json:"encoding_source"`
	ModelCallID       string      `json:"model_call_id,omitempty"`
	Vector            []float64   `json:"user_vector"`
}

type PairAuthorization struct {
	Pair        PairRef `json:"pair"`
	ApprovalRef string  `json:"approval_ref"`
	Revision    int64   `json:"revision"`
	Active      bool    `json:"active"`
}

type ServingPointer struct {
	Subject          SubjectRef `json:"subject_ref"`
	PairID           string     `json:"pair_id"`
	BundleID         string     `json:"bundle_id"`
	State            string     `json:"state"`
	ApprovalRef      string     `json:"approval_ref"`
	ApprovalRevision int64      `json:"approval_revision"`
	Version          int64      `json:"pointer_version"`
}

type PairAuthorizer interface {
	AuthorizePair(context.Context, PairRef) (PairAuthorization, error)
}
