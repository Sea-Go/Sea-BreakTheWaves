package main

import (
	"os"
	"testing"
	"time"

	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
)

func TestFastMediumPolicyIsOptionalVersionedAndStrictlyBounded(t *testing.T) {
	values := testEnv(t)
	base, err := loadConfig(envMap(values))
	if err != nil || base.FastMedium != nil || len(base.Policy.Profiles[searchdomain.Fast]) != 1 ||
		base.Policy.Version != "local-fast-low-v1" || base.Policy.ProfileVersions != nil {
		t.Fatalf("old low-only policy changed: %+v, err=%v", base.Policy, err)
	}
	valid := `{"version":"local-fast-low-v1","fast_low":{"max_batches":1,"max_subqueries":1,"top_k_per_lane":8,"max_evidence":4,"wall_time":"5s"},"fast_medium":{"version":"local-fast-medium-v1","max_batches":1,"max_subqueries":3,"top_k_per_lane":8,"max_evidence":6,"wall_time":"15s","planner_max_output_tokens":128}}`
	for _, scenario := range []struct {
		name  string
		body  string
		valid bool
	}{
		{name: "explicit_medium", body: valid, valid: true},
		{name: "same_version", body: `{"version":"low","fast_low":{"max_batches":1,"max_subqueries":1,"top_k_per_lane":8,"max_evidence":4,"wall_time":"5s"},"fast_medium":{"version":"low","max_batches":1,"max_subqueries":3,"top_k_per_lane":8,"max_evidence":6,"wall_time":"15s","planner_max_output_tokens":128}}`},
		{name: "two_total_queries", body: `{"version":"low","fast_low":{"max_batches":1,"max_subqueries":1,"top_k_per_lane":8,"max_evidence":4,"wall_time":"5s"},"fast_medium":{"version":"medium","max_batches":1,"max_subqueries":2,"top_k_per_lane":8,"max_evidence":6,"wall_time":"15s","planner_max_output_tokens":128}}`},
		{name: "unbounded_model_output", body: `{"version":"low","fast_low":{"max_batches":1,"max_subqueries":1,"top_k_per_lane":8,"max_evidence":4,"wall_time":"5s"},"fast_medium":{"version":"medium","max_batches":1,"max_subqueries":3,"top_k_per_lane":8,"max_evidence":6,"wall_time":"15s","planner_max_output_tokens":0}}`},
		{name: "unbounded_wall", body: `{"version":"low","fast_low":{"max_batches":1,"max_subqueries":1,"top_k_per_lane":8,"max_evidence":4,"wall_time":"5s"},"fast_medium":{"version":"medium","max_batches":1,"max_subqueries":3,"top_k_per_lane":8,"max_evidence":6,"wall_time":"31s","planner_max_output_tokens":128}}`},
		{name: "extra_key", body: valid[:len(valid)-1] + `,"allow_detailed":true}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if err := os.WriteFile(values["BTW_SEARCH_POLICY_FILE"], []byte(scenario.body), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadConfig(envMap(values))
			if scenario.valid {
				if err != nil || cfg.FastMedium == nil || cfg.FastMedium.PlannerMaxOutputTokens != 128 || cfg.FastMedium.WallTime != 15*time.Second ||
					cfg.Policy.Version != "local-fast-low-v1" || cfg.Policy.ProfileVersions[searchdomain.Fast][searchdomain.Medium] != "local-fast-medium-v1" ||
					cfg.Policy.Profiles[searchdomain.Fast][searchdomain.Low].MaxSubqueries != 1 || cfg.Policy.Profiles[searchdomain.Fast][searchdomain.Medium].MaxSubqueries != 3 {
					t.Fatalf("medium policy did not preserve distinct low profile: %+v, err=%v", cfg.Policy, err)
				}
			} else if err == nil {
				t.Fatal("malformed model-planned policy was accepted")
			}
		})
	}
}
