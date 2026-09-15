package recommend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"reflect"
	"regexp"
	"strconv"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/prediction"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"github.com/google/uuid"
)

var (
	ErrPredictionDisabled = errors.New("recommend prediction consumer is disabled")
	ErrPredictionContract = errors.New("recommend prediction contract mismatch")
)

var predictionLogicalCall = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,176}$`)

// PredictionSubject is the frozen SubjectRef v2 shape. RTW UserCenter is the
// human source system, while the machine issuer remains rtw.identity.
type PredictionSubject struct {
	Issuer    string `json:"issuer"`
	SubjectID string `json:"subject_id"`
}

func (s PredictionSubject) valid() bool {
	if s.Issuer != "rtw.identity" || s.SubjectID == "" || (len(s.SubjectID) > 1 && s.SubjectID[0] == '0') {
		return false
	}
	id, err := strconv.ParseInt(s.SubjectID, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == s.SubjectID
}

// SyntheticPredictionFeatures is deliberately not a usermodel FeatureSpec.
// No current approved rule maps favorite/read counts into user_interest, so
// the real-DC acceptance uses this explicit typed fixture instead of guessing.
type SyntheticPredictionFeatures struct {
	Source       string  `json:"source"`
	UserInterest float64 `json:"user_interest"`
	ItemQuality  float64 `json:"item_quality"`
}

func (f SyntheticPredictionFeatures) valid() bool {
	return f.Source == "synthetic_typed_fixture" && finitePrediction(f.UserInterest) && finitePrediction(f.ItemQuality)
}

type PredictionCandidateConfig struct {
	SyntheticEvaluationEnabled bool   `json:"synthetic_evaluation_enabled"`
	Model                      string `json:"model"`
	ConfigurationID            string `json:"configuration_id"`
	ArtifactSHA256             string `json:"artifact_sha256"`
	FeatureContractID          string `json:"feature_contract_id"`
	PairID                     string `json:"pair_id"`
	SpaceID                    string `json:"space_id"`
}

func (c PredictionCandidateConfig) validate() error {
	if !c.SyntheticEvaluationEnabled {
		return nil
	}
	user, item := 0.0, 0.0
	request := prediction.Request{Model: c.Model, ConfigurationID: c.ConfigurationID,
		OutputContract: prediction.OutputContract(prediction.Ranker), FeatureContractID: c.FeatureContractID,
		PairID: c.PairID, SpaceID: c.SpaceID, Task: prediction.Ranker,
		Input: []prediction.Input{{ID: "configuration-probe", UserInterest: &user, ItemQuality: &item}}}
	if err := request.Validate(); err != nil || !artifacts.ValidHash(c.ArtifactSHA256) ||
		c.FeatureContractID != "engagement-features.v1" {
		return ErrPredictionContract
	}
	return nil
}

type PredictionCaller interface {
	Predict(context.Context, prediction.Request, string) (datacenter.PredictionResult, error)
}

type PredictionTaskReceipt struct {
	Task              prediction.Task   `json:"task"`
	LogicalCallSHA256 string            `json:"logical_call_sha256"`
	ModelCallID       string            `json:"model_call_id"`
	ResponseSHA256    string            `json:"response_sha256"`
	PointerRevision   int64             `json:"pointer_revision"`
	Output            prediction.Output `json:"output"`
	Usage             prediction.Usage  `json:"usage"`
}

type PredictionCandidate struct {
	ID                 string                      `json:"candidate_id"`
	Status             string                      `json:"status"`
	BusinessActivation string                      `json:"business_activation"`
	Subject            PredictionSubject           `json:"subject"`
	Features           SyntheticPredictionFeatures `json:"features"`
	InputID            string                      `json:"input_id"`
	FeatureDependency  string                      `json:"feature_dependency"`
	Model              string                      `json:"model"`
	ConfigurationID    string                      `json:"configuration_id"`
	ArtifactSHA256     string                      `json:"artifact_sha256"`
	FeatureContractID  string                      `json:"feature_contract_id"`
	PairID             string                      `json:"pair_id"`
	SpaceID            string                      `json:"space_id"`
	UserTower          PredictionTaskReceipt       `json:"user_tower"`
	ItemTower          PredictionTaskReceipt       `json:"item_tower"`
	Ranker             PredictionTaskReceipt       `json:"ranker"`
}

type PredictionCandidateRequest struct {
	Subject     PredictionSubject           `json:"subject"`
	Features    SyntheticPredictionFeatures `json:"features"`
	LogicalCall string                      `json:"logical_call"`
}

func (q PredictionCandidateRequest) validate() error {
	if !q.Subject.valid() || !q.Features.valid() || !predictionLogicalCall.MatchString(q.LogicalCall) {
		return ErrInvalid
	}
	return nil
}

type PredictionCandidateUseCase struct {
	config   PredictionCandidateConfig
	caller   PredictionCaller
	observed *telemetry.Bundle
}

func NewPredictionCandidateUseCase(config PredictionCandidateConfig, caller PredictionCaller,
	observed *telemetry.Bundle) (*PredictionCandidateUseCase, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	if !config.SyntheticEvaluationEnabled {
		return &PredictionCandidateUseCase{config: config}, nil
	}
	if nilPredictionCaller(caller) || observed == nil || observed.Closed() {
		return nil, ErrInvalid
	}
	return &PredictionCandidateUseCase{config: config, caller: caller, observed: observed}, nil
}

func nilPredictionCaller(caller PredictionCaller) bool {
	if caller == nil {
		return true
	}
	value := reflect.ValueOf(caller)
	return (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface || value.Kind() == reflect.Func) && value.IsNil()
}

func (u *PredictionCandidateUseCase) BuildCandidate(ctx context.Context,
	request PredictionCandidateRequest) (candidate PredictionCandidate, err error) {
	if u == nil || !u.config.SyntheticEvaluationEnabled {
		return PredictionCandidate{}, ErrPredictionDisabled
	}
	if ctx == nil || request.validate() != nil {
		return PredictionCandidate{}, ErrInvalid
	}
	ctx, stage, err := u.observed.Begin(ctx, "recommend", "recommend.prediction_candidate",
		slog.String("configuration_id", u.config.ConfigurationID),
		slog.String("representation_space", u.config.SpaceID), slog.String("pair_id", u.config.PairID))
	if err != nil {
		return PredictionCandidate{}, err
	}
	defer func() {
		outcome, code := predictionCandidateOutcome(err)
		stage.End(ctx, outcome, code, err, slog.String("candidate_id", candidate.ID),
			slog.String("business_activation", candidate.BusinessActivation))
	}()

	inputID := predictionInputID(request.Subject, request.Features)
	userRequest := u.request(prediction.UserTower, prediction.Input{ID: inputID, UserInterest: &request.Features.UserInterest})
	itemRequest := u.request(prediction.ItemTower, prediction.Input{ID: inputID, ItemQuality: &request.Features.ItemQuality})
	rankerRequest := u.request(prediction.Ranker, prediction.Input{ID: inputID,
		UserInterest: &request.Features.UserInterest, ItemQuality: &request.Features.ItemQuality})
	user, err := u.call(ctx, userRequest, request.LogicalCall+".user")
	if err != nil {
		return PredictionCandidate{}, err
	}
	item, err := u.call(ctx, itemRequest, request.LogicalCall+".item")
	if err != nil {
		return PredictionCandidate{}, err
	}
	ranker, err := u.call(ctx, rankerRequest, request.LogicalCall+".ranker")
	if err != nil {
		return PredictionCandidate{}, err
	}
	if !sameVector(user.Output.UserEmbedding, ranker.Output.UserEmbedding) ||
		!sameVector(item.Output.ItemEmbedding, ranker.Output.ItemEmbedding) ||
		user.Output.ItemEmbedding != nil || item.Output.UserEmbedding != nil ||
		user.PointerRevision != item.PointerRevision || user.PointerRevision != ranker.PointerRevision ||
		user.ModelCallID == item.ModelCallID || user.ModelCallID == ranker.ModelCallID || item.ModelCallID == ranker.ModelCallID {
		return PredictionCandidate{}, ErrPredictionContract
	}
	candidate = PredictionCandidate{
		Status: "candidate_default_off", BusinessActivation: "none", Subject: request.Subject,
		Features:          request.Features,
		InputID:           inputID,
		FeatureDependency: "approved real FeatureSpec mapping to user_interest/item_quality is not defined",
		Model:             u.config.Model, ConfigurationID: u.config.ConfigurationID, ArtifactSHA256: u.config.ArtifactSHA256,
		FeatureContractID: u.config.FeatureContractID, PairID: u.config.PairID, SpaceID: u.config.SpaceID,
		UserTower: user, ItemTower: item, Ranker: ranker,
	}
	candidate.ID, err = predictionCandidateID(candidate)
	if err != nil || !validPredictionCandidate(candidate) {
		return PredictionCandidate{}, ErrPredictionContract
	}
	return candidate, nil
}

func (u *PredictionCandidateUseCase) request(task prediction.Task, input prediction.Input) prediction.Request {
	return prediction.Request{Model: u.config.Model, ConfigurationID: u.config.ConfigurationID,
		OutputContract: prediction.OutputContract(task), FeatureContractID: u.config.FeatureContractID,
		PairID: u.config.PairID, SpaceID: u.config.SpaceID, Task: task, Input: []prediction.Input{input}}
}

func (u *PredictionCandidateUseCase) call(ctx context.Context, request prediction.Request,
	logicalCall string) (receipt PredictionTaskReceipt, err error) {
	var attempts int
	ctx, stage, err := u.observed.Begin(ctx, "recommend", "recommend.prediction_call",
		slog.String("configuration_id", u.config.ConfigurationID), slog.String("representation_space", u.config.SpaceID),
		slog.String("pair_id", u.config.PairID), slog.String("task", string(request.Task)),
		slog.String("logical_call_id", digestPrediction([]byte(logicalCall))))
	if err != nil {
		return PredictionTaskReceipt{}, err
	}
	defer func() {
		outcome, code := predictionCandidateOutcome(err)
		stage.End(ctx, outcome, code, err, slog.String("model_call_id", receipt.ModelCallID),
			slog.Int("attempts", attempts), slog.Int64("input_rows", receipt.Usage.InputRows),
			slog.Int64("input_feature_values", receipt.Usage.InputFeatureValues),
			slog.Int64("output_rows", receipt.Usage.OutputRows), slog.Int64("output_values", receipt.Usage.OutputValues))
	}()
	var result datacenter.PredictionResult
	result, attempts, err = u.predictWithRecovery(ctx, request, logicalCall)
	if err != nil {
		return PredictionTaskReceipt{}, err
	}
	var decoded prediction.Response
	if err := prediction.Decode(result.Body, &decoded); err != nil || !reflect.DeepEqual(decoded, result.Response) ||
		result.Response.Validate(request) != nil || result.Response.ArtifactSHA256 != u.config.ArtifactSHA256 ||
		result.Response.ConfigurationID != u.config.ConfigurationID ||
		result.Response.PairID != u.config.PairID || result.Response.SpaceID != u.config.SpaceID ||
		result.Response.FeatureContractID != u.config.FeatureContractID || result.Response.Task != request.Task ||
		len(result.Response.Data) != 1 || result.Response.Usage == nil {
		return PredictionTaskReceipt{}, ErrPredictionContract
	}
	receipt = PredictionTaskReceipt{Task: request.Task, LogicalCallSHA256: digestPrediction([]byte(logicalCall)),
		ModelCallID: result.Response.ModelCallID, ResponseSHA256: digestPrediction(result.Body),
		PointerRevision: result.Response.PointerRevision,
		Output:          result.Response.Data[0], Usage: *result.Response.Usage}
	stage.SetAttributes(slog.String("model_call_id", receipt.ModelCallID))
	return receipt, nil
}

func (u *PredictionCandidateUseCase) predictWithRecovery(ctx context.Context, request prediction.Request,
	logicalCall string) (datacenter.PredictionResult, int, error) {
	result, err := u.caller.Predict(ctx, request, logicalCall)
	if err == nil {
		return result, 1, nil
	}
	if !errors.Is(err, datacenter.ErrPredictionOutcomeUnknown) || ctx.Err() != nil {
		return datacenter.PredictionResult{}, 1, err
	}
	result, err = u.caller.Predict(ctx, request, logicalCall)
	return result, 2, err
}

func validPredictionCandidate(candidate PredictionCandidate) bool {
	configurationID, configurationErr := uuid.Parse(candidate.ConfigurationID)
	if !artifacts.ValidHash(candidate.ID) || candidate.Status != "candidate_default_off" ||
		candidate.BusinessActivation != "none" || !candidate.Subject.valid() ||
		!candidate.Features.valid() || candidate.InputID != predictionInputID(candidate.Subject, candidate.Features) ||
		candidate.FeatureDependency == "" || configurationErr != nil || configurationID == uuid.Nil ||
		configurationID.String() != candidate.ConfigurationID ||
		!validID(candidate.Model) || !validID(candidate.FeatureContractID) || !validID(candidate.PairID) ||
		!validID(candidate.SpaceID) || !artifacts.ValidHash(candidate.ArtifactSHA256) ||
		candidate.UserTower.Task != prediction.UserTower || candidate.ItemTower.Task != prediction.ItemTower ||
		candidate.Ranker.Task != prediction.Ranker ||
		candidate.UserTower.PointerRevision != candidate.ItemTower.PointerRevision ||
		candidate.UserTower.PointerRevision != candidate.Ranker.PointerRevision ||
		candidate.UserTower.Output.ID != candidate.InputID || candidate.ItemTower.Output.ID != candidate.InputID ||
		candidate.Ranker.Output.ID != candidate.InputID ||
		!sameVector(candidate.UserTower.Output.UserEmbedding, candidate.Ranker.Output.UserEmbedding) ||
		!sameVector(candidate.ItemTower.Output.ItemEmbedding, candidate.Ranker.Output.ItemEmbedding) {
		return false
	}
	for _, receipt := range []PredictionTaskReceipt{candidate.UserTower, candidate.ItemTower, candidate.Ranker} {
		modelCallID, err := uuid.Parse(receipt.ModelCallID)
		if !artifacts.ValidHash(receipt.LogicalCallSHA256) || !artifacts.ValidHash(receipt.ResponseSHA256) ||
			err != nil || modelCallID == uuid.Nil || modelCallID.String() != receipt.ModelCallID ||
			receipt.PointerRevision < 1 {
			return false
		}
	}
	if !validTaskShape(candidate.UserTower, 1, 2) || !validTaskShape(candidate.ItemTower, 1, 2) ||
		!validTaskShape(candidate.Ranker, 2, 6) ||
		candidate.UserTower.ModelCallID == candidate.ItemTower.ModelCallID ||
		candidate.UserTower.ModelCallID == candidate.Ranker.ModelCallID ||
		candidate.ItemTower.ModelCallID == candidate.Ranker.ModelCallID {
		return false
	}
	copy := candidate
	copy.ID = ""
	id, err := predictionCandidateID(copy)
	return err == nil && id == candidate.ID
}

func validTaskShape(receipt PredictionTaskReceipt, inputValues, outputValues int64) bool {
	if receipt.Usage.InputRows != 1 || receipt.Usage.InputFeatureValues != inputValues ||
		receipt.Usage.OutputRows != 1 || receipt.Usage.OutputValues != outputValues {
		return false
	}
	switch receipt.Task {
	case prediction.UserTower:
		return finitePredictionVector(receipt.Output.UserEmbedding, 2) && receipt.Output.ItemEmbedding == nil &&
			receipt.Output.TowerDot == nil && receipt.Output.RankerLogit == nil
	case prediction.ItemTower:
		return receipt.Output.UserEmbedding == nil && finitePredictionVector(receipt.Output.ItemEmbedding, 2) &&
			receipt.Output.TowerDot == nil && receipt.Output.RankerLogit == nil
	case prediction.Ranker:
		return finitePredictionVector(receipt.Output.UserEmbedding, 2) && finitePredictionVector(receipt.Output.ItemEmbedding, 2) &&
			receipt.Output.TowerDot != nil && finitePrediction(*receipt.Output.TowerDot) &&
			receipt.Output.RankerLogit != nil && finitePrediction(*receipt.Output.RankerLogit)
	default:
		return false
	}
}

func predictionCandidateID(candidate PredictionCandidate) (string, error) {
	candidate.ID = ""
	raw, err := json.Marshal(candidate)
	if err != nil {
		return "", err
	}
	return digestPrediction(raw), nil
}

func predictionInputID(subject PredictionSubject, features SyntheticPredictionFeatures) string {
	raw, _ := json.Marshal(struct {
		Subject  PredictionSubject           `json:"subject"`
		Features SyntheticPredictionFeatures `json:"features"`
	}{subject, features})
	return "synthetic-" + digestPrediction(raw)
}

func digestPrediction(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func finitePrediction(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func finitePredictionVector(values []float64, length int) bool {
	if len(values) != length {
		return false
	}
	for _, value := range values {
		if !finitePrediction(value) {
			return false
		}
	}
	return true
}

func sameVector(left, right []float64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if math.Float64bits(left[index]) != math.Float64bits(right[index]) {
			return false
		}
	}
	return true
}

func predictionCandidateOutcome(err error) (string, string) {
	if err == nil {
		return "succeeded", ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled", "CANCELLED"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed_out", "TIMEOUT"
	case errors.Is(err, ErrPredictionDisabled):
		return "rejected", "PREDICTION_DISABLED"
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrPredictionContract):
		return "rejected", "PREDICTION_CONTRACT"
	case errors.Is(err, datacenter.ErrPredictionOutcomeUnknown):
		return "failed", "DC_PREDICTION_UNKNOWN"
	default:
		return "failed", "DC_PREDICTION_FAILED"
	}
}
