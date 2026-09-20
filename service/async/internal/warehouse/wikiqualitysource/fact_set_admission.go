package wikiqualitysource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
)

var ErrFactSetUnconfigured = errors.New("warehouse fact-set original authority or verifier unconfigured")

// FactSetAuthority belongs to RTW's private immutable Event read surface.
// DataCenter is separately responsible for its event InputHash and offset.
type FactSetAuthority interface {
	ReadFactSetEvent(context.Context, string) (FactSetEventProof, error)
}

type FactSetVerifier interface {
	VerifyFactSetEvent(context.Context, eventing.Event, FactSetEventProof) error
}

// FactSetV1Verifier checks the fixed frozen payload. RTW's private GET
// rechecks historical Source/Wiki bytes and quote spans. Source eligibility
// was checked at freeze time; historical reads do not requalify withdrawal.
type FactSetV1Verifier struct{}

func (FactSetV1Verifier) VerifyFactSetEvent(ctx context.Context, event eventing.Event,
	proof FactSetEventProof) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrContract
	}
	_, err := ParseFactSetV1(event, proof.EventJSON, proof.FactSetJCSSHA256)
	return err
}

// verifyFactSetDC proves that the original RTW Event, the DC Event copy and
// its technical input hash denote the same whole JCS bytes. FactSet payload
// JCS is a third domain and cannot substitute for DC's whole-event InputHash.
func verifyFactSetDC(event eventing.Event, proof FactSetEventProof, inputHash string) error {
	if !factSetEvent(event.EventType) || !shaPattern.MatchString(inputHash) ||
		!shaPattern.MatchString(proof.EventRawSHA256) ||
		!shaPattern.MatchString(proof.EventJCSSHA256) ||
		!shaPattern.MatchString(proof.FactSetJCSSHA256) ||
		digest(proof.EventJSON) != proof.EventRawSHA256 {
		return ErrContract
	}
	sourceJCS, err := canonical(proof.EventJSON)
	if err != nil || digest(sourceJCS) != proof.EventJCSSHA256 ||
		proof.EventJCSSHA256 != inputHash {
		return ErrContract
	}
	dcRaw, err := json.Marshal(event)
	if err != nil {
		return ErrContract
	}
	dcJCS, err := canonical(dcRaw)
	if err != nil || !bytes.Equal(sourceJCS, dcJCS) {
		return ErrContract
	}
	if _, err := ParseFactSetV1(event, proof.EventJSON, proof.FactSetJCSSHA256); err != nil {
		return err
	}
	return nil
}
