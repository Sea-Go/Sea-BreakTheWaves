package app

import (
	"encoding/base64"
	"errors"
	"regexp"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
)

var ErrWikiCompileModelIdentity = errors.New("wiki compile model session differs from frozen DC native account")

var wikiModelAccountID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// WikiCompileModelSession is frozen once by the authoritative cmd assembly.
// Its bearer is private and never serialized with the Wiki job or Agent state.
// A token rotation requires a new worker instance for the same DC account.
type WikiCompileModelSession struct {
	AccountID string
	bearer    string
	digest    string
}

type WikiCompileModelSessionProof struct {
	AccountID   string
	TokenDigest string
}

func NewWikiCompileModelSession(accountID, nativeBearer string) (WikiCompileModelSession, error) {
	if !wikiModelAccountID.MatchString(accountID) ||
		!strings.HasPrefix(nativeBearer, "wh_access_") ||
		strings.TrimSpace(nativeBearer) != nativeBearer ||
		strings.ContainsAny(nativeBearer, "\r\n") {
		return WikiCompileModelSession{}, ErrWikiCompileModelIdentity
	}
	suffix := strings.TrimPrefix(nativeBearer, "wh_access_")
	decoded, err := base64.RawURLEncoding.DecodeString(suffix)
	if err != nil || len(decoded) != 32 ||
		base64.RawURLEncoding.EncodeToString(decoded) != suffix {
		return WikiCompileModelSession{}, ErrWikiCompileModelIdentity
	}
	return WikiCompileModelSession{AccountID: accountID, bearer: nativeBearer,
		digest: artifacts.Hash([]byte(nativeBearer))}, nil
}

func (s WikiCompileModelSession) NativeBearer() string { return s.bearer }
func (s WikiCompileModelSession) Proof() WikiCompileModelSessionProof {
	return WikiCompileModelSessionProof{AccountID: s.AccountID, TokenDigest: s.digest}
}

func (s WikiCompileModelSession) valid() bool {
	if !wikiModelAccountID.MatchString(s.AccountID) ||
		!artifacts.ValidHash(s.digest) || s.digest != artifacts.Hash([]byte(s.bearer)) {
		return false
	}
	_, err := NewWikiCompileModelSession(s.AccountID, s.bearer)
	return err == nil
}
