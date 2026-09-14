package recommend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/usermodel"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrPairPending = errors.New("recommend pair is not ready for activation")
var ErrActivationUnknown = errors.New("pair activation committed but final eligibility is uncertain")

type EncoderArtifact struct {
	EncoderID     string     `json:"encoder_id"`
	Weights       corpus.Ref `json:"weights"`
	InputSpecHash string     `json:"input_spec_hash"`
	SpaceID       string     `json:"space_id"`
	Dimension     int        `json:"dimension"`
	Metric        string     `json:"metric"`
}

type PairProposal struct {
	ID                string            `json:"proposal_id"`
	ModuleID          string            `json:"module_id"`
	PoolReleaseID     string            `json:"pool_release_id"`
	ItemFeatureHash   string            `json:"item_feature_hash"`
	Pair              usermodel.PairRef `json:"pair"`
	UserEncoder       EncoderArtifact   `json:"user_encoder"`
	ItemEncoder       EncoderArtifact   `json:"item_encoder"`
	CandidateManifest corpus.Ref        `json:"candidate_manifest"`
	RankerKind        string            `json:"ranker_kind"` // rule_freshness or model_logit
	RankerWeights     corpus.Ref        `json:"ranker_weights"`
	PolicyVersion     string            `json:"policy_version"`
}

type IndexedItem struct {
	ItemID      string `json:"item_id"`
	RevisionID  string `json:"revision_id"`
	ContentHash string `json:"content_hash"`
}

// ItemIndexGeneration is an immutable prebuild claim. Its object and coverage
// must be independently read and probed before a pair release can be approved.
type ItemIndexGeneration struct {
	ID            string          `json:"item_index_generation_id"`
	ProposalID    string          `json:"proposal_id"`
	PoolReleaseID string          `json:"pool_release_id"`
	PairID        string          `json:"pair_id"`
	SpaceID       string          `json:"space_id"`
	Dimension     int             `json:"dimension"`
	Metric        string          `json:"metric"`
	ItemEncoder   EncoderArtifact `json:"item_encoder"`
	Index         corpus.Ref      `json:"index"`
	BuildManifest corpus.Ref      `json:"build_manifest"`
	Items         []IndexedItem   `json:"items"`
}

type ModelProbe struct {
	PairID                string    `json:"pair_id"`
	SpaceID               string    `json:"space_id"`
	Dimension             int       `json:"dimension"`
	Metric                string    `json:"metric"`
	UserWeightsHash       string    `json:"user_weights_hash"`
	ItemWeightsHash       string    `json:"item_weights_hash"`
	CandidateManifestHash string    `json:"candidate_manifest_hash"`
	ModelCallID           string    `json:"model_call_id"`
	ProbeRef              string    `json:"probe_ref"`
	ObservedAt            time.Time `json:"observed_at"`
}

type IndexProbe struct {
	GenerationID      string    `json:"item_index_generation_id"`
	PoolReleaseID     string    `json:"pool_release_id"`
	PairID            string    `json:"pair_id"`
	SpaceID           string    `json:"space_id"`
	Dimension         int       `json:"dimension"`
	Metric            string    `json:"metric"`
	IndexHash         string    `json:"index_hash"`
	BuildManifestHash string    `json:"build_manifest_hash"`
	CoveredItems      int       `json:"covered_items"`
	ProbeRef          string    `json:"probe_ref"`
	ObservedAt        time.Time `json:"observed_at"`
}

type RankerProbe struct {
	PairID        string    `json:"pair_id"`
	Kind          string    `json:"kind"`
	PolicyVersion string    `json:"policy_version"`
	WeightsHash   string    `json:"weights_hash,omitempty"`
	ProbeRef      string    `json:"probe_ref"`
	ObservedAt    time.Time `json:"observed_at"`
}

type Approval struct {
	Ref      string `json:"approval_ref"`
	Revision int64  `json:"approval_revision"`
}

type ApprovalProof struct {
	ProposalID string    `json:"proposal_id"`
	IndexID    string    `json:"item_index_generation_id"`
	PairID     string    `json:"pair_id"`
	Approval   Approval  `json:"approval"`
	ProofRef   string    `json:"proof_ref"`
	ApprovedAt time.Time `json:"approved_at"`
}

// PairVerifier belongs to real DC item/model loading and the policy owner.
// There is no production adapter in this slice; tests use explicit fixtures.
type PairVerifier interface {
	VerifyModel(context.Context, PairProposal) (ModelProbe, error)
	VerifyIndex(context.Context, ItemIndexGeneration) (IndexProbe, error)
	VerifyRanker(context.Context, PairProposal) (RankerProbe, error)
	VerifyApproval(context.Context, PairProposal, ItemIndexGeneration, Approval) (ApprovalProof, error)
}

type EncoderPairRelease struct {
	ID          string              `json:"encoder_pair_release_id"`
	Proposal    PairProposal        `json:"proposal"`
	Index       ItemIndexGeneration `json:"item_index"`
	ModelProbe  ModelProbe          `json:"model_probe"`
	IndexProbe  IndexProbe          `json:"index_probe"`
	RankerProbe RankerProbe         `json:"ranker_probe"`
	Approval    ApprovalProof       `json:"approval"`
}

type PairPointer struct {
	ModuleID         string `json:"module_id"`
	ReleaseID        string `json:"encoder_pair_release_id"`
	PairID           string `json:"pair_id"`
	Version          int64  `json:"pointer_version"`
	ApprovalRef      string `json:"approval_ref"`
	ApprovalRevision int64  `json:"approval_revision"`
}

type PairStore struct {
	pool     *Store
	db       *pgxpool.Pool
	verifier PairVerifier
}

func NewPairStore(pool *Store, verifier PairVerifier) (*PairStore, error) {
	if pool == nil || pool.db == nil || pool.source == nil {
		return nil, ErrInvalid
	}
	if nilPairVerifier(verifier) {
		verifier = nil
	}
	return &PairStore{pool: pool, db: pool.db, verifier: verifier}, nil
}

func nilPairVerifier(value any) bool {
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func validRef(ref corpus.Ref) bool {
	return artifacts.ValidHash(ref.SHA256) && ref.Key == "sha256/"+ref.SHA256
}

func validPairRef(p usermodel.PairRef) bool {
	return validID(p.PairID) && validID(p.SpaceID) && validID(p.EncoderID) &&
		validID(p.FeatureSpecVersion) && artifacts.ValidHash(p.FeatureSpecHash) &&
		p.Dimension >= 1 && p.Dimension <= 4096 &&
		(p.Kind == "fixed_baseline" || p.Kind == "model") &&
		(p.Metric == "dot" || p.Metric == "cosine" || p.Metric == "l2")
}

func validEncoder(encoder EncoderArtifact, p usermodel.PairRef, specHash string) bool {
	return validID(encoder.EncoderID) && validRef(encoder.Weights) &&
		encoder.InputSpecHash == specHash && encoder.SpaceID == p.SpaceID &&
		encoder.Dimension == p.Dimension && encoder.Metric == p.Metric
}

func digestValue(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func proposalID(p PairProposal) (string, error)          { p.ID = ""; return digestValue(p) }
func indexID(i ItemIndexGeneration) (string, error)      { i.ID = ""; return digestValue(i) }
func pairReleaseID(r EncoderPairRelease) (string, error) { r.ID = ""; return digestValue(r) }

func validProposal(p PairProposal) bool {
	if !validID(p.ModuleID) || !artifacts.ValidHash(p.PoolReleaseID) ||
		!artifacts.ValidHash(p.ItemFeatureHash) || !validPairRef(p.Pair) ||
		!validEncoder(p.UserEncoder, p.Pair, p.Pair.FeatureSpecHash) ||
		!validEncoder(p.ItemEncoder, p.Pair, p.ItemFeatureHash) ||
		p.UserEncoder.EncoderID != p.Pair.EncoderID || !validRef(p.CandidateManifest) ||
		!validID(p.PolicyVersion) ||
		(p.Pair.Kind == "fixed_baseline" && (p.RankerKind != "rule_freshness" || p.RankerWeights != (corpus.Ref{}))) ||
		(p.Pair.Kind == "model" && (p.RankerKind != "model_logit" || !validRef(p.RankerWeights))) {
		return false
	}
	id, err := proposalID(p)
	return err == nil && id == p.ID
}

func indexedItems(pool PoolRelease) []IndexedItem {
	items := make([]IndexedItem, len(pool.Items))
	for n, item := range pool.Items {
		items[n] = IndexedItem{ItemID: item.ItemID, RevisionID: item.RevisionID,
			ContentHash: item.ContentHash}
	}
	return items
}

func validItemIndex(i ItemIndexGeneration, p PairProposal, pool PoolRelease) bool {
	if !validProposal(p) || !validRelease(pool) || len(pool.Items) == 0 || i.ProposalID != p.ID ||
		i.PoolReleaseID != pool.ID || i.PoolReleaseID != p.PoolReleaseID ||
		i.PairID != p.Pair.PairID || i.SpaceID != p.Pair.SpaceID ||
		i.Dimension != p.Pair.Dimension || i.Metric != p.Pair.Metric ||
		i.ItemEncoder != p.ItemEncoder || !validRef(i.Index) || !validRef(i.BuildManifest) ||
		len(i.Items) != len(pool.Items) || !reflect.DeepEqual(i.Items, indexedItems(pool)) {
		return false
	}
	id, err := indexID(i)
	return err == nil && id == i.ID
}

func validModelProbe(probe ModelProbe, p PairProposal) bool {
	return probe.PairID == p.Pair.PairID && probe.SpaceID == p.Pair.SpaceID &&
		probe.Dimension == p.Pair.Dimension && probe.Metric == p.Pair.Metric &&
		probe.UserWeightsHash == p.UserEncoder.Weights.SHA256 &&
		probe.ItemWeightsHash == p.ItemEncoder.Weights.SHA256 &&
		probe.CandidateManifestHash == p.CandidateManifest.SHA256 &&
		validID(probe.ModelCallID) && validID(probe.ProbeRef) && !probe.ObservedAt.IsZero()
}

func validIndexProbe(probe IndexProbe, i ItemIndexGeneration) bool {
	return probe.GenerationID == i.ID && probe.PoolReleaseID == i.PoolReleaseID &&
		probe.PairID == i.PairID && probe.SpaceID == i.SpaceID &&
		probe.Dimension == i.Dimension && probe.Metric == i.Metric &&
		probe.IndexHash == i.Index.SHA256 && probe.CoveredItems == len(i.Items) &&
		probe.BuildManifestHash == i.BuildManifest.SHA256 &&
		validID(probe.ProbeRef) && !probe.ObservedAt.IsZero()
}

func validApprovalProof(proof ApprovalProof, p PairProposal, i ItemIndexGeneration) bool {
	return proof.ProposalID == p.ID && proof.IndexID == i.ID &&
		proof.PairID == p.Pair.PairID && validID(proof.Approval.Ref) &&
		proof.Approval.Revision > 0 && validID(proof.ProofRef) && !proof.ApprovedAt.IsZero()
}

func validRankerProbe(probe RankerProbe, p PairProposal) bool {
	if probe.PairID != p.Pair.PairID || probe.Kind != p.RankerKind ||
		probe.PolicyVersion != p.PolicyVersion || !validID(probe.ProbeRef) || probe.ObservedAt.IsZero() {
		return false
	}
	if p.RankerKind == "rule_freshness" {
		return probe.WeightsHash == ""
	}
	return probe.WeightsHash == p.RankerWeights.SHA256
}

func validPairRelease(r EncoderPairRelease, pool PoolRelease) bool {
	if !validItemIndex(r.Index, r.Proposal, pool) ||
		!validModelProbe(r.ModelProbe, r.Proposal) || !validIndexProbe(r.IndexProbe, r.Index) ||
		!validRankerProbe(r.RankerProbe, r.Proposal) || !validApprovalProof(r.Approval, r.Proposal, r.Index) {
		return false
	}
	id, err := pairReleaseID(r)
	return err == nil && id == r.ID
}

func sameIndexedSet(a, b []IndexedItem) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = append([]IndexedItem(nil), a...), append([]IndexedItem(nil), b...)
	sort.Slice(a, func(i, j int) bool { return a[i].ItemID < a[j].ItemID })
	sort.Slice(b, func(i, j int) bool { return b[i].ItemID < b[j].ItemID })
	return reflect.DeepEqual(a, b)
}

func proofFresh(now time.Time, observedAt time.Time) bool {
	return !observedAt.IsZero() && !observedAt.After(now.Add(30*time.Second)) &&
		now.Sub(observedAt) <= 5*time.Minute
}
