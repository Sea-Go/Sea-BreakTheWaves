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

	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/search"
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
	v2Body := body
	v2Body.SearchID = "search-2"
	v2BodyBytes, err := json.Marshal(v2Body)
	if err != nil {
		t.Fatal(err)
	}
	v2Hash := sha256.Sum256(v2BodyBytes)
	v2 := signedToolsScopeV2{Audience: toolsScopeAudienceV2,
		Subject:   signedSubjectV2{Issuer: rtwIdentityIssuer, SubjectID: payload.Subject.SubjectID},
		SessionID: "session-2", OperationID: "toolop_124", BudgetRef: "budget_124", SearchID: v2Body.SearchID,
		SnapshotRef: payload.SnapshotRef, Snapshot: payload.Snapshot,
		AllowPartial: payload.AllowPartial, AllowLowerIntelligence: payload.AllowLowerIntelligence,
		RequestHash: hex.EncodeToString(v2Hash[:]), IssuedAtUnix: payload.IssuedAtUnix,
		ExpiresAtUnix: payload.ExpiresAtUnix}
	v2Raw, err := json.Marshal(v2)
	if err != nil {
		t.Fatal(err)
	}
	v2Header := signedHeader(key, v2Raw)
	v1Raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	wrongV2Body := v2Body
	wrongV2Body.Query = "changed"
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
	tests := []struct {
		name    string
		body    ToolsRequest
		headers []string
	}{
		{"missing", body, nil}, {"duplicate", body, []string{validHeader, validHeader}},
		{"bad signature", body, []string{badSig}}, {"bad encoding", body, []string{validHeader + "="}},
		{"bad audience", body, []string{signed(badAudience)}},
		{"v1 wrong issuer", body, []string{func() string { p := payload; p.Subject.AuthorityID = "other.identity"; return signed(p) }()}},
		{"v1 nonplatform slot", body, []string{func() string { p := payload; p.Subject.TenantID = "tenant-a"; return signed(p) }()}},
		{"v1 leading zero UID", body, []string{func() string { p := payload; p.Subject.SubjectID = "0123"; return signed(p) }()}},
		{"v1 nonnumeric UID", body, []string{func() string { p := payload; p.Subject.SubjectID = "not-a-uid"; return signed(p) }()}},
		{"v1 overflow UID", body, []string{func() string { p := payload; p.Subject.SubjectID = "9223372036854775808"; return signed(p) }()}},
		{"bad snapshot hash", body, []string{signed(badSnapshot)}},
		{"bad time", body, []string{signed(badTime)}},
		{"wrong body hash", wrongBody, []string{validHeader}},
		{"missing flag", body, []string{signedHeader(key, missingFlagRaw)}},
		{"unknown nested", body, []string{signed(badNested)}},
		{"v1 wire with v2 audience", body, []string{signedHeader(key, bytes.Replace(v1Raw, []byte(toolsScopeAudience), []byte(toolsScopeAudienceV2), 1))}},
		{"v2 wire with v1 audience", v2Body, []string{signedHeader(key, bytes.Replace(v2Raw, []byte(toolsScopeAudienceV2), []byte(toolsScopeAudience), 1))}},
		{"v2 summary audience", v2Body, []string{signedHeader(key, bytes.Replace(v2Raw, []byte(toolsScopeAudienceV2), []byte(searchScopeAudienceV2), 1))}},
		{"v2 extra tenant", v2Body, []string{signedHeader(key, bytes.Replace(v2Raw, []byte(`"issuer":`), []byte(`"tenant_id":"platform","issuer":`), 1))}},
		{"v2 extra authority", v2Body, []string{signedHeader(key, bytes.Replace(v2Raw, []byte(`"issuer":`), []byte(`"authority_id":"rtw.identity","issuer":`), 1))}},
		{"v2 extra realm", v2Body, []string{signedHeader(key, bytes.Replace(v2Raw, []byte(`"issuer":`), []byte(`"realm":"platform","issuer":`), 1))}},
		{"v2 duplicate subject", v2Body, []string{signedHeader(key, bytes.Replace(v2Raw, []byte(`"issuer":`), []byte(`"issuer":"rtw.identity","issuer":`), 1))}},
		{"v2 duplicate header", v2Body, []string{v2Header, v2Header}},
		{"v2 bad encoding", v2Body, []string{v2Header + "="}},
		{"v2 bad signature", v2Body, []string{func() string {
			last := "A"
			if strings.HasSuffix(v2Header, "A") {
				last = "B"
			}
			return v2Header[:len(v2Header)-1] + last
		}()}},
		{"v2 missing flag", v2Body, []string{signedHeader(key, bytes.Replace(v2Raw, []byte(`"allow_partial":false,`), nil, 1))}},
		{"v2 missing subject field", v2Body, []string{signedHeader(key, bytes.Replace(v2Raw, []byte(`,"subject_id":"123"`), nil, 1))}},
		{"v2 unknown field", v2Body, []string{signedHeader(key, bytes.Replace(v2Raw, []byte(`"aud":`), []byte(`"unknown":true,"aud":`), 1))}},
		{"v2 null subject", v2Body, []string{signedHeader(key, bytes.Replace(v2Raw, []byte(`"subject_ref":{"issuer":"rtw.identity","subject_id":"123"}`), []byte(`"subject_ref":null`), 1))}},
		{"v2 trailing JSON", v2Body, []string{signedHeader(key, append(append([]byte(nil), v2Raw...), []byte(`{}`)...))}},
		{"v2 reordered bytes", v2Body, []string{signedHeader(key, bytes.Replace(v2Raw, []byte(`"issuer":"rtw.identity","subject_id":"123"`), []byte(`"subject_id":"123","issuer":"rtw.identity"`), 1))}},
		{"v2 wrong issuer", v2Body, []string{func() string {
			p := v2
			p.Subject.Issuer = "other.identity"
			raw, _ := json.Marshal(p)
			return signedHeader(key, raw)
		}()}},
		{"v2 overflow UID", v2Body, []string{func() string {
			p := v2
			p.Subject.SubjectID = "9223372036854775808"
			raw, _ := json.Marshal(p)
			return signedHeader(key, raw)
		}()}},
		{"v2 wrong body hash", wrongV2Body, []string{v2Header}},
		{"v2 wrong snapshot", v2Body, []string{func() string {
			p := v2
			p.SnapshotRef = "snapshot_" + strings.Repeat("0", 64)
			raw, _ := json.Marshal(p)
			return signedHeader(key, raw)
		}()}},
		{"v2 future issued", v2Body, []string{func() string {
			p := v2
			p.IssuedAtUnix += 31
			raw, _ := json.Marshal(p)
			return signedHeader(key, raw)
		}()}},
		{"v2 excessive TTL", v2Body, []string{func() string {
			p := v2
			p.ExpiresAtUnix = now.Add(121 * time.Second).Unix()
			raw, _ := json.Marshal(p)
			return signedHeader(key, raw)
		}()}},
		{"v2 expired", v2Body, []string{func() string {
			p := v2
			p.ExpiresAtUnix = now.Unix()
			raw, _ := json.Marshal(p)
			return signedHeader(key, raw)
		}()}},
	}
	for _, tc := range tests {
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
	v2Request := httptest.NewRequest(http.MethodPost, ToolsRoute, bytes.NewReader(v2BodyBytes))
	v2Request.Header.Set(toolsScopeHeader, v2Header)
	trustedV2, err := resolver.ResolveTools(context.Background(), v2Request, v2Body)
	legacy := btwruntime.SubjectRef{AuthorityID: rtwIdentityIssuer, TenantID: legacyPlatformSlot, SubjectID: v2.Subject.SubjectID}
	if err != nil || trustedV2.Subject != legacy || trustedV2.SnapshotRef != v2.SnapshotRef {
		t.Fatalf("v2 Tools scope selected a different Session identity: %+v %v", trustedV2, err)
	}
	v2Status, v2Response := send(v2Body, v2Header)
	var v2Result ToolsResponse
	if err := json.Unmarshal(v2Response, &v2Result); err != nil || v2Status != http.StatusOK ||
		v2Result.SearchID != v2Body.SearchID || v2Result.Status != "complete" || sourceReads.Load() != 2 {
		t.Fatalf("valid v2 Tools scope did not complete native Graph: status=%d result=%+v source=%d err=%v", v2Status, v2Result, sourceReads.Load(), err)
	}
	if err := boundary.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var nativeRoot, completed, denied int
	for _, span := range exporter.snapshot() {
		if span.Name() == "invoke_agent search_tools_root" && span.InstrumentationScope().Name == "trpc.agent.go" {
			nativeRoot++
		}
	}
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte{'\n'}) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("Tools terminal log is not JSON: %q", line)
		}
		if record["event"] != "search.http.tools.finished" {
			continue
		}
		if record["outcome"] == "succeeded" {
			completed++
		} else if record["outcome"] == "rejected" && record["error_code"] == "SEARCH_SCOPE_DENIED" {
			denied++
		}
	}
	if nativeRoot != 2 || completed != 2 || denied < len(tests) {
		t.Fatalf("Tools v1/v2 native or JSON terminal evidence drift: native=%d completed=%d denied=%d", nativeRoot, completed, denied)
	}
}
