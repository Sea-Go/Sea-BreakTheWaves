package main

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/app"
)

const wikiCompileNativeSessionSource = "dc-native-app-user"
const wikiCompileModelCallpoint = "knowledge-wiki-compiler"

var wikiCompileContractID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
var wikiCompileBucket = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{2,62}$`)

type wikiCompileConfig struct {
	Enabled             bool
	ObjectBackend       string
	ObjectBucket        string
	RTWObjectBucket     string
	SharedObjectRoot    string
	RTWObjectRoot       string
	NativeSessionSource string
	ModelCallpoint      string
	ResultRefContractID string
}

// Wiki mode has its own default-off configuration. Even an explicit enabled
// value cannot start without the separate authorized model/session, RTW-shared
// object and versioned ResultRef providers checked in wiki_compile_start.go.
func loadWikiCompileConfig(getenv func(string) string) (config, error) {
	c := config{JobType: app.WikiCompileJobType, Wiki: &wikiCompileConfig{}}
	switch getenv("BTW_WIKI_COMPILE_ENABLED") {
	case "", "false":
		return c, nil
	case "true":
		c.Wiki.Enabled = true
	default:
		return c, errors.New("BTW_WIKI_COMPILE_ENABLED must be true or false")
	}
	if getenv("BTW_MODE") != "local" {
		return c, errors.New("Wiki compile candidate requires BTW_MODE=local")
	}
	for _, key := range []string{"BTW_WORKER_ID", "BTW_RESOURCE_PROFILE", "BTW_DC_URL", "BTW_DC_TOKEN",
		"BTW_RTW_URL", "BTW_RTW_TOKEN", "BTW_OTLP_TRACES_URL", "BTW_METRICS_ADDR",
		"BTW_SERVICE_VERSION", "BTW_ENVIRONMENT", "BTW_INSTANCE_ID",
		"BTW_WIKI_NATIVE_SESSION_SOURCE", "BTW_WIKI_MODEL_CALLPOINT", "BTW_WIKI_RESULT_REF_CONTRACT_ID"} {
		if strings.TrimSpace(getenv(key)) == "" {
			return c, fmt.Errorf("%s is required for Wiki compile", key)
		}
	}
	c.WorkerID, c.Resource = getenv("BTW_WORKER_ID"), getenv("BTW_RESOURCE_PROFILE")
	if c.Resource != "cpu" {
		return c, errors.New("Wiki compile job resource profile must be cpu")
	}
	c.DCURL, c.DCToken = getenv("BTW_DC_URL"), getenv("BTW_DC_TOKEN")
	c.RTWURL, c.RTWToken = getenv("BTW_RTW_URL"), getenv("BTW_RTW_TOKEN")
	c.OTLPTracesURL, c.MetricsAddr = getenv("BTW_OTLP_TRACES_URL"), getenv("BTW_METRICS_ADDR")
	c.Version, c.Environment, c.InstanceID = getenv("BTW_SERVICE_VERSION"), getenv("BTW_ENVIRONMENT"), getenv("BTW_INSTANCE_ID")
	c.Wiki.NativeSessionSource = getenv("BTW_WIKI_NATIVE_SESSION_SOURCE")
	c.Wiki.ModelCallpoint = getenv("BTW_WIKI_MODEL_CALLPOINT")
	c.Wiki.ResultRefContractID = getenv("BTW_WIKI_RESULT_REF_CONTRACT_ID")
	if c.Wiki.NativeSessionSource != wikiCompileNativeSessionSource ||
		c.Wiki.ModelCallpoint != wikiCompileModelCallpoint ||
		!wikiCompileContractID.MatchString(c.Wiki.ResultRefContractID) ||
		!revision.MatchString(c.Version) || (c.Environment != "local" && c.Environment != "test") {
		return c, errors.New("Wiki compile model/session, ResultRef contract or version is not fixed")
	}
	var err error
	if c.LeaseSeconds, err = positiveInt(getenv("BTW_LEASE_SECONDS"), "BTW_LEASE_SECONDS", 5, 3600); err != nil {
		return c, err
	}
	if c.PollInterval, err = boundedDuration(getenv("BTW_POLL_INTERVAL"), "BTW_POLL_INTERVAL", 100*time.Millisecond, time.Minute); err != nil {
		return c, err
	}
	if c.HTTPTimeout, err = boundedDuration(getenv("BTW_HTTP_TIMEOUT"), "BTW_HTTP_TIMEOUT", time.Second, time.Minute); err != nil {
		return c, err
	}
	for name, raw := range map[string]string{"BTW_DC_URL": c.DCURL, "BTW_RTW_URL": c.RTWURL,
		"BTW_OTLP_TRACES_URL": c.OTLPTracesURL} {
		if err := endpoint(raw); err != nil {
			return c, fmt.Errorf("%s: %w", name, err)
		}
	}
	host, port, splitErr := net.SplitHostPort(c.MetricsAddr)
	if splitErr != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || port == "" {
		return c, errors.New("BTW_METRICS_ADDR must bind a loopback IP and port")
	}
	if n, parseErr := strconv.Atoi(port); parseErr != nil || n < 1 || n > 65535 {
		return c, errors.New("BTW_METRICS_ADDR port must be 1..65535")
	}
	c.Wiki.ObjectBackend = getenv("BTW_WIKI_OBJECT_BACKEND")
	switch c.Wiki.ObjectBackend {
	case "s3":
		c.Wiki.ObjectBucket = getenv("BTW_WIKI_OBJECT_BUCKET")
		c.Wiki.RTWObjectBucket = getenv("BTW_RTW_OBJECT_BUCKET")
		if !wikiCompileBucket.MatchString(c.Wiki.ObjectBucket) ||
			c.Wiki.RTWObjectBucket != c.Wiki.ObjectBucket {
			return c, errors.New("Wiki candidate S3 bucket must be the RTW ObjectStore bucket")
		}
	case "shared-local":
		c.Wiki.SharedObjectRoot = getenv("BTW_WIKI_SHARED_OBJECT_ROOT")
		c.Wiki.RTWObjectRoot = getenv("BTW_RTW_OBJECT_ROOT")
		if !filepath.IsAbs(c.Wiki.SharedObjectRoot) ||
			c.Wiki.SharedObjectRoot != c.Wiki.RTWObjectRoot {
			return c, errors.New("Wiki local object root must be the same task-owned RTW root")
		}
	default:
		return c, errors.New("BTW_WIKI_OBJECT_BACKEND must be s3 or shared-local")
	}
	return c, nil
}
