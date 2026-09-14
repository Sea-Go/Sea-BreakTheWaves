package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
var revision = regexp.MustCompile(`^[0-9a-f]{40}$`)
var factConsumer = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

const favoriteFactJobType = "usermodel.favorite-facts.v1"

type config struct {
	JobType        string
	IndexConfig    string
	IndexBackend   string
	IndexSettings  indexSettings
	WorkerID       string
	Resource       string
	LeaseSeconds   int
	PollInterval   time.Duration
	HTTPTimeout    time.Duration
	DCURL          string
	DCToken        string
	RTWURL         string
	RTWToken       string
	ContentDSN     string
	ContentSchema  string
	ContentMigrate bool
	ArtifactDir    string
	ChunkProfile   string
	ChunkSize      int
	ChunkOverlap   int
	SessionDSN     string
	SessionSchema  string
	SessionPrefix  string
	SessionInit    bool
	OTLPTracesURL  string
	MetricsAddr    string
	Version        string
	Environment    string
	InstanceID     string
	FactConsumer   string
	FactBatchLimit int
	FactDSN        string
	FactSchema     string
	AuthorityURL   string
	AuthorityToken string
}

func loadConfig(getenv func(string) string) (config, error) {
	var c config
	if getenv("BTW_JOB_TYPE") == favoriteFactJobType {
		return loadFavoriteFactConfig(getenv)
	}
	if getenv("BTW_MODE") != "local" || getenv("BTW_ARTIFACT_STORE") != "local" {
		return c, errors.New("BTW_MODE and BTW_ARTIFACT_STORE must explicitly be local")
	}
	required := []string{"BTW_WORKER_ID", "BTW_RESOURCE_PROFILE", "BTW_DC_URL", "BTW_RTW_URL",
		"BTW_CONTENT_POSTGRES_DSN", "BTW_CONTENT_SCHEMA", "BTW_ARTIFACT_DIR",
		"BTW_SESSION_POSTGRES_DSN", "BTW_SESSION_SCHEMA", "BTW_SESSION_TABLE_PREFIX",
		"BTW_OTLP_TRACES_URL", "BTW_METRICS_ADDR", "BTW_SERVICE_VERSION", "BTW_ENVIRONMENT", "BTW_INSTANCE_ID"}
	jobType := getenv("BTW_JOB_TYPE")
	if jobType == "" || jobType == "content.prepare.v1" {
		required = append(required, "BTW_CHUNK_PROFILE_ID")
	}
	for _, key := range required {
		if strings.TrimSpace(getenv(key)) == "" {
			return c, fmt.Errorf("%s is required", key)
		}
	}
	c.WorkerID, c.Resource = getenv("BTW_WORKER_ID"), getenv("BTW_RESOURCE_PROFILE")
	c.JobType = getenv("BTW_JOB_TYPE")
	if c.JobType == "" {
		c.JobType = "content.prepare.v1"
	}
	if c.JobType != "content.prepare.v1" && c.JobType != "content.build.v1" {
		return config{}, errors.New("BTW_JOB_TYPE must be content.prepare.v1 or content.build.v1")
	}
	if c.JobType == "content.build.v1" {
		c.IndexConfig, c.IndexBackend = strings.TrimSpace(getenv("BTW_INDEX_CONFIG_FILE")), getenv("BTW_INDEX_BACKEND")
		if c.IndexConfig == "" || c.IndexBackend != "exact" {
			return config{}, errors.New("content.build.v1 requires BTW_INDEX_CONFIG_FILE and BTW_INDEX_BACKEND=exact")
		}
	}
	c.DCURL, c.DCToken = getenv("BTW_DC_URL"), getenv("BTW_DC_TOKEN")
	c.RTWURL, c.RTWToken = getenv("BTW_RTW_URL"), getenv("BTW_RTW_TOKEN")
	c.ContentDSN, c.ContentSchema = getenv("BTW_CONTENT_POSTGRES_DSN"), getenv("BTW_CONTENT_SCHEMA")
	c.ArtifactDir, c.ChunkProfile = getenv("BTW_ARTIFACT_DIR"), getenv("BTW_CHUNK_PROFILE_ID")
	c.SessionDSN, c.SessionSchema, c.SessionPrefix = getenv("BTW_SESSION_POSTGRES_DSN"), getenv("BTW_SESSION_SCHEMA"), getenv("BTW_SESSION_TABLE_PREFIX")
	c.OTLPTracesURL, c.MetricsAddr = getenv("BTW_OTLP_TRACES_URL"), getenv("BTW_METRICS_ADDR")
	c.Version, c.Environment, c.InstanceID = getenv("BTW_SERVICE_VERSION"), getenv("BTW_ENVIRONMENT"), getenv("BTW_INSTANCE_ID")
	var err error
	if c.LeaseSeconds, err = positiveInt(getenv("BTW_LEASE_SECONDS"), "BTW_LEASE_SECONDS", 5, 3600); err != nil {
		return config{}, err
	}
	if c.JobType == "content.prepare.v1" {
		if c.ChunkSize, err = positiveInt(getenv("BTW_CHUNK_SIZE"), "BTW_CHUNK_SIZE", 1, 32768); err != nil {
			return config{}, err
		}
		if c.ChunkOverlap, err = positiveInt(getenv("BTW_CHUNK_OVERLAP"), "BTW_CHUNK_OVERLAP", 0, c.ChunkSize-1); err != nil {
			return config{}, err
		}
	}
	if c.PollInterval, err = boundedDuration(getenv("BTW_POLL_INTERVAL"), "BTW_POLL_INTERVAL", 100*time.Millisecond, time.Minute); err != nil {
		return config{}, err
	}
	if c.HTTPTimeout, err = boundedDuration(getenv("BTW_HTTP_TIMEOUT"), "BTW_HTTP_TIMEOUT", time.Second, time.Minute); err != nil {
		return config{}, err
	}
	if c.ContentMigrate, err = explicitBool(getenv("BTW_CONTENT_MIGRATE"), "BTW_CONTENT_MIGRATE"); err != nil {
		return config{}, err
	}
	if c.SessionInit, err = explicitBool(getenv("BTW_SESSION_INITIALIZE"), "BTW_SESSION_INITIALIZE"); err != nil {
		return config{}, err
	}
	if !identifier.MatchString(c.ContentSchema) || !identifier.MatchString(c.SessionSchema) || !identifier.MatchString(c.SessionPrefix) {
		return config{}, errors.New("content/session schema and session table prefix must be simple PostgreSQL identifiers")
	}
	if !revision.MatchString(c.Version) || (c.Environment != "local" && c.Environment != "test") {
		return config{}, errors.New("BTW_SERVICE_VERSION must be a full commit SHA and BTW_ENVIRONMENT must be local or test")
	}
	if err := endpoint(c.DCURL); err != nil {
		return config{}, fmt.Errorf("BTW_DC_URL: %w", err)
	}
	if err := endpoint(c.RTWURL); err != nil {
		return config{}, fmt.Errorf("BTW_RTW_URL: %w", err)
	}
	if err := endpoint(c.OTLPTracesURL); err != nil {
		return config{}, fmt.Errorf("BTW_OTLP_TRACES_URL: %w", err)
	}
	host, port, err := net.SplitHostPort(c.MetricsAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || port == "" {
		return config{}, errors.New("BTW_METRICS_ADDR must bind an explicit loopback IP and port")
	}
	if n, e := strconv.Atoi(port); e != nil || n < 1 || n > 65535 {
		return config{}, errors.New("BTW_METRICS_ADDR port must be 1..65535")
	}
	if c.JobType == "content.build.v1" {
		c.IndexSettings, err = readIndexSettings(c.IndexConfig)
		if err != nil {
			return config{}, fmt.Errorf("BTW_INDEX_CONFIG_FILE: %w", err)
		}
	}
	return c, nil
}

func loadFavoriteFactConfig(getenv func(string) string) (config, error) {
	c := config{JobType: favoriteFactJobType}
	if getenv("BTW_MODE") != "local" {
		return c, errors.New("favorite fact consumer requires BTW_MODE=local")
	}
	for _, key := range []string{"BTW_DC_URL", "BTW_DC_TOKEN", "BTW_FAVORITE_AUTHORITY_URL",
		"BTW_FAVORITE_AUTHORITY_TOKEN", "BTW_FACT_POSTGRES_DSN", "BTW_FACT_SCHEMA", "BTW_FACT_CONSUMER",
		"BTW_FACT_BATCH_LIMIT", "BTW_POLL_INTERVAL", "BTW_HTTP_TIMEOUT", "BTW_OTLP_TRACES_URL",
		"BTW_METRICS_ADDR", "BTW_SERVICE_VERSION", "BTW_ENVIRONMENT", "BTW_INSTANCE_ID"} {
		if strings.TrimSpace(getenv(key)) == "" {
			return c, fmt.Errorf("%s is required", key)
		}
	}
	c.DCURL, c.DCToken = getenv("BTW_DC_URL"), getenv("BTW_DC_TOKEN")
	c.AuthorityURL, c.AuthorityToken = getenv("BTW_FAVORITE_AUTHORITY_URL"), getenv("BTW_FAVORITE_AUTHORITY_TOKEN")
	c.FactDSN, c.FactSchema, c.FactConsumer = getenv("BTW_FACT_POSTGRES_DSN"), getenv("BTW_FACT_SCHEMA"), getenv("BTW_FACT_CONSUMER")
	c.OTLPTracesURL, c.MetricsAddr = getenv("BTW_OTLP_TRACES_URL"), getenv("BTW_METRICS_ADDR")
	c.Version, c.Environment, c.InstanceID = getenv("BTW_SERVICE_VERSION"), getenv("BTW_ENVIRONMENT"), getenv("BTW_INSTANCE_ID")
	var err error
	if c.FactBatchLimit, err = positiveInt(getenv("BTW_FACT_BATCH_LIMIT"), "BTW_FACT_BATCH_LIMIT", 1, 128); err != nil {
		return c, err
	}
	if c.PollInterval, err = boundedDuration(getenv("BTW_POLL_INTERVAL"), "BTW_POLL_INTERVAL", 100*time.Millisecond, time.Minute); err != nil {
		return c, err
	}
	if c.HTTPTimeout, err = boundedDuration(getenv("BTW_HTTP_TIMEOUT"), "BTW_HTTP_TIMEOUT", time.Second, time.Minute); err != nil {
		return c, err
	}
	if !factConsumer.MatchString(c.FactConsumer) || !identifier.MatchString(c.FactSchema) {
		return c, errors.New("fact consumer and schema must be fixed bounded identifiers")
	}
	if len(c.AuthorityToken) < 32 {
		return c, errors.New("BTW_FAVORITE_AUTHORITY_TOKEN must have at least 32 bytes")
	}
	if !revision.MatchString(c.Version) || (c.Environment != "local" && c.Environment != "test") {
		return c, errors.New("BTW_SERVICE_VERSION must be a full commit SHA and BTW_ENVIRONMENT must be local or test")
	}
	for name, raw := range map[string]string{"BTW_DC_URL": c.DCURL,
		"BTW_FAVORITE_AUTHORITY_URL": c.AuthorityURL, "BTW_OTLP_TRACES_URL": c.OTLPTracesURL} {
		if err := endpoint(raw); err != nil {
			return c, fmt.Errorf("%s: %w", name, err)
		}
	}
	host, port, err := net.SplitHostPort(c.MetricsAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || port == "" {
		return c, errors.New("BTW_METRICS_ADDR must bind an explicit loopback IP and port")
	}
	if n, e := strconv.Atoi(port); e != nil || n < 1 || n > 65535 {
		return c, errors.New("BTW_METRICS_ADDR port must be 1..65535")
	}
	return c, nil
}

func positiveInt(raw, name string, min, max int) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be %d..%d", name, min, max)
	}
	return n, nil
}

func boundedDuration(raw, name string, min, max time.Duration) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if err != nil || d < min || d > max {
		return 0, fmt.Errorf("%s must be %s..%s", name, min, max)
	}
	return d, nil
}

func explicitBool(raw, name string) (bool, error) {
	if raw != "true" && raw != "false" {
		return false, fmt.Errorf("%s must explicitly be true or false", name)
	}
	return raw == "true", nil
}

func endpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return errors.New("absolute HTTP(S) URL without credentials, query or fragment required")
	}
	return nil
}
