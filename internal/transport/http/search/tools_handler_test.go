package search

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
)

func toolsFixture(t *testing.T, key []byte, now time.Time) (ToolsRequest, signedToolsScope, string) {
	t.Helper()
	request := ToolsRequest{ModuleID: "module-1", Query: "why", Depth: searchdomain.Fast,
		Intelligence: searchdomain.Low, SearchID: "search-1"}
	request.Limits.ReadCalls, request.Limits.QuoteRunes = 1, 100
	requestBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(requestBytes)
	snapshot, err := json.Marshal(fixedSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	snapshotHash := sha256.Sum256(snapshot)
	scope := signedToolsScope{Audience: toolsScopeAudience,
		Subject:   btwruntime.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "123"},
		SessionID: "session-1", OperationID: "toolop_123", BudgetRef: "budget_123", SearchID: request.SearchID,
		SnapshotRef: "snapshot_" + hex.EncodeToString(snapshotHash[:]), Snapshot: snapshot,
		RequestHash: hex.EncodeToString(hash[:]), IssuedAtUnix: now.Unix(), ExpiresAtUnix: now.Add(90 * time.Second).Unix()}
	scopeBytes, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	return request, scope, signedHeader(key, scopeBytes)
}

func TestSignedToolsScopeAndNativeGraphHTTP(t *testing.T) {
	if os.Getenv("SEARCH_TOOLS_HTTP_CHILD") != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestSignedToolsScopeAndNativeGraphHTTP$")
		cmd.Env = append(os.Environ(), "SEARCH_TOOLS_HTTP_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Tools HTTP subprocess: %v\n%s", err, output)
		}
		return
	}
	key := bytes.Repeat([]byte("t"), 32)
	now := time.Unix(1_700_000_000, 0)
	resolver, err := NewSignedToolsScopeResolver(key)
	if err != nil {
		t.Fatal(err)
	}
	resolver.now = func() time.Time { return now }
	var logs bytes.Buffer
	exporter := &traceSink{}
	bundle, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-search-tools-fixture",
		Environment: "test", Version: "fixture-sha", InstanceID: "fixture", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: exporter})
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Bool
	var sourceReads atomic.Int32
	boundary, err := searchdomain.NewToolRunBoundary(fixedDelivery(t, &accepted, fixedSnapshot(), &sourceReads), bundle)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewToolsHandler(resolver, boundary, bundle)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	defer func() {
		if err := boundary.Close(); err != nil {
			t.Error(err)
		}
		if err := bundle.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	body, payload, validHeader := toolsFixture(t, key, now)
	signed := func(payload signedToolsScope) string {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return signedHeader(key, raw)
	}
	send := func(body ToolsRequest, headers ...string) (int, []byte) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequest(http.MethodPost, server.URL+ToolsRoute, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		for _, header := range headers {
			request.Header.Add(toolsScopeHeader, header)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		contents, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, contents
	}
	badSig := validHeader[:len(validHeader)-1] + "A"
	if strings.HasSuffix(validHeader, "A") {
		badSig = validHeader[:len(validHeader)-1] + "B"
	}
	badSnapshot := payload
	badSnapshot.SnapshotRef = "snapshot_" + strings.Repeat("0", 64)
	badAudience := payload
	badAudience.Audience = "other"
	badTime := payload
	badTime.ExpiresAtUnix = now.Add(121 * time.Second).Unix()
	wrongBody := body
	wrongBody.Query = "changed"
	missingFlagRaw, _ := json.Marshal(payload)
	missingFlagRaw = bytes.Replace(missingFlagRaw, []byte(`"allow_partial":false,`), nil, 1)
	unknownNested := bytes.Replace(payload.Snapshot, []byte(`"module_id":`), []byte(`"unknown":true,"module_id":`), 1)
	badNested := payload
	badNested.Snapshot = unknownNested
	for _, tc := range []struct {
		name    string
		body    ToolsRequest
		headers []string
	}{
		{"missing", body, nil}, {"duplicate", body, []string{validHeader, validHeader}},
		{"bad signature", body, []string{badSig}}, {"bad encoding", body, []string{validHeader + "="}},
		{"bad audience", body, []string{signed(badAudience)}},
		{"bad snapshot hash", body, []string{signed(badSnapshot)}},
		{"bad time", body, []string{signed(badTime)}},
		{"wrong body hash", wrongBody, []string{validHeader}},
		{"missing flag", body, []string{signedHeader(key, missingFlagRaw)}},
		{"unknown nested", body, []string{signed(badNested)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, response := send(tc.body, tc.headers...)
			if status != http.StatusForbidden || sourceReads.Load() != 0 || accepted.Load() {
				t.Fatalf("untrusted Tools scope passed: %d %s source=%d accepted=%t", status, response, sourceReads.Load(), accepted.Load())
			}
		})
	}
	high := body
	high.Intelligence = searchdomain.High
	highBytes, _ := json.Marshal(high)
	highHash := sha256.Sum256(highBytes)
	highScope := payload
	highScope.RequestHash = hex.EncodeToString(highHash[:])
	status, response := send(high, signed(highScope))
	if status != http.StatusServiceUnavailable || !bytes.Contains(response, []byte("TOOLS_PROFILE_UNAVAILABLE")) || sourceReads.Load() != 0 {
		t.Fatalf("unsupported signed profile entered Graph: %d %s", status, response)
	}
	status, response = send(body, validHeader)
	var result ToolsResponse
	if err := json.Unmarshal(response, &result); err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || result.SearchID != body.SearchID || result.SnapshotRef != payload.SnapshotRef ||
		result.Status != "complete" || len(result.Evidence) != 1 || result.Evidence[0].Quote != "citation text" ||
		result.PackHash == "" || result.CitationReceipt == nil || result.CitationReceipt.PackHash != result.PackHash ||
		result.Usage.ReadCalls != 1 || result.Usage.QuoteRunes != len("citation text") ||
		result.Gaps == nil || result.Conflicts == nil || sourceReads.Load() != 1 || !accepted.Load() {
		t.Fatalf("valid Tools run did not return durable bare evidence: status=%d result=%+v source=%d", status, result, sourceReads.Load())
	}
	if bytes.Contains(response, []byte(`"answer"`)) || bytes.Contains(response, []byte(`"remaining"`)) {
		t.Fatalf("Tools response leaked summary or in-memory parent budget: %s", response)
	}
	if err := boundary.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var nativeRoot bool
	for _, span := range exporter.snapshot() {
		if span.Name() == "invoke_agent search_tools_root" && span.InstrumentationScope().Name == "trpc.agent.go" {
			nativeRoot = true
		}
	}
	if !nativeRoot || !bytes.Contains(logs.Bytes(), []byte(`"event":"search.http.tools.finished"`)) {
		t.Fatal("Tools run lacks native Graph Agent span or structured stage log")
	}
}
