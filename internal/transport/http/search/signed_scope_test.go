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
	bundle, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-search-signed-scope-fixture",
		Environment: "test", Version: "fixture-sha", InstanceID: "fixture", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: &traceSink{}})
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
		{"future issued", public, []string{malformed(func(p signedScope) signedScope { p.IssuedAtUnix += 31; return p })}},
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
