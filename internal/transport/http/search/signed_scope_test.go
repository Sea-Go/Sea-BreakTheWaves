package search

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"math"
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

func signedHeader(key, payload []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func signedPayload(t *testing.T, key []byte, payload signedScope) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return signedHeader(key, raw)
}

func signedPayloadV2(t *testing.T, key []byte, payload signedScopeV2) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return signedHeader(key, raw)
}

func TestSignedScopeHTTPRejectsBeforeSourceAndAgent(t *testing.T) {
	if os.Getenv("SEARCH_SIGNED_SCOPE_CHILD") != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestSignedScopeHTTPRejectsBeforeSourceAndAgent$")
		cmd.Env = append(os.Environ(), "SEARCH_SIGNED_SCOPE_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("signed scope HTTP subprocess: %v\n%s", err, output)
		}
		return
	}
	key := bytes.Repeat([]byte("s"), 32)
	resolver, err := NewSignedScopeResolver(key)
	if err != nil {
		t.Fatal(err)
	}
	fixedNow := time.Unix(1_700_000_000, 0)
	resolver.now = func() time.Time { return fixedNow }
	var logs bytes.Buffer
	exporter := &traceSink{}
	bundle, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-search-signed-scope-fixture",
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
	model := &fixtureModel{accepted: &accepted}
	history := &acceptedHistory{}
	boundary, err := searchdomain.NewRootSessionBoundary(fixedDelivery(t, &accepted, fixedSnapshot(), &sourceReads), model, history, bundle)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(resolver, boundary, bundle)
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

	public := PublicRequest{ModuleID: "module-1", Query: "why", Depth: searchdomain.Fast, Intelligence: searchdomain.Low}
	requestBytes, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	requestHash := sha256.Sum256(requestBytes)
	payload := signedScope{Audience: searchScopeAudience,
		Subject:   btwruntime.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "123"},
		SessionID: "conversation-1", SearchID: "search-1", AnswerID: "answer-1",
		Snapshot: fixedSnapshot(), RequestHash: hex.EncodeToString(requestHash[:]),
		IssuedAtUnix: fixedNow.Unix(), ExpiresAtUnix: fixedNow.Add(120 * time.Second).Unix()}
	goodHeader := signedPayload(t, key, payload)
	v2 := signedScopeV2{Audience: searchScopeAudienceV2,
		Subject:   signedSubjectV2{Issuer: rtwIdentityIssuer, SubjectID: payload.Subject.SubjectID},
		SessionID: "conversation-2", SearchID: "search-2", AnswerID: "answer-2",
		Snapshot: payload.Snapshot, AllowPartial: payload.AllowPartial,
		AllowLowerIntelligence: payload.AllowLowerIntelligence, RequestHash: payload.RequestHash,
		IssuedAtUnix: payload.IssuedAtUnix, ExpiresAtUnix: payload.ExpiresAtUnix}
	goodV2 := signedPayloadV2(t, key, v2)
	send := func(body PublicRequest, headers ...string) *http.Response {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r, err := http.NewRequest(http.MethodPost, server.URL+Route, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		for _, header := range headers {
			r.Header.Add(searchScopeHeader, header)
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	malformed := func(replace func(signedScope) signedScope) string {
		return signedPayload(t, key, replace(payload))
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	rawV2, err := json.Marshal(v2)
	if err != nil {
		t.Fatal(err)
	}
	badSignature := goodHeader[:len(goodHeader)-1] + "A"
	if strings.HasSuffix(goodHeader, "A") {
		badSignature = goodHeader[:len(goodHeader)-1] + "B"
	}
	tests := []struct {
		name    string
		body    PublicRequest
		headers []string
	}{
		{"missing header", public, nil},
		{"duplicate header", public, []string{goodHeader, goodHeader}},
		{"bad signature", public, []string{badSignature}},
		{"bad encoding", public, []string{goodHeader + "="}},
		{"wrong audience", public, []string{malformed(func(p signedScope) signedScope { p.Audience = "other"; return p })}},
		{"missing subject", public, []string{malformed(func(p signedScope) signedScope { p.Subject.SubjectID = ""; return p })}},
		{"v1 wrong issuer", public, []string{malformed(func(p signedScope) signedScope { p.Subject.AuthorityID = "other.identity"; return p })}},
		{"v1 nonplatform slot", public, []string{malformed(func(p signedScope) signedScope { p.Subject.TenantID = "tenant-a"; return p })}},
		{"v1 leading zero UID", public, []string{malformed(func(p signedScope) signedScope { p.Subject.SubjectID = "0123"; return p })}},
		{"v1 nonnumeric UID", public, []string{malformed(func(p signedScope) signedScope { p.Subject.SubjectID = "not-a-uid"; return p })}},
		{"v1 overflow UID", public, []string{malformed(func(p signedScope) signedScope { p.Subject.SubjectID = "9223372036854775808"; return p })}},
		{"future issued", public, []string{malformed(func(p signedScope) signedScope { p.IssuedAtUnix += 31; return p })}},
		{"ancient issued overflow", public, []string{malformed(func(p signedScope) signedScope {
			p.IssuedAtUnix = math.MinInt64
			p.ExpiresAtUnix = math.MaxInt64
			return p
		})}},
		{"expired", public, []string{malformed(func(p signedScope) signedScope { p.ExpiresAtUnix = fixedNow.Unix(); return p })}},
		{"ttl too long", public, []string{malformed(func(p signedScope) signedScope { p.ExpiresAtUnix += 181; return p })}},
		{"wrong module", public, []string{malformed(func(p signedScope) signedScope { p.Snapshot.ModuleID = "another"; return p })}},
		{"wrong query", PublicRequest{ModuleID: "module-1", Query: "changed", Depth: searchdomain.Fast, Intelligence: searchdomain.Low}, []string{goodHeader}},
		{"wrong depth", PublicRequest{ModuleID: "module-1", Query: "why", Depth: searchdomain.Detailed, Intelligence: searchdomain.Low}, []string{goodHeader}},
		{"wrong intelligence", PublicRequest{ModuleID: "module-1", Query: "why", Depth: searchdomain.Fast, Intelligence: searchdomain.High}, []string{goodHeader}},
		{"unknown field", public, []string{signedHeader(key, bytes.Replace(rawPayload, []byte(`"aud":`), []byte(`"unknown":true,"aud":`), 1))}},
		{"unknown nested field", public, []string{signedHeader(key, bytes.Replace(rawPayload, []byte(`"authority_id":`), []byte(`"unknown":true,"authority_id":`), 1))}},
		{"missing flag", public, []string{signedHeader(key, bytes.Replace(rawPayload, []byte(`"allow_partial":false,`), nil, 1))}},
		{"duplicate payload key", public, []string{signedHeader(key, bytes.Replace(rawPayload, []byte(`"aud":`), []byte(`"aud":"btw.search.summary.v1","aud":`), 1))}},
		{"v1 wire with v2 audience", public, []string{signedHeader(key, bytes.Replace(rawPayload, []byte(searchScopeAudience), []byte(searchScopeAudienceV2), 1))}},
		{"v2 wire with v1 audience", public, []string{signedHeader(key, bytes.Replace(rawV2, []byte(searchScopeAudienceV2), []byte(searchScopeAudience), 1))}},
		{"v2 tools audience", public, []string{signedHeader(key, bytes.Replace(rawV2, []byte(searchScopeAudienceV2), []byte(toolsScopeAudienceV2), 1))}},
		{"v2 extra tenant", public, []string{signedHeader(key, bytes.Replace(rawV2, []byte(`"issuer":`), []byte(`"tenant_id":"platform","issuer":`), 1))}},
		{"v2 extra authority", public, []string{signedHeader(key, bytes.Replace(rawV2, []byte(`"issuer":`), []byte(`"authority_id":"rtw.identity","issuer":`), 1))}},
		{"v2 extra realm", public, []string{signedHeader(key, bytes.Replace(rawV2, []byte(`"issuer":`), []byte(`"realm":"platform","issuer":`), 1))}},
		{"v2 duplicate subject", public, []string{signedHeader(key, bytes.Replace(rawV2, []byte(`"issuer":`), []byte(`"issuer":"rtw.identity","issuer":`), 1))}},
		{"v2 duplicate header", public, []string{goodV2, goodV2}},
		{"v2 bad encoding", public, []string{goodV2 + "="}},
		{"v2 bad signature", public, []string{func() string {
			last := "A"
			if strings.HasSuffix(goodV2, "A") {
				last = "B"
			}
			return goodV2[:len(goodV2)-1] + last
		}()}},
		{"v2 unknown field", public, []string{signedHeader(key, bytes.Replace(rawV2, []byte(`"aud":`), []byte(`"unknown":true,"aud":`), 1))}},
		{"v2 missing flag", public, []string{signedHeader(key, bytes.Replace(rawV2, []byte(`"allow_partial":false,`), nil, 1))}},
		{"v2 missing subject field", public, []string{signedHeader(key, bytes.Replace(rawV2, []byte(`,"subject_id":"123"`), nil, 1))}},
		{"v2 null subject", public, []string{signedHeader(key, bytes.Replace(rawV2, []byte(`"subject_ref":{"issuer":"rtw.identity","subject_id":"123"}`), []byte(`"subject_ref":null`), 1))}},
		{"v2 trailing JSON", public, []string{signedHeader(key, append(append([]byte(nil), rawV2...), []byte(`{}`)...))}},
		{"v2 reordered bytes", public, []string{signedHeader(key, bytes.Replace(rawV2, []byte(`"issuer":"rtw.identity","subject_id":"123"`), []byte(`"subject_id":"123","issuer":"rtw.identity"`), 1))}},
		{"v2 wrong issuer", public, []string{signedPayloadV2(t, key, func() signedScopeV2 { p := v2; p.Subject.Issuer = "other.identity"; return p }())}},
		{"v2 missing UID", public, []string{signedPayloadV2(t, key, func() signedScopeV2 { p := v2; p.Subject.SubjectID = ""; return p }())}},
		{"v2 zero UID", public, []string{signedPayloadV2(t, key, func() signedScopeV2 { p := v2; p.Subject.SubjectID = "0"; return p }())}},
		{"v2 negative UID", public, []string{signedPayloadV2(t, key, func() signedScopeV2 { p := v2; p.Subject.SubjectID = "-1"; return p }())}},
		{"v2 leading zero UID", public, []string{signedPayloadV2(t, key, func() signedScopeV2 { p := v2; p.Subject.SubjectID = "0123"; return p }())}},
		{"v2 overflow UID", public, []string{signedPayloadV2(t, key, func() signedScopeV2 { p := v2; p.Subject.SubjectID = "9223372036854775808"; return p }())}},
		{"v2 wrong request hash", PublicRequest{ModuleID: "module-1", Query: "changed", Depth: searchdomain.Fast, Intelligence: searchdomain.Low}, []string{goodV2}},
		{"v2 wrong module", public, []string{signedPayloadV2(t, key, func() signedScopeV2 { p := v2; p.Snapshot.ModuleID = "another"; return p }())}},
		{"v2 future issued", public, []string{signedPayloadV2(t, key, func() signedScopeV2 { p := v2; p.IssuedAtUnix += 31; return p }())}},
		{"v2 excessive TTL", public, []string{signedPayloadV2(t, key, func() signedScopeV2 { p := v2; p.ExpiresAtUnix += 181; return p }())}},
		{"v2 expired", public, []string{signedPayloadV2(t, key, func() signedScopeV2 { p := v2; p.ExpiresAtUnix = fixedNow.Unix(); return p }())}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			response := send(tc.body, tc.headers...)
			defer response.Body.Close()
			if response.StatusCode != http.StatusForbidden || sourceReads.Load() != 0 || model.calls.Load() != 0 || len(history.turns) != 0 {
				t.Fatalf("scope rejection touched source/Agent/history: status=%d source=%d model=%d history=%d", response.StatusCode, sourceReads.Load(), model.calls.Load(), len(history.turns))
			}
		})
	}
	response := send(public, goodHeader)
	defer response.Body.Close()
	var result Response
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || result.SearchID != payload.SearchID || result.AnswerID != payload.AnswerID ||
		result.Status != "succeeded" || result.ReceiptRef == "" || len(result.Citations) != 1 ||
		sourceReads.Load() != 1 || model.calls.Load() != 1 || len(history.turns) != 1 {
		t.Fatalf("valid signed scope did not pass root boundary: status=%d result=%+v source=%d model=%d history=%d", response.StatusCode, result, sourceReads.Load(), model.calls.Load(), len(history.turns))
	}
	v2Response := send(public, goodV2)
	defer v2Response.Body.Close()
	var v2Result Response
	if err := json.NewDecoder(v2Response.Body).Decode(&v2Result); err != nil {
		t.Fatal(err)
	}
	legacy := btwruntime.SubjectRef{AuthorityID: rtwIdentityIssuer, TenantID: legacyPlatformSlot, SubjectID: v2.Subject.SubjectID}
	if v2Response.StatusCode != http.StatusOK || v2Result.SearchID != v2.SearchID || v2Result.AnswerID != v2.AnswerID ||
		sourceReads.Load() != 2 || model.calls.Load() != 2 || len(history.turns) != 2 ||
		history.turns[1].Request.Subject != legacy {
		t.Fatalf("v2 scope did not preserve legacy Runner subject: status=%d result=%+v source=%d model=%d history=%+v", v2Response.StatusCode,
			v2Result, sourceReads.Load(), model.calls.Load(), history.turns)
	}
	v1Key, v1Err := history.turns[0].Request.Subject.UserKey()
	v2Key, v2Err := history.turns[1].Request.Subject.UserKey()
	if v1Err != nil || v2Err != nil || v1Key != v2Key {
		t.Fatal("v2 summary scope changed the existing Runner user key")
	}
	if err := boundary.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var nativeRuns, completed, denied int
	for _, span := range exporter.snapshot() {
		if span.Name() == "invoke_agent search_summary_root" && span.InstrumentationScope().Name == "trpc.agent.go" {
			nativeRuns++
		}
	}
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte{'\n'}) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("summary terminal log is not JSON: %q", line)
		}
		if record["event"] != "search.http.summary.finished" {
			continue
		}
		if record["outcome"] == "succeeded" {
			completed++
		} else if record["outcome"] == "rejected" && record["error_code"] == "SEARCH_SCOPE_DENIED" {
			denied++
		}
	}
	if nativeRuns != 2 || completed != 2 || denied < len(tests) {
		t.Fatalf("summary v1/v2 native or JSON terminal evidence drift: native=%d completed=%d denied=%d", nativeRuns, completed, denied)
	}
}

func TestSignedScopeResolverRejectsShortKeyAndCopiesKey(t *testing.T) {
	if _, err := NewSignedScopeResolver([]byte("short")); err == nil {
		t.Fatal("short signing key accepted")
	}
	key := bytes.Repeat([]byte("s"), 32)
	resolver, err := NewSignedScopeResolver(key)
	if err != nil {
		t.Fatal(err)
	}
	key[0] = 'x'
	if resolver.key[0] != 's' {
		t.Fatal("resolver retained mutable caller key")
	}
}
