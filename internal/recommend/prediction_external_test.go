package recommend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/prediction"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

type predictionExternalRuntime struct {
	Endpoint      string `json:"endpoint"`
	PostgresDSN   string `json:"postgres_dsn"`
	AdminEmail    string `json:"admin_email"`
	AdminPassword string `json:"admin_password"`
	UserAEmail    string `json:"user_a_email"`
	UserAPassword string `json:"user_a_password"`
	UserBEmail    string `json:"user_b_email"`
	UserBPassword string `json:"user_b_password"`
	MetricsToken  string `json:"metrics_token"`
	DCCommit      string `json:"dc_commit"`
}

type predictionExternalConfiguration struct {
	ID                   string `json:"id"`
	Model                string `json:"model"`
	ArtifactSHA256       string `json:"artifact_sha256"`
	FeatureContractID    string `json:"feature_contract_id"`
	PairID               string `json:"pair_id"`
	SpaceID              string `json:"space_id"`
	EmbeddingDimension   int    `json:"embedding_dimension"`
	VerificationStatus   string `json:"verification_status"`
	VerificationRevision int64  `json:"verification_revision"`
}

type predictionExternalPointer struct {
	Model           string  `json:"model"`
	ConfigurationID *string `json:"configuration_id"`
	Revision        int64   `json:"revision"`
}

type predictionExternalSession struct {
	AccessToken string `json:"accessToken"`
	User        struct {
		ID string `json:"id"`
	} `json:"user"`
}

type predictionExternalExporter struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

func (e *predictionExternalExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans = append(e.spans, spans...)
	return nil
}
func (*predictionExternalExporter) Shutdown(context.Context) error { return nil }
func (e *predictionExternalExporter) snapshot() []sdktrace.ReadOnlySpan {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]sdktrace.ReadOnlySpan(nil), e.spans...)
}

type lostPredictionProxy struct {
	upstream  string
	lostKey   string
	client    *http.Client
	mu        sync.Mutex
	captured  []byte
	keyCounts map[string]int
}

func (p *lostPredictionProxy) ServeHTTP(w http.ResponseWriter, incoming *http.Request) {
	key := incoming.Header.Get("Idempotency-Key")
	p.mu.Lock()
	if p.keyCounts == nil {
		p.keyCounts = map[string]int{}
	}
	p.keyCounts[key]++
	attempt := p.keyCounts[key]
	p.mu.Unlock()
	// The second response is an explicit intermediary fault: DC has already
	// committed the first response, but BTW must treat this exact lease
	// envelope as pending and poll the original key. The third request reaches
	// real DC and reads its immutable completed replay.
	if key == p.lostKey && attempt == 2 {
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":"prediction logical call is still in progress"}`)
		return
	}
	body, err := io.ReadAll(incoming.Body)
	if err != nil {
		http.Error(w, "read", http.StatusBadGateway)
		return
	}
	target := p.upstream + incoming.URL.RequestURI()
	request, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, target, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "request", http.StatusBadGateway)
		return
	}
	for key, values := range incoming.Header {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	response, err := p.client.Do(request)
	if err != nil {
		http.Error(w, "upstream", http.StatusBadGateway)
		return
	}
	responseBody, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		http.Error(w, "upstream-read", http.StatusBadGateway)
		return
	}
	unknown := key == p.lostKey && attempt == 1 && response.StatusCode == http.StatusOK
	if unknown {
		p.mu.Lock()
		p.captured = append([]byte(nil), responseBody...)
		p.mu.Unlock()
	}
	if unknown {
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"prediction outcome is unknown; retry the same idempotency key"}`)
		return
	}
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}

func (p *lostPredictionProxy) evidence() ([]byte, map[string]int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	counts := make(map[string]int, len(p.keyCounts))
	for key, count := range p.keyCounts {
		counts[key] = count
	}
	return append([]byte(nil), p.captured...), counts
}

func readPredictionExternalRuntime(t *testing.T) predictionExternalRuntime {
	t.Helper()
	path := os.Getenv("BTW_PREDICTION_DC_RUNTIME")
	if path == "" {
		t.Skip("run integration/prediction/acceptance.sh for real DC prediction acceptance")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var runtime predictionExternalRuntime
	if err := json.Unmarshal(body, &runtime); err != nil || runtime.Endpoint == "" || runtime.PostgresDSN == "" ||
		runtime.AdminEmail == "" || runtime.AdminPassword == "" || runtime.UserAEmail == "" ||
		runtime.UserBEmail == "" || runtime.MetricsToken == "" || runtime.DCCommit == "" {
		t.Fatal("incomplete private DC prediction runtime")
	}
	return runtime
}

func predictionHTTPJSON(t *testing.T, client *http.Client, method, target string, input any,
	destination any, expected int) []byte {
	t.Helper()
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != expected {
		t.Fatalf("%s %s status=%d body=%s", method, target, response.StatusCode, raw)
	}
	if destination != nil {
		if err := json.Unmarshal(raw, destination); err != nil {
			t.Fatal(err)
		}
	}
	return raw
}

func predictionAdminClient(t *testing.T, runtime predictionExternalRuntime) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	predictionHTTPJSON(t, client, http.MethodPost, runtime.Endpoint+"/api/v1/admin/auth/login",
		map[string]string{"email": runtime.AdminEmail, "password": runtime.AdminPassword}, &map[string]any{}, http.StatusOK)
	return client
}

func predictionNativeSession(t *testing.T, runtime predictionExternalRuntime, email, password string) predictionExternalSession {
	t.Helper()
	var session predictionExternalSession
	predictionHTTPJSON(t, &http.Client{Timeout: 10 * time.Second}, http.MethodPost, runtime.Endpoint+"/v1/auth/sessions",
		map[string]string{"email": email, "password": password}, &session, http.StatusCreated)
	if session.AccessToken == "" || uuid.Validate(session.User.ID) != nil {
		t.Fatalf("invalid native prediction session: %+v", session)
	}
	return session
}

func predictionArtifactPackage(t *testing.T, directory string) (prediction.ArtifactPackage, prediction.ModelConfig, []byte) {
	t.Helper()
	read := func(name string) []byte {
		body, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	manifest := read("manifest.json")
	pkg := prediction.ArtifactPackage{Manifest: manifest, Weights: read("weights.json"),
		Preprocessing: read("preprocessing.json"), ModelConfig: read("model_config.json"), Probes: read("probes.json")}
	var modelConfig prediction.ModelConfig
	if err := prediction.Decode(pkg.ModelConfig, &modelConfig); err != nil {
		t.Fatal(err)
	}
	return pkg, modelConfig, manifest
}

func expectedPredictionOutput(t *testing.T, directory string, userInterest, itemQuality float64) prediction.Output {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(directory, "probes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var probes struct {
		Cases []struct {
			Input struct {
				UserInterest float64 `json:"user_interest"`
				ItemQuality  float64 `json:"item_quality"`
			} `json:"input"`
			Serving prediction.Output `json:"serving_output"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(body, &probes); err != nil {
		t.Fatal(err)
	}
	for _, item := range probes.Cases {
		if item.Input.UserInterest == userInterest && item.Input.ItemQuality == itemQuality {
			return item.Serving
		}
	}
	t.Fatal("exporter probe input is missing")
	return prediction.Output{}
}

func samePredictionNumber(left, right float64) bool {
	delta := left - right
	return delta >= -1e-12 && delta <= 1e-12
}

func requirePredictionNumbers(t *testing.T, candidate PredictionCandidate, expected prediction.Output) {
	t.Helper()
	for name, values := range map[string][2][]float64{
		"user":        {candidate.UserTower.Output.UserEmbedding, expected.UserEmbedding},
		"item":        {candidate.ItemTower.Output.ItemEmbedding, expected.ItemEmbedding},
		"ranker_user": {candidate.Ranker.Output.UserEmbedding, expected.UserEmbedding},
		"ranker_item": {candidate.Ranker.Output.ItemEmbedding, expected.ItemEmbedding},
	} {
		if len(values[0]) != len(values[1]) {
			t.Fatalf("%s vector shape differs", name)
		}
		for index := range values[0] {
			if !samePredictionNumber(values[0][index], values[1][index]) {
				t.Fatalf("%s[%d]=%.17g expected %.17g", name, index, values[0][index], values[1][index])
			}
		}
	}
	if candidate.Ranker.Output.TowerDot == nil || expected.TowerDot == nil ||
		!samePredictionNumber(*candidate.Ranker.Output.TowerDot, *expected.TowerDot) ||
		candidate.Ranker.Output.RankerLogit == nil || expected.RankerLogit == nil ||
		!samePredictionNumber(*candidate.Ranker.Output.RankerLogit, *expected.RankerLogit) {
		t.Fatalf("ranker scalar differs actual=%+v expected=%+v", candidate.Ranker.Output, expected)
	}
}

type predictionReceiptRow struct {
	ID           string
	UserID       string
	Task         string
	Attempt      int
	InputValues  int64
	OutputValues int64
}

func predictionReceiptRows(t *testing.T, pool *pgxpool.Pool, userIDs ...string) []predictionReceiptRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT id::text,user_id::text,task,attempt,input_feature_values,output_values
		FROM telemetry.prediction_call WHERE user_id::text=ANY($1) ORDER BY user_id,task`, userIDs)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := []predictionReceiptRow{}
	for rows.Next() {
		var item predictionReceiptRow
		if err := rows.Scan(&item.ID, &item.UserID, &item.Task, &item.Attempt, &item.InputValues, &item.OutputValues); err != nil {
			t.Fatal(err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPredictionCandidateThroughRealDC(t *testing.T) {
	runtime := readPredictionExternalRuntime(t)
	candidateDirectory, evidenceDirectory := os.Getenv("BTW_PREDICTION_CANDIDATE"), os.Getenv("BTW_PREDICTION_EVIDENCE_DIR")
	if candidateDirectory == "" || evidenceDirectory == "" {
		t.Fatal("candidate and evidence directories are required")
	}
	pkg, modelConfig, manifestBytes := predictionArtifactPackage(t, candidateDirectory)
	manifestDigest := sha256.Sum256(manifestBytes)
	artifactSHA := hex.EncodeToString(manifestDigest[:])
	admin := predictionAdminClient(t, runtime)
	var registered struct {
		Configuration predictionExternalConfiguration `json:"configuration"`
	}
	predictionHTTPJSON(t, admin, http.MethodPost, runtime.Endpoint+"/api/v1/admin/prediction-configurations",
		map[string]any{"model": "recommend-engagement", "artifact": pkg}, &registered, http.StatusCreated)
	configuration := registered.Configuration
	if configuration.ArtifactSHA256 != artifactSHA || configuration.PairID != modelConfig.PairID ||
		configuration.SpaceID != modelConfig.SpaceID || configuration.VerificationStatus != "candidate" ||
		configuration.VerificationRevision != 0 || configuration.EmbeddingDimension != 2 {
		t.Fatalf("DC registration drift: %+v", configuration)
	}
	var probe struct {
		Probe struct {
			Status   string `json:"status"`
			Revision int64  `json:"revision"`
		} `json:"probe"`
	}
	predictionHTTPJSON(t, admin, http.MethodPost, runtime.Endpoint+"/api/v1/admin/prediction-configurations/"+configuration.ID+"/probe",
		nil, &probe, http.StatusOK)
	if probe.Probe.Status != "passed" || probe.Probe.Revision != 1 {
		t.Fatalf("DC probe drift: %+v", probe)
	}
	var activated struct {
		Pointer predictionExternalPointer `json:"pointer"`
	}
	predictionHTTPJSON(t, admin, http.MethodPost, runtime.Endpoint+"/api/v1/admin/prediction-configurations/"+configuration.ID+"/activate",
		map[string]int64{"expected_revision": 0}, &activated, http.StatusOK)
	if activated.Pointer.ConfigurationID == nil || *activated.Pointer.ConfigurationID != configuration.ID || activated.Pointer.Revision != 1 {
		t.Fatalf("DC technical pointer activation drift: %+v", activated)
	}

	sessionA := predictionNativeSession(t, runtime, runtime.UserAEmail, runtime.UserAPassword)
	sessionB := predictionNativeSession(t, runtime, runtime.UserBEmail, runtime.UserBPassword)
	logicalCall := "synthetic-evaluation-0001"
	proxy := &lostPredictionProxy{upstream: runtime.Endpoint, lostKey: logicalCall + ".user",
		client: &http.Client{Timeout: 10 * time.Second}}
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()
	tracedClient := &http.Client{Timeout: 10 * time.Second, Transport: otelhttp.NewTransport(http.DefaultTransport)}
	dcA, err := datacenter.New(httpclient.Config{BaseURL: proxyServer.URL, Token: sessionA.AccessToken, HTTPClient: tracedClient})
	if err != nil {
		t.Fatal(err)
	}
	dcB, err := datacenter.New(httpclient.Config{BaseURL: runtime.Endpoint, Token: sessionB.AccessToken, HTTPClient: tracedClient})
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	exporter := &predictionExternalExporter{}
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "sea-btw-prediction-acceptance",
		Environment: "test", Version: os.Getenv("BTW_PREDICTION_EXPECTED_COMMIT"), InstanceID: "cross-repo",
		Output: &logs, Level: slog.LevelInfo, TraceExporter: exporter, SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	config := PredictionCandidateConfig{SyntheticEvaluationEnabled: true, Model: configuration.Model,
		ConfigurationID: configuration.ID, ArtifactSHA256: configuration.ArtifactSHA256,
		FeatureContractID: configuration.FeatureContractID, PairID: configuration.PairID, SpaceID: configuration.SpaceID}
	useCaseA, err := NewPredictionCandidateUseCase(config, dcA, observed)
	if err != nil {
		t.Fatal(err)
	}
	useCaseB, err := NewPredictionCandidateUseCase(config, dcB, observed)
	if err != nil {
		t.Fatal(err)
	}
	sessions := inmemory.NewSessionService()
	graphA, err := NewPredictionGraphRuntime("prediction-acceptance-a", useCaseA, sessions, observed)
	if err != nil {
		t.Fatal(err)
	}
	graphB, err := NewPredictionGraphRuntime("prediction-acceptance-b", useCaseB, sessions, observed)
	if err != nil {
		t.Fatal(err)
	}
	ctx, parent := observed.Tracer().Start(context.Background(), "btw.prediction.acceptance")
	requestA := PredictionCandidateRequest{Subject: PredictionSubject{Issuer: "rtw.identity", SubjectID: "1001"},
		Features:    SyntheticPredictionFeatures{Source: "synthetic_typed_fixture", UserInterest: 0.2, ItemQuality: 0.1},
		LogicalCall: logicalCall}
	candidateA, err := graphA.Run(ctx, PredictionGraphRequest{Candidate: requestA, SessionID: "prediction-a-1", RunID: "prediction-a-run-1"})
	if err != nil || !validPredictionCandidate(candidateA) || candidateA.BusinessActivation != "none" {
		t.Fatalf("subject A real Graph candidate=%+v err=%v", candidateA, err)
	}
	expected := expectedPredictionOutput(t, candidateDirectory, 0.2, 0.1)
	requirePredictionNumbers(t, candidateA, expected)
	lostBody, proxyCounts := proxy.evidence()
	if len(lostBody) == 0 || digestPrediction(lostBody) != candidateA.UserTower.ResponseSHA256 ||
		proxyCounts[logicalCall+".user"] != 3 {
		t.Fatalf("503/409/replay sequence did not recover with same key: hash=%s receipt=%s counts=%v",
			digestPrediction(lostBody), candidateA.UserTower.ResponseSHA256, proxyCounts)
	}
	replayedA, err := graphA.Run(ctx, PredictionGraphRequest{Candidate: requestA,
		SessionID: "prediction-a-2", RunID: "prediction-a-run-2"})
	if err != nil || replayedA.ID != candidateA.ID || replayedA.UserTower.ModelCallID != candidateA.UserTower.ModelCallID ||
		replayedA.ItemTower.ModelCallID != candidateA.ItemTower.ModelCallID || replayedA.Ranker.ModelCallID != candidateA.Ranker.ModelCallID {
		t.Fatalf("subject A logical replay changed candidate: first=%+v replay=%+v err=%v", candidateA, replayedA, err)
	}
	requestB := requestA
	requestB.Subject.SubjectID = "1002"
	candidateB, err := graphB.Run(ctx, PredictionGraphRequest{Candidate: requestB,
		SessionID: "prediction-b-1", RunID: "prediction-b-run-1"})
	if err != nil || !validPredictionCandidate(candidateB) || candidateB.BusinessActivation != "none" ||
		candidateB.UserTower.ModelCallID == candidateA.UserTower.ModelCallID ||
		candidateB.ItemTower.ModelCallID == candidateA.ItemTower.ModelCallID ||
		candidateB.Ranker.ModelCallID == candidateA.Ranker.ModelCallID {
		t.Fatalf("second DC actor was not isolated: A=%+v B=%+v err=%v", candidateA, candidateB, err)
	}
	parent.End()

	badBase := prediction.Request{Model: configuration.Model, ConfigurationID: configuration.ID,
		OutputContract: prediction.OutputContract(prediction.Ranker), FeatureContractID: configuration.FeatureContractID,
		PairID: configuration.PairID, SpaceID: configuration.SpaceID, Task: prediction.Ranker,
		Input: []prediction.Input{{ID: candidateA.InputID, UserInterest: &requestA.Features.UserInterest,
			ItemQuality: &requestA.Features.ItemQuality}}}
	for name, mutate := range map[string]func(*prediction.Request){
		"configuration": func(request *prediction.Request) { request.ConfigurationID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" },
		"pair":          func(request *prediction.Request) { request.PairID = "sea.recommend.pair.other" },
		"space":         func(request *prediction.Request) { request.SpaceID = "sea.recommend.space.other" },
	} {
		request := badBase
		mutate(&request)
		_, err := dcA.Predict(context.Background(), request, "wrong-"+name+"-0001")
		var status *httpclient.HTTPError
		if !errors.As(err, &status) || status.StatusCode != http.StatusConflict {
			t.Fatalf("wrong %s request was not rejected by real DC: %v", name, err)
		}
	}
	badShape := badBase
	badShape.Task, badShape.OutputContract = prediction.UserTower, prediction.OutputContract(prediction.UserTower)
	if _, err := dcA.Predict(context.Background(), badShape, "wrong-shape-0001"); err == nil {
		t.Fatal("invalid prediction shape reached real DC")
	}

	pool, err := pgxpool.New(context.Background(), runtime.PostgresDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	receipts := predictionReceiptRows(t, pool, sessionA.User.ID, sessionB.User.ID)
	if len(receipts) != 6 {
		t.Fatalf("logical replay or wrong contract changed receipt count: %+v", receipts)
	}
	perUser := map[string]struct {
		rows          int
		input, output int64
	}{}
	for _, receipt := range receipts {
		if receipt.Attempt != 1 {
			t.Fatalf("lost completed response triggered recompute: %+v", receipt)
		}
		aggregate := perUser[receipt.UserID]
		aggregate.rows++
		aggregate.input += receipt.InputValues
		aggregate.output += receipt.OutputValues
		perUser[receipt.UserID] = aggregate
	}
	for _, userID := range []string{sessionA.User.ID, sessionB.User.ID} {
		aggregate := perUser[userID]
		if aggregate.rows != 3 || aggregate.input != 4 || aggregate.output != 10 {
			t.Fatalf("DC non-token usage duplicated for actor %s: %+v", userID, aggregate)
		}
	}
	byID := make(map[string]predictionReceiptRow, len(receipts))
	for _, receipt := range receipts {
		byID[receipt.ID] = receipt
	}
	for actor, candidate := range map[string]PredictionCandidate{
		sessionA.User.ID: candidateA, sessionB.User.ID: candidateB,
	} {
		for _, task := range []PredictionTaskReceipt{candidate.UserTower, candidate.ItemTower, candidate.Ranker} {
			row, ok := byID[task.ModelCallID]
			if !ok || row.UserID != actor || row.Task != string(task.Task) || row.Attempt != 1 ||
				row.InputValues != task.Usage.InputFeatureValues || row.OutputValues != task.Usage.OutputValues {
				t.Fatalf("Graph task did not match one durable actor-scoped PG receipt: actor=%s task=%+v row=%+v", actor, task, row)
			}
		}
	}
	firstPG := byID[candidateA.UserTower.ModelCallID]
	actorARows := perUser[sessionA.User.ID].rows
	var distinctKeys int
	if err := pool.QueryRow(context.Background(), `SELECT count(DISTINCT idempotency_key_hash)
		FROM telemetry.prediction_call WHERE user_id::text=ANY($1)`, []string{sessionA.User.ID, sessionB.User.ID}).Scan(&distinctKeys); err != nil || distinctKeys != 3 {
		t.Fatalf("native actor scope did not reuse three logical keys: count=%d err=%v", distinctKeys, err)
	}

	metricsRequest, _ := http.NewRequest(http.MethodGet, runtime.Endpoint+"/metrics", nil)
	metricsRequest.Header.Set("Authorization", "Bearer "+runtime.MetricsToken)
	metricsResponse, err := http.DefaultClient.Do(metricsRequest)
	if err != nil {
		t.Fatal(err)
	}
	metricsBody, _ := io.ReadAll(metricsResponse.Body)
	_ = metricsResponse.Body.Close()
	if metricsResponse.StatusCode != http.StatusOK {
		t.Fatalf("DC metrics status=%d", metricsResponse.StatusCode)
	}
	for _, sample := range []string{
		`datacenter_prediction_usage_total{task="user_tower",unit="input_rows"} 2`,
		`datacenter_prediction_usage_total{task="item_tower",unit="output_values"} 4`,
		`datacenter_prediction_usage_total{task="ranker",unit="input_feature_values"} 4`,
		`datacenter_prediction_usage_total{task="ranker",unit="output_values"} 12`,
	} {
		if !bytes.Contains(metricsBody, []byte(sample)) {
			t.Fatalf("DC non-token metric missing %q", sample)
		}
	}

	var disabled struct {
		Pointer predictionExternalPointer `json:"pointer"`
	}
	predictionHTTPJSON(t, admin, http.MethodPost, runtime.Endpoint+"/api/v1/admin/prediction-models/"+url.PathEscape(configuration.Model)+"/disable",
		map[string]int64{"expected_revision": 1}, &disabled, http.StatusOK)
	if disabled.Pointer.ConfigurationID != nil || disabled.Pointer.Revision != 2 {
		t.Fatalf("technical pointer was not withdrawn: %+v", disabled)
	}

	if err := graphA.Close(); err != nil {
		t.Error(err)
	}
	if err := graphB.Close(); err != nil {
		t.Error(err)
	}
	if err := sessions.Close(); err != nil {
		t.Error(err)
	}
	if err := observed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := exporter.snapshot()
	names := map[string]int{}
	trpcScopes := map[string]bool{}
	for _, span := range spans {
		names[span.Name()]++
		if strings.HasPrefix(span.Name(), "invoke_agent ") || strings.HasPrefix(span.Name(), "workflow execute_") {
			trpcScopes[span.Name()] = span.InstrumentationScope().Name == "trpc.agent.go"
		}
	}
	for _, name := range []string{"btw.prediction.acceptance", "recommend.prediction_candidate",
		"recommend.prediction_call", "runtime.run", "invoke_agent recommend_prediction_candidate",
		"workflow execute_graph recommend_prediction_candidate",
		"workflow execute_function_node build_prediction_candidate"} {
		if names[name] == 0 {
			t.Fatalf("missing BTW prediction span %q: %v", name, names)
		}
	}
	for name, native := range trpcScopes {
		if !native {
			t.Fatalf("framework span %q did not use native tRPC scope", name)
		}
	}

	logEvents := map[string]int{}
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte{'\n'}) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("BTW prediction emitted non-JSON log: %q", line)
		}
		event, _ := record["event"].(string)
		logEvents[event]++
		if event == "recommend.prediction_call.finished" && record["outcome"] == "succeeded" {
			for _, field := range []string{"duration_ms", "model_call_id", "attempts", "input_rows",
				"input_feature_values", "output_rows", "output_values", "trace_id", "span_id"} {
				if record[field] == nil {
					t.Fatalf("BTW prediction log omitted %s: %v", field, record)
				}
			}
		}
	}
	if logEvents["recommend.prediction_call.finished"] < 9 || logEvents["runtime.run.finished"] != 3 {
		t.Fatalf("incomplete structured log evidence: %v", logEvents)
	}

	if err := os.MkdirAll(evidenceDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	spanNames := make([]string, 0, len(names))
	for name := range names {
		spanNames = append(spanNames, name)
	}
	sort.Strings(spanNames)
	report := map[string]any{
		"schema":     "sea.btw.real-dc-prediction-acceptance.v1",
		"btw_commit": os.Getenv("BTW_PREDICTION_EXPECTED_COMMIT"), "dc_commit": runtime.DCCommit,
		"candidate_manifest_sha256": artifactSHA, "source_data_kind": "synthetic",
		"feature_source":                     "synthetic_typed_fixture",
		"feature_dependency":                 "approved real FeatureSpec mapping to user_interest/item_quality is not defined",
		"rtw_subject_contract":               map[string]any{"issuer": "rtw.identity", "subject_ids": []string{"1001", "1002"}},
		"runtime_subject_compatibility_slot": "fixed platform slot in v1 Runner adapter; not read from SubjectRef v2 and not a tenant claim",
		"dc_native_actor_binding":            "not_approved_and_not_equal_to_rtw_subject",
		"dc_actor_isolation_claim":           "same logical keys are isolated only by two independent DC native actor UUIDs",
		"configuration_id":                   configuration.ID, "artifact_sha256": configuration.ArtifactSHA256,
		"pair_id": configuration.PairID, "space_id": configuration.SpaceID,
		"technical_pointer": map[string]any{"activated_revision": 1, "withdrawn_revision": 2},
		"business_status":   "candidate_default_off", "business_activation": "none",
		"quality_claim": "not_evaluated", "candidate_a": candidateA, "candidate_a_replay": replayedA,
		"candidate_b": candidateB, "exporter_expected": expected,
		"recovery_sequence": map[string]any{"http_statuses": []int{503, 409, 200},
			"same_key_requests":   proxyCounts[logicalCall+".user"],
			"fault_origin":        "intermediary injected 503 and 409 after DC completed first response",
			"dc_pg_first_attempt": firstPG.Attempt, "dc_pg_logical_rows_for_actor": actorARows,
			"dc_pg_model_call_id":                firstPG.ID,
			"captured_completed_response_sha256": digestPrediction(lostBody),
			"replayed_candidate_equal":           replayedA.ID == candidateA.ID},
		"postgres_receipts": receipts, "distinct_logical_key_hashes_across_two_actors": distinctKeys,
		"metrics_verified": true, "trpc_native_spans_verified": true, "span_names": spanNames,
		"contract_rejections": map[string]any{"wrong_configuration": "dc_http_409", "wrong_pair": "dc_http_409",
			"wrong_space": "dc_http_409", "wrong_shape": "btw_typed_client_rejected_before_http"},
		"structured_log_events": logEvents,
	}
	reportBody, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evidenceDirectory, "report.json"), append(reportBody, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evidenceDirectory, "btw-structured.log"), logs.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("real DC prediction candidate=%s replay=%s actorB=%s config=%s artifact=%s receipts=%d",
		candidateA.ID, replayedA.ID, candidateB.ID, configuration.ID, configuration.ArtifactSHA256, len(receipts))
}
