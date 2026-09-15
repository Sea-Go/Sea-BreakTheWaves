package wikiqualitysource

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
)

const factSetGoldenFileSHA = "994e66750ed6ff060ab99f4850400da12fc173db23b6db0be5de5cc222d9586b"
const factSetGoldenRawSHA = "44050320a905bf0d4dc6bb3a6a8affe36314257577cb972a340023ec8b4136d8"
const factSetGoldenEventJCS = "661394315e235904139dea20e9b92f8071cfeb2f9c26ea1e06d4cbb14a3e5b1b"
const factSetGoldenPayloadJCS = "3be3a92f9307419911cccf2c72986b6f6af264b9a28537159da461aab0c8ef1a"

func staticFactSetGolden(t *testing.T) (eventing.Event, []byte) {
	t.Helper()
	file, err := os.ReadFile("testdata/wiki-fact-set-event-v1.json")
	if err != nil || digest(file) != factSetGoldenFileSHA || !bytes.HasSuffix(file, []byte("\n")) {
		t.Fatalf("RTW fixed FactSet v1 Golden file differs: %v", err)
	}
	original := bytes.TrimSuffix(file, []byte("\n"))
	wholeJCS, err := canonical(original)
	if err != nil || digest(original) != factSetGoldenRawSHA ||
		digest(wholeJCS) != factSetGoldenEventJCS {
		t.Fatalf("RTW provisional raw Event/JCS SHA domains were confused: %v", err)
	}
	var event eventing.Event
	if err := json.Unmarshal(original, &event); err != nil {
		t.Fatal(err)
	}
	payloadJCS, err := canonical(event.Payload)
	if err != nil || digest(payloadJCS) != factSetGoldenPayloadJCS {
		t.Fatalf("RTW FactSet payload SHA is not whole Event JCS SHA: %v", err)
	}
	return event, original
}

func TestRTWFixedFactSetV1GoldenSeparatesSourceScopeAndThreeSHA(t *testing.T) {
	event, original := staticFactSetGolden(t)
	value, err := ParseFactSetV1(event, original, factSetGoldenPayloadJCS)
	if err != nil || value.FactSetRevision != "1" || value.BaseFactSetRevisionID != "" ||
		value.ActorID != "test-admin" || value.WikiOriginKind != "manual_revision" ||
		value.SourceScopeRevision != "scope_366341033338938e811684949ec8e3b0079d7b7f4d55e4bd5e50c582d6d71a9a" ||
		len(value.SourceRevisions) != 2 || len(value.Facts) != 2 ||
		value.Facts[0].SourceByteStart != "21" || value.Facts[0].SourceByteEnd != "31" ||
		value.Facts[0].SourceQuote != "short fact" || !value.Facts[0].Required ||
		value.Facts[1].ConflictGroup != value.Facts[0].ConflictGroup ||
		!value.FactsComplete {
		t.Fatalf("RTW FactSet declared Scope/FactID/byte span/hash contract lost: %+v %v", value, err)
	}
	if factSetGoldenRawSHA == factSetGoldenEventJCS || factSetGoldenPayloadJCS == factSetGoldenEventJCS {
		t.Fatal("three source-provenance hash domains collapsed")
	}
}

func TestRTWFixedFactSetV1RejectsForgedScopeQuoteAndNestedLiterals(t *testing.T) {
	event, original := staticFactSetGolden(t)
	change := func(modify func(*FactSetV1)) {
		t.Helper()
		var value FactSetV1
		if err := json.Unmarshal(event.Payload, &value); err != nil {
			t.Fatal(err)
		}
		modify(&value)
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		modified := event
		modified.Payload = payload
		encoded := jsonEvent(t, modified)
		canonicalPayload, err := canonical(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseFactSetV1(modified, encoded, digest(canonicalPayload)); !errors.Is(err, ErrContract) {
			t.Fatalf("forged RTW FactSet declared source or Fact label accepted: %v", err)
		}
	}
	change(func(v *FactSetV1) { v.SourceScopeRevision = "scope_" + strings.Repeat("0", 64) })
	change(func(v *FactSetV1) { v.Facts[0].SourceByteStart = "20" })
	change(func(v *FactSetV1) { v.Facts[0].FactID = "fact_" + strings.Repeat("0", 64) })
	change(func(v *FactSetV1) { v.Facts[0].SourceQuoteSHA256 = strings.Repeat("0", 64) })
	change(func(v *FactSetV1) { v.Facts[1].ConflictGroup = "" })
	change(func(v *FactSetV1) { v.Facts[0].Required, v.Facts[1].Required = false, false })
	change(func(v *FactSetV1) { v.FactsComplete = false })
	change(func(v *FactSetV1) { v.SourceRevisions = v.SourceRevisions[:1] })
	if _, err := ParseFactSetV1(event, original, strings.Repeat("0", 64)); !errors.Is(err, ErrContract) {
		t.Fatal("RTW FactSet payload JCS proof disagreed with original Event")
	}
	for _, scenario := range []struct {
		name string
		raw  []byte
	}{
		{"duplicate nested fact", bytes.Replace(original,
			[]byte(`"required":true,"conflict_group":"conflict_1"`),
			[]byte(`"required":true,"required":true,"conflict_group":"conflict_1"`), 1)},
		{"unknown nested tenant", bytes.Replace(original,
			[]byte(`"required":true,"conflict_group":"conflict_1"`),
			[]byte(`"required":true,"tenant_id":"invented","conflict_group":"conflict_1"`), 1)},
	} {
		if bytes.Equal(scenario.raw, original) {
			t.Fatalf("%s fixture did not mutate RTW raw Event", scenario.name)
		}
		var modified eventing.Event
		if err := json.Unmarshal(scenario.raw, &modified); err != nil {
			t.Fatalf("%s fixture invalid JSON: %v", scenario.name, err)
		}
		modifiedPayloadJCS, _ := canonical(modified.Payload)
		if _, err := ParseFactSetV1(modified, scenario.raw, digest(modifiedPayloadJCS)); !errors.Is(err, ErrContract) {
			t.Fatalf("%s source literal was admitted: %v", scenario.name, err)
		}
	}
}

func TestFactSetConsumerModeOnlyAdmitsThePinnedV1Event(t *testing.T) {
	event, _ := staticFactSetGolden(t)
	batch, _, _ := testBatch(t)
	batch.Events[0].Event = event
	canonicalEvent, err := canonical(jsonEvent(t, event))
	if err != nil {
		t.Fatal(err)
	}
	batch.Events[0].InputHash = digest(canonicalEvent)
	resetBatchHash(t, &batch)
	if !errors.Is(validateBatch(batch), ErrContract) ||
		validateBatchWithFactSet(batch, true) != nil {
		t.Fatal("FactSet v1 bypassed default-off or was rejected under configured mode")
	}
	batch.Events[0].Event.EventType = "knowledge.wiki.fact-set.frozen.v2"
	canonicalEvent, err = canonical(jsonEvent(t, batch.Events[0].Event))
	if err != nil {
		t.Fatal(err)
	}
	batch.Events[0].InputHash = digest(canonicalEvent)
	resetBatchHash(t, &batch)
	if !errors.Is(validateBatchWithFactSet(batch, true), ErrContract) {
		t.Fatal("unknown FactSet event version became an accepted quality prefix")
	}
}
