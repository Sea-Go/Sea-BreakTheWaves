package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
)

func wikiCompileConfigFixture(t *testing.T) map[string]string {
	t.Helper()
	v := validEnvironment()
	root := t.TempDir()
	v["BTW_JOB_TYPE"] = app.WikiCompileJobType
	v["BTW_WIKI_COMPILE_ENABLED"] = "true"
	v["BTW_WIKI_NATIVE_SESSION_SOURCE"] = wikiCompileNativeSessionSource
	v["BTW_WIKI_MODEL_CALLPOINT"] = wikiCompileModelCallpoint
	v["BTW_WIKI_RESULT_REF_CONTRACT_ID"] = app.WikiCompileResultRefContractID
	v["BTW_WIKI_OBJECT_BACKEND"] = "shared-local"
	v["BTW_WIKI_SHARED_OBJECT_ROOT"] = root
	v["BTW_RTW_OBJECT_ROOT"] = root
	return v
}

type wikiGateJobs struct{ claims int }

func (f *wikiGateJobs) ClaimJob(context.Context, jobs.Claim) (jobs.Job, error) {
	f.claims++
	return jobs.Job{}, errors.New("fixture must never claim before startup gate")
}
func (*wikiGateJobs) GetJob(context.Context, string) (jobs.Job, error) { return jobs.Job{}, nil }
func (*wikiGateJobs) AcknowledgeCancellation(context.Context, string, jobs.Lease) (jobs.Job, error) {
	return jobs.Job{}, errors.New("fixture must never acknowledge before startup gate")
}
func (*wikiGateJobs) CompleteJob(context.Context, string, jobs.Complete) (jobs.CompletionReceipt, error) {
	return jobs.CompletionReceipt{}, nil
}

type wikiGateOwner struct{}

func (*wikiGateOwner) GetCompile(context.Context, string) (ridethewind.Compile, error) {
	return ridethewind.Compile{}, nil
}
func (*wikiGateOwner) GetRevision(context.Context, string) (ridethewind.Revision, error) {
	return ridethewind.Revision{}, nil
}
func (*wikiGateOwner) ClaimCompile(context.Context, ridethewind.ClaimCompileReq) (ridethewind.Compile, error) {
	return ridethewind.Compile{}, nil
}
func (*wikiGateOwner) AcceptCompile(context.Context, ridethewind.AcceptCompileReq) (ridethewind.Compile, error) {
	return ridethewind.Compile{}, nil
}

type wikiGateRuns struct {
	session           app.WikiCompileModelSession
	err               error
	checks            int
	opens             int
	binds             int
	closes            int
	bindErr           error
	closeErr          error
	observed          *telemetry.Bundle
	preBound          bool
	closedAfterBundle bool
}

func (f *wikiGateRuns) Open(context.Context, content.WikiCompileInput,
	app.WikiCompileModelSession) (app.WikiCompileRun, error) {
	f.opens++
	return nil, errors.New("fixture model must not open during startup gate")
}
func (f *wikiGateRuns) CurrentNativeSession(context.Context) (app.WikiCompileModelSession, error) {
	f.checks++
	return f.session, f.err
}
func (f *wikiGateRuns) BindTelemetry(observed *telemetry.Bundle) error {
	f.binds++
	if f.bindErr != nil {
		return f.bindErr
	}
	if observed == nil || !observed.Installed() || observed.Closed() || f.observed != nil || f.preBound {
		return app.ErrWikiCompileNativeFactory
	}
	f.observed = observed
	return nil
}
func (f *wikiGateRuns) Close() error {
	f.closes++
	if f.observed != nil && f.observed.Closed() {
		f.closedAfterBundle = true
	}
	return f.closeErr
}

type wikiGateBucketStore struct {
	artifacts.Store
	bucket string
}

func (s wikiGateBucketStore) Bucket() string { return s.bucket }

func wikiGateBearer() string {
	return "wh_access_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, 32))
}

func wikiGateSession(t *testing.T) app.WikiCompileModelSession {
	t.Helper()
	session, err := app.NewWikiCompileModelSession("11111111-1111-4111-8111-111111111111", wikiGateBearer())
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func wikiGateFixtureDeps(t *testing.T, cfg config) (wikiCompileStartDeps, *wikiGateJobs, *wikiGateRuns) {
	t.Helper()
	jobsFixture := &wikiGateJobs{}
	runs := &wikiGateRuns{session: wikiGateSession(t)}
	root := cfg.Wiki.SharedObjectRoot
	if root == "" {
		root = t.TempDir()
	}
	objects, err := artifacts.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	deps := wikiCompileStartDeps{Jobs: jobsFixture, Owner: &wikiGateOwner{}, Runs: runs,
		Objects: objects, ObjectBackend: "shared-local",
		SharedObjectRoot: cfg.Wiki.SharedObjectRoot, RTWObjectRoot: cfg.Wiki.RTWObjectRoot,
		ResultRefContractID: cfg.Wiki.ResultRefContractID}
	return deps, jobsFixture, runs
}

func TestWikiCompileCommandDefaultOffBeforeEffects(t *testing.T) {
	cfg, err := loadConfig(func(key string) string {
		if key == "BTW_JOB_TYPE" {
			return app.WikiCompileJobType
		}
		return ""
	})
	if err != nil || cfg.JobType != app.WikiCompileJobType || cfg.Wiki == nil || cfg.Wiki.Enabled ||
		workerService(cfg) != "sea-btw-wiki-compile-worker" {
		t.Fatalf("default Wiki job fell through prepare/index: %+v %v", cfg, err)
	}
	var output bytes.Buffer
	if err := serveWikiCompile(context.Background(), cfg, &output); !errors.Is(err, errWikiCompileStartup) {
		t.Fatalf("unprovisioned Wiki process started: %v", err)
	}
	var record map[string]any
	if json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record) != nil ||
		record["event"] != "content.wiki_compile.start_rejected" ||
		record["error_code"] != "SIGNED_DEPENDENCIES_MISSING" {
		t.Fatalf("Wiki startup denial lacked canonical JSON observability: %s", output.Bytes())
	}
}

func TestWikiCompileCommandRejectsUnownedModelObjectOrResultRef(t *testing.T) {
	v := wikiCompileConfigFixture(t)
	get := func(key string) string { return v[key] }
	cfg, err := loadConfig(get)
	if err != nil || cfg.Wiki == nil || !cfg.Wiki.Enabled || cfg.JobType != app.WikiCompileJobType {
		t.Fatalf("explicit Wiki fixture configuration rejected: %+v %v", cfg, err)
	}
	deps, jobsFixture, runs := wikiGateFixtureDeps(t, cfg)
	if err := wikiCompileStartupGate(context.Background(), cfg, deps); err != nil || jobsFixture.claims != 0 ||
		runs.checks != 1 || runs.binds != 0 || runs.closes != 0 {
		t.Fatalf("complete isolated Wiki fixture did not cross startup seam: %v", err)
	}
	unsupported := cfg
	copyWiki := *cfg.Wiki
	unsupported.Wiki = &copyWiki
	unsupported.Wiki.ResultRefContractID = "fixture-only-result-ref"
	if err := wikiCompileStartupGate(context.Background(), unsupported, deps); !errors.Is(err, errWikiCompileStartup) || jobsFixture.claims != 0 {
		t.Fatalf("syntactically valid but unsigned ResultRef contract entered Wiki jobs: %v", err)
	}
	for name, change := range map[string]func(*wikiCompileStartDeps){
		"missing-result-contract": func(d *wikiCompileStartDeps) { d.ResultRefContractID = "" },
		"wrong-result-contract":   func(d *wikiCompileStartDeps) { d.ResultRefContractID = "other" },
		"other-RTW-root":          func(d *wikiCompileStartDeps) { d.RTWObjectRoot = t.TempDir() },
		"missing-jobs-client":     func(d *wikiCompileStartDeps) { d.Jobs = nil },
		"jobs-token-as-model": func(d *wikiCompileStartDeps) {
			d.Runs = &wikiGateRuns{err: app.ErrWikiCompileModelIdentity}
		},
		"wrong-native-account": func(d *wikiCompileStartDeps) {
			d.Runs = &wikiGateRuns{err: app.ErrWikiCompileModelIdentity}
		},
		"native-session-unavailable": func(d *wikiCompileStartDeps) {
			d.Runs = &wikiGateRuns{err: errors.New("fixture native session unavailable")}
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := deps
			change(&bad)
			if err := wikiCompileStartupGate(context.Background(), cfg, bad); !errors.Is(err, errWikiCompileStartup) || jobsFixture.claims != 0 {
				t.Fatalf("missing Wiki dependency reached DC Claim/model: %v claims=%d", err, jobsFixture.claims)
			}
		})
	}
	if _, err := app.NewWikiCompileModelSession("11111111-1111-4111-8111-111111111111",
		v["BTW_DC_TOKEN"]); !errors.Is(err, app.ErrWikiCompileModelIdentity) {
		t.Fatalf("jobs token became a DC native model session: %v", err)
	}
	if _, err := app.NewWikiCompileModelSession("not-a-DC-user", wikiGateBearer()); !errors.Is(err, app.ErrWikiCompileModelIdentity) {
		t.Fatalf("wrong DC account became a model session: %v", err)
	}
	for name, change := range map[string]func(map[string]string){
		"wrong-native-source":    func(m map[string]string) { m["BTW_WIKI_NATIVE_SESSION_SOURCE"] = "jobs-token" },
		"wrong-callpoint":        func(m map[string]string) { m["BTW_WIKI_MODEL_CALLPOINT"] = "other" },
		"unversioned-result-ref": func(m map[string]string) { m["BTW_WIKI_RESULT_REF_CONTRACT_ID"] = "" },
		"other-local-root":       func(m map[string]string) { m["BTW_RTW_OBJECT_ROOT"] = t.TempDir() },
		"bad-enable":             func(m map[string]string) { m["BTW_WIKI_COMPILE_ENABLED"] = "yes" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := make(map[string]string, len(v))
			for key, value := range v {
				bad[key] = value
			}
			change(bad)
			if _, err := loadConfig(func(key string) string { return bad[key] }); err == nil {
				t.Fatalf("Wiki startup config accepted %s", name)
			}
		})
	}
}

func TestWikiCompileCommandS3StoreMustReportRTWBucket(t *testing.T) {
	v := wikiCompileConfigFixture(t)
	v["BTW_WIKI_OBJECT_BACKEND"] = "s3"
	v["BTW_WIKI_OBJECT_BUCKET"] = "sea-wiki-shared"
	v["BTW_RTW_OBJECT_BUCKET"] = "sea-wiki-shared"
	cfg, err := loadConfig(func(key string) string { return v[key] })
	if err != nil {
		t.Fatal(err)
	}
	deps, jobsFixture, _ := wikiGateFixtureDeps(t, cfg)
	deps.ObjectBackend = "s3"
	deps.RTWObjectBucket = v["BTW_RTW_OBJECT_BUCKET"]
	// The local object adapter cannot claim to be the RTW S3 bucket merely
	// because parallel configuration strings agree.
	if err := wikiCompileStartupGate(context.Background(), cfg, deps); !errors.Is(err, errWikiCompileStartup) || jobsFixture.claims != 0 {
		t.Fatalf("local Store impersonated shared RTW S3 bucket: %v", err)
	}
	deps.Objects = wikiGateBucketStore{Store: deps.Objects, bucket: "other-bucket"}
	if err := wikiCompileStartupGate(context.Background(), cfg, deps); !errors.Is(err, errWikiCompileStartup) {
		t.Fatalf("S3 adapter with other bucket entered Wiki worker: %v", err)
	}
	deps.Objects = wikiGateBucketStore{Store: deps.Objects.(wikiGateBucketStore).Store, bucket: cfg.Wiki.ObjectBucket}
	if err := wikiCompileStartupGate(context.Background(), cfg, deps); err != nil || jobsFixture.claims != 0 {
		t.Fatalf("test S3 bucket adapter failed only constructed-bucket gate: %v", err)
	}
	v["BTW_RTW_OBJECT_BUCKET"] = "other-bucket"
	if _, err := loadConfig(func(key string) string { return v[key] }); err == nil {
		t.Fatal("config let BTW candidate target a different RTW bucket")
	}
}

func TestWikiCompileCommandLogsConfiguredStartupFailureBeforeClaim(t *testing.T) {
	v := wikiCompileConfigFixture(t)
	cfg, err := loadConfig(func(key string) string { return v[key] })
	if err != nil {
		t.Fatal(err)
	}
	deps, jobsFixture, runs := wikiGateFixtureDeps(t, cfg)
	cfg.OTLPTracesURL = "" // Directly inject one runtime failure after the owned gate.
	var output bytes.Buffer
	if err := serveWikiCompileWithDeps(context.Background(), cfg, &output, deps); err == nil ||
		jobsFixture.claims != 0 || runs.checks != 1 || runs.binds != 0 || runs.closes != 1 {
		t.Fatalf("configured Wiki failure claimed DC job or hid error: err=%v claims=%d checks=%d",
			err, jobsFixture.claims, runs.checks)
	}
	var record map[string]any
	if json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record) != nil ||
		record["event"] != "content.wiki_compile.start_failed" ||
		record["error_code"] != "TELEMETRY_INIT_FAILED" ||
		strings.Contains(output.String(), v["BTW_DC_TOKEN"]) {
		t.Fatalf("Wiki startup failure lacked bounded JSON or exposed token: %s", output.Bytes())
	}
}
