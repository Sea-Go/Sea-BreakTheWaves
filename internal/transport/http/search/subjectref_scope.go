package search

import (
	"encoding/json"
	"strconv"

	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
)

const (
	searchScopeAudienceV2 = "btw.search.summary.v2"
	toolsScopeAudienceV2  = "btw.search.tools.v2"
	legacyPlatformSlot    = "platform"
	rtwIdentityIssuer     = "rtw.identity"
)

// signedSubjectV2 is only the new signed wire shape. The Runner and Session
// continue using their v1 user key during this consumer-first migration.
type signedSubjectV2 struct {
	Issuer    string `json:"issuer"`
	SubjectID string `json:"subject_id"`
}

func (subject signedSubjectV2) legacySubject() (btwruntime.SubjectRef, bool) {
	legacy := btwruntime.SubjectRef{
		AuthorityID: subject.Issuer,
		TenantID:    legacyPlatformSlot,
		SubjectID:   subject.SubjectID,
	}
	if !validLegacySubject(legacy) {
		return btwruntime.SubjectRef{}, false
	}
	return legacy, true
}

// The v1 platform field is a fixed compatibility slot, not an organization.
// Checking it after HMAC validation preserves the exact signed v1 bytes.
func validLegacySubject(subject btwruntime.SubjectRef) bool {
	if subject.AuthorityID != rtwIdentityIssuer || subject.TenantID != legacyPlatformSlot {
		return false
	}
	uid, err := strconv.ParseInt(subject.SubjectID, 10, 64)
	return err == nil && uid > 0 && strconv.FormatInt(uid, 10) == subject.SubjectID
}

func signedScopeAudience(payload []byte) string {
	var value struct {
		Audience string `json:"aud"`
	}
	if json.Unmarshal(payload, &value) != nil {
		return ""
	}
	return value.Audience
}
