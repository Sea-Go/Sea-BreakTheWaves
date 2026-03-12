package config

import (
	"os"
	"sea/zlog"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

var Cfg Config

// Config 是整个服务的配置入口。
// ⚠️ 按你的要求：项目里不使用 .env / getenv，一切配置都从 config.yaml 读取。
type Config struct {
	Log      LogConfig      `mapstructure:"log" yaml:"log"`
	Otel     OtelConfig     `mapstructure:"otel" yaml:"otel"`
	Postgres PostgresConfig `mapstructure:"postgres" yaml:"postgres"`

	Milvus   MilvusConfig   `mapstructure:"milvus" yaml:"milvus"`
	Cohere   CohereConfig   `mapstructure:"cohere" yaml:"cohere"`
	Ali      AliConfig      `mapstructure:"ali" yaml:"ali"`
	Kafka    KafkaConfig    `mapstructure:"Kafka" yaml:"Kafka"` // 注意：你 YAML 里是 "Kafka"
	Neo4j    Neo4jConfig    `mapstructure:"neo4j" yaml:"neo4j"`
	Services ServicesConfig `mapstructure:"services" yaml:"services"`

	Pools   PoolsConfig   `mapstructure:"pools" yaml:"pools"`
	Agent   AgentConfig   `mapstructure:"agent" yaml:"agent"`
	Split   SplitConfig   `mapstructure:"split" yaml:"split"`
	Ranking RankingConfig `mapstructure:"ranking" yaml:"ranking"`
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

type ServicesConfig struct {
	HTTPAddr string `mapstructure:"httpAddr" yaml:"httpAddr"`
	HTTPPort string `mapstructure:"httpPort" yaml:"httpPort"` // 你 YAML 里是字符串 "20721"，这里用 string 最稳
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
	Dim    int    `mapstructure:"dim" yaml:"dim"`
	Metric string `mapstructure:"metric" yaml:"metric"` // COSINE / IP / L2
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
	Address string `mapstructure:"address" yaml:"address"`
	Topic   string `mapstructure:"topic" yaml:"topic"`
	Group   string `mapstructure:"group" yaml:"group"`
}

type Neo4jConfig struct {
	Address  string `mapstructure:"address" yaml:"address"`
	Username string `mapstructure:"username" yaml:"username"`
	Password string `mapstructure:"password" yaml:"password"`
}

// PoolsConfig 维护多个“候选池子”的补充阈值与批量大小（长期/短期/周期）。
type PoolsConfig struct {
	LongTerm  PoolPolicy `mapstructure:"long_term" yaml:"long_term"`
	ShortTerm PoolPolicy `mapstructure:"short_term" yaml:"short_term"`
	Periodic  PoolPolicy `mapstructure:"periodic" yaml:"periodic"`

	Recommend RecommendPolicy `mapstructure:"recommend" yaml:"recommend"`
}

type PoolPolicy struct {
	MinSize    int `mapstructure:"min_size" yaml:"min_size"`
	RefillSize int `mapstructure:"refill_size" yaml:"refill_size"`
}

type RecommendPolicy struct {
	TakeSize             int  `mapstructure:"take_size" yaml:"take_size"`
	RemoveAfterRecommend bool `mapstructure:"remove_after_recommend" yaml:"remove_after_recommend"`
}

// AgentConfig 推荐 Agent 的基本运行参数（模型、预算、温度等）。
type AgentConfig struct {
	Model        string  `mapstructure:"model" yaml:"model"`
	Temperature  float64 `mapstructure:"temperature" yaml:"temperature"`
	MaxToolCalls int     `mapstructure:"max_tool_calls" yaml:"max_tool_calls"`
}

// SplitConfig 文档切分相关参数（避免切太碎/切太长）。
type SplitConfig struct {
	ChunkMaxTokens     int `mapstructure:"chunk_max_tokens" yaml:"chunk_max_tokens"`
	ChunkOverlapTokens int `mapstructure:"chunk_overlap_tokens" yaml:"chunk_overlap_tokens"`
	KeywordTopK        int `mapstructure:"keyword_topk" yaml:"keyword_topk"`

	// 记忆 tokenize 分块参数（用于 user_memory_chunks）
	MemoryChunkMaxTokens     int `mapstructure:"memory_chunk_max_tokens" yaml:"memory_chunk_max_tokens"`
	MemoryChunkOverlapTokens int `mapstructure:"memory_chunk_overlap_tokens" yaml:"memory_chunk_overlap_tokens"`
}

// RankingConfig 排序策略相关参数。
//
// 说明：
//   - SimilarityWeight / ScoreWeight 保留给旧的手工粗排逻辑兼容使用。
//   - RecommendCoarseLinearDecay 用于推荐接口的粗排：在 Milvus coarse search 阶段直接应用
//     linear decay reranker（基于 created_at_unix）。
type RankingConfig struct {
	SimilarityWeight           float64           `mapstructure:"similarity_weight" yaml:"similarity_weight"`
	ScoreWeight                float64           `mapstructure:"score_weight" yaml:"score_weight"`
	RecommendCoarseLinearDecay LinearDecayConfig `mapstructure:"recommend_coarse_linear_decay" yaml:"recommend_coarse_linear_decay"`
}

// LinearDecayConfig 是 Milvus linear decay reranker 的配置。
// 所有时间参数都使用与 collection 字段一致的单位；当前 coarse collection 的
// created_at_unix 以秒为单位，因此这里也全部使用秒。
type LinearDecayConfig struct {
	Enabled       bool    `mapstructure:"enabled" yaml:"enabled"`
	FieldName     string  `mapstructure:"field_name" yaml:"field_name"`
	OffsetSeconds int64   `mapstructure:"offset_seconds" yaml:"offset_seconds"`
	ScaleSeconds  int64   `mapstructure:"scale_seconds" yaml:"scale_seconds"`
	Decay         float64 `mapstructure:"decay" yaml:"decay"`
}

// Load 从指定路径读取 YAML 配置并反序列化到全局变量 Cfg。
func Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		zlog.L().Error("读取配置文件失败", zap.Error(err), zap.String("path", path))
		return err
	}

	if err := yaml.Unmarshal(data, &Cfg); err != nil {
		zlog.L().Error("解析配置文件失败", zap.Error(err), zap.String("path", path))
		return err
	}

	return nil
}

func Init() error {
	return Load("config.yaml")
}
