package settings

import (
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
)

type Storage struct {
	ItemPartitions int `json:",default=32"`
	DSN            string
	Schema         string
	ObjectMode     string
	Directory      string
	S3Endpoint     string `json:",optional"`
	S3Bucket       string `json:",optional"`
	S3AccessKey    string `json:",optional"`
	S3SecretKey    string `json:",optional"`
	S3Secure       bool   `json:",optional"`
	S3Prefix       string `json:",default=btw"`
}
type Limits struct {
	QueryBytes          int64 `json:",default=1048576"`
	LexicalSectionBytes int64 `json:",default=67108864"`
	UploadBytes         int64
	UncompressedBytes   int64
	WindowRunes         int
	LeaseSeconds        int
	MaxAttempts         int
	ReadBytes           int64
}
type Milvus struct {
	Address         string
	Token           string `json:",optional"`
	BatchSize       int
	QueryTokenLimit int
}
type Elasticsearch struct {
	URL      string
	Index    string
	Username string `json:",optional"`
	Password string `json:",optional"`
	APIKey   string `json:",optional"`
}
type Neo4j struct {
	URI      string
	Database string `json:",default=neo4j"`
	Username string
	Password string
}
type Kafka struct {
	Brokers       []string `json:",optional"`
	ContentTopic  string   `json:",optional"`
	Group         string   `json:",optional"`
	BehaviorTopic string   `json:",optional"`
}
type Analytics struct {
	URL      string `json:",optional"`
	Database string `json:",default=btw"`
	User     string `json:",optional"`
	Password string `json:",optional"`
}
type Observability struct {
	Endpoint    string `json:",optional"`
	Environment string `json:",default=development"`
}
type Config struct {
	DataCenterURL string
	Observability Observability `json:",optional"`
	Storage       Storage
	Limits        Limits
	Milvus        Milvus
	Elasticsearch Elasticsearch
	Neo4j         Neo4j
	Kafka         Kafka
	Analytics     Analytics
	Scenarios     map[string]item.Scenario
}
