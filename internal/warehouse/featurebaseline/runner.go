// Package featurebaseline builds the narrow H10.c fact-feature handoff from a
// committed usermodel projection through ClickHouse/dbt and immutable S3 bytes.
package featurebaseline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/usermodel"
)

var name = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Runner owns one isolated warehouse generation. Production DC execution
// fencing and authenticated S3/CH adapters are separate integrations.
type Runner struct {
	Source      *usermodel.Store
	ClickHouse  string
	S3Prefix    string
	DBT         string
	ProjectDir  string
	ProfilesDir string
	WorkDir     string
	HTTPClient  *http.Client
}

type Manifest struct {
	SchemaVersion       string                `json:"schema_version"`
	Generation          string                `json:"generation"`
	Revision            int64                 `json:"revision"`
	Subject             usermodel.SubjectRef  `json:"subject_ref"`
	SpecVersion         string                `json:"feature_spec_version"`
	SpecHash            string                `json:"feature_spec_hash"`
	AsOf                time.Time             `json:"as_of"`
	AvailableAt         time.Time             `json:"available_at"`
	InputStateVersion   int64                 `json:"input_state_version"`
	Watermarks          []usermodel.Watermark `json:"watermarks"`
	ContributionCount   int                   `json:"contribution_count"`
	SourceSHA256        string                `json:"source_sha256"`
	DWSValuesSHA256     string                `json:"dws_values_sha256"`
	BaselineSHA256      string                `json:"baseline_sha256"`
	DBTManifestSHA256   string                `json:"dbt_manifest_sha256"`
	DBTRunResultsSHA256 string                `json:"dbt_run_results_sha256"`
	DBTInvocationID     string                `json:"dbt_invocation_id"`
}

type BuildResult struct {
	Manifest Manifest
	Receipt  usermodel.FeatureReceipt
	Snapshot usermodel.FeatureSnapshot
}

type sourceRow struct {
	Producer   string    `json:"producer"`
	EventID    string    `json:"event_id"`
	Kind       string    `json:"semantic_kind"`
	Predicate  string    `json:"predicate"`
	ValueRef   string    `json:"value_ref"`
	OccurredAt time.Time `json:"occurred_at"`
	ObservedAt time.Time `json:"observed_at"`
}

func hash(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

func (r Runner) client() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return &http.Client{Timeout: 90 * time.Second}
}

func (r Runner) check() error {
	if r.Source == nil || r.DBT == "" || r.ProjectDir == "" || r.ProfilesDir == "" || r.WorkDir == "" {
		return errors.New("feature baseline runner configuration incomplete")
	}
	for _, raw := range []string{r.ClickHouse, r.S3Prefix} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("local feature baseline runner requires isolated localhost CH/S3 endpoints")
		}
	}
	return nil
}

func (r Runner) request(ctx context.Context, method, target string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return got, resp.StatusCode, nil
}

func (r Runner) ch(ctx context.Context, sql string) ([]byte, error) {
	got, status, err := r.request(ctx, http.MethodPost, r.ClickHouse, []byte(sql))
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("ClickHouse status %d: %s", status, strings.TrimSpace(string(got)))
	}
	return got, nil
}

func (r Runner) objectURL(generation, key string) string {
	return strings.TrimRight(r.S3Prefix, "/") + "/" + generation + "/" + key
}

func (r Runner) putFixed(ctx context.Context, target string, body []byte) error {
	old, status, err := r.request(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		if !bytes.Equal(old, body) {
			return errors.New("immutable S3 object conflicts with generation")
		}
		return nil
	}
	if status != http.StatusNotFound {
		return fmt.Errorf("S3 preflight status %d", status)
	}
	_, status, err = r.request(ctx, http.MethodPut, target, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("S3 PUT status %d", status)
	}
	old, status, err = r.request(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK || !bytes.Equal(old, body) {
		return errors.New("S3 readback differs from fixed bytes")
	}
	return nil
}

func (r Runner) getFixed(ctx context.Context, target, expectedHash string) ([]byte, error) {
	body, status, err := r.request(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK || hash(body) != expectedHash {
		return nil, errors.New("S3 artifact unavailable or hash differs")
	}
	return body, nil
}

// Build uses Current's repeatable-read PG snapshot. A source watermark is
// exported only through its accepted contiguous prefix; gaps stay in the
// near-line tail. The CH input contains every covered active fact, including
// facts not selected by the current FeatureSpec.
func (r Runner) Build(ctx context.Context, subject usermodel.SubjectRef, revision int64, generation string, inputSpec usermodel.FeatureSpec) (Manifest, error) {
	if err := r.check(); err != nil {
		return Manifest{}, err
	}
	if !name.MatchString(generation) || revision <= 0 {
		return Manifest{}, errors.New("invalid feature generation or revision")
	}
	spec, specHash, err := usermodel.PrepareFeatureSpec(inputSpec)
	if err != nil {
		return Manifest{}, err
	}
	for _, def := range spec.Features {
		if def.Source != "fact" {
			return Manifest{}, errors.New("warehouse fact baseline cannot compute ontology projection")
		}
	}
	projection, err := r.Source.Current(ctx, subject)
	if err != nil {
		return Manifest{}, err
	}
	cutoff := time.Now().UTC().Truncate(time.Microsecond)
	watermarks := make([]usermodel.Watermark, 0, len(projection.Watermarks))
	covered := make(map[string]int64, len(projection.Watermarks))
	for _, w := range projection.Watermarks {
		prefix := w.ContiguousSequence
		for _, f := range projection.Active {
			if f.Producer == w.Producer && f.SourcePartition == w.SourcePartition && f.SourceSequence > 0 &&
				f.SourceSequence <= prefix && (f.OccurredAt.After(cutoff) || f.ObservedAt.After(cutoff)) {
				prefix = f.SourceSequence - 1
			}
		}
		if prefix == 0 {
			continue
		}
		watermarks = append(watermarks, usermodel.Watermark{Producer: w.Producer, SourcePartition: w.SourcePartition,
			ContiguousSequence: prefix, MaxSeenSequence: prefix, Complete: true})
		covered[w.Producer+"\x00"+w.SourcePartition] = prefix
	}
	if len(watermarks) == 0 {
		return Manifest{}, errors.New("no complete accepted source prefix for warehouse baseline")
	}
	contributions := make([]usermodel.Fact, 0, len(projection.Active))
	rows := make([]sourceRow, 0, len(projection.Active))
	for _, f := range projection.Active {
		if f.Status != "accepted" || f.SourceSequence <= 0 || f.SourceSequence > covered[f.Producer+"\x00"+f.SourcePartition] ||
			f.OccurredAt.After(cutoff) || f.ObservedAt.After(cutoff) {
			continue
		}
		// Attribution is a mutable read projection, not part of the immutable
		// source event contribution admitted by WS08-C.
		f.ImpressionLinked, f.LinkedImpression = false, ""
		contributions = append(contributions, f)
		rows = append(rows, sourceRow{f.Producer, f.EventID, string(f.Kind), f.Predicate, f.ValueRef, f.OccurredAt, f.ObservedAt})
	}
	sort.Slice(contributions, func(i, j int) bool {
		if contributions[i].Producer == contributions[j].Producer {
			return contributions[i].EventID < contributions[j].EventID
		}
		return contributions[i].Producer < contributions[j].Producer
	})
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Producer == rows[j].Producer {
			return rows[i].EventID < rows[j].EventID
		}
		return rows[i].Producer < rows[j].Producer
	})
	var source bytes.Buffer
	for _, row := range rows {
		body, err := json.Marshal(row)
		if err != nil {
			return Manifest{}, err
		}
		source.Write(body)
		source.WriteByte('\n')
	}
	sourceHash := hash(source.Bytes())
	if err := r.putFixed(ctx, r.objectURL(generation, "source/"+sourceHash+".jsonl"), source.Bytes()); err != nil {
		return Manifest{}, err
	}
	if _, err := r.ch(ctx, "CREATE DATABASE "+generation); err != nil {
		return Manifest{}, err
	}
	landing := generation + ".feature_landing"
	if _, err := r.ch(ctx, "CREATE TABLE "+landing+` (producer String,event_id String,semantic_kind String,predicate String,value_ref String,occurred_at DateTime64(6,'UTC'),observed_at DateTime64(6,'UTC')) ENGINE=MergeTree ORDER BY (producer,event_id)`); err != nil {
		return Manifest{}, err
	}
	if len(rows) > 0 {
		url := r.objectURL(generation, "source/"+sourceHash+".jsonl")
		query := fmt.Sprintf("INSERT INTO %s SELECT * FROM s3('%s', NOSIGN, 'JSONEachRow', 'producer String,event_id String,semantic_kind String,predicate String,value_ref String,occurred_at DateTime64(6,\\'UTC\\'),observed_at DateTime64(6,\\'UTC\\')') SETTINGS date_time_input_format='best_effort'", landing, url)
		if _, err := r.ch(ctx, query); err != nil {
			return Manifest{}, err
		}
	}
	var countRows []struct {
		N int `json:"n"`
	}
	if err := r.chRows(ctx, "SELECT count() AS n FROM "+landing, &countRows); err != nil {
		return Manifest{}, err
	}
	if len(countRows) != 1 || countRows[0].N != len(rows) {
		return Manifest{}, errors.New("ClickHouse source row count differs from PG projection")
	}
	vars := map[string]any{"landing_table": landing, "feature_spec": spec, "as_of": cutoff.Format(time.RFC3339Nano), "available_at": cutoff.Format(time.RFC3339Nano)}
	varsBody, err := json.Marshal(vars)
	if err != nil {
		return Manifest{}, err
	}
	targetDir := filepath.Join(r.WorkDir, generation, "target")
	logDir := filepath.Join(r.WorkDir, generation, "logs")
	if err := os.MkdirAll(filepath.Dir(targetDir), 0700); err != nil {
		return Manifest{}, err
	}
	u, _ := url.Parse(r.ClickHouse)
	cmd := exec.CommandContext(ctx, r.DBT, "run", "--project-dir", r.ProjectDir, "--profiles-dir", r.ProfilesDir,
		"--target-path", targetDir, "--log-path", logDir, "--vars", string(varsBody), "--no-partial-parse")
	cmd.Env = append(os.Environ(), "WAREHOUSE_CH_PORT="+u.Port(), "WAREHOUSE_GENERATION_SCHEMA="+generation,
		"DBT_SEND_ANONYMOUS_USAGE_STATS=false", "DBT_USE_COLORS=false")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return Manifest{}, fmt.Errorf("dbt user feature DWS failed: %w: %s", err, output)
	}
	manifestBody, err := os.ReadFile(filepath.Join(targetDir, "manifest.json"))
	if err != nil {
		return Manifest{}, err
	}
	resultsBody, err := os.ReadFile(filepath.Join(targetDir, "run_results.json"))
	if err != nil {
		return Manifest{}, err
	}
	var dbtManifest struct {
		Metadata struct {
			InvocationID string `json:"invocation_id"`
		} `json:"metadata"`
	}
	var dbtResults struct {
		Metadata struct {
			InvocationID string `json:"invocation_id"`
		} `json:"metadata"`
		Results []struct {
			UniqueID string `json:"unique_id"`
			Status   string `json:"status"`
		} `json:"results"`
	}
	if err := json.Unmarshal(manifestBody, &dbtManifest); err != nil {
		return Manifest{}, err
	}
	if err := json.Unmarshal(resultsBody, &dbtResults); err != nil {
		return Manifest{}, err
	}
	if dbtManifest.Metadata.InvocationID == "" || dbtResults.Metadata.InvocationID != dbtManifest.Metadata.InvocationID ||
		len(dbtResults.Results) != 1 || dbtResults.Results[0].UniqueID != "model.sea_user_feature_baselines.dws_user_feature_values" || dbtResults.Results[0].Status != "success" {
		return Manifest{}, errors.New("dbt DWS invocation or model status differs")
	}
	var dwsRows []struct {
		Name    string `json:"name"`
		Value   string `json:"value"`
		Missing uint8  `json:"missing"`
		OOV     uint8  `json:"oov"`
	}
	if err := r.chRows(ctx, "SELECT name,value,missing,oov FROM "+generation+".dws_user_feature_values ORDER BY name", &dwsRows); err != nil {
		return Manifest{}, err
	}
	if len(dwsRows) != len(spec.Features) {
		return Manifest{}, errors.New("DWS feature row count differs from FeatureSpec")
	}
	values := make([]usermodel.FeatureValue, len(dwsRows))
	for i, def := range spec.Features {
		if dwsRows[i].Name != def.Name || dwsRows[i].Missing > 1 || dwsRows[i].OOV > 1 {
			return Manifest{}, errors.New("DWS feature name differs from FeatureSpec")
		}
		values[i] = usermodel.FeatureValue{Name: dwsRows[i].Name, Value: dwsRows[i].Value,
			Missing: dwsRows[i].Missing == 1, OOV: dwsRows[i].OOV == 1}
	}
	valuesBody, err := json.Marshal(values)
	if err != nil {
		return Manifest{}, err
	}
	baseline := usermodel.FeatureBaseline{Subject: subject, Revision: revision, Generation: generation, SpecVersion: spec.Version,
		SpecHash: specHash, AsOf: cutoff, AvailableAt: cutoff, Watermarks: watermarks, Contributions: contributions, Values: values}
	baselineBody, err := json.Marshal(baseline)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{SchemaVersion: "sea.user-feature-baseline.v1", Generation: generation, Revision: revision, Subject: subject,
		SpecVersion: spec.Version, SpecHash: specHash, AsOf: cutoff, AvailableAt: cutoff, InputStateVersion: projection.StateVersion,
		Watermarks: watermarks, ContributionCount: len(contributions), SourceSHA256: sourceHash, BaselineSHA256: hash(baselineBody),
		DWSValuesSHA256:   hash(valuesBody),
		DBTManifestSHA256: hash(manifestBody), DBTRunResultsSHA256: hash(resultsBody), DBTInvocationID: dbtManifest.Metadata.InvocationID}
	mBody, err := json.Marshal(m)
	if err != nil {
		return Manifest{}, err
	}
	if err := r.putFixed(ctx, r.objectURL(generation, "baseline/"+m.BaselineSHA256+".json"), baselineBody); err != nil {
		return Manifest{}, err
	}
	for _, artifact := range []struct {
		key  string
		body []byte
	}{
		{"dws/" + m.DWSValuesSHA256 + ".json", valuesBody},
		{"dbt/manifest/" + m.DBTManifestSHA256 + ".json", manifestBody},
		{"dbt/run_results/" + m.DBTRunResultsSHA256 + ".json", resultsBody},
	} {
		if err := r.putFixed(ctx, r.objectURL(generation, artifact.key), artifact.body); err != nil {
			return Manifest{}, err
		}
	}
	if err := r.putFixed(ctx, r.objectURL(generation, "manifest/"+hash(mBody)+".json"), mBody); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func (r Runner) chRows(ctx context.Context, sql string, target any) error {
	body, err := r.ch(ctx, sql+" FORMAT JSONEachRow")
	if err != nil {
		return err
	}
	var array bytes.Buffer
	array.WriteByte('[')
	for i, line := range bytes.Split(bytes.TrimSpace(body), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		if i > 0 {
			array.WriteByte(',')
		}
		array.Write(line)
	}
	array.WriteByte(']')
	return json.Unmarshal(array.Bytes(), target)
}

// Accept downloads the immutable S3 handoff and lets usermodel compare every
// covered contribution with its PG ledger before advancing the baseline head.
func (r Runner) Accept(ctx context.Context, m Manifest, spec usermodel.FeatureSpec) (usermodel.FeatureReceipt, usermodel.FeatureSnapshot, error) {
	if err := r.check(); err != nil {
		return usermodel.FeatureReceipt{}, usermodel.FeatureSnapshot{}, err
	}
	if m.SchemaVersion != "sea.user-feature-baseline.v1" || !name.MatchString(m.Generation) || m.Revision <= 0 ||
		!digest.MatchString(m.BaselineSHA256) || !digest.MatchString(m.SourceSHA256) || !digest.MatchString(m.DWSValuesSHA256) ||
		!digest.MatchString(m.DBTManifestSHA256) || !digest.MatchString(m.DBTRunResultsSHA256) || m.DBTInvocationID == "" {
		return usermodel.FeatureReceipt{}, usermodel.FeatureSnapshot{}, errors.New("invalid H10.c manifest")
	}
	mBody, err := json.Marshal(m)
	if err != nil {
		return usermodel.FeatureReceipt{}, usermodel.FeatureSnapshot{}, err
	}
	if _, err := r.getFixed(ctx, r.objectURL(m.Generation, "manifest/"+hash(mBody)+".json"), hash(mBody)); err != nil {
		return usermodel.FeatureReceipt{}, usermodel.FeatureSnapshot{}, err
	}
	body, err := r.getFixed(ctx, r.objectURL(m.Generation, "baseline/"+m.BaselineSHA256+".json"), m.BaselineSHA256)
	if err != nil {
		return usermodel.FeatureReceipt{}, usermodel.FeatureSnapshot{}, err
	}
	var baseline usermodel.FeatureBaseline
	if err := json.Unmarshal(body, &baseline); err != nil {
		return usermodel.FeatureReceipt{}, usermodel.FeatureSnapshot{}, err
	}
	if baseline.Subject != m.Subject || baseline.Revision != m.Revision || baseline.Generation != m.Generation ||
		baseline.SpecVersion != m.SpecVersion || baseline.SpecHash != m.SpecHash ||
		!baseline.AsOf.Equal(m.AsOf) || !baseline.AvailableAt.Equal(m.AvailableAt) || len(baseline.Contributions) != m.ContributionCount ||
		!reflect.DeepEqual(baseline.Watermarks, m.Watermarks) {
		return usermodel.FeatureReceipt{}, usermodel.FeatureSnapshot{}, errors.New("H10.c manifest and baseline disagree")
	}
	if _, err := r.getFixed(ctx, r.objectURL(m.Generation, "source/"+m.SourceSHA256+".jsonl"), m.SourceSHA256); err != nil {
		return usermodel.FeatureReceipt{}, usermodel.FeatureSnapshot{}, err
	}
	valuesBody, err := r.getFixed(ctx, r.objectURL(m.Generation, "dws/"+m.DWSValuesSHA256+".json"), m.DWSValuesSHA256)
	if err != nil {
		return usermodel.FeatureReceipt{}, usermodel.FeatureSnapshot{}, err
	}
	var values []usermodel.FeatureValue
	if err := json.Unmarshal(valuesBody, &values); err != nil {
		return usermodel.FeatureReceipt{}, usermodel.FeatureSnapshot{}, err
	}
	if !reflect.DeepEqual(values, baseline.Values) {
		return usermodel.FeatureReceipt{}, usermodel.FeatureSnapshot{}, errors.New("DWS values differ from fixed baseline")
	}
	for _, artifact := range []struct{ key, digest string }{
		{"dbt/manifest/" + m.DBTManifestSHA256 + ".json", m.DBTManifestSHA256},
		{"dbt/run_results/" + m.DBTRunResultsSHA256 + ".json", m.DBTRunResultsSHA256},
	} {
		if _, err := r.getFixed(ctx, r.objectURL(m.Generation, artifact.key), artifact.digest); err != nil {
			return usermodel.FeatureReceipt{}, usermodel.FeatureSnapshot{}, err
		}
	}
	at := time.Now().UTC()
	return r.Source.AcceptFeatureBaseline(ctx, baseline, spec, at, at)
}
