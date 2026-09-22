package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/google/uuid"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// These are the source-owned 0600 runtime contracts written by the RTW Wiki
// external Holder and DC WS05-A consumer Holder. Every field is a real source
// key; the two DC URLs intentionally name different Jobs and Model processes.
type wikiNativeExternalRTWRuntime struct {
	SchemaVersion    string `json:"schema_version"`
	RTWBaseURL       string `json:"rtw_base_url"`
	RTWWorkerToken   string `json:"rtw_worker_token"`
	DCBaseURL        string `json:"dc_base_url"`
	DCJobsToken      string `json:"dc_jobs_token"`
	ObjectRoot       string `json:"object_root"`
	CompileID        string `json:"compile_id"`
	SourceRevisionID string `json:"source_revision_id"`
	JobID            string `json:"job_id"`
	JobInputHash     string `json:"job_input_hash"`
	ReleaseFile      string `json:"release_file"`
}

type wikiNativeExternalDCRuntime struct {
	SchemaVersion        string `json:"schema_version"`
	Endpoint             string `json:"endpoint"`
	AccessToken          string `json:"access_token"`
	LogicalModel         string `json:"logical_model"`
	PhysicalModel        string `json:"physical_model"`
	ModelConfigurationID string `json:"model_configuration_id"`
	UserID               string `json:"user_id"`
	PostgresDSN          string `json:"postgres_dsn"`
	ReadyAt              string `json:"ready_at"`
	ReleaseFile          string `json:"release_file"`
	UsageReceiptFile     string `json:"usage_receipt_file"`
}

var wikiNativeExternalRTWKeys = []string{"schema_version", "rtw_base_url",
	"rtw_worker_token", "dc_base_url", "dc_jobs_token", "object_root", "compile_id",
	"source_revision_id", "job_id", "job_input_hash", "release_file"}
var wikiNativeExternalDCKeys = []string{"schema_version", "endpoint", "access_token",
	"logical_model", "physical_model", "model_configuration_id", "user_id",
	"postgres_dsn", "ready_at", "release_file", "usage_receipt_file"}

var errWikiNativeExternalRuntime = errors.New("private Wiki holder runtime differs from frozen contract")

// The Holders marshal only flat string objects. Reject duplicate/unknown keys,
// non-private files and trailing JSON before any Claim/model/object side effect.
func wikiNativeExternalReadRuntime(path string, keys []string, target any) error {
	if !filepath.IsAbs(path) {
		return errWikiNativeExternalRuntime
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 ||
		info.Size() < 2 || info.Size() > 64<<10 {
		return errWikiNativeExternalRuntime
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return errWikiNativeExternalRuntime
	}
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if first, err := d.Token(); err != nil || first != json.Delim('{') {
		return errWikiNativeExternalRuntime
	}
	seen := make(map[string]bool, len(keys))
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || !allowed[name] || seen[name] {
			return errWikiNativeExternalRuntime
		}
		value, err := d.Token()
		if err != nil {
			return errWikiNativeExternalRuntime
		}
		if _, ok := value.(string); !ok {
			return errWikiNativeExternalRuntime
		}
		seen[name] = true
	}
	if last, err := d.Token(); err != nil || last != json.Delim('}') || len(seen) != len(keys) {
		return errWikiNativeExternalRuntime
	}
	if _, err := d.Token(); err != io.EOF {
		return errWikiNativeExternalRuntime
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return errWikiNativeExternalRuntime
	}
	return nil
}

func TestWikiNativeExternalRuntimeDecoderBoundary(t *testing.T) {
	base, err := json.Marshal(wikiNativeExternalRTWRuntime{SchemaVersion: "sea.wiki.external-consumer.v1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, trial := range []struct {
		name string
		raw  []byte
		perm os.FileMode
		want bool
	}{
		{"exact-private", base, 0600, true},
		{"duplicate-job-id", append(bytes.TrimSuffix(base, []byte("}")), []byte(`,"job_id":"other"}`)...), 0600, false},
		{"unknown-key", append(bytes.TrimSuffix(base, []byte("}")), []byte(`,"extra":"x"}`)...), 0600, false},
		{"trailing-object", append(append([]byte(nil), base...), []byte(`{}`)...), 0600, false},
		{"non-string-job-id", bytes.Replace(base, []byte(`"job_id":""`), []byte(`"job_id":64`), 1), 0600, false},
		{"public-permission", base, 0644, false},
	} {
		t.Run(trial.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private-runtime.json")
			if err := os.WriteFile(path, trial.raw, trial.perm); err != nil {
				t.Fatal(err)
			}
			var got wikiNativeExternalRTWRuntime
			err := wikiNativeExternalReadRuntime(path, wikiNativeExternalRTWKeys, &got)
			if trial.want && err != nil || !trial.want && !errors.Is(err, errWikiNativeExternalRuntime) {
				t.Fatalf("private flat RTW runtime boundary changed: %v", err)
			}
		})
	}
	t.Run("relative-existing-runtime", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "private-runtime.json")
		if err := os.WriteFile(path, base, 0600); err != nil {
			t.Fatal(err)
		}
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(cwd, path)
		if err != nil {
			t.Fatal(err)
		}
		var got wikiNativeExternalRTWRuntime
		if err := wikiNativeExternalReadRuntime(relative, wikiNativeExternalRTWKeys, &got); !errors.Is(err, errWikiNativeExternalRuntime) {
			t.Fatalf("existing relative runtime bypassed absolute task-file boundary: %v", err)
		}
	})
	t.Run("symlink-to-private-runtime", func(t *testing.T) {
		root := t.TempDir()
		target, alias := filepath.Join(root, "runtime.json"), filepath.Join(root, "linked-runtime.json")
		if err := os.WriteFile(target, base, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, alias); err != nil {
			t.Fatal(err)
		}
		var got wikiNativeExternalRTWRuntime
		if err := wikiNativeExternalReadRuntime(alias, wikiNativeExternalRTWKeys, &got); !errors.Is(err, errWikiNativeExternalRuntime) {
			t.Fatalf("symlink followed a private runtime outside Holder authority: %v", err)
		}
	})
}

func wikiNativeExternalLoopbackRoot(raw string, mustHaveV1 bool) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Port() == "" ||
		net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		return "", false
	}
	if mustHaveV1 {
		if u.Path != "/v1" {
			return "", false
		}
		u.Path = ""
	} else if u.Path != "" && u.Path != "/" {
		return "", false
	}
	return strings.TrimRight(u.String(), "/"), true
}

func wikiNativeExternalRelease(path string) error {
	return os.WriteFile(path, []byte("native consumer returned\n"), 0600)
}

func wikiNativeExternalReleasePaths(t *testing.T, rtwPath, dcPath string) (string, string) {
	t.Helper()
	rtwRelease, dcRelease := "", ""
	if rtwPath != "" {
		rtwRelease = filepath.Join(filepath.Dir(rtwPath), "wiki-external-consumer-release")
	}
	if dcPath != "" {
		dcRelease = filepath.Join(filepath.Dir(dcPath), "consumer-release")
	}
	t.Cleanup(func() {
		for _, path := range []string{rtwRelease, dcRelease} {
			if path != "" {
				if err := wikiNativeExternalRelease(path); err != nil {
					t.Errorf("release private Wiki development Holder: %v", err)
				}
			}
		}
	})
	return rtwRelease, dcRelease
}

func wikiNativeExternalLoad(t *testing.T, rtwPath, dcPath string) (
	wikiNativeExternalRTWRuntime, wikiNativeExternalDCRuntime, string) {
	t.Helper()
	var rtw wikiNativeExternalRTWRuntime
	var dc wikiNativeExternalDCRuntime
	if err := wikiNativeExternalReadRuntime(rtwPath, wikiNativeExternalRTWKeys, &rtw); err != nil {
		t.Fatal("RTW private external Wiki runtime contract rejected")
	}
	if err := wikiNativeExternalReadRuntime(dcPath, wikiNativeExternalDCKeys, &dc); err != nil {
		t.Fatal("DC private native model runtime contract rejected")
	}
	_, rtwOK := wikiNativeExternalLoopbackRoot(rtw.RTWBaseURL, false)
	_, jobsOK := wikiNativeExternalLoopbackRoot(rtw.DCBaseURL, false)
	modelRoot, modelOK := wikiNativeExternalLoopbackRoot(dc.Endpoint, true)
	if rtw.SchemaVersion != "sea.wiki.external-consumer.v1" ||
		dc.SchemaVersion != "sea.dc.local-chat-consumer.v1" ||
		!rtwOK || !jobsOK || !modelOK ||
		rtw.RTWWorkerToken == "" || rtw.DCJobsToken == "" ||
		rtw.RTWWorkerToken == rtw.DCJobsToken ||
		rtw.CompileID == "" || rtw.SourceRevisionID == "" ||
		rtw.JobID == "" || !artifacts.ValidHash(rtw.JobInputHash) ||
		!filepath.IsAbs(rtw.ObjectRoot) ||
		rtw.ReleaseFile != filepath.Join(filepath.Dir(rtwPath), "wiki-external-consumer-release") ||
		dc.ReleaseFile != filepath.Join(filepath.Dir(dcPath), "consumer-release") ||
		dc.UsageReceiptFile != filepath.Join(filepath.Dir(dcPath), "consumer-usage.json") ||
		dc.LogicalModel != wikiCompileModelCallpoint || dc.PhysicalModel == "" ||
		dc.PostgresDSN == "" ||
		dc.AccessToken == rtw.DCJobsToken || dc.AccessToken == rtw.RTWWorkerToken {
		t.Fatal("Wiki two-Holder runtime lacks frozen distinct Jobs/Model/RTW identity")
	}
	if _, err := uuid.Parse(rtw.JobID); err != nil {
		t.Fatal("RTW runtime job id is not UUID")
	}
	if _, err := uuid.Parse(dc.UserID); err != nil {
		t.Fatal("DC native runtime user id is not UUID")
	}
	if _, err := uuid.Parse(dc.ModelConfigurationID); err != nil {
		t.Fatal("DC runtime model configuration id is not UUID")
	}
	if _, err := time.Parse(time.RFC3339Nano, dc.ReadyAt); err != nil {
		t.Fatal("DC runtime ready_at is not time")
	}
	if _, err := app.NewWikiCompileModelSession(dc.UserID, dc.AccessToken); err != nil {
		t.Fatal("DC runtime does not contain a native app-user session")
	}
	return rtw, dc, modelRoot
}

type wikiNativeExternalOTLP struct {
	mu         sync.Mutex
	graphTrace string
	appTrace   string
}

func (c *wikiNativeExternalOTLP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/traces" {
		http.Error(w, "bad OTLP path", http.StatusBadRequest)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "bad OTLP body", http.StatusBadRequest)
		return
	}
	var request collectortrace.ExportTraceServiceRequest
	if proto.Unmarshal(raw, &request) != nil {
		http.Error(w, "bad OTLP protobuf", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	for _, resource := range request.ResourceSpans {
		for _, scope := range resource.ScopeSpans {
			for _, span := range scope.Spans {
				switch span.Name {
				case "workflow execute_graph wiki_compile_candidate":
					if scope.Scope != nil && scope.Scope.Name == "trpc.agent.go" {
						c.graphTrace = string(span.TraceId)
					}
				case "content.wiki_compile.process":
					c.appTrace = string(span.TraceId)
				}
			}
		}
	}
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-protobuf")
}

func (c *wikiNativeExternalOTLP) SameNativeAppTrace() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.graphTrace) == 16 && c.graphTrace == c.appTrace
}

func wikiNativeExternalProcessFinished(raw []byte) string {
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
		var record struct {
			Event   string `json:"event"`
			Outcome string `json:"outcome"`
		}
		if json.Unmarshal(line, &record) == nil &&
			record.Event == "content.wiki_compile.process.finished" {
			return record.Outcome
		}
	}
	return ""
}

func wikiNativeExternalUsage(t *testing.T, path, configID, physicalModel string) {
	t.Helper()
	if !filepath.IsAbs(path) {
		t.Fatal("DC Holder usage receipt path is not absolute")
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		info, err := os.Lstat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal("DC Holder usage receipt cannot be read")
		}
		if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600) {
			t.Fatal("DC Holder usage receipt is not a private regular file")
		}
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm() == 0600 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("DC Holder did not produce private model usage receipt after release")
		}
		time.Sleep(100 * time.Millisecond)
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 64<<10 {
		t.Fatal("DC Holder usage receipt is absent or unbounded")
	}
	var receipt struct {
		SchemaVersion        string `json:"schema_version"`
		ModelConfigurationID string `json:"model_configuration_id"`
		PhysicalModel        string `json:"physical_model"`
		Activation           string `json:"activation"`
		Interactions         []struct {
			ModelConfigurationID string          `json:"model_configuration_id"`
			Outcome              string          `json:"outcome"`
			ResponseStatus       *int            `json:"response_status"`
			ResponseBytes        int64           `json:"response_bytes"`
			ResponseSHA          string          `json:"response_sha256"`
			ProviderUsage        json.RawMessage `json:"provider_usage"`
		} `json:"interactions"`
	}
	if json.Unmarshal(raw, &receipt) != nil ||
		receipt.SchemaVersion != "sea.dc.local-chat-usage.v1" ||
		receipt.Activation != "test-only" ||
		receipt.ModelConfigurationID != configID ||
		receipt.PhysicalModel != physicalModel ||
		len(receipt.Interactions) != 1 ||
		receipt.Interactions[0].ModelConfigurationID != configID ||
		receipt.Interactions[0].Outcome != "completed" ||
		receipt.Interactions[0].ResponseStatus == nil ||
		*receipt.Interactions[0].ResponseStatus != http.StatusOK ||
		receipt.Interactions[0].ResponseBytes < 1 ||
		!artifacts.ValidHash(receipt.Interactions[0].ResponseSHA) ||
		len(receipt.Interactions[0].ProviderUsage) == 0 ||
		string(receipt.Interactions[0].ProviderUsage) == "null" {
		t.Fatal("DC native app-user model receipt does not prove one successful published configuration")
	}
	var usage struct {
		TotalTokens int64 `json:"total_tokens"`
	}
	if json.Unmarshal(receipt.Interactions[0].ProviderUsage, &usage) != nil ||
		usage.TotalTokens < 1 {
		t.Fatal("DC native Holder did not retain actual Provider token usage")
	}
	// Holder's SQL WHERE user_id=<runtime native UID> is authoritative.
}

func TestWikiCompileNativeExternalTwoHolder(t *testing.T) {
	rtwPath, dcPath := os.Getenv("SEA_WIKI_EXTERNAL_RUNTIME_FILE"),
		os.Getenv("SEA_WIKI_NATIVE_DC_RUNTIME_FILE")
	if rtwPath == "" && dcPath == "" {
		t.Skip("requires both private RTW Wiki and DC native Model runtime files")
	}
	_, _ = wikiNativeExternalReleasePaths(t, rtwPath, dcPath)
	if rtwPath == "" || dcPath == "" {
		t.Fatal("native Wiki external acceptance requires both live Holder runtime files")
	}
	if os.Getenv("SEA_WIKI_NATIVE_EXTERNAL_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0],
			"-test.run=^TestWikiCompileNativeExternalTwoHolder$", "-test.v")
		cmd.Env = append(os.Environ(), "SEA_WIKI_NATIVE_EXTERNAL_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated two-Holder native Wiki consumer failed: %v\n%s", err, output)
		}
		return
	}
	rtwRuntime, dcRuntime, modelRoot := wikiNativeExternalLoad(t, rtwPath, dcPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rtw, err := ridethewind.New(httpclient.Config{BaseURL: rtwRuntime.RTWBaseURL,
		Token: rtwRuntime.RTWWorkerToken, HTTPClient: &http.Client{Timeout: 20 * time.Second}})
	if err != nil {
		t.Fatal("RTW holder official HTTP client rejected frozen endpoint")
	}
	dcJobs, err := datacenter.New(httpclient.Config{BaseURL: rtwRuntime.DCBaseURL,
		Token: rtwRuntime.DCJobsToken, HTTPClient: &http.Client{Timeout: 20 * time.Second}})
	if err != nil {
		t.Fatal("DC Jobs holder official HTTP client rejected frozen endpoint")
	}
	queued, err := dcJobs.GetJob(ctx, rtwRuntime.JobID)
	if err != nil || queued.State != "queued" || queued.InputHash != rtwRuntime.JobInputHash ||
		queued.Request.JobType != app.WikiCompileJobType {
		t.Fatal("RTW holder did not dispatch the single frozen queued Wiki DC Job")
	}
	build, err := rtw.GetCompile(ctx, rtwRuntime.CompileID)
	if err != nil || build.State != "BUILDING" || build.CompileId != rtwRuntime.CompileID ||
		len(build.SourceRevisionIds) != 1 || build.SourceRevisionIds[0] != rtwRuntime.SourceRevisionID {
		t.Fatal("RTW holder did not retain original one-source BUILDING Compile")
	}
	source, err := rtw.GetRevision(ctx, rtwRuntime.SourceRevisionID)
	if err != nil || source.Kind != "source" || source.Withdrawn ||
		source.ContentHash != artifacts.Hash([]byte(source.Content)) {
		t.Fatal("RTW holder source revision bytes are no longer current")
	}
	objects, err := artifacts.NewLocal(rtwRuntime.ObjectRoot)
	if err != nil {
		t.Fatal("BTW cannot open RTW task-owned shared object root")
	}
	independentReader, err := artifacts.NewLocal(rtwRuntime.ObjectRoot)
	if err != nil {
		t.Fatal("independent object reader cannot open RTW shared root")
	}
	frozen, err := app.NewWikiCompileModelSession(dcRuntime.UserID, dcRuntime.AccessToken)
	if err != nil {
		t.Fatal("DC native app-user runtime did not yield frozen model session")
	}
	provider := app.WikiCompileNativeSessionFunc(func(context.Context) (app.WikiCompileModelSession, error) {
		return frozen, nil
	})
	factory, err := app.NewWikiCompileNativeRunFactory(app.WikiCompileNativeRunFactoryConfig{
		DCGatewayRoot: modelRoot, HTTPClient: &http.Client{Timeout: 90 * time.Second}}, provider)
	if err != nil {
		t.Fatal("official native Wiki model factory rejected DC Gateway")
	}
	defer factory.Close()
	collector := &wikiNativeExternalOTLP{}
	collectorServer := httptest.NewServer(collector)
	defer collectorServer.Close()
	values := wikiCompileConfigFixture(t)
	values["BTW_DC_URL"], values["BTW_DC_TOKEN"] = rtwRuntime.DCBaseURL, rtwRuntime.DCJobsToken
	values["BTW_RTW_URL"], values["BTW_RTW_TOKEN"] = rtwRuntime.RTWBaseURL, rtwRuntime.RTWWorkerToken
	values["BTW_WIKI_SHARED_OBJECT_ROOT"], values["BTW_RTW_OBJECT_ROOT"] =
		rtwRuntime.ObjectRoot, rtwRuntime.ObjectRoot
	values["BTW_OTLP_TRACES_URL"] = collectorServer.URL + "/v1/traces"
	values["BTW_METRICS_ADDR"] = wikiNativeStartMetricsAddr(t)
	values["BTW_LEASE_SECONDS"], values["BTW_POLL_INTERVAL"] = "180", "100ms"
	config, err := loadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal("true two-Holder native Wiki configuration rejected")
	}
	deps := wikiCompileStartDeps{Jobs: dcJobs, Owner: rtw, Runs: factory,
		Objects: objects, ResultRefContractID: app.WikiCompileResultRefContractID,
		ObjectBackend: "shared-local", SharedObjectRoot: rtwRuntime.ObjectRoot,
		RTWObjectRoot: rtwRuntime.ObjectRoot}
	var logs wikiNativeStartLogs
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			if wikiNativeExternalProcessFinished(logs.Bytes()) != "" {
				// Stop after the first native run. A real failed model output must
				// remain red, without waiting for the Holder's lease expiry.
				cancel()
				return
			}
		}
	}()
	serveErr := serveWikiCompileWithDeps(ctx, config, &logs, deps)
	cancel()
	<-watchDone
	if err := os.WriteFile(filepath.Join(filepath.Dir(rtwPath), "btw-wiki-native-external.jsonl"),
		logs.Bytes(), 0600); err != nil {
		t.Fatal("failed to retain bounded native Wiki Holder observability")
	}
	if serveErr != nil || wikiNativeExternalProcessFinished(logs.Bytes()) != "succeeded" {
		t.Fatalf("native DC Wiki Graph did not complete first frozen Holder attempt: process_outcome=%s serve_error=%v",
			wikiNativeExternalProcessFinished(logs.Bytes()), serveErr)
	}
	finalJob, err := dcJobs.GetJob(context.Background(), rtwRuntime.JobID)
	accepted, compileErr := rtw.GetCompile(context.Background(), rtwRuntime.CompileID)
	if err != nil || compileErr != nil || finalJob.State != "succeeded" ||
		accepted.State != "ACCEPTED" || accepted.RevisionId == "" ||
		accepted.ResultHash == "" || finalJob.Result == nil || finalJob.Result.Ref == nil {
		t.Fatal("fresh RTW/DC Holder reads do not confirm native Wiki business and technical success")
	}
	wiki, err := rtw.GetRevision(context.Background(), accepted.RevisionId)
	if err != nil || wiki.Kind != "wiki" || wiki.Withdrawn ||
		wiki.CreatedBy != "btw.compile/"+accepted.CompileId ||
		wiki.ContentHash != artifacts.Hash([]byte(wiki.Content)) ||
		wiki.ObjectKey != "sha256/"+wiki.ContentHash ||
		len(wiki.SourceRefs) != 1 || wiki.SourceRefs[0].RevisionId != source.RevisionId {
		t.Fatal("RTW accepted native Wiki revision does not preserve original source and Markdown")
	}
	markdown, err := independentReader.Get(context.Background(), corpus.Ref{
		Key: wiki.ObjectKey, SHA256: wiki.ContentHash})
	if err != nil || !bytes.Equal(markdown, []byte(wiki.Content)) {
		t.Fatal("independent shared Store reader does not see accepted native Markdown")
	}
	ref := finalJob.Result.Ref
	if ref.URI != "sha256:"+ref.Hash ||
		ref.MediaType != "application/vnd.sea.wiki-compile-result+json" {
		t.Fatal("DC technical Wiki ResultRef does not use frozen sha256 manifest contract")
	}
	manifest, err := independentReader.Get(context.Background(), corpus.Ref{
		Key: "sha256/" + ref.Hash, SHA256: ref.Hash})
	canonical, canonicalErr := jsoncanonicalizer.Transform(manifest)
	var fields map[string]string
	if err != nil || canonicalErr != nil || !bytes.Equal(canonical, manifest) ||
		json.Unmarshal(manifest, &fields) != nil || len(fields) != 13 ||
		fields["job_id"] != rtwRuntime.JobID ||
		fields["job_input_hash"] != rtwRuntime.JobInputHash ||
		fields["wiki_revision_id"] != wiki.RevisionId ||
		fields["candidate_content_sha256"] != wiki.ContentHash ||
		fields["rtw_accept_result_hash"] != accepted.ResultHash ||
		fields["generation"] != strconv.FormatInt(accepted.Generation, 10) {
		t.Fatal("DC native technical manifest did not bind accepted RTW Wiki revision and frozen Job")
	}
	// Quality evaluation consumes the exact accepted Markdown bytes separately
	// from bounded relationship metadata. Both files are task-owned 0600 and
	// contain no native bearer, service token or model response envelope.
	if strings.Contains(wiki.Content, dcRuntime.AccessToken) ||
		strings.Contains(wiki.Content, rtwRuntime.DCJobsToken) ||
		strings.Contains(wiki.Content, rtwRuntime.RTWWorkerToken) {
		t.Fatal("accepted Wiki Markdown unexpectedly contains a Holder credential")
	}
	qualityDir := filepath.Dir(rtwPath)
	markdownFile := filepath.Join(qualityDir, "wiki-external-native-candidate.md")
	if err := os.WriteFile(markdownFile, []byte(wiki.Content), 0600); err != nil {
		t.Fatal("cannot retain accepted Wiki Markdown bytes for quality review")
	}
	quality := struct {
		SchemaVersion  string                  `json:"schema_version"`
		CompileID      string                  `json:"compile_id"`
		WikiRevisionID string                  `json:"wiki_revision_id"`
		Title          string                  `json:"title"`
		MarkdownSHA256 string                  `json:"markdown_sha256"`
		MarkdownFile   string                  `json:"markdown_file"`
		SourceRefs     []ridethewind.SourceRef `json:"source_refs"`
	}{SchemaVersion: "sea.wiki.native-candidate-quality.v1",
		CompileID: accepted.CompileId, WikiRevisionID: wiki.RevisionId,
		Title: wiki.Title, MarkdownSHA256: wiki.ContentHash,
		MarkdownFile: filepath.Base(markdownFile), SourceRefs: wiki.SourceRefs}
	qualityBytes, err := json.Marshal(quality)
	if err != nil || os.WriteFile(filepath.Join(qualityDir, "wiki-external-native-candidate-quality.json"),
		append(qualityBytes, '\n'), 0600) != nil {
		t.Fatal("cannot retain accepted Wiki candidate source refs for quality review")
	}
	if !collector.SameNativeAppTrace() ||
		strings.Contains(string(logs.Bytes()), dcRuntime.AccessToken) ||
		strings.Contains(string(logs.Bytes()), rtwRuntime.DCJobsToken) ||
		strings.Contains(string(logs.Bytes()), rtwRuntime.RTWWorkerToken) {
		t.Fatal("native Graph/Worker trace or structured logging leaked a Holder credential")
	}
	if err := wikiNativeExternalRelease(dcRuntime.ReleaseFile); err != nil {
		t.Fatal("cannot release DC Model Holder for native usage receipt")
	}
	wikiNativeExternalUsage(t, dcRuntime.UsageReceiptFile,
		dcRuntime.ModelConfigurationID, dcRuntime.PhysicalModel)
	proof := map[string]any{"schema_version": "sea.wiki.external-model-evidence.v1",
		"provider_kind": "dc_model_gateway", "user_id": dcRuntime.UserID,
		"model_configuration_id": dcRuntime.ModelConfigurationID,
		"logical_model":          wikiCompileModelCallpoint, "model_call_complete": true,
		"fixed_model_fixture": false}
	proofBytes, err := json.MarshalIndent(proof, "", "  ")
	if err != nil {
		t.Fatal("native external model proof encoding failed")
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(rtwPath), "wiki-external-model-evidence.json"),
		append(proofBytes, '\n'), 0600); err != nil {
		t.Fatal("native model evidence is not durable before RTW Holder release")
	}
}
