package parity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
)

const ScoringSchema = "sea.search.three-lane-scoring.v1"
const ReportSchema = "sea.search.three-lane-go-parity.v1"
const scoreTolerance = 1e-9

type ScoredChunk struct {
	DocumentID       string  `json:"document_id"`
	DocumentRevision string  `json:"document_revision"`
	ChunkID          string  `json:"chunk_id"`
	ChunkKey         string  `json:"chunk_key"`
	Score            float64 `json:"score"`
	JudgedMask       bool    `json:"judged_mask"`
	Grade            *int    `json:"grade"`
}

type ScorerLane struct {
	RawScores  []ScoredChunk   `json:"raw_scores"`
	TopK       []ScoredChunk   `json:"top_k"`
	Coverage   json.RawMessage `json:"judged_coverage"`
	Evaluation json.RawMessage `json:"evaluation"`
}

type ScorerQuery struct {
	QueryID                string                `json:"query_id"`
	QueryFamilyID          string                `json:"query_family_id"`
	NearDuplicateClusterID string                `json:"near_duplicate_cluster_id"`
	CandidateSet           []string              `json:"candidate_set"`
	Lanes                  map[string]ScorerLane `json:"lanes"`
}

type ScorerReport struct {
	SchemaVersion                string          `json:"schema_version"`
	Status                       string          `json:"status"`
	RepresentationManifestSHA256 string          `json:"representation_manifest_sha256"`
	QrelManifestSHA256           string          `json:"qrel_manifest_sha256"`
	DataKind                     string          `json:"data_kind"`
	QrelReaderStatus             string          `json:"qrel_reader_status"`
	CandidatePoolScope           string          `json:"candidate_pool_scope"`
	Evaluation                   json.RawMessage `json:"evaluation"`
	Split                        string          `json:"split"`
	TopK                         int             `json:"top_k"`
	Queries                      []ScorerQuery   `json:"queries"`
}

type LaneReport struct {
	ComparedScores      int     `json:"compared_scores"`
	TopKEqual           bool    `json:"topk_equal"`
	PythonFullTopKEqual bool    `json:"python_full_topk_equal"`
	ZeroScoreExcluded   int     `json:"zero_score_excluded"`
	MaxAbsDelta         float64 `json:"max_abs_delta"`
	CandidateSemantics  string  `json:"candidate_semantics"`
}

type ParityReport struct {
	SchemaVersion                string                `json:"schema_version"`
	Status                       string                `json:"status"`
	RepresentationManifestSHA256 string                `json:"representation_manifest_sha256"`
	PythonScoresSHA256           string                `json:"python_scores_sha256"`
	QrelManifestSHA256           string                `json:"qrel_manifest_sha256"`
	QueryCount                   int                   `json:"query_count"`
	ChunkCount                   int                   `json:"chunk_count"`
	MultivectorFullTokenRows     int                   `json:"multivector_full_token_rows"`
	Lanes                        map[string]LaneReport `json:"lanes"`
	Limits                       []string              `json:"limits"`
}

func LoadScorer(path, expectedSHA string, frozen Frozen) (ScorerReport, string, error) {
	var report ScorerReport
	body, err := os.ReadFile(path)
	if err != nil || len(body) == 0 || len(body) > 16<<20 || expectedSHA == "" || digest(body) != expectedSHA {
		return report, "", ErrArtifact
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return ScorerReport{}, "", fmt.Errorf("Python scorer JSON: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ScorerReport{}, "", fmt.Errorf("Python scorer trailing JSON: %w", ErrArtifact)
	}
	if report.SchemaVersion != ScoringSchema ||
		report.Status != "candidate_default_off" || report.RepresentationManifestSHA256 != frozen.ManifestSHA256 ||
		report.QrelManifestSHA256 != frozen.Manifest.DatasetManifestSHA256 || report.DataKind != frozen.Manifest.DataKind ||
		report.CandidatePoolScope != "all_frozen_chunks_available_at_query_time" || report.Split != "test" ||
		report.TopK < 1 || len(report.Queries) == 0 {
		return ScorerReport{}, "", fmt.Errorf("Python scorer metadata: %w", ErrArtifact)
	}
	var evaluation struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(report.Evaluation, &evaluation) != nil || evaluation.Status != "not_evaluable" {
		return ScorerReport{}, "", fmt.Errorf("Python scorer evaluation: %w", ErrArtifact)
	}
	queries := map[string]QueryRow{}
	for _, query := range frozen.Queries {
		queries[query.QueryID] = query
	}
	chunks := map[string]ChunkRow{}
	for _, chunk := range frozen.Chunks {
		chunks[chunk.ChunkKey] = chunk
	}
	seenQueries := map[string]bool{}
	for _, query := range report.Queries {
		frozenQuery, ok := queries[query.QueryID]
		if !ok || seenQueries[query.QueryID] || frozenQuery.Split != report.Split ||
			frozenQuery.QueryFamilyID != query.QueryFamilyID ||
			frozenQuery.NearDuplicateClusterID != query.NearDuplicateClusterID ||
			len(query.Lanes) != 3 || len(query.CandidateSet) == 0 {
			return ScorerReport{}, "", fmt.Errorf("Python scorer query %s scope: %w", query.QueryID, ErrArtifact)
		}
		seenQueries[query.QueryID] = true
		seenCandidates := map[string]bool{}
		for _, key := range query.CandidateSet {
			if _, ok := chunks[key]; !ok || seenCandidates[key] {
				return ScorerReport{}, "", fmt.Errorf("Python scorer candidate %s: %w", key, ErrArtifact)
			}
			seenCandidates[key] = true
		}
		for _, lane := range []string{"dense", "sparse", "token_matrix"} {
			item, ok := query.Lanes[lane]
			if !ok || len(item.RawScores) != len(query.CandidateSet) || len(item.TopK) != min(report.TopK, len(item.RawScores)) {
				return ScorerReport{}, "", fmt.Errorf("Python scorer lane %s cardinality: %w", lane, ErrArtifact)
			}
			seenRaw := map[string]bool{}
			for i, score := range item.RawScores {
				chunk, ok := chunks[score.ChunkKey]
				if !ok || !seenCandidates[score.ChunkKey] || seenRaw[score.ChunkKey] ||
					chunk.DocumentID != score.DocumentID || chunk.DocumentRevision != score.DocumentRevision ||
					chunk.ChunkID != score.ChunkID || math.IsInf(score.Score, 0) || math.IsNaN(score.Score) ||
					(i > 0 && scoredLess(score, item.RawScores[i-1])) {
					return ScorerReport{}, "", fmt.Errorf("Python scorer lane %s raw row %d: %w", lane, i, ErrArtifact)
				}
				seenRaw[score.ChunkKey] = true
			}
			for i, top := range item.TopK {
				if top.ChunkKey != item.RawScores[i].ChunkKey || top.Score != item.RawScores[i].Score {
					return ScorerReport{}, "", fmt.Errorf("Python scorer lane %s TopK row %d: %w", lane, i, ErrArtifact)
				}
			}
		}
	}
	return report, expectedSHA, nil
}

func scoredLess(a, b ScoredChunk) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if a.DocumentID != b.DocumentID {
		return a.DocumentID < b.DocumentID
	}
	if a.DocumentRevision != b.DocumentRevision {
		return a.DocumentRevision < b.DocumentRevision
	}
	return a.ChunkID < b.ChunkID
}

func hitKey(hit Hit, chunks map[string]ChunkRow) (string, error) {
	key := hit.DocumentID + "\x00" + hit.DocumentRevision + "\x00" + hit.ChunkID
	row, ok := chunks[key]
	if !ok {
		return "", ErrArtifact
	}
	return row.ChunkKey, nil
}

func compareLane(name string, scorer ScorerLane, got []Hit, topK int,
	chunks map[string]ChunkRow) (LaneReport, error) {
	result := LaneReport{TopKEqual: true, PythonFullTopKEqual: true}
	if name == "sparse" {
		result.CandidateSemantics = "positive_intersection_only"
	} else {
		result.CandidateSemantics = "all_time_eligible_frozen_chunks"
	}
	goScores := map[string]float64{}
	goKeys := make([]string, 0, len(got))
	for _, item := range got {
		key, err := hitKey(item, chunks)
		if err != nil || math.IsInf(item.Score, 0) || math.IsNaN(item.Score) {
			return result, ErrArtifact
		}
		if _, duplicate := goScores[key]; duplicate {
			return result, ErrArtifact
		}
		goScores[key] = item.Score
		goKeys = append(goKeys, key)
	}
	expectedKeys := make([]string, 0, len(scorer.RawScores))
	for _, raw := range scorer.RawScores {
		gotScore, ok := goScores[raw.ChunkKey]
		if name == "sparse" && raw.Score == 0 && !ok {
			result.ZeroScoreExcluded++
			gotScore = 0
		} else if !ok {
			return result, fmt.Errorf("%w: nonzero or dense/multi score missing", ErrArtifact)
		}
		delta := math.Abs(raw.Score - gotScore)
		if delta > scoreTolerance {
			return result, fmt.Errorf("%w: %s score delta %g exceeds %g", ErrArtifact, name, delta, scoreTolerance)
		}
		result.MaxAbsDelta = max(result.MaxAbsDelta, delta)
		result.ComparedScores++
		if name != "sparse" || raw.Score > 0 {
			expectedKeys = append(expectedKeys, raw.ChunkKey)
		}
	}
	if len(goKeys) != len(expectedKeys) {
		return result, ErrArtifact
	}
	for i, key := range expectedKeys {
		if goKeys[i] != key {
			return result, fmt.Errorf("%w: %s positive/full rank changed", ErrArtifact, name)
		}
	}
	for i, key := range goKeys[:min(topK, len(goKeys))] {
		if i >= len(expectedKeys) || key != expectedKeys[i] {
			result.TopKEqual = false
		}
		if i >= len(scorer.TopK) || key != scorer.TopK[i].ChunkKey {
			result.PythonFullTopKEqual = false
		}
	}
	if len(goKeys) < len(scorer.TopK) {
		result.PythonFullTopKEqual = false
	}
	if !result.TopKEqual {
		return result, ErrArtifact
	}
	return result, nil
}

// Compare runs all three existing Go exact lanes on the scorer's explicit
// time-eligible candidate sets and checks raw values plus rank semantics.
func Compare(ctx context.Context, frozen Frozen, scorer ScorerReport, scorerSHA, artifactDir string) (ParityReport, error) {
	report := ParityReport{SchemaVersion: ReportSchema, Status: "failed",
		RepresentationManifestSHA256: frozen.ManifestSHA256, PythonScoresSHA256: scorerSHA,
		QrelManifestSHA256: frozen.Manifest.DatasetManifestSHA256,
		QueryCount:         len(scorer.Queries), ChunkCount: len(frozen.Chunks),
		Lanes: map[string]LaneReport{}, Limits: []string{
			"synthetic H10.b qrels only; no all-candidates-judged receipt or human relevance claim",
			"exact full-token reference, not ANN scale or production token budget",
			"candidate_set is the Python reader's query-time eligibility scope; online search time filter is not proven",
		}}
	allowed := map[string][]string{}
	for _, query := range scorer.Queries {
		allowed[query.QueryID] = query.CandidateSet
	}
	got, err := RunExact(ctx, frozen, artifactDir, allowed)
	if err != nil {
		return report, err
	}
	report.MultivectorFullTokenRows = got.FullTokenRows
	chunks := map[string]ChunkRow{}
	for _, chunk := range frozen.Chunks {
		chunks[chunk.DocumentID+"\x00"+chunk.DocumentRevision+"\x00"+chunk.ChunkID] = chunk
	}
	for _, name := range []string{"dense", "sparse", "token_matrix"} {
		aggregate := LaneReport{TopKEqual: true, PythonFullTopKEqual: true}
		for _, query := range scorer.Queries {
			var hits []Hit
			switch name {
			case "dense":
				hits = got.Dense[query.QueryID]
			case "sparse":
				hits = got.Sparse[query.QueryID]
			case "token_matrix":
				hits = got.TokenMatrix[query.QueryID]
			}
			lane, err := compareLane(name, query.Lanes[name], hits, scorer.TopK, chunks)
			if err != nil {
				return report, err
			}
			aggregate.ComparedScores += lane.ComparedScores
			aggregate.ZeroScoreExcluded += lane.ZeroScoreExcluded
			aggregate.MaxAbsDelta = max(aggregate.MaxAbsDelta, lane.MaxAbsDelta)
			aggregate.TopKEqual = aggregate.TopKEqual && lane.TopKEqual
			aggregate.PythonFullTopKEqual = aggregate.PythonFullTopKEqual && lane.PythonFullTopKEqual
			aggregate.CandidateSemantics = lane.CandidateSemantics
		}
		report.Lanes[name] = aggregate
	}
	report.Status = "passed"
	return report, nil
}

func WriteReport(path string, report ParityReport) error {
	if path == "" {
		return ErrArtifact
	}
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(body)
	return err
}
