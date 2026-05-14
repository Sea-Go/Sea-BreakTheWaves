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
  high_models: ["analysis-model"]
  low_models: ["low-model"]
  apikey: "llm-key"
postgres:
  dsn: "postgres://example"
  database: "agent_db"
zhihu:
  access_secret: "zhihu-secret"
  openapi_base_url: "https://developer.example.com"
  zhihu_search_url: "https://developer.example.com/zhihu"
  global_search_url: "https://developer.example.com/global"
bilibili:
  cookie: "SESSDATA=test-cookie"
  search_timeout: 7
  guide_material:
    query_count: 8
    per_query_count: 12
    review_pool_size: 20
    selected_video_count: 6
    min_view_count: 1000
    should_keywords: ["美食", "景点"]
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
`), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := Load(configPath); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(Cfg.Ali.HighModels) == 0 || Cfg.Ali.HighModels[0] != "analysis-model" {
		t.Fatalf("high_models[0] not loaded, got %v", Cfg.Ali.HighModels)
	}
	if Cfg.Amap.BaseURL != "https://amap.example/v4" {
		t.Fatalf("amap.baseurl not loaded, got %q", Cfg.Amap.BaseURL)
	}
	if Cfg.Amap.APIKey != "literal-amap-key" {
		t.Fatalf("amap.api_key not loaded, got %q", Cfg.Amap.APIKey)
	}
	if Cfg.Bilibili.Cookie != "SESSDATA=test-cookie" {
		t.Fatalf("bilibili.cookie not loaded, got %q", Cfg.Bilibili.Cookie)
	}
	if Cfg.Zhihu.GlobalSearchURL != "https://developer.example.com/global" {
		t.Fatalf("zhihu.global_search_url not loaded, got %q", Cfg.Zhihu.GlobalSearchURL)
	}
	if Cfg.Bilibili.SearchTimeout != 7 {
		t.Fatalf("bilibili.search_timeout = %d, want 7", Cfg.Bilibili.SearchTimeout)
	}
	biliGuide := Cfg.Bilibili.GuideMaterial.WithDefaults()
	if biliGuide.QueryCount != 8 || biliGuide.PerQueryCount != 12 || biliGuide.SelectedVideoCount != 6 {
		t.Fatalf("bilibili guide config not loaded: %+v", biliGuide)
	}
	if len(biliGuide.ShouldKeywords) != 2 || biliGuide.ShouldKeywords[0] != "美食" {
		t.Fatalf("bilibili should_keywords not loaded: %+v", biliGuide.ShouldKeywords)
	}
	if got := Cfg.Amap.WithDefaults().Retry.BackoffSeconds; got != 0.2 {
		t.Fatalf("retry.backoff_seconds = %v", got)
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

func TestBilibiliGuideMaterialConfigDefaults(t *testing.T) {
	cfg := BilibiliGuideMaterialConfig{PerQueryCount: 99, MinViewCount: -10}.WithDefaults()
	if cfg.QueryCount != 10 {
		t.Fatalf("default QueryCount = %d, want 10", cfg.QueryCount)
	}
	if cfg.PerQueryCount != 20 {
		t.Fatalf("clamped PerQueryCount = %d, want 20", cfg.PerQueryCount)
	}
	if cfg.ReviewPoolSize != 30 {
		t.Fatalf("default ReviewPoolSize = %d, want 30", cfg.ReviewPoolSize)
	}
	if cfg.SelectedVideoCount != 12 {
		t.Fatalf("default SelectedVideoCount = %d, want 12", cfg.SelectedVideoCount)
	}
	if cfg.AcceptScore != 70 || cfg.ReviewScore != 45 {
		t.Fatalf("default scores = %.1f/%.1f, want 70/45", cfg.AcceptScore, cfg.ReviewScore)
	}
	if cfg.MinSummaryChars != 10 {
		t.Fatalf("default MinSummaryChars = %d, want 10", cfg.MinSummaryChars)
	}
	if cfg.MinViewCount != 0 {
		t.Fatalf("negative MinViewCount normalized to %d, want 0", cfg.MinViewCount)
	}
	if cfg.MaxAgeDays != 1095 {
		t.Fatalf("default MaxAgeDays = %d, want 1095", cfg.MaxAgeDays)
	}
}
