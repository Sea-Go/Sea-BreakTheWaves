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
	if subject.Issuer != rtwIdentityIssuer {
		return btwruntime.SubjectRef{}, false
	}
	uid, err := strconv.ParseInt(subject.SubjectID, 10, 64)
	if err != nil || uid <= 0 || strconv.FormatInt(uid, 10) != subject.SubjectID {
		return btwruntime.SubjectRef{}, false
	}
	return btwruntime.SubjectRef{
		AuthorityID: subject.Issuer,
		TenantID:    legacyPlatformSlot,
		SubjectID:   subject.SubjectID,
	}, true
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
