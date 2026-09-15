package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/sparse"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
)

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
var mediumPolicyVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type config struct {
	APIAddr, MetricsAddr             string
	ScopeKey, ToolsScopeKey          string
	RTWURL, RTWToken                 string
	DCURL, DCToken                   string
	ModelURL, ModelKey               string
	ModelName                        string
	ArtifactDir                      string
	OTLPTracesURL                    string
	Version, Environment, InstanceID string
	HTTPTimeout                      time.Duration
	Indexes                          indexSettings
	Policy                           searchdomain.Policy
	FastMedium                       *fastMediumConfig
	FastMediumTools                  bool
	MaxQuoteRunes                    int
	RepresentationMaxInFlight        int
}

type indexSettings struct {
	Dense       dense.Config       `json:"dense"`
	Sparse      sparse.Config      `json:"sparse"`
	MultiVector multivector.Config `json:"multivector"`
}

type policySettings struct {
	Version string `json:"version"`
	FastLow struct {
		MaxBatches    int    `json:"max_batches"`
		MaxSubqueries int    `json:"max_subqueries"`
		TopKPerLane   int    `json:"top_k_per_lane"`
		MaxEvidence   int    `json:"max_evidence"`
		WallTime      string `json:"wall_time"`
	} `json:"fast_low"`
	FastMedium *struct {
		Version                string `json:"version"`
		MaxBatches             int    `json:"max_batches"`
		MaxSubqueries          int    `json:"max_subqueries"`
		TopKPerLane            int    `json:"top_k_per_lane"`
		MaxEvidence            int    `json:"max_evidence"`
		WallTime               string `json:"wall_time"`
		PlannerMaxOutputTokens int    `json:"planner_max_output_tokens"`
	} `json:"fast_medium,omitempty"`
}

type fastMediumConfig struct {
	Version                string
	WallTime               time.Duration
	PlannerMaxOutputTokens int
}

func loadConfig(getenv func(string) string) (config, error) {
	var c config
	if getenv("BTW_SEARCH_MODE") != "local-exact" {
		return c, errors.New("BTW_SEARCH_MODE must explicitly be local-exact")
	}
	required := []string{"BTW_SEARCH_API_ADDR", "BTW_SEARCH_METRICS_ADDR", "BTW_SEARCH_SCOPE_KEY", "BTW_SEARCH_TOOLS_SCOPE_KEY",
		"BTW_RTW_URL", "BTW_RTW_TOKEN", "BTW_DC_URL", "BTW_DC_TOKEN", "BTW_SEARCH_MODEL_URL",
		"BTW_SEARCH_MODEL_KEY", "BTW_SEARCH_MODEL_NAME", "BTW_ARTIFACT_DIR", "BTW_SEARCH_INDEX_FILE",
		"BTW_SEARCH_POLICY_FILE", "BTW_SEARCH_MAX_QUOTE_RUNES", "BTW_SEARCH_HTTP_TIMEOUT",
		"BTW_SEARCH_REPRESENTATION_MAX_IN_FLIGHT",
		"BTW_OTLP_TRACES_URL", "BTW_SERVICE_VERSION", "BTW_ENVIRONMENT", "BTW_INSTANCE_ID"}
	for _, key := range required {
		if strings.TrimSpace(getenv(key)) == "" {
			return c, fmt.Errorf("%s is required", key)
		}
	}
	c.APIAddr, c.MetricsAddr = getenv("BTW_SEARCH_API_ADDR"), getenv("BTW_SEARCH_METRICS_ADDR")
	if err := loopback(c.APIAddr); err != nil {
		return c, fmt.Errorf("BTW_SEARCH_API_ADDR: %w", err)
	}
	if err := loopback(c.MetricsAddr); err != nil || c.MetricsAddr == c.APIAddr {
		return c, errors.New("BTW_SEARCH_METRICS_ADDR must be a distinct explicit loopback address")
	}
	c.ScopeKey = getenv("BTW_SEARCH_SCOPE_KEY")
	c.ToolsScopeKey = getenv("BTW_SEARCH_TOOLS_SCOPE_KEY")
	if len(c.ScopeKey) < 32 || len(c.ToolsScopeKey) < 32 {
		return c, errors.New("BTW_SEARCH_SCOPE_KEY and BTW_SEARCH_TOOLS_SCOPE_KEY each require at least 32 bytes")
	}
	c.RTWURL, c.RTWToken = getenv("BTW_RTW_URL"), getenv("BTW_RTW_TOKEN")
	c.DCURL, c.DCToken = getenv("BTW_DC_URL"), getenv("BTW_DC_TOKEN")
	c.ModelURL, c.ModelKey, c.ModelName = getenv("BTW_SEARCH_MODEL_URL"), getenv("BTW_SEARCH_MODEL_KEY"), getenv("BTW_SEARCH_MODEL_NAME")
	c.ArtifactDir, c.OTLPTracesURL = getenv("BTW_ARTIFACT_DIR"), getenv("BTW_OTLP_TRACES_URL")
	for name, raw := range map[string]string{"BTW_RTW_URL": c.RTWURL, "BTW_DC_URL": c.DCURL,
		"BTW_SEARCH_MODEL_URL": c.ModelURL, "BTW_OTLP_TRACES_URL": c.OTLPTracesURL} {
		if err := endpoint(raw); err != nil {
			return c, fmt.Errorf("%s: %w", name, err)
		}
	}
	c.Version, c.Environment, c.InstanceID = getenv("BTW_SERVICE_VERSION"), getenv("BTW_ENVIRONMENT"), getenv("BTW_INSTANCE_ID")
	if !commitSHA.MatchString(c.Version) || (c.Environment != "local" && c.Environment != "test") {
		return c, errors.New("BTW_SERVICE_VERSION must be a full commit SHA and BTW_ENVIRONMENT local or test")
	}
	var err error
	c.HTTPTimeout, err = time.ParseDuration(getenv("BTW_SEARCH_HTTP_TIMEOUT"))
	if err != nil || c.HTTPTimeout < time.Second || c.HTTPTimeout > 95*time.Second {
		return c, errors.New("BTW_SEARCH_HTTP_TIMEOUT must be 1s..95s")
	}
	c.MaxQuoteRunes, err = strconv.Atoi(getenv("BTW_SEARCH_MAX_QUOTE_RUNES"))
	if err != nil || c.MaxQuoteRunes < 1 || c.MaxQuoteRunes > 4096 {
		return c, errors.New("BTW_SEARCH_MAX_QUOTE_RUNES must be 1..4096")
	}
	c.RepresentationMaxInFlight, err = strconv.Atoi(getenv("BTW_SEARCH_REPRESENTATION_MAX_IN_FLIGHT"))
	if err != nil || c.RepresentationMaxInFlight < 1 || c.RepresentationMaxInFlight > 32 {
		return c, errors.New("BTW_SEARCH_REPRESENTATION_MAX_IN_FLIGHT must be 1..32")
	}
	if err = readJSON(getenv("BTW_SEARCH_INDEX_FILE"), &c.Indexes); err != nil {
		return c, fmt.Errorf("BTW_SEARCH_INDEX_FILE: %w", err)
	}
	var p policySettings
	if err = readPolicyJSON(getenv("BTW_SEARCH_POLICY_FILE"), &p); err != nil {
		return c, fmt.Errorf("BTW_SEARCH_POLICY_FILE: %w", err)
	}
	wall, err := time.ParseDuration(p.FastLow.WallTime)
	if err != nil || wall < time.Second || wall > 30*time.Second || p.Version == "" {
		return c, errors.New("BTW_SEARCH_POLICY_FILE requires version and fast_low.wall_time of 1s..30s")
	}
	c.Policy = searchdomain.Policy{Version: p.Version, Profiles: map[searchdomain.Depth]map[searchdomain.Intelligence]searchdomain.Limits{
		searchdomain.Fast: {searchdomain.Low: {
			MaxBatches: p.FastLow.MaxBatches, MaxSubqueries: p.FastLow.MaxSubqueries,
			TopKPerLane: p.FastLow.TopKPerLane, MaxEvidence: p.FastLow.MaxEvidence, WallTime: wall,
		}},
	}}
	if p.FastLow.MaxBatches != 1 || p.FastLow.MaxSubqueries != 1 || p.FastLow.MaxEvidence < 1 ||
		p.FastLow.MaxEvidence > 32 || p.FastLow.TopKPerLane < 1 || p.FastLow.TopKPerLane > 100 {
		return c, errors.New("BTW_SEARCH_POLICY_FILE fast_low requires one batch/query and bounded evidence/TopK")
	}
	if p.FastMedium != nil {
		medium := p.FastMedium
		mediumWall, wallErr := time.ParseDuration(medium.WallTime)
		if !mediumPolicyVersion.MatchString(medium.Version) || medium.Version == p.Version ||
			wallErr != nil || mediumWall < time.Second || mediumWall > 30*time.Second ||
			medium.MaxBatches != 1 || medium.MaxSubqueries != 3 ||
			medium.MaxEvidence < 1 || medium.MaxEvidence > 32 ||
			medium.TopKPerLane < 1 || medium.TopKPerLane > 100 ||
			medium.PlannerMaxOutputTokens < 64 || medium.PlannerMaxOutputTokens > 512 {
			return c, errors.New("BTW_SEARCH_POLICY_FILE fast_medium requires its own version, one batch, three queries and bounded model/evidence limits")
		}
		c.Policy.Profiles[searchdomain.Fast][searchdomain.Medium] = searchdomain.Limits{
			MaxBatches: medium.MaxBatches, MaxSubqueries: medium.MaxSubqueries,
			TopKPerLane: medium.TopKPerLane, MaxEvidence: medium.MaxEvidence, WallTime: mediumWall,
		}
		c.Policy.ProfileVersions = map[searchdomain.Depth]map[searchdomain.Intelligence]string{
			searchdomain.Fast: {searchdomain.Medium: medium.Version},
		}
		c.FastMedium = &fastMediumConfig{Version: medium.Version, WallTime: mediumWall,
			PlannerMaxOutputTokens: medium.PlannerMaxOutputTokens}
	}
	switch getenv("BTW_SEARCH_FAST_MEDIUM_TOOLS") {
	case "":
		// Existing deployments stay fast/low Tools even with a medium Summary policy.
	case "enabled":
		if c.FastMedium == nil {
			return c, errors.New("BTW_SEARCH_FAST_MEDIUM_TOOLS requires an explicit fast_medium policy")
		}
		c.FastMediumTools = true
	default:
		return c, errors.New("BTW_SEARCH_FAST_MEDIUM_TOOLS must be unset or enabled")
	}
	return c, nil
}

func readJSON(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("configuration file unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil || len(raw) > 64*1024 {
		return errors.New("configuration file must be at most 64 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("configuration file must contain one strict JSON object")
	}
	return nil
}

// readPolicyJSON preserves the historical low-only parser while requiring the
// optional medium object to have one literal root key and unique literal field
// names. A duplicate medium key followed by null must not silently disable it.
func readPolicyJSON(path string, out *policySettings) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("configuration file unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil || len(raw) > 64*1024 {
		return errors.New("configuration file must be at most 64 KiB")
	}
	if !literalFastMediumPolicyKeys(raw) {
		return errors.New("fast_medium policy requires one literal object with unique keys")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("configuration file must contain one strict JSON object")
	}
	return nil
}

func literalFastMediumPolicyKeys(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return false
	}
	seenMedium := false
	for decoder.More() {
		keyStart := decoder.InputOffset()
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return false
		}
		keyEnd := decoder.InputOffset()
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
		// encoding/json matches struct fields case-insensitively. Every spelling
		// that could bind FastMedium must be checked before the struct decode.
		if !strings.EqualFold(key, "fast_medium") {
			continue
		}
		if seenMedium || !literalJSONKeySpelling(raw, keyStart, keyEnd, "fast_medium") ||
			!literalFastMediumObjectKeys(value) {
			return false
		}
		seenMedium = true
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}

func literalFastMediumObjectKeys(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return false
	}
	allowed := map[string]bool{"version": true, "max_batches": true, "max_subqueries": true,
		"top_k_per_lane": true, "max_evidence": true, "wall_time": true,
		"planner_max_output_tokens": true}
	seen := make(map[string]bool, len(allowed))
	for decoder.More() {
		keyStart := decoder.InputOffset()
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || !allowed[key] || seen[key] ||
			!literalJSONKeySpelling(raw, keyStart, decoder.InputOffset(), key) {
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}

func literalJSONKeySpelling(raw []byte, start, end int64, key string) bool {
	spelling := bytes.TrimSpace(raw[start:end])
	if len(spelling) > 0 && spelling[0] == ',' {
		spelling = bytes.TrimSpace(spelling[1:])
	}
	return bytes.Equal(spelling, []byte(strconv.Quote(key)))
}

func endpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return errors.New("absolute HTTP(S) URL without credentials, query or fragment required")
	}
	return nil
}

func loopback(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || port == "" {
		return errors.New("explicit loopback IP and port required")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("port must be 1..65535")
	}
	return nil
}
