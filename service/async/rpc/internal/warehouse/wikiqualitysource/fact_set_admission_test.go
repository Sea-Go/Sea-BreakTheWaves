package wikiqualitysource

import (
	"errors"
	"strings"
	"testing"
)

func TestRTWFactSetSourceProofMustEqualDCWholeEventNotPayloadHash(t *testing.T) {
	event, original := staticFactSetGolden(t)
	proof := FactSetEventProof{EventJSON: original, EventRawSHA256: factSetGoldenRawSHA,
		EventJCSSHA256: factSetGoldenEventJCS, FactSetJCSSHA256: factSetGoldenPayloadJCS}
	if err := verifyFactSetDC(event, proof, factSetGoldenEventJCS); err != nil {
		t.Fatalf("fixed RTW original and DC whole Event JCS disagreed: %v", err)
	}
	value, parsedErr := ParseFactSetV1(event, original, proof.FactSetJCSSHA256)
	if err := (FactSetV1Verifier{}).VerifyFactSetEvent(t.Context(), event, proof); err != nil ||
		parsedErr != nil ||
		value.SourceScopeRevision == "" || !value.FactsComplete {
		t.Fatalf("fact-set verifier rejected real source fields: %+v %v %v", value, err, parsedErr)
	}
	if !errors.Is(verifyFactSetDC(event, proof, factSetGoldenPayloadJCS), ErrContract) {
		t.Fatal("FactSet payload SHA impersonated DC whole Event InputHash")
	}
	for _, modified := range []FactSetEventProof{
		{EventJSON: original, EventRawSHA256: strings.Repeat("0", 64),
			EventJCSSHA256: factSetGoldenEventJCS, FactSetJCSSHA256: factSetGoldenPayloadJCS},
		{EventJSON: original, EventRawSHA256: factSetGoldenRawSHA,
			EventJCSSHA256: strings.Repeat("0", 64), FactSetJCSSHA256: factSetGoldenPayloadJCS},
		{EventJSON: original, EventRawSHA256: factSetGoldenRawSHA,
			EventJCSSHA256: factSetGoldenEventJCS, FactSetJCSSHA256: strings.Repeat("0", 64)},
	} {
		if !errors.Is(verifyFactSetDC(event, modified, factSetGoldenEventJCS), ErrContract) {
			t.Fatal("forged RTW original/raw/JCS/payload proof reached DC ODS")
		}
	}
}
