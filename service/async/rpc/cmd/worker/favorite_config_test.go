package main

import (
	"strings"
	"testing"
)

func favoriteEnvironment() map[string]string {
	return map[string]string{
		"BTW_MODE": "local", "BTW_JOB_TYPE": favoriteFactJobType,
		"BTW_DC_URL": "http://127.0.0.1:8190", "BTW_DC_TOKEN": "dc-service-test",
		"BTW_FAVORITE_AUTHORITY_URL":   "http://127.0.0.1:8191",
		"BTW_FAVORITE_AUTHORITY_TOKEN": "source-service-token-at-least-32-bytes",
		"BTW_FACT_POSTGRES_DSN":        "postgres://test@127.0.0.1:5432/facts?sslmode=disable",
		"BTW_FACT_SCHEMA":              "facts", "BTW_FACT_CONSUMER": "btw-favorite-facts",
		"BTW_FACT_BATCH_LIMIT": "10", "BTW_POLL_INTERVAL": "100ms", "BTW_HTTP_TIMEOUT": "5s",
		"BTW_OTLP_TRACES_URL": "http://127.0.0.1:4318/v1/traces",
		"BTW_METRICS_ADDR":    "127.0.0.1:9091", "BTW_SERVICE_VERSION": strings.Repeat("a", 40),
		"BTW_ENVIRONMENT": "test", "BTW_INSTANCE_ID": "favorite-process-1",
	}
}

func TestFavoriteFactModeIsExplicitAndFailsClosed(t *testing.T) {
	base := favoriteEnvironment()
	get := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	cfg, err := loadConfig(get(base))
	if err != nil || cfg.JobType != favoriteFactJobType || cfg.FactBatchLimit != 10 || cfg.FactConsumer != "btw-favorite-facts" {
		t.Fatalf("explicit favorite mode: %+v %v", cfg, err)
	}
	for name, edit := range map[string]func(map[string]string){
		"missing-mode":      func(v map[string]string) { delete(v, "BTW_JOB_TYPE") },
		"missing-authority": func(v map[string]string) { delete(v, "BTW_FAVORITE_AUTHORITY_URL") },
		"short-token":       func(v map[string]string) { v["BTW_FAVORITE_AUTHORITY_TOKEN"] = "short" },
		"missing-dc-token":  func(v map[string]string) { delete(v, "BTW_DC_TOKEN") },
		"missing-fact-db":   func(v map[string]string) { delete(v, "BTW_FACT_POSTGRES_DSN") },
		"unbounded-batch":   func(v map[string]string) { v["BTW_FACT_BATCH_LIMIT"] = "129" },
		"invalid-consumer":  func(v map[string]string) { v["BTW_FACT_CONSUMER"] = "other/../consumer" },
		"external-metrics":  func(v map[string]string) { v["BTW_METRICS_ADDR"] = "0.0.0.0:9091" },
	} {
		t.Run(name, func(t *testing.T) {
			values := favoriteEnvironment()
			edit(values)
			if _, err := loadConfig(get(values)); err == nil || strings.Contains(err.Error(), values["BTW_FAVORITE_AUTHORITY_TOKEN"]) {
				t.Fatalf("invalid favorite mode accepted or token exposed: %v", err)
			}
		})
	}
	values := favoriteEnvironment()
	values["BTW_JOB_TYPE"] = "content.prepare.v1"
	if _, err := loadConfig(get(values)); err == nil || !strings.Contains(err.Error(), "BTW_ARTIFACT_STORE") {
		t.Fatalf("content mode's original artifact gate changed: %v", err)
	}
}

func communityEnvironment(jobType string) map[string]string {
	values := favoriteEnvironment()
	values["BTW_JOB_TYPE"] = jobType
	delete(values, "BTW_FAVORITE_AUTHORITY_URL")
	delete(values, "BTW_FAVORITE_AUTHORITY_TOKEN")
	values["BTW_COMMUNITY_AUTHORITY_URL"] = "http://127.0.0.1:8291"
	values["BTW_COMMUNITY_AUTHORITY_TOKEN"] = "community-source-token-at-least-32-bytes"
	return values
}

func TestCommunityFactModesFixProducerAndAuthority(t *testing.T) {
	get := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	for jobType, producer := range map[string]string{
		commentFactJobType: "rtw.comment-rpc",
		likeFactJobType:    "rtw.like-mq",
	} {
		t.Run(jobType, func(t *testing.T) {
			values := communityEnvironment(jobType)
			cfg, err := loadConfig(get(values))
			if err != nil || cfg.JobType != jobType || cfg.FactProducer != producer ||
				cfg.AuthorityURL != values["BTW_COMMUNITY_AUTHORITY_URL"] {
				t.Fatalf("community fact config: %+v %v", cfg, err)
			}
			for _, key := range []string{"BTW_COMMUNITY_AUTHORITY_URL", "BTW_COMMUNITY_AUTHORITY_TOKEN"} {
				broken := communityEnvironment(jobType)
				delete(broken, key)
				if _, err := loadConfig(get(broken)); err == nil {
					t.Fatalf("missing %s accepted", key)
				}
			}
		})
	}
}
