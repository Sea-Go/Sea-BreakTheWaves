// Code generated from the DataCenter public wire contract. DO NOT EDIT.
// Package representation defines the Sea v1 text representation wire contract.
// It is shared by provider probes, gateway validation and external consumers.
package representation

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

type Kind string

const (
	Dense       Kind = "dense"
	Sparse      Kind = "sparse"
	TokenMatrix Kind = "token_matrix"
	MaxBatch         = 128
)

func OutputContract(kind Kind) string { return "sea.representation." + string(kind) + ".v1" }
func IsOutputContract(value string) bool {
	return value == OutputContract(Dense) || value == OutputContract(Sparse) || value == OutputContract(TokenMatrix)
}

// Contract is immutable for an output contract and representation space.
// Dimensions is the vector width, or the full sparse feature space size.
// Token matrix aggregation is explicit: sum_maxsim sums each valid query token
// maximum dot product over valid document tokens; mean_maxsim divides that sum
// by the number of valid query tokens. Masked rows participate in neither term.
// These definitions are not interchangeable within an immutable contract/space.
type Contract struct {
	ID            string `json:"id"`
	Kind          Kind   `json:"kind"`
	Dimensions    int    `json:"dimensions"`
	TokenizerID   string `json:"tokenizer_id"`
	VocabularyID  string `json:"vocabulary_id,omitempty"`
	Normalization string `json:"normalization"`
	Metric        string `json:"metric"`
	Aggregation   string `json:"aggregation"`
	MaxTokens     int    `json:"max_tokens,omitempty"`
	MaxNonzero    int    `json:"max_nonzero,omitempty"`
}

func (c Contract) Validate() error {
	if !identifier(c.ID) || !identifier(c.TokenizerID) {
		return errors.New("representation requires contract and tokenizer identifiers")
	}
	if c.Normalization != "none" && c.Normalization != "l2" {
		return errors.New("invalid representation normalization")
	}
	if c.Dimensions < 1 || c.Dimensions > math.MaxInt32 {
		return errors.New("invalid representation dimensions")
	}
	switch c.Kind {
	case Dense:
		if c.Dimensions > 65536 || c.Aggregation != "none" || (c.Metric != "dot" && c.Metric != "cosine") || c.MaxTokens != 0 || c.MaxNonzero != 0 || c.VocabularyID != "" {
			return errors.New("invalid dense contract")
		}
	case Sparse:
		if !identifier(c.VocabularyID) || c.Metric != "dot" || (c.Aggregation != "sum" && c.Aggregation != "max") || c.MaxTokens != 0 || c.MaxNonzero < 1 || c.MaxNonzero > min(c.Dimensions, 65536) {
			return errors.New("invalid sparse contract")
		}
	case TokenMatrix:
		if c.Dimensions > 65536 || c.Metric != "maxsim" || (c.Aggregation != "sum_maxsim" && c.Aggregation != "mean_maxsim") || c.MaxTokens < 1 || c.MaxTokens > 8192 || c.MaxNonzero != 0 || c.VocabularyID != "" || int64(c.MaxTokens)*int64(c.Dimensions) > 8*1024*1024 {
			return errors.New("invalid token matrix contract")
		}
	default:
		return errors.New("invalid representation kind")
	}
	return nil
}

var configurationPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Input struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}
type Request struct {
	Model           string  `json:"model"`
	ConfigurationID string  `json:"configuration_id"`
	OutputContract  string  `json:"output_contract"`
	ContractID      string  `json:"representation_contract_id"`
	Space           string  `json:"representation_space"`
	Role            string  `json:"role"`
	Input           []Input `json:"input"`
}

func (r Request) Validate() error {
	if !identifier(r.Model) || !configurationPattern.MatchString(r.ConfigurationID) || r.ConfigurationID == "00000000-0000-0000-0000-000000000000" || !IsOutputContract(r.OutputContract) || !identifier(r.ContractID) || !identifier(r.Space) || (r.Role != "query" && r.Role != "document") {
		return errors.New("invalid representation request contract")
	}
	if len(r.Input) < 1 || len(r.Input) > MaxBatch {
		return errors.New("representation batch must contain 1..128 inputs")
	}
	seen := map[string]bool{}
	for _, v := range r.Input {
		if !identifier(v.ID) || seen[v.ID] || strings.TrimSpace(v.Text) == "" {
			return errors.New("representation requires unique input IDs and nonempty text")
		}
		seen[v.ID] = true
	}
	return nil
}
func (r Request) Match(c Contract, space, configurationID string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if r.ContractID != c.ID || r.OutputContract != OutputContract(c.Kind) || r.Space != space || r.ConfigurationID != configurationID {
		return errors.New("representation configuration or contract mismatch")
	}
	return nil
}

type DenseValues struct {
	Values []float64 `json:"values"`
}
type SparseValues struct {
	Indices []int     `json:"indices"`
	Weights []float64 `json:"weights"`
}
type TokenValues struct {
	Shape  []int       `json:"shape"`
	Values [][]float64 `json:"values"`
	Mask   []bool      `json:"mask"`
}
type Item struct {
	ID          string        `json:"id"`
	Dense       *DenseValues  `json:"dense,omitempty"`
	Sparse      *SparseValues `json:"sparse,omitempty"`
	TokenMatrix *TokenValues  `json:"token_matrix,omitempty"`
}
type Usage struct {
	PromptTokens int64 `json:"prompt_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}
type Response struct {
	Model           string `json:"model"`
	ConfigurationID string `json:"configuration_id"`
	OutputContract  string `json:"output_contract"`
	ContractID      string `json:"representation_contract_id"`
	Space           string `json:"representation_space"`
	Role            string `json:"role"`
	TokenizerID     string `json:"tokenizer_id"`
	VocabularyID    string `json:"vocabulary_id,omitempty"`
	Data            []Item `json:"data"`
	Usage           *Usage `json:"usage"`
}

func (r Response) Validate(request Request, c Contract, physicalModel string) error {
	if err := request.Match(c, r.Space, r.ConfigurationID); err != nil {
		return err
	}
	if r.Model != physicalModel || r.OutputContract != request.OutputContract || r.ContractID != request.ContractID || r.Role != request.Role || r.TokenizerID != c.TokenizerID || r.VocabularyID != c.VocabularyID || r.Usage == nil || r.Usage.PromptTokens < 0 || r.Usage.TotalTokens < r.Usage.PromptTokens || r.Usage.TotalTokens > 1e12 {
		return errors.New("invalid representation response metadata")
	}
	if len(r.Data) != len(request.Input) {
		return errors.New("representation response cardinality mismatch")
	}
	expected := map[string]bool{}
	for _, in := range request.Input {
		expected[in.ID] = true
	}
	for _, item := range r.Data {
		if !expected[item.ID] {
			return errors.New("unknown or duplicate representation response ID")
		}
		delete(expected, item.ID)
		if err := item.validate(c); err != nil {
			return fmt.Errorf("representation item %q: %w", item.ID, err)
		}
	}
	return nil
}
func (v Item) validate(c Contract) error {
	invalid := errors.New("invalid representation shape, values or mask")
	switch c.Kind {
	case Dense:
		if v.Dense == nil || v.Sparse != nil || v.TokenMatrix != nil || len(v.Dense.Values) != c.Dimensions || !validValues(v.Dense.Values, c.Normalization, true) {
			return invalid
		}
	case Sparse:
		if v.Dense != nil || v.Sparse == nil || v.TokenMatrix != nil {
			return invalid
		}
		s := v.Sparse
		if len(s.Indices) == 0 || len(s.Indices) > c.MaxNonzero || len(s.Indices) != len(s.Weights) || !validValues(s.Weights, c.Normalization, true) {
			return invalid
		}
		last := -1
		for i, n := range s.Indices {
			if n <= last || n >= c.Dimensions || s.Weights[i] == 0 {
				return invalid
			}
			last = n
		}
	case TokenMatrix:
		if v.Dense != nil || v.Sparse != nil || v.TokenMatrix == nil {
			return invalid
		}
		m := v.TokenMatrix
		if len(m.Shape) != 2 || m.Shape[0] < 1 || m.Shape[0] > c.MaxTokens || m.Shape[1] != c.Dimensions || len(m.Values) != m.Shape[0] || len(m.Mask) != m.Shape[0] {
			return invalid
		}
		active := false
		for i, row := range m.Values {
			if len(row) != c.Dimensions || !validValues(row, "none", false) {
				return invalid
			}
			if m.Mask[i] {
				active = true
				if !validValues(row, c.Normalization, true) {
					return invalid
				}
			} else {
				for _, x := range row {
					if x != 0 {
						return invalid
					}
				}
			}
		}
		if !active {
			return invalid
		}
	default:
		return invalid
	}
	return nil
}
func validValues(values []float64, normalization string, nonzero bool) bool {
	sum := 0.0
	for _, x := range values {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return false
		}
		sum += x * x
	}
	if math.IsInf(sum, 0) || math.IsNaN(sum) {
		return false
	}
	if nonzero && sum == 0 {
		return false
	}
	return normalization != "l2" || math.Abs(sum-1) <= 1e-4
}
func identifier(v string) bool {
	return v != "" && len(v) <= 256 && strings.TrimSpace(v) == v && strings.IndexFunc(v, unicode.IsControl) < 0
}

// Decode rejects unknown fields, trailing JSON and duplicate object keys.
func Decode(data []byte, dst any) error {
	if err := uniqueKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func uniqueKeys(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("null is not a representation value")
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return errors.New("duplicate JSON key")
			}
			seen[s] = true
			if err := uniqueKeys(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	case json.Delim('['):
		for d.More() {
			if err := uniqueKeys(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	return nil
}

func (u *Usage) UnmarshalJSON(data []byte) error {
	var wire struct {
		PromptTokens *int64 `json:"prompt_tokens"`
		TotalTokens  *int64 `json:"total_tokens"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if wire.PromptTokens == nil || wire.TotalTokens == nil {
		return errors.New("representation usage requires prompt_tokens and total_tokens")
	}
	u.PromptTokens = *wire.PromptTokens
	u.TotalTokens = *wire.TotalTokens
	return nil
}
