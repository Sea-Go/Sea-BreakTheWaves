package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSyncsYAMLConfig(t *testing.T) {
	oldCfg := Cfg
	defer func() { Cfg = oldCfg }()

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
  api_key: "literal-amap-key"
  free_only: true
  output: "JSON"
  timeout_seconds: 3
  retry:
    max_retries: 2
    backoff_seconds: 0.2
backend:
  article_base_url: "https://article.example"
  comment_base_url: "https://comment.example"
  auth_token: "jwt-token"
  timeout_seconds: 9
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
	if Cfg.Amap.APIKey != "literal-amap-key" {
		t.Fatalf("amap.api_key not loaded, got %q", Cfg.Amap.APIKey)
	}
	if got := Cfg.Amap.WithDefaults().Retry.BackoffSeconds; got != 0.2 {
		t.Fatalf("retry.backoff_seconds = %v", got)
	}
	if Cfg.Backend.ArticleBaseURL != "https://article.example" {
		t.Fatalf("backend.article_base_url = %q", Cfg.Backend.ArticleBaseURL)
	}
	if got := Cfg.Backend.WithDefaults().TimeoutSeconds; got != 9 {
		t.Fatalf("backend.timeout_seconds = %v", got)
	}
}

func TestAmapConfigDefaults(t *testing.T) {
	cfg := AmapConfig{APIKey: "literal-key"}.WithDefaults()
	if cfg.BaseURL != "" {
		t.Fatalf("default BaseURL = %q", cfg.BaseURL)
	}
	if cfg.Output != "JSON" {
		t.Fatalf("default Output = %q", cfg.Output)
	}
	if cfg.APIKey != "literal-key" {
		t.Fatalf("api key changed to %q", cfg.APIKey)
	}
}

func TestBackendConfigDefaults(t *testing.T) {
	cfg := BackendConfig{}.WithDefaults()
	if cfg.ArticleBaseURL != "http://127.0.0.1:8889" {
		t.Fatalf("default ArticleBaseURL = %q", cfg.ArticleBaseURL)
	}
	if cfg.CommentBaseURL != "http://127.0.0.1:8888" {
		t.Fatalf("default CommentBaseURL = %q", cfg.CommentBaseURL)
	}
	if cfg.TimeoutSeconds != 15 {
		t.Fatalf("default TimeoutSeconds = %d", cfg.TimeoutSeconds)
	}
}
