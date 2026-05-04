package config

import (
	"os"
	"strings"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

var Cfg Config

type Config struct {
	Ali      AliConfig      `yaml:"ali"`
	Postgres PostgresConfig `yaml:"postgres"`
	Agent    AgentConfig    `yaml:"agent"`
	Amap     AmapConfig     `yaml:"amap"`
}

func Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		zap.L().Error("读取配置文件失败", zap.Error(err), zap.String("path", path))
		return err
	}

	if err := yaml.Unmarshal(data, &Cfg); err != nil {
		zap.L().Error("解析配置文件失败", zap.Error(err), zap.String("path", path))
		return err
	}

	return nil
}
func Init() error {
	return Load("config.yaml")
}

type AliConfig struct {
	BaseURL       string `yaml:"baseurl"`
	AnalysisModel string `yaml:"analysis_model"`
	TestModel     string `yaml:"test_model"`
	ApiKey        string `yaml:"apikey"`
}

type PostgresConfig struct {
	DSN      string `yaml:"dsn"`
	Database string `yaml:"database"`
}

type AgentConfig struct {
	AppName                      string  `yaml:"app_name"`
	Name                         string  `yaml:"name"`
	SessionTablePrefix           string  `yaml:"session_table_prefix"`
	SessionTTL                   string  `yaml:"session_ttl"`
	AsyncPersisterNum            int     `yaml:"async_persister_num"`
	MaxHistoryRuns               int     `yaml:"max_history_runs"`
	PreloadSessionRecall         int     `yaml:"preload_session_recall"`
	PreloadSessionRecallMinScore float64 `yaml:"preload_session_recall_min_score"`
	ReadHeaderTimeout            string  `yaml:"read_header_timeout"`
}

type AmapConfig struct {
	BaseURL        string          `yaml:"baseurl"`
	APIKey         string          `yaml:"api_key"`
	FreeOnly       bool            `yaml:"free_only"`
	Output         string          `yaml:"output"`
	TimeoutSeconds int             `yaml:"timeout_seconds"`
	Retry          AmapRetryConfig `yaml:"retry"`
}

type AmapRetryConfig struct {
	MaxRetries     int     `yaml:"max_retries"`
	BackoffSeconds float64 `yaml:"backoff_seconds"`
}

func (c AmapConfig) WithDefaults() AmapConfig {
	if strings.TrimSpace(c.Output) == "" {
		c.Output = "JSON"
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 10
	}
	if c.Retry.MaxRetries < 0 {
		c.Retry.MaxRetries = 0
	}
	if c.Retry.BackoffSeconds <= 0 {
		c.Retry.BackoffSeconds = 0.5
	}
	return c
}

func (c AmapConfig) ResolvedAPIKey() string {
	raw := strings.TrimSpace(c.APIKey)
	if raw == "" {
		return ""
	}
	if value := strings.TrimSpace(os.Getenv(raw)); value != "" {
		return value
	}
	if looksLikeEnvName(raw) {
		return ""
	}
	return raw
}

func (c AmapConfig) APIKeySource() string {
	raw := strings.TrimSpace(c.APIKey)
	if raw == "" {
		return "amap.api_key"
	}
	if looksLikeEnvName(raw) {
		return "environment variable " + raw
	}
	return "amap.api_key"
}

func looksLikeEnvName(s string) bool {
	if s == "" || strings.ToUpper(s) != s || !strings.Contains(s, "_") {
		return false
	}
	for _, r := range s {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}
