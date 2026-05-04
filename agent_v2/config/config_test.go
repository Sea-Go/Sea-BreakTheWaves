package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSyncsYAMLConfig(t *testing.T) {
	oldCfg := Cfg
	defer func() { Cfg = oldCfg }()

	t.Setenv("AMAP_TEST_KEY", "resolved-amap-key")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`
ali:
  baseurl: "https://dashscope.example/v1"
  analysis_model: "analysis-model"
  test_model: "test-model"
  apikey: "llm-key"
postgres:
  dsn: "postgres://example"
  database: "agent_db"
agent:
  app_name: "BreakTheWaves"
  name: "chat-agent"
  session_table_prefix: "chat"
  session_ttl: "24h"
  async_persister_num: 4
  max_history_runs: 20
  preload_session_recall: 1
  preload_session_recall_min_score: 0.6
  read_header_timeout: "5s"
amap:
  baseurl: "https://amap.example/v4"
  api_key: "AMAP_TEST_KEY"
  free_only: true
  output: "JSON"
  timeout_seconds: 3
  retry:
    max_retries: 2
    backoff_seconds: 0.2
`), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := Load(configPath); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if Cfg.Ali.AnalysisModel != "analysis-model" {
		t.Fatalf("analysis_model not loaded, got %q", Cfg.Ali.AnalysisModel)
	}
	if Cfg.Amap.BaseURL != "https://amap.example/v4" {
		t.Fatalf("amap.baseurl not loaded, got %q", Cfg.Amap.BaseURL)
	}
	if Cfg.Amap.ResolvedAPIKey() != "resolved-amap-key" {
		t.Fatalf("ResolvedAPIKey() = %q", Cfg.Amap.ResolvedAPIKey())
	}
	if got := Cfg.Amap.WithDefaults().Retry.BackoffSeconds; got != 0.2 {
		t.Fatalf("retry.backoff_seconds = %v", got)
	}
}

func TestAmapConfigDefaultsAndEnvKeySource(t *testing.T) {
	cfg := AmapConfig{APIKey: "AMAP_MISSING_KEY"}.WithDefaults()
	if cfg.BaseURL != "" {
		t.Fatalf("default BaseURL = %q", cfg.BaseURL)
	}
	if cfg.Output != "JSON" {
		t.Fatalf("default Output = %q", cfg.Output)
	}
	if cfg.ResolvedAPIKey() != "" {
		t.Fatalf("missing env-style api key should resolve empty")
	}
	if got := cfg.APIKeySource(); got != "environment variable AMAP_MISSING_KEY" {
		t.Fatalf("APIKeySource() = %q", got)
	}

	literal := AmapConfig{APIKey: "literal-key"}.WithDefaults()
	if literal.ResolvedAPIKey() != "literal-key" {
		t.Fatalf("literal key resolved to %q", literal.ResolvedAPIKey())
	}
}
