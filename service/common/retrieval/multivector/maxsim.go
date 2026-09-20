// Package multivector owns token-row retrieval and complete late interaction.
package multivector

import (
	"errors"
	"math"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/representation"
)

var ErrInvalid = errors.New("invalid multivector contract or artifact")

// ValidateMatrix independently checks stored and model-provided token rows.
// Masked rows must be zero and never participate in candidate or score math.
func ValidateMatrix(v representation.TokenValues, c representation.Contract) error {
	if c.Kind != representation.TokenMatrix || c.Validate() != nil || len(v.Shape) != 2 || v.Shape[0] < 1 || v.Shape[0] > c.MaxTokens || v.Shape[1] != c.Dimensions || len(v.Values) != v.Shape[0] || len(v.Mask) != v.Shape[0] {
		return ErrInvalid
	}
	active := 0
	for i, row := range v.Values {
		if len(row) != c.Dimensions {
			return ErrInvalid
		}
		norm := 0.0
		for _, x := range row {
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return ErrInvalid
			}
			if !v.Mask[i] && x != 0 {
				return ErrInvalid
			}
			norm += x * x
		}
		if math.IsNaN(norm) || math.IsInf(norm, 0) {
			return ErrInvalid
		}
		if v.Mask[i] {
			active++
			if norm == 0 || (c.Normalization == "l2" && math.Abs(norm-1) > 1e-4) {
				return ErrInvalid
			}
		}
	}
	if active == 0 {
		return ErrInvalid
	}
	return nil
}

func dot(a, b []float64) (float64, error) {
	if len(a) == 0 || len(a) != len(b) {
		return 0, ErrInvalid
	}
	var sum float64
	for i, x := range a {
		sum += x * b[i]
	}
	if math.IsInf(sum, 0) || math.IsNaN(sum) {
		return 0, ErrInvalid
	}
	return sum, nil
}

// MaxSim is the exact H05 late-interaction reference. sum_maxsim is a sum;
// mean_maxsim divides by the number of valid query tokens, not document rows.
func MaxSim(query, document representation.TokenValues, c representation.Contract) (float64, error) {
	if err := ValidateMatrix(query, c); err != nil {
		return 0, err
	}
	if err := ValidateMatrix(document, c); err != nil {
		return 0, err
	}
	var score float64
	validQueries := 0
	for i, q := range query.Values {
		if !query.Mask[i] {
			continue
		}
		best := math.Inf(-1)
		for j, d := range document.Values {
			if !document.Mask[j] {
				continue
			}
			value, err := dot(q, d)
			if err != nil {
				return 0, err
			}
			best = math.Max(best, value)
		}
		if math.IsInf(best, -1) {
			return 0, ErrInvalid
		}
		score += best
		validQueries++
	}
	if c.Aggregation == "mean_maxsim" {
		score /= float64(validQueries)
	}
	if math.IsInf(score, 0) || math.IsNaN(score) {
		return 0, ErrInvalid
	}
	return score, nil
}
