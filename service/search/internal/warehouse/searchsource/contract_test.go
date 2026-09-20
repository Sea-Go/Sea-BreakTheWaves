package searchsource

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
)

func fixtureJudgment() Judgment {
	grade := 0 // an explicit human grade 0 is not an unjudged candidate
	return Judgment{
		JudgmentID: "judge-1", RevisionID: "revision-1", JudgmentRevision: 1,
		SearchID: "search-1", QueryText: "coffee grinder", QueryTextSHA256: digest([]byte("coffee grinder")),
		QueryTime: "2026-09-15T00:00:00Z", ModuleID: "module-1", ReleaseID: "release-1",
		Generation: 1, PublicationRevision: "publication-1", RequestSHA256: strings.Repeat("a", 64),
		IndexManifestRef: "index-ref", IndexManifestSHA256: strings.Repeat("b", 64),
		ChunkManifestRef: "chunk-ref", ChunkManifestSHA256: strings.Repeat("c", 64),
		SourceKind: "wiki", ContentID: "content-1", ContentRevisionID: "content-r1",
		ChunkID: "chunk-1", ChunkText: "A published passage about grinders.",
		ChunkTextSHA256: digest([]byte("A published passage about grinders.")),
		OriginalRef:     "original-ref", OriginalSHA256: strings.Repeat("d", 64),
		ContentAvailableAt: "2026-09-14T00:00:00Z", Grade: &grade,
		RubricVersion: "rubric-1", JudgmentSource: "human_judgment", ActorID: "admin-1",
		JudgedAt: "2026-09-15T00:10:00Z", State: "judged", Reason: "explicit review",
	}
}

func fixtureEvent(t *testing.T, judgment Judgment) (eventing.Event, []byte, string, string) {
	t.Helper()
	payload, err := json.Marshal(judgment)
	if err != nil {
		t.Fatal(err)
	}
	event := eventing.Event{EventID: "event-1", EventType: Revised, SchemaVersion: 1,
		Producer: Producer, AggregateID: judgment.ModuleID, AggregateVersion: 1,
		OperationID: "operation-1", OccurredAt: "2026-09-15T00:11:00Z", Payload: payload}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := canonical(raw)
	if err != nil {
		t.Fatal(err)
	}
	return event, raw, digest(raw), digest(encoded)
}

func TestFrozenHumanJudgmentRejectsForgedIdentityAndLabels(t *testing.T) {
	judgment := fixtureJudgment()
	event, raw, rawSHA, inputSHA := fixtureEvent(t, judgment)
	parsed, err := parseJudgment(event, raw, rawSHA, inputSHA)
	if err != nil || parsed.Grade == nil || *parsed.Grade != 0 {
		t.Fatalf("explicit human grade zero rejected: %+v %v", parsed, err)
	}
	if _, err := parseJudgment(event, raw, rawSHA, strings.Repeat("0", 64)); !errors.Is(err, ErrContract) {
		t.Fatalf("DC input differs from RTW authority: %v", err)
	}
	if _, err := parseJudgment(event, raw, strings.Repeat("0", 64), inputSHA); !errors.Is(err, ErrContract) {
		t.Fatalf("RTW original byte hash mismatch accepted: %v", err)
	}
	bad := judgment
	bad.JudgmentSource = "synthetic_fixture"
	event, raw, rawSHA, inputSHA = fixtureEvent(t, bad)
	if _, err := parseJudgment(event, raw, rawSHA, inputSHA); !errors.Is(err, ErrContract) {
		t.Fatalf("synthetic label masqueraded as human: %v", err)
	}
	bad = judgment
	bad.ChunkText = "changed text"
	event, raw, rawSHA, inputSHA = fixtureEvent(t, bad)
	if _, err := parseJudgment(event, raw, rawSHA, inputSHA); !errors.Is(err, ErrContract) {
		t.Fatalf("chunk text/hash mismatch accepted: %v", err)
	}
	bad = judgment
	invalidGrade := 4
	bad.Grade = &invalidGrade
	event, raw, rawSHA, inputSHA = fixtureEvent(t, bad)
	if _, err := parseJudgment(event, raw, rawSHA, inputSHA); !errors.Is(err, ErrContract) {
		t.Fatalf("out-of-rubric grade accepted: %v", err)
	}
	bad = judgment
	bad.State, bad.Grade = "withdrawn", nil
	event, _, _, _ = fixtureEvent(t, bad)
	event.EventType = Withdrawn
	raw, _ = json.Marshal(event)
	rawSHA = digest(raw)
	encoded, _ := canonical(raw)
	inputSHA = digest(encoded)
	if _, err := parseJudgment(event, raw, rawSHA, inputSHA); !errors.Is(err, ErrContract) {
		t.Fatalf("withdrawal without predecessor accepted: %v", err)
	}
}

func TestSharedKnowledgeProducerRequiresEveryOffset(t *testing.T) {
	judgment := fixtureJudgment()
	event, _, _, inputSHA := fixtureEvent(t, judgment)
	other := event
	other.EventID, other.EventType, other.AggregateVersion = "event-2", "knowledge.release.activated.v1", 2
	otherRaw, err := json.Marshal(other)
	if err != nil {
		t.Fatal(err)
	}
	otherCanonical, err := canonical(otherRaw)
	if err != nil {
		t.Fatal(err)
	}
	items := []eventing.Item{{Offset: 1, InputHash: inputSHA, Event: event},
		{Offset: 2, InputHash: digest(otherCanonical), Event: other}}
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := canonical(raw)
	if err != nil {
		t.Fatal(err)
	}
	batch := eventing.Batch{Consumer: DefaultConsumer, Producer: Producer, FromOffset: 1, ToOffset: 2,
		BatchHash: digest(encoded), Events: items}
	if err := validateBatch(batch); err != nil {
		t.Fatalf("valid mixed RTW Knowledge batch rejected: %v", err)
	}
	batch.Events[1].Offset = 3
	if !errors.Is(validateBatch(batch), ErrContract) {
		t.Fatal("shared producer gap was accepted")
	}
}
