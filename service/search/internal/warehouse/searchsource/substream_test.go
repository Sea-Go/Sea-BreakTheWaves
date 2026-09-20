package searchsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/jackc/pgx/v5/pgxpool"
)

func mixedSubstreamFixture(t *testing.T) (eventing.Batch, map[string]eventing.Receipt,
	fixtureAuthority, []byte) {
	t.Helper()
	authority := fixtureAuthority{}
	receipts := map[string]eventing.Receipt{}
	items := []eventing.Item{}
	assignments := []map[string]string{}
	base := fixtureJudgment()
	var first Judgment
	for i := 0; i < 8; i++ {
		var event eventing.Event
		var received string
		if i == 2 {
			event = eventing.Event{EventID: "event-3", EventType: "knowledge.module.created.v1",
				SchemaVersion: 1, Producer: Producer, AggregateID: base.ModuleID,
				AggregateVersion: 3, OperationID: "operation-3",
				OccurredAt: "2026-09-01T10:00:00Z", Payload: json.RawMessage(`{"kind":"synthetic-skip"}`)}
			received = "2026-09-01T10:01:00Z"
		} else {
			index := i
			if i > 2 {
				index--
			}
			j := base
			if i == 7 {
				j = first
				j.JudgmentRevision, j.RevisionID, j.BaseRevisionID = 2, "judgment-r2-0", first.RevisionID
				j.State, j.Grade, j.JudgedAt = "withdrawn", nil, "2026-09-06T08:00:00Z"
				event.EventType = Withdrawn
				received = "2026-09-06T08:02:00Z"
			} else {
				day := []int{1, 1, 3, 3, 5, 5}[index]
				j.JudgmentID = fmt.Sprintf("judgment-%d", index)
				j.RevisionID = fmt.Sprintf("judgment-r1-%d", index)
				j.SearchID = fmt.Sprintf("search-%d", index)
				j.QueryText = fmt.Sprintf("synthetic query %d", index)
				j.QueryTextSHA256 = digest([]byte(j.QueryText))
				j.ChunkID = fmt.Sprintf("chunk-%d", index)
				j.ContentID = fmt.Sprintf("document-%d", index)
				j.ChunkText = fmt.Sprintf("Synthetic published passage %d.", index)
				j.ChunkTextSHA256 = digest([]byte(j.ChunkText))
				j.ContentAvailableAt = "2026-08-01T00:00:00Z"
				j.QueryTime = fmt.Sprintf("2026-09-%02dT08:00:00Z", day)
				j.JudgedAt = fmt.Sprintf("2026-09-%02dT08:10:00Z", day)
				received = fmt.Sprintf("2026-09-%02dT08:12:00Z", day)
				grade := 0
				if index%2 == 0 {
					grade = 3
				}
				j.Grade = &grade
				if index == 0 {
					first = j
				}
				family := fmt.Sprintf("family-%d", index)
				cluster := fmt.Sprintf("near-%d", day)
				assignments = append(assignments, map[string]string{
					"search_id": j.SearchID, "query_text_sha256": j.QueryTextSHA256,
					"query_family_id": family, "near_duplicate_cluster_id": cluster})
				event.EventType = Revised
			}
			payload, err := json.Marshal(j)
			if err != nil {
				t.Fatal(err)
			}
			event.EventID = fmt.Sprintf("event-%d", i+1)
			event.SchemaVersion, event.Producer, event.AggregateID = 1, Producer, j.ModuleID
			event.AggregateVersion, event.OperationID = int64(i+1), fmt.Sprintf("operation-%d", i+1)
			event.OccurredAt, event.Payload = j.JudgedAt, payload
		}
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := canonical(raw)
		if err != nil {
			t.Fatal(err)
		}
		inputHash := digest(encoded)
		offset := int64(i + 1)
		items = append(items, eventing.Item{Offset: offset, Event: event, InputHash: inputHash})
		receipts[event.EventID] = eventing.Receipt{EventID: event.EventID,
			Producer: Producer, TechnicalStatus: "accepted", ReceiptID: "receipt-" + event.EventID,
			InputHash: inputHash, Offset: offset, ReceivedAt: received}
		if isJudgment(event.EventType) {
			authority[event.EventID] = raw
		}
	}
	itemRaw, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	itemCanonical, err := canonical(itemRaw)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := json.Marshal(map[string]any{"schema_version": GroupingSchema,
		"data_kind":          "synthetic",
		"fixture_provenance": "internal/warehouse/searchsource/substream_test.go:mixedSubstreamFixture",
		"policy_id":          "synthetic-query-groups-v1", "assignments": assignments})
	if err != nil {
		t.Fatal(err)
	}
	return eventing.Batch{Consumer: DefaultConsumer, Producer: Producer, FromOffset: 1,
		ToOffset: int64(len(items)), BatchHash: digest(itemCanonical), Events: items}, receipts, authority, policy
}

func TestRealPGMixedPrefixToContiguousQrelSubstream(t *testing.T) {
	dsn := os.Getenv("SEARCH_QREL_SOURCE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL source acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Initialize(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE warehouse_search_source.ods_event,warehouse_search_source.consumer_cursor`); err != nil {
		t.Fatal(err)
	}
	batch, receipts, authority, policyRaw := mixedSubstreamFixture(t)
	if err := validateBatch(batch); err != nil {
		t.Fatalf("mixed fixture DC batch invalid: %v", err)
	}
	consumer := &Consumer{DB: pool, Source: &fixtureSource{batch: batch, receipts: receipts},
		Authority: authority, Consumer: DefaultConsumer}
	result, err := consumer.RunOnce(ctx)
	if err != nil || result.Read != 8 || result.Judgments != 7 || result.TechnicalSkips != 1 ||
		result.CommittedOffset != 8 || result.AcknowledgedOffset != 8 {
		t.Fatalf("synthetic RTW/DC into real PG ODS: %+v %v", result, err)
	}
	policy, policySHA, err := ParseGroupingPolicy(policyRaw)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := ExportSyntheticSubstream(ctx, pool, 8, policy, policySHA)
	if err != nil || bundle.Manifest.QrelCount != 7 || bundle.Manifest.TechnicalSkipCount != 1 ||
		bundle.Manifest.ThroughOffset != 8 || bundle.Manifest.JudgmentScopeComplete ||
		ValidateCoverage(bundle) != nil {
		t.Fatalf("real PG mixed prefix export: %+v %v", bundle.Manifest, err)
	}
	var landing []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(bundle.LandingJSONL)), "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		landing = append(landing, row)
	}
	if len(landing) != 7 || landing[6]["status"] != "retracted" ||
		landing[6]["relevance_grade"] != nil || landing[6]["judged_mask"] != false ||
		landing[6]["source_sequence"] != float64(7) || landing[2]["source_sequence"] != float64(3) {
		t.Fatalf("withdrawal/null grade or explicit qrel ordinal changed: %+v", landing)
	}
	if _, err := ExportSyntheticSubstream(ctx, pool, 9, policy, policySHA); !errors.Is(err, ErrContract) {
		t.Fatalf("incomplete producer prefix accepted: %v", err)
	}
	bad := policy
	bad.Assignments = policy.Assignments[:5]
	badRaw, _ := json.Marshal(bad)
	_, badSHA, _ := ParseGroupingPolicy(badRaw)
	if _, err := ExportSyntheticSubstream(ctx, pool, 8, bad, badSHA); !errors.Is(err, ErrContract) {
		t.Fatalf("missing explicit grouping accepted: %v", err)
	}
	tampered := bundle
	tampered.CoverageJSONL = append([]byte(nil), bundle.CoverageJSONL...)
	tampered.CoverageJSONL[0] ^= 1
	if !errors.Is(ValidateCoverage(tampered), ErrContract) {
		t.Fatal("changed original-offset/skip coverage accepted")
	}
	changed := bundle
	lines := strings.Split(strings.TrimSpace(string(bundle.LandingJSONL)), "\n")
	var forged map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &forged); err != nil {
		t.Fatal(err)
	}
	forged["relevance_grade"] = float64(2)
	delete(forged, "payload_hash")
	delete(forged, "batch_id")
	withoutHash, _ := json.Marshal(forged)
	canonicalPayload, _ := canonical(withoutHash)
	forged["payload_hash"] = digest(canonicalPayload)
	forged["batch_id"] = bundle.Manifest.LandingBatchID
	withHash, _ := json.Marshal(forged)
	lines[0] = string(withHash)
	changed.LandingJSONL = []byte(strings.Join(lines, "\n") + "\n")
	changed.Manifest.LandingSHA256 = digest(changed.LandingJSONL)
	if !errors.Is(ValidateCoverage(changed), ErrContract) {
		t.Fatal("changed grade with recomputed row and file hashes escaped coverage root")
	}
	if output := os.Getenv("SEARCH_QREL_SUBSTREAM_OUTPUT"); output != "" {
		if err := os.Mkdir(output, 0700); err != nil {
			t.Fatal(err)
		}
		manifestRaw, err := json.MarshalIndent(bundle.Manifest, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{
			"manifest.json": append(manifestRaw, '\n'), "coverage.jsonl": bundle.CoverageJSONL,
			bundle.Manifest.LandingBatchID + ".jsonl": bundle.LandingJSONL,
			"grouping.json": policyRaw,
		} {
			if err := os.WriteFile(filepath.Join(output, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
