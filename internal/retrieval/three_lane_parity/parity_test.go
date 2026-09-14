package parity

import (
	"context"
	"os"
	"testing"
)

func TestFrozenBGEThreeLaneParity(t *testing.T) {
	manifestPath := os.Getenv("SEA_BGE_THREE_LANE_MANIFEST")
	scorerPath := os.Getenv("SEA_BGE_THREE_LANE_SCORES")
	scorerSHA := os.Getenv("SEA_BGE_THREE_LANE_SCORES_SHA256")
	if manifestPath == "" || scorerPath == "" || scorerSHA == "" {
		t.Skip("set frozen BGE manifest and independent Python scorer path/SHA")
	}
	profiles, modelLock := pinnedBGEPaths()
	frozen, err := LoadFrozen(manifestPath, profiles, modelLock)
	if err != nil {
		t.Fatalf("load locked real BGE-M3 three-head values: %v", err)
	}
	scorer, actualSHA, err := LoadScorer(scorerPath, scorerSHA, frozen)
	if err != nil {
		t.Fatalf("load independent Python scorer: %v", err)
	}
	report, parityErr := Compare(context.Background(), frozen, scorer, actualSHA, t.TempDir())
	if path := os.Getenv("SEA_BGE_THREE_LANE_REPORT"); path != "" {
		if err := WriteReport(path, report); err != nil {
			t.Fatalf("write machine-readable parity report: %v", err)
		}
	}
	if parityErr != nil {
		t.Fatalf("existing Go exact lanes differ from Python frozen-value scores: %v", parityErr)
	}
	if report.Status != "passed" || !report.Lanes["dense"].TopKEqual ||
		!report.Lanes["sparse"].TopKEqual || !report.Lanes["token_matrix"].TopKEqual ||
		report.Lanes["sparse"].ZeroScoreExcluded == 0 || report.Lanes["sparse"].PythonFullTopKEqual {
		t.Fatalf("parity semantics not made explicit: %+v", report)
	}
	for lane, result := range report.Lanes {
		t.Logf("%s: scores=%d max_delta=%g topk_equal=%t python_full_topk_equal=%t zero_excluded=%d",
			lane, result.ComparedScores, result.MaxAbsDelta, result.TopKEqual,
			result.PythonFullTopKEqual, result.ZeroScoreExcluded)
	}
}
