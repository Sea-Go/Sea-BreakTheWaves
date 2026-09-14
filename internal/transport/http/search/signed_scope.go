package search

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
)

const (
	searchScopeHeader        = "X-Sea-Search-Scope"
	searchScopeAudience      = "btw.search.summary.v1"
	maxScopeTTLSeconds       = 300
	maxScopeClockSkewSeconds = 30
)

// signedScope is the RTW H02 payload wire format. Do not reorder or omit its
// JSON fields: the signer HMACs the exact bytes produced by json.Marshal.
type signedScope struct {
	Audience               string                `json:"aud"`
	Subject                btwruntime.SubjectRef `json:"subject_ref"`
	SessionID              string                `json:"session_id"`
	SearchID               string                `json:"search_id"`
	AnswerID               string                `json:"answer_id"`
	Snapshot               searchdomain.Snapshot `json:"snapshot"`
	AllowPartial           bool                  `json:"allow_partial"`
	AllowLowerIntelligence bool                  `json:"allow_lower_intelligence"`
	RequestHash            string                `json:"request_hash"`
	IssuedAtUnix           int64                 `json:"issued_at_unix"`
	ExpiresAtUnix          int64                 `json:"expires_at_unix"`
}

// SignedScopeResolver accepts only a single RTW-issued HMAC scope header. Its
// key is copied at construction; callers own key loading and rotation.
type SignedScopeResolver struct {
	key []byte
	now func() time.Time
}

func NewSignedScopeResolver(key []byte) (*SignedScopeResolver, error) {
	if len(key) < 32 {
		return nil, ErrInvalidRequest
	}
	return &SignedScopeResolver{key: append([]byte(nil), key...), now: time.Now}, nil
}

func (s *SignedScopeResolver) ResolveSearch(_ context.Context, r *http.Request, request PublicRequest) (TrustedScope, error) {
	if s == nil || len(s.key) < 32 || s.now == nil || r == nil {
		return TrustedScope{}, ErrScopeUnavailable
	}
	values := r.Header.Values(searchScopeHeader)
	if len(values) != 1 {
		return TrustedScope{}, ErrScopeDenied
	}
	parts := strings.Split(values[0], ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return TrustedScope{}, ErrScopeDenied
	}
	encoded := base64.RawURLEncoding
	payloadBytes, err := encoded.DecodeString(parts[0])
	if err != nil || encoded.EncodeToString(payloadBytes) != parts[0] {
		return TrustedScope{}, ErrScopeDenied
	}
	signature, err := encoded.DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size || encoded.EncodeToString(signature) != parts[1] {
		return TrustedScope{}, ErrScopeDenied
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write(payloadBytes)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return TrustedScope{}, ErrScopeDenied
	}
	var payload signedScope
	decoder := json.NewDecoder(bytes.NewReader(payloadBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(new(any)) != io.EOF {
		return TrustedScope{}, ErrScopeDenied
	}
	canonical, err := json.Marshal(payload)
	if err != nil || !bytes.Equal(canonical, payloadBytes) {
		// Also rejects omitted fields, duplicate keys and noncanonical JSON.
		return TrustedScope{}, ErrScopeDenied
	}
	now := s.now().Unix()
	if payload.Audience != searchScopeAudience || payload.IssuedAtUnix > now+maxScopeClockSkewSeconds ||
		payload.IssuedAtUnix < now-maxScopeTTLSeconds ||
		payload.ExpiresAtUnix <= now || payload.ExpiresAtUnix <= payload.IssuedAtUnix ||
		payload.ExpiresAtUnix-payload.IssuedAtUnix > maxScopeTTLSeconds {
		return TrustedScope{}, ErrScopeDenied
	}
	if _, err := payload.Subject.UserKey(); err != nil ||
		!constantTimeStringEqual(payload.Snapshot.ModuleID, request.ModuleID) {
		return TrustedScope{}, ErrScopeDenied
	}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		return TrustedScope{}, ErrScopeDenied
	}
	wantHash := sha256.Sum256(requestBytes)
	gotHash, err := hex.DecodeString(payload.RequestHash)
	if err != nil || len(gotHash) != sha256.Size ||
		hex.EncodeToString(gotHash) != payload.RequestHash ||
		subtle.ConstantTimeCompare(gotHash, wantHash[:]) != 1 {
		return TrustedScope{}, ErrScopeDenied
	}
	scope := TrustedScope{Subject: payload.Subject, SessionID: payload.SessionID,
		SearchID: payload.SearchID, AnswerID: payload.AnswerID,
		Snapshot: payload.Snapshot, AllowPartial: payload.AllowPartial,
		AllowLowerIntelligence: payload.AllowLowerIntelligence}
	if validScope(request.ModuleID, scope) != nil {
		return TrustedScope{}, ErrScopeDenied
	}
	return scope, nil
}

func constantTimeStringEqual(a, b string) bool {
	aHash, bHash := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(aHash[:], bHash[:]) == 1
}
