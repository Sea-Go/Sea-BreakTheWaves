package usermodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/sourcecoverage"
)

type realCoverageAuthorityFact struct {
	Event              json.RawMessage           `json:"event"`
	SubjectRef         sourcecoverage.SubjectRef `json:"subject_ref"`
	PredecessorEventID string                    `json:"predecessor_event_id"`
	TechnicalReceipt   eventing.Receipt          `json:"technical_receipt"`
	SourceEventHash    string                    `json:"source_event_hash"`
}

func readRealCoverageAuthority(t *testing.T, producer, eventID string) realCoverageAuthorityFact {
	t.Helper()
	url := os.Getenv("SEA_FACT_AUTHORITY_URL") + "/internal/v1/favorite/facts/" + producer + "/" + eventID
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+os.Getenv("SEA_FACT_AUTHORITY_TOKEN"))
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("RTW authority source unavailable: %d", response.StatusCode)
	}
	var fact realCoverageAuthorityFact
	if err := json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&fact); err != nil {
		t.Fatal(err)
	}
	return fact
}

func TestCoverageVerifierRealDCAndRTW(t *testing.T) {
	for _, key := range []string{"SEA_FACT_DC_URL", "SEA_FACT_DC_TOKEN", "SEA_FACT_AUTHORITY_URL",
		"SEA_FACT_AUTHORITY_TOKEN", "USERMODEL_TEST_POSTGRES_DSN", "SEA_FACT_ASSERT_EVENT_ID", "SEA_FACT_RETRACT_EVENT_ID"} {
		if os.Getenv(key) == "" {
			t.Skip("run through coverage_source_acceptance.sh with real RTW/DC")
		}
	}
	ctx := context.Background()
	store := coverageTestStore(t)
	dc, err := datacenter.New(httpclient.Config{BaseURL: os.Getenv("SEA_FACT_DC_URL"), Token: os.Getenv("SEA_FACT_DC_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewFavoriteCoverageHTTPProof(dc, FavoriteCoverageProofConfig{
		AuthorityURL: os.Getenv("SEA_FACT_AUTHORITY_URL"), AuthorityToken: os.Getenv("SEA_FACT_AUTHORITY_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := dc.ReadEvents(ctx, "btw-coverage-source-test", favoriteCoverageProducer, 10)
	if err != nil || len(batch.Events) != 2 || batch.FromOffset != 1 || batch.ToOffset != 2 ||
		batch.Events[0].Event.EventID != os.Getenv("SEA_FACT_ASSERT_EVENT_ID") ||
		batch.Events[1].Event.EventID != os.Getenv("SEA_FACT_RETRACT_EVENT_ID") {
		t.Fatalf("real DC global batch: %+v %v", batch, err)
	}
	var rows []sourcecoverage.EventIndexRow
	for _, item := range batch.Events {
		auth := readRealCoverageAuthority(t, favoriteCoverageProducer, item.Event.EventID)
		dcReceipt, err := dc.EventReceipt(ctx, favoriteCoverageProducer, item.Event.EventID)
		if err != nil {
			t.Fatal(err)
		}
		row := sourcecoverage.EventIndexRow{Producer: favoriteCoverageProducer,
			Offset: strconv.FormatInt(item.Offset, 10), EventID: item.Event.EventID,
			InputHash: item.InputHash, RTWSourceHash: auth.SourceEventHash,
			ReceiptID: dcReceipt.ReceiptID, ReceivedAt: dcReceipt.ReceivedAt,
			Subject: auth.SubjectRef, EventSpec: auth.Event}
		if err := proof.VerifyEvent(ctx, row); err != nil {
			t.Fatalf("real DC/RTW row %d: %v", item.Offset, err)
		}
		var source eventing.Event
		var payload struct {
			TargetType     string  `json:"target_type"`
			TargetID       string  `json:"target_id"`
			TargetRevision *string `json:"target_revision"`
		}
		if json.Unmarshal(auth.Event, &source) != nil || json.Unmarshal(source.Payload, &payload) != nil {
			t.Fatal("real RTW frozen source event malformed")
		}
		occurred, err1 := time.Parse(time.RFC3339Nano, source.OccurredAt)
		received, err2 := time.Parse(time.RFC3339Nano, dcReceipt.ReceivedAt)
		if err1 != nil || err2 != nil {
			t.Fatal("real RTW/DC timestamps invalid")
		}
		valueRef := payload.TargetType + "/" + payload.TargetID
		if payload.TargetRevision != nil {
			valueRef += "/revision/" + *payload.TargetRevision
		}
		fact := Event{Subject: SubjectRef(auth.SubjectRef), EventKey: EventKey{Producer: favoriteCoverageProducer, EventID: source.EventID},
			Action: Assert, Kind: ProductAction, Predicate: "favorite", ValueRef: valueRef, ItemID: payload.TargetID,
			EvidenceRef: fmt.Sprintf("dc:event:%s:%d", favoriteCoverageProducer, item.Offset), EvidenceHash: item.InputHash,
			OccurredAt: occurred, ObservedAt: received, SourcePartition: "dc:" + favoriteCoverageProducer,
			SourceSequence: item.Offset}
		if source.EventType == "rtw.favorite.retract" {
			fact.Action = Retract
			fact.Supersedes = &EventKey{Producer: favoriteCoverageProducer, EventID: auth.PredecessorEventID}
		}
		if receipt, err := store.Append(ctx, fact); err != nil || receipt.Status != "accepted" {
			t.Fatalf("real source fact/Outbox PG: %+v %v", receipt, err)
		}
		rows = append(rows, row)
	}
	computedBatch, err := sourcecoverage.BatchHash(rows)
	if err != nil || computedBatch != batch.BatchHash {
		t.Fatalf("real DC batch differs from sourcecoverage JCS: hash=%q err=%v", computedBatch, err)
	}
	ref, index, batches := coverageTestPrefix(t, rows, "real-rtw-dc-g1")
	verifier, err := NewCoverageVerifier(store, proof)
	if err != nil {
		t.Fatal(err)
	}
	if accepted, err := verifier.VerifyPrefix(ctx, ref, index, batches); err != nil || accepted.Replay {
		t.Fatalf("real source full prefix: %+v %v", accepted, err)
	}
	subject := SubjectRef(rows[0].Subject)
	subRef, sparse := coverageTestSubject(t, ref, subject, rows)
	if _, err := verifier.VerifySubject(ctx, subRef, sparse); err != nil {
		t.Fatal(err)
	}
	state, err := store.CoveredStateAt(ctx, subject, ref, time.Now())
	if err != nil || len(state.ActiveAtPrefix) != 0 || len(state.Tail) != 0 || !state.CurrentComplete || state.StateVersion != 2 {
		t.Fatalf("real assert/retract covered state: %+v %v", state, err)
	}
	forged := append([]sourcecoverage.EventIndexRow(nil), rows...)
	forged[0].Subject.SubjectID = "impostor"
	badRef, badIndex, badBatches := coverageTestPrefix(t, forged, "forged-source-g2")
	if _, err := verifier.VerifyPrefix(ctx, badRef, badIndex, badBatches); !errors.Is(err, ErrCoverageConflict) {
		t.Fatalf("forged warehouse SubjectRef bypassed RTW authority: %v", err)
	}
	again, err := dc.ReadEvents(ctx, "btw-coverage-source-test", favoriteCoverageProducer, 10)
	if err != nil || again.FromOffset != 1 || len(again.Events) != 2 {
		t.Fatalf("Verifier stole publisher/worker DC ACK: %+v %v", again, err)
	}
}
