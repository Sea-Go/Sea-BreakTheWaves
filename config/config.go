package config

import (
	"fmt"
	"net/url"
	"os"

	"sea/zlog"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

var Cfg Config

type Config struct {
	Log            LogConfig            `mapstructure:"log" yaml:"log"`
	Otel           OtelConfig           `mapstructure:"otel" yaml:"otel"`
	Postgres       PostgresConfig       `mapstructure:"postgres" yaml:"postgres"`
	SourcePostgres SourcePostgresConfig `mapstructure:"source_postgres" yaml:"source_postgres"`

	Milvus MilvusConfig `mapstructure:"milvus" yaml:"milvus"`
	Cohere CohereConfig `mapstructure:"cohere" yaml:"cohere"`
	Ali    AliConfig    `mapstructure:"ali" yaml:"ali"`
	Kafka  KafkaConfig  `mapstructure:"Kafka" yaml:"Kafka"`

	ArticleSyncKafka       KafkaEndpointConfig `mapstructure:"article_sync_kafka" yaml:"article_sync_kafka"`
	ArticleSyncResultKafka KafkaEndpointConfig `mapstructure:"article_sync_result_kafka" yaml:"article_sync_result_kafka"`
	ArticleSyncRetryKafka  KafkaEndpointConfig `mapstructure:"article_sync_retry_kafka" yaml:"article_sync_retry_kafka"`

	Neo4j    Neo4jConfig    `mapstructure:"neo4j" yaml:"neo4j"`
	Services ServicesConfig `mapstructure:"services" yaml:"services"`

	Pools   PoolsConfig   `mapstructure:"pools" yaml:"pools"`
	Agent   AgentConfig   `mapstructure:"agent" yaml:"agent"`
	Split   SplitConfig   `mapstructure:"split" yaml:"split"`
	Ranking RankingConfig `mapstructure:"ranking" yaml:"ranking"`
	Search  SearchConfig  `mapstructure:"search" yaml:"search"`
}

type LogConfig struct {
	Path        string `mapstructure:"path" yaml:"path"`
	Level       string `mapstructure:"level" yaml:"level"`
	ServiceName string `mapstructure:"service_name" yaml:"service_name"`
}

type OtelConfig struct {
	Enable          bool   `mapstructure:"enable" yaml:"enable"`
	ServiceName     string `mapstructure:"service_name" yaml:"service_name"`
	OtlpGrpcAddress string `mapstructure:"otlp_grpc_address" yaml:"otlp_grpc_address"`
	Insecure        bool   `mapstructure:"insecure" yaml:"insecure"`
}

type PostgresConfig struct {
	DSN                    string `mapstructure:"dsn" yaml:"dsn"`
	MaxOpenConns           int    `mapstructure:"max_open_conns" yaml:"max_open_conns"`
	MaxIdleConns           int    `mapstructure:"max_idle_conns" yaml:"max_idle_conns"`
	ConnMaxLifetimeSeconds int    `mapstructure:"conn_max_lifetime_seconds" yaml:"conn_max_lifetime_seconds"`
}

type SourcePostgresConfig struct {
	Host                   string `mapstructure:"host" yaml:"host"`
	Port                   int    `mapstructure:"port" yaml:"port"`
	User                   string `mapstructure:"user" yaml:"user"`
	Password               string `mapstructure:"password" yaml:"password"`
	DBName                 string `mapstructure:"dbname" yaml:"dbname"`
	SSLMode                string `mapstructure:"sslmode" yaml:"sslmode"`
	MaxOpenConns           int    `mapstructure:"max_open_conns" yaml:"max_open_conns"`
	MaxIdleConns           int    `mapstructure:"max_idle_conns" yaml:"max_idle_conns"`
	ConnMaxLifetimeSeconds int    `mapstructure:"conn_max_lifetime_seconds" yaml:"conn_max_lifetime_seconds"`
}

func (c SourcePostgresConfig) HostValue() string {
	if c.Host != "" {
		return c.Host
	}
	return "127.0.0.1"
}

func (c SourcePostgresConfig) PortValue() int {
	if c.Port > 0 {
		return c.Port
	}
	return 35432
}

func (c SourcePostgresConfig) UserValue() string {
	if c.User != "" {
		return c.User
	}
	return "admin"
}

func (c SourcePostgresConfig) PasswordValue() string {
	if c.Password != "" {
		return c.Password
	}
	return "Sea-TryGo"
}

func (c SourcePostgresConfig) DBNameValue() string {
	if c.DBName != "" {
		return c.DBName
	}
	return "first_db"
}

func (c SourcePostgresConfig) SSLModeValue() string {
	if c.SSLMode != "" {
		return c.SSLMode
	}
	return "disable"
}

func (c SourcePostgresConfig) DSN() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		url.QueryEscape(c.UserValue()),
		url.QueryEscape(c.PasswordValue()),
		c.HostValue(),
		c.PortValue(),
		url.PathEscape(c.DBNameValue()),
		url.QueryEscape(c.SSLModeValue()),
	)
}

type ServicesConfig struct {
	HTTPAddr string `mapstructure:"httpAddr" yaml:"httpAddr"`
	HTTPPort string `mapstructure:"httpPort" yaml:"httpPort"`
}

type MilvusConfig struct {
	Address     string               `mapstructure:"address" yaml:"address"`
	Username    string               `mapstructure:"username" yaml:"username"`
	Password    string               `mapstructure:"password" yaml:"password"`
	DBName      string               `mapstructure:"dbname" yaml:"dbname"`
	Collections MilvusCollectionsCfg `mapstructure:"collections" yaml:"collections"`
}

type MilvusCollectionsCfg struct {
	Coarse string `mapstructure:"coarse" yaml:"coarse"`
	Fine   string `mapstructure:"fine" yaml:"fine"`
	Image  string `mapstructure:"image" yaml:"image"`
	Dim    int    `mapstructure:"dim" yaml:"dim"`
	Metric string `mapstructure:"metric" yaml:"metric"`
}

type CohereConfig struct {
	APIKey             string `mapstructure:"api_key" yaml:"api_key"`
	Model              string `mapstructure:"model" yaml:"model"`
	MaxClientBatchSize int    `mapstructure:"max_client_batch_size" yaml:"max_client_batch_size"`
	MaxTokensPerDoc    int    `mapstructure:"max_tokens_per_doc" yaml:"max_tokens_per_doc"`
}

type AliConfig struct {
	APIKey            string `mapstructure:"apikey" yaml:"apikey"`
	BaseURL           string `mapstructure:"baseurl" yaml:"baseurl"`
	MultimodalBaseURL string `mapstructure:"multimodal_baseurl" yaml:"multimodal_baseurl"`
	TextModel         string `mapstructure:"text_model" yaml:"text_model"`
	MultimodalModel   string `mapstructure:"multimodal_model" yaml:"multimodal_model"`
	Dimensions        int    `mapstructure:"dimensions" yaml:"dimensions"`
	RerankURL         string `yaml:"rerank_url"`
	RerankModel       string `yaml:"rerank_model"`
	RerankInstruct    string `yaml:"rerank_instruct"`
	RerankTopNCap     int    `yaml:"rerank_topn_cap"`
}

type KafkaConfig struct {
	Address    string `mapstructure:"address" yaml:"address"`
	Topic      string `mapstructure:"topic" yaml:"topic"`
	Group      string `mapstructure:"group" yaml:"group"`
	RetryTopic string `mapstructure:"retry_topic" yaml:"retry_topic"`
	RetryGroup string `mapstructure:"retry_group" yaml:"retry_group"`
}

type KafkaEndpointConfig struct {
	Address string `mapstructure:"address" yaml:"address"`
	Topic   string `mapstructure:"topic" yaml:"topic"`
	Group   string `mapstructure:"group" yaml:"group"`
}

type Neo4jConfig struct {
	Address  string `mapstructure:"address" yaml:"address"`
	Username string `mapstructure:"username" yaml:"username"`
	Password string `mapstructure:"password" yaml:"password"`
}

type PoolsConfig struct {
	LongTerm  PoolPolicy      `mapstructure:"long_term" yaml:"long_term"`
	ShortTerm PoolPolicy      `mapstructure:"short_term" yaml:"short_term"`
	Periodic  PoolPolicy      `mapstructure:"periodic" yaml:"periodic"`
	Async     AsyncPoolConfig `mapstructure:"async" yaml:"async"`
	Recommend RecommendPolicy `mapstructure:"recommend" yaml:"recommend"`
}

type PoolPolicy struct {
	MinSize    int `mapstructure:"min_size" yaml:"min_size"`
	RefillSize int `mapstructure:"refill_size" yaml:"refill_size"`
}

type AsyncPoolConfig struct {
	Enabled            *bool `mapstructure:"enabled" yaml:"enabled"`
	Workers            int   `mapstructure:"workers" yaml:"workers"`
	QueueSize          int   `mapstructure:"queue_size" yaml:"queue_size"`
	TaskTimeoutSeconds int   `mapstructure:"task_timeout_seconds" yaml:"task_timeout_seconds"`
	QueryFanout        int   `mapstructure:"query_fanout" yaml:"query_fanout"`
}

func (c AsyncPoolConfig) EnabledValue() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

func (c AsyncPoolConfig) WorkersValue() int {
	if c.Workers > 0 {
		return c.Workers
	}
	return 6
}

func (c AsyncPoolConfig) QueueSizeValue() int {
	if c.QueueSize > 0 {
		return c.QueueSize
	}
	return 256
}

func (c AsyncPoolConfig) TaskTimeoutSecondsValue() int {
	if c.TaskTimeoutSeconds > 0 {
		return c.TaskTimeoutSeconds
	}
	return 90
}

func (c AsyncPoolConfig) QueryFanoutValue() int {
	if c.QueryFanout > 0 {
		return c.QueryFanout
	}
	return 3
}

type RecommendPolicy struct {
	TakeSize             int  `mapstructure:"take_size" yaml:"take_size"`
	RemoveAfterRecommend bool `mapstructure:"remove_after_recommend" yaml:"remove_after_recommend"`
}

type AgentConfig struct {
	Model        string  `mapstructure:"model" yaml:"model"`
	Temperature  float64 `mapstructure:"temperature" yaml:"temperature"`
	MaxToolCalls int     `mapstructure:"max_tool_calls" yaml:"max_tool_calls"`
}

type SplitConfig struct {
	ChunkMaxTokens           int `mapstructure:"chunk_max_tokens" yaml:"chunk_max_tokens"`
	ChunkOverlapTokens       int `mapstructure:"chunk_overlap_tokens" yaml:"chunk_overlap_tokens"`
	KeywordTopK              int `mapstructure:"keyword_topk" yaml:"keyword_topk"`
	MemoryChunkMaxTokens     int `mapstructure:"memory_chunk_max_tokens" yaml:"memory_chunk_max_tokens"`
	MemoryChunkOverlapTokens int `mapstructure:"memory_chunk_overlap_tokens" yaml:"memory_chunk_overlap_tokens"`
}

type RankingConfig struct {
	SimilarityWeight           float64           `mapstructure:"similarity_weight" yaml:"similarity_weight"`
	ScoreWeight                float64           `mapstructure:"score_weight" yaml:"score_weight"`
	RecommendCoarseLinearDecay LinearDecayConfig `mapstructure:"recommend_coarse_linear_decay" yaml:"recommend_coarse_linear_decay"`
}

type LinearDecayConfig struct {
	Enabled       bool    `mapstructure:"enabled" yaml:"enabled"`
	FieldName     string  `mapstructure:"field_name" yaml:"field_name"`
	OffsetSeconds int64   `mapstructure:"offset_seconds" yaml:"offset_seconds"`
	ScaleSeconds  int64   `mapstructure:"scale_seconds" yaml:"scale_seconds"`
	Decay         float64 `mapstructure:"decay" yaml:"decay"`
}

type SearchConfig struct {
	CoarseRecallK        int     `mapstructure:"coarse_recall_k" yaml:"coarse_recall_k"`
	FineRecallK          int     `mapstructure:"fine_recall_k" yaml:"fine_recall_k"`
	MaxArticleCandidates int     `mapstructure:"max_article_candidates" yaml:"max_article_candidates"`
	MinRerankScore       float64 `mapstructure:"min_rerank_score" yaml:"min_rerank_score"`
	MinPassScore         float64 `mapstructure:"min_pass_score" yaml:"min_pass_score"`
	SupportBonus         float64 `mapstructure:"support_bonus" yaml:"support_bonus"`
}

func Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		zlog.L().Error("read config file failed", zap.Error(err), zap.String("path", path))
		return err
	}

	if err := yaml.Unmarshal(data, &Cfg); err != nil {
		zlog.L().Error("unmarshal config file failed", zap.Error(err), zap.String("path", path))
		return err
	}

	return nil
}

func Init() error {
	return Load("config.yaml")
}
