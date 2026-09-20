package main

import (
	"testing"

	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/search"
)

// TestHistoryBudgetMustBeOneExplicitPair pins the deployment gate: injection
// stays off by default, one-sided bounds never start the process, and a valid
// pair reaches the boundary verbatim.
func TestHistoryBudgetMustBeOneExplicitPair(t *testing.T) {
	values := testEnv(t)
	cfg, err := loadConfig(envMap(values))
	if err != nil || cfg.History != (searchdomain.RootHistoryBudget{}) {
		t.Fatalf("default config acquired a history budget: %+v %v", cfg.History, err)
	}
	values["BTW_SEARCH_HISTORY_MAX_TURNS"] = "3"
	if _, err := loadConfig(envMap(values)); err == nil {
		t.Fatal("history budget accepted a turn bound without its byte bound")
	}
	delete(values, "BTW_SEARCH_HISTORY_MAX_TURNS")
	values["BTW_SEARCH_HISTORY_MAX_BYTES"] = "2048"
	if _, err := loadConfig(envMap(values)); err == nil {
		t.Fatal("history budget accepted a byte bound without its turn bound")
	}
	values["BTW_SEARCH_HISTORY_MAX_TURNS"] = "0"
	if _, err := loadConfig(envMap(values)); err == nil {
		t.Fatal("history budget accepted zero turns")
	}
	values["BTW_SEARCH_HISTORY_MAX_TURNS"] = "9"
	if _, err := loadConfig(envMap(values)); err == nil {
		t.Fatal("history budget accepted more than eight turns")
	}
	values["BTW_SEARCH_HISTORY_MAX_TURNS"] = "3"
	values["BTW_SEARCH_HISTORY_MAX_BYTES"] = "128"
	if _, err := loadConfig(envMap(values)); err == nil {
		t.Fatal("history budget accepted an undersized byte bound")
	}
	values["BTW_SEARCH_HISTORY_MAX_BYTES"] = "65536"
	if _, err := loadConfig(envMap(values)); err == nil {
		t.Fatal("history budget accepted an oversized byte bound")
	}
	values["BTW_SEARCH_HISTORY_MAX_BYTES"] = "not-a-number"
	if _, err := loadConfig(envMap(values)); err == nil {
		t.Fatal("history budget accepted a non-integer byte bound")
	}
	values["BTW_SEARCH_HISTORY_MAX_BYTES"] = "4096"
	cfg, err = loadConfig(envMap(values))
	if err != nil || cfg.History != (searchdomain.RootHistoryBudget{MaxTurns: 3, MaxBytes: 4096}) {
		t.Fatalf("valid budget did not reach the boundary verbatim: %+v %v", cfg.History, err)
	}
}
