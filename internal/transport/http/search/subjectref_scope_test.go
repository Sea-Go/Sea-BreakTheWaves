package search

import (
	"testing"

	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
)

func TestSignedSubjectV2UsesOnlyTheFixedLegacyRunnerSlot(t *testing.T) {
	for _, uid := range []string{"1", "123", "9223372036854775807"} {
		v2 := signedSubjectV2{Issuer: rtwIdentityIssuer, SubjectID: uid}
		got, ok := v2.legacySubject()
		want := btwruntime.SubjectRef{AuthorityID: rtwIdentityIssuer, TenantID: legacyPlatformSlot, SubjectID: uid}
		if !ok || got != want {
			t.Fatalf("v2 UID %q legacy projection=%+v valid=%t", uid, got, ok)
		}
		gotKey, gotErr := got.UserKey()
		wantKey, wantErr := want.UserKey()
		if gotErr != nil || wantErr != nil || gotKey != wantKey {
			t.Fatalf("v2 UID %q changed the existing Runner Session key", uid)
		}
	}
	for _, uid := range []string{"", "0", "-1", "01", "+1", " 1", "1 ", "1\n", "1\x00", "9223372036854775808", "user-1"} {
		if _, ok := (signedSubjectV2{Issuer: rtwIdentityIssuer, SubjectID: uid}).legacySubject(); ok {
			t.Fatalf("invalid v2 UID %q accepted", uid)
		}
	}
	if _, ok := (signedSubjectV2{Issuer: "other.identity", SubjectID: "123"}).legacySubject(); ok {
		t.Fatal("caller-selected issuer accepted")
	}
}
