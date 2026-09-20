// Code generated from the DataCenter public wire contract. DO NOT EDIT.
// Package prediction defines the Sea v1 structured recommendation prediction
// contract. It is deliberately separate from chat, rerank and text
// representation protocols: feature order, pair identity and vector space are
// part of every request and response.
package prediction

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode"
)

const MaxBatch = 128

// ArtifactPackage carries the exact five JSON files produced by the current
// BTW recommendation exporter. []byte is base64 on the HTTP wire, preserving
// each file's hash rather than reparsing and reserializing it in transit.
type ArtifactPackage struct {
	Manifest      []byte `json:"manifest"`
	Weights       []byte `json:"weights"`
	Preprocessing []byte `json:"preprocessing"`
	ModelConfig   []byte `json:"model_config"`
	Probes        []byte `json:"probes"`
}

type Task string

const (
	UserTower Task = "user_tower"
	ItemTower Task = "item_tower"
	Ranker    Task = "ranker"
)

func OutputContract(task Task) string {
	return "sea.prediction.recommend." + string(task) + ".v1"
}

func (task Task) valid() bool {
	return task == UserTower || task == ItemTower || task == Ranker
}

var (
	configurationPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	digestPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ModelConfig is the immutable numerical contract exported with a candidate.
// FeatureOrder remains explicit even for a single-tower request so a user
// tower can never be mixed with another item tower that happens to have the
// same output dimension.
type ModelConfig struct {
	Recipe              string   `json:"recipe"`
	FeatureContractID   string   `json:"feature_contract_id"`
	FeatureOrder        []string `json:"feature_order"`
	RankerFeatureOrder  []string `json:"ranker_feature_order"`
	PreprocessingSHA256 string   `json:"preprocessing_sha256"`
	EmbeddingDimension  int      `json:"embedding_dimension"`
	Metric              string   `json:"metric"`
	ScoreSemantics      string   `json:"score_semantics"`
	Calibration         string   `json:"calibration"`
	PairID              string   `json:"pair_id"`
	SpaceID             string   `json:"space_id"`
}

func (config ModelConfig) Validate() error {
	if !identifier(config.Recipe) || !identifier(config.FeatureContractID) ||
		!digestPattern.MatchString(config.PreprocessingSHA256) ||
		!identifier(config.PairID) || !identifier(config.SpaceID) ||
		config.EmbeddingDimension < 1 || config.EmbeddingDimension > 4096 ||
		(config.Metric != "dot" && config.Metric != "cosine" && config.Metric != "l2") ||
		!identifier(config.ScoreSemantics) || !identifier(config.Calibration) {
		return errors.New("invalid prediction model configuration")
	}
	if !equalStrings(config.FeatureOrder, []string{"user_interest", "item_quality"}) ||
		!equalStrings(config.RankerFeatureOrder, []string{"bias", "user_interest", "item_quality", "interaction", "tower_dot"}) {
		return errors.New("unsupported prediction feature order")
	}
	return nil
}

type Input struct {
	ID           string   `json:"id"`
	UserInterest *float64 `json:"user_interest,omitempty"`
	ItemQuality  *float64 `json:"item_quality,omitempty"`
}

func (input Input) validate(task Task) error {
	if !identifier(input.ID) {
		return errors.New("prediction input requires an ID")
	}
	if input.UserInterest != nil && !finite(*input.UserInterest) ||
		input.ItemQuality != nil && !finite(*input.ItemQuality) {
		return errors.New("prediction input contains a non-finite feature")
	}
	switch task {
	case UserTower:
		if input.UserInterest == nil || input.ItemQuality != nil {
			return errors.New("user tower requires exactly user_interest")
		}
	case ItemTower:
		if input.UserInterest != nil || input.ItemQuality == nil {
			return errors.New("item tower requires exactly item_quality")
		}
	case Ranker:
		if input.UserInterest == nil || input.ItemQuality == nil {
			return errors.New("ranker requires user_interest and item_quality")
		}
	default:
		return errors.New("invalid prediction task")
	}
	return nil
}

type Request struct {
	Model             string  `json:"model"`
	ConfigurationID   string  `json:"configuration_id"`
	OutputContract    string  `json:"output_contract"`
	FeatureContractID string  `json:"feature_contract_id"`
	PairID            string  `json:"pair_id"`
	SpaceID           string  `json:"space_id"`
	Task              Task    `json:"task"`
	Input             []Input `json:"input"`
}

func (request Request) Validate() error {
	if !identifier(request.Model) || !configurationPattern.MatchString(request.ConfigurationID) ||
		request.ConfigurationID == "00000000-0000-0000-0000-000000000000" ||
		!request.Task.valid() || request.OutputContract != OutputContract(request.Task) ||
		!identifier(request.FeatureContractID) || !identifier(request.PairID) || !identifier(request.SpaceID) {
		return errors.New("invalid prediction request contract")
	}
	if len(request.Input) < 1 || len(request.Input) > MaxBatch {
		return errors.New("prediction batch must contain 1..128 inputs")
	}
	seen := make(map[string]struct{}, len(request.Input))
	for _, input := range request.Input {
		if _, exists := seen[input.ID]; exists {
			return errors.New("prediction input IDs must be unique")
		}
		if err := input.validate(request.Task); err != nil {
			return fmt.Errorf("prediction input %q: %w", input.ID, err)
		}
		seen[input.ID] = struct{}{}
	}
	return nil
}

type Output struct {
	ID            string    `json:"id"`
	UserEmbedding []float64 `json:"user_embedding,omitempty"`
	ItemEmbedding []float64 `json:"item_embedding,omitempty"`
	TowerDot      *float64  `json:"tower_dot,omitempty"`
	RankerLogit   *float64  `json:"ranker_logit,omitempty"`
}

type Usage struct {
	InputRows          int64 `json:"input_rows"`
	InputFeatureValues int64 `json:"input_feature_values"`
	OutputRows         int64 `json:"output_rows"`
	OutputValues       int64 `json:"output_values"`
}

type Response struct {
	Model             string      `json:"model"`
	ConfigurationID   string      `json:"configuration_id"`
	ArtifactSHA256    string      `json:"artifact_sha256"`
	ModelCallID       string      `json:"model_call_id"`
	PointerRevision   int64       `json:"pointer_revision"`
	OutputContract    string      `json:"output_contract"`
	FeatureContractID string      `json:"feature_contract_id"`
	PairID            string      `json:"pair_id"`
	SpaceID           string      `json:"space_id"`
	Task              Task        `json:"task"`
	ModelConfig       ModelConfig `json:"model_config"`
	Data              []Output    `json:"data"`
	Usage             *Usage      `json:"usage"`
}

func (response Response) Validate(request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if err := response.ModelConfig.Validate(); err != nil {
		return err
	}
	if response.Model != request.Model || response.ConfigurationID != request.ConfigurationID ||
		!digestPattern.MatchString(response.ArtifactSHA256) || !configurationPattern.MatchString(response.ModelCallID) ||
		response.ModelCallID == "00000000-0000-0000-0000-000000000000" ||
		response.PointerRevision < 1 || response.OutputContract != request.OutputContract ||
		response.FeatureContractID != request.FeatureContractID || response.PairID != request.PairID ||
		response.SpaceID != request.SpaceID || response.Task != request.Task ||
		response.ModelConfig.FeatureContractID != request.FeatureContractID ||
		response.ModelConfig.PairID != request.PairID || response.ModelConfig.SpaceID != request.SpaceID {
		return errors.New("prediction response metadata mismatch")
	}
	if len(response.Data) != len(request.Input) || response.Usage == nil {
		return errors.New("prediction response cardinality or usage mismatch")
	}
	inputs := make(map[string]struct{}, len(request.Input))
	for _, input := range request.Input {
		inputs[input.ID] = struct{}{}
	}
	for _, output := range response.Data {
		if _, ok := inputs[output.ID]; !ok {
			return errors.New("unknown or duplicate prediction response ID")
		}
		delete(inputs, output.ID)
		if err := output.validate(request.Task, response.ModelConfig.EmbeddingDimension); err != nil {
			return fmt.Errorf("prediction output %q: %w", output.ID, err)
		}
	}
	featuresPerRow, valuesPerRow := int64(1), int64(response.ModelConfig.EmbeddingDimension)
	if request.Task == Ranker {
		featuresPerRow = 2
		valuesPerRow = int64(2*response.ModelConfig.EmbeddingDimension + 2)
	}
	rows := int64(len(request.Input))
	if response.Usage.InputRows != rows || response.Usage.OutputRows != rows ||
		response.Usage.InputFeatureValues != rows*featuresPerRow ||
		response.Usage.OutputValues != rows*valuesPerRow {
		return errors.New("prediction usage does not match the fixed shape")
	}
	return nil
}

func (output Output) validate(task Task, dimension int) error {
	if !identifier(output.ID) {
		return errors.New("missing prediction output ID")
	}
	validVector := func(values []float64) bool {
		if len(values) != dimension {
			return false
		}
		for _, value := range values {
			if !finite(value) {
				return false
			}
		}
		return true
	}
	validScalar := func(value *float64) bool { return value != nil && finite(*value) }
	switch task {
	case UserTower:
		if !validVector(output.UserEmbedding) || output.ItemEmbedding != nil || output.TowerDot != nil || output.RankerLogit != nil {
			return errors.New("invalid user tower output shape")
		}
	case ItemTower:
		if output.UserEmbedding != nil || !validVector(output.ItemEmbedding) || output.TowerDot != nil || output.RankerLogit != nil {
			return errors.New("invalid item tower output shape")
		}
	case Ranker:
		if !validVector(output.UserEmbedding) || !validVector(output.ItemEmbedding) || !validScalar(output.TowerDot) || !validScalar(output.RankerLogit) {
			return errors.New("invalid ranker output shape")
		}
	default:
		return errors.New("invalid prediction task")
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func identifier(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// Decode rejects duplicate keys, unknown fields, null and trailing JSON.
func Decode(data []byte, destination any) error {
	if err := uniqueKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func uniqueKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("null is not a prediction value")
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]struct{}{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, exists := seen[name]; exists {
				return errors.New("duplicate JSON key")
			}
			seen[name] = struct{}{}
			if err := uniqueKeys(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case json.Delim('['):
		for decoder.More() {
			if err := uniqueKeys(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return nil
	}
}

// UnmarshalJSON requires all four counters; a missing zero-valued field cannot
// silently become a valid-looking usage receipt.
func (usage *Usage) UnmarshalJSON(data []byte) error {
	var wire struct {
		InputRows          *int64 `json:"input_rows"`
		InputFeatureValues *int64 `json:"input_feature_values"`
		OutputRows         *int64 `json:"output_rows"`
		OutputValues       *int64 `json:"output_values"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if wire.InputRows == nil || wire.InputFeatureValues == nil || wire.OutputRows == nil || wire.OutputValues == nil {
		return errors.New("prediction usage requires all counters")
	}
	usage.InputRows = *wire.InputRows
	usage.InputFeatureValues = *wire.InputFeatureValues
	usage.OutputRows = *wire.OutputRows
	usage.OutputValues = *wire.OutputValues
	return nil
}
