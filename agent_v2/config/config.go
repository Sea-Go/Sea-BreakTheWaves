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
	Zhihu    ZhihuConfig    `yaml:"zhihu"`
	Bilibili BilibiliConfig `yaml:"bilibili"`
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
	AmapModel     string `yaml:"amap_model"`
	ApiKey        string `yaml:"apikey"`
}

type ZhihuConfig struct {
	AccessSecret    string                   `yaml:"access_secret"`
	OpenAPIBaseURL  string                   `yaml:"openapi_base_url"`
	ZhihuSearchURL  string                   `yaml:"zhihu_search_url"`
	GlobalSearchURL string                   `yaml:"global_search_url"`
	GuideMaterial   ZhihuGuideMaterialConfig `yaml:"guide_material"`
}

type ZhihuGuideMaterialConfig struct {
	QueryCount           int      `yaml:"query_count"`
	PerQueryCount        int      `yaml:"per_query_count"`
	ReviewPoolSize       int      `yaml:"review_pool_size"`
	SelectedArticleCount int      `yaml:"selected_article_count"`
	ArticleOnly          bool     `yaml:"article_only"`
	AcceptScore          float64  `yaml:"accept_score"`
	ReviewScore          float64  `yaml:"review_score"`
	MinSummaryChars      int      `yaml:"min_summary_chars"`
	MinVoteUpCount       int64    `yaml:"min_vote_up_count"`
	MaxAgeDays           int      `yaml:"max_age_days"`
	MustKeywords         []string `yaml:"must_keywords"`
	ShouldKeywords       []string `yaml:"should_keywords"`
	NegativeKeywords     []string `yaml:"negative_keywords"`
	BlockedAuthors       []string `yaml:"blocked_authors"`
	TrustedAuthors       []string `yaml:"trusted_authors"`
}

type BilibiliConfig struct {
	Cookie        string                      `yaml:"cookie"`
	SearchTimeout int                         `yaml:"search_timeout"`
	GuideMaterial BilibiliGuideMaterialConfig `yaml:"guide_material"`
}

type BilibiliGuideMaterialConfig struct {
	QueryCount         int      `yaml:"query_count"`
	PerQueryCount      int      `yaml:"per_query_count"`
	ReviewPoolSize     int      `yaml:"review_pool_size"`
	SelectedVideoCount int      `yaml:"selected_video_count"`
	AcceptScore        float64  `yaml:"accept_score"`
	ReviewScore        float64  `yaml:"review_score"`
	MinSummaryChars    int      `yaml:"min_summary_chars"`
	MinViewCount       int64    `yaml:"min_view_count"`
	MaxAgeDays         int      `yaml:"max_age_days"`
	MustKeywords       []string `yaml:"must_keywords"`
	ShouldKeywords     []string `yaml:"should_keywords"`
	NegativeKeywords   []string `yaml:"negative_keywords"`
	BlockedAuthors     []string `yaml:"blocked_authors"`
	TrustedAuthors     []string `yaml:"trusted_authors"`
}

func (c BilibiliGuideMaterialConfig) WithDefaults() BilibiliGuideMaterialConfig {
	if c.QueryCount <= 0 {
		c.QueryCount = 10
	}
	if c.PerQueryCount <= 0 {
		c.PerQueryCount = 10
	}
	if c.PerQueryCount > 20 {
		c.PerQueryCount = 20
	}
	if c.ReviewPoolSize <= 0 {
		c.ReviewPoolSize = 30
	}
	if c.SelectedVideoCount <= 0 {
		c.SelectedVideoCount = 12
	}
	if c.AcceptScore <= 0 {
		c.AcceptScore = 70
	}
	if c.ReviewScore <= 0 {
		c.ReviewScore = 45
	}
	if c.MinSummaryChars <= 0 {
		c.MinSummaryChars = 10
	}
	if c.MinViewCount < 0 {
		c.MinViewCount = 0
	}
	if c.MaxAgeDays <= 0 {
		c.MaxAgeDays = 1095
	}
	return c
}

func (c ZhihuGuideMaterialConfig) WithDefaults() ZhihuGuideMaterialConfig {
	if c.QueryCount <= 0 {
		c.QueryCount = 10
	}
	if c.PerQueryCount <= 0 {
		c.PerQueryCount = 10
	}
	if c.PerQueryCount > 10 {
		c.PerQueryCount = 10
	}
	if c.ReviewPoolSize <= 0 {
		c.ReviewPoolSize = 30
	}
	if c.SelectedArticleCount <= 0 {
		c.SelectedArticleCount = 12
	}
	if c.AcceptScore <= 0 {
		c.AcceptScore = 70
	}
	if c.ReviewScore <= 0 {
		c.ReviewScore = 45
	}
	if c.MinSummaryChars <= 0 {
		c.MinSummaryChars = 20
	}
	if c.MinVoteUpCount < 0 {
		c.MinVoteUpCount = 0
	}
	if c.MaxAgeDays <= 0 {
		c.MaxAgeDays = 1095
	}
	return c
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
	QPS            float64         `yaml:"qps"`
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
	if c.QPS <= 0 {
		c.QPS = 1
	}
	if c.Retry.MaxRetries < 0 {
		c.Retry.MaxRetries = 0
	}
	if c.Retry.BackoffSeconds <= 0 {
		c.Retry.BackoffSeconds = 0.5
	}
	return c
}
