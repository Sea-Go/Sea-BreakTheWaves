package config

import (
	"os"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

var Cfg Config

type Config struct {
	Ali      AliConfig      `yaml:"ali"`
	Postgres PostgresConfig `yaml:"postgres"`
	Agent    AgentConfig    `yaml:"agent"`
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
	AnalysisModel string `yaml:"analysismodel"`
	TestModel     string `yaml:"test_model"`
	ApiKey        string `yaml:"apikey"`
}

type PostgresConfig struct {
	DSN      string `yaml:"dsn"`
	Database string `yaml:"database"`
}

type AgentConfig struct {
	AppName                    string  `yaml:"app_name"`
	Name                       string  `yaml:"name"`
	SessionTablePrefix         string  `yaml:"session_table_prefix"`
	SessionTTL                 string  `yaml:"session_ttl"`
	AsyncPersisterNum          int     `yaml:"async_persister_num"`
	MaxHistoryRuns             int     `yaml:"max_history_runs"`
	PreloadSessionRecall       int     `yaml:"preload_session_recall"`
	PreloadSessionRecallMinScore float64 `yaml:"preload_session_recall_min_score"`
	ReadHeaderTimeout          string  `yaml:"read_header_timeout"`
}
