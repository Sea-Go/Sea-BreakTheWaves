package favoritesource

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/sourcecoverage"
)

func preflightFixtureRow(id, folder, uid, target, operation string, offset int64,
	subject sourcecoverage.SubjectRef) preflightODS {
	item, receipt := fixtureFavoriteEvent(id, folder, uid, target, operation, offset)
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(item.Event.Payload, &payload)
	payload["subject_ref"], _ = json.Marshal(subject)
	item.Event.Payload, _ = json.Marshal(payload)
	canonical, _ := coverageCanonical(item.Event)
	item.InputHash = coverageHash(canonical)
	receipt.InputHash = item.InputHash
	spec, _ := json.Marshal(item.Event)
	receiptBody, _ := json.Marshal(receipt)
	predecessor := ""
	if operation == "retract" {
		predecessor = "favorite." + id + ".v1"
	}
	revision := target + ":r1"
	eventTime, _ := time.Parse(time.RFC3339Nano, "2026-09-15T00:00:00Z")
	receivedAt, _ := time.Parse(time.RFC3339Nano, receipt.ReceivedAt)
	return preflightODS{producer: Producer, offset: offset, eventID: item.Event.EventID,
		eventType: item.Event.EventType, aggregateID: item.Event.AggregateID,
		aggregateVersion: item.Event.AggregateVersion, eventSpec: spec, sourceHash: item.InputHash, receipt: receiptBody,
		subject: subject, favoriteID: id, folderID: folder, targetType: "article", targetID: target, revision: &revision,
		operation: operation, predecessor: predecessor, eventTime: eventTime, availableAt: eventTime, receivedAt: receivedAt}
}

func findingCount(report SubjectRefV2Report, code string) int {
	for _, finding := range report.Findings {
		if finding.Code == code {
			return finding.Count
		}
	}
	return 0
}

func TestSubjectRefV2ProjectionPreservesVersionChainAndRejectsOldSlots(t *testing.T) {
	canonical := sourcecoverage.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	otherSlot := sourcecoverage.SubjectRef{AuthorityID: "legacy.identity", TenantID: "platform", SubjectID: "1001"}
	assert := preflightFixtureRow("9001", "7001", "1001", "article-a", "assert", 1, canonical)
	retract := preflightFixtureRow("9001", "7001", "1001", "article-a", "retract", 2, canonical)
	clean := assessSubjectRefV2(preflightSnapshot{cursor: 2, ods: []preflightODS{assert, retract}})
	if !clean.Clear() {
		t.Fatalf("same-slot assert/retract unexpectedly blocked: %+v", clean.Findings)
	}
	legacy := preflightFixtureRow("9001", "7001", "1001", "article-a", "assert", 3, otherSlot)
	blocked := assessSubjectRefV2(preflightSnapshot{cursor: 3, ods: []preflightODS{assert, retract, legacy}})
	for _, code := range []string{"exceptional_legacy_slot_requires_review", "projected_favorite_key_collision", "projected_folder_target_collision"} {
		if findingCount(blocked, code) == 0 {
			t.Fatalf("missing %s: %+v", code, blocked.Findings)
		}
	}
	// A different favorite_id in the same folder/target still collides with
	// RTW's uk_folder_target after projection.
	legacy = preflightFixtureRow("9002", "7001", "1001", "article-a", "assert", 3, otherSlot)
	blocked = assessSubjectRefV2(preflightSnapshot{cursor: 3, ods: []preflightODS{assert, retract, legacy}})
	if findingCount(blocked, "projected_folder_target_collision") == 0 {
		t.Fatal("folder/target collision disappeared")
	}
	encoded, _ := json.Marshal(blocked)
	for _, private := range []string{"legacy.identity", "article-a", "favorite.9001", "\"1001\""} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("private source value leaked in report: %s", private)
		}
	}
}

func TestSubjectRefV2InvalidUIDAndReceiptProjectionCollision(t *testing.T) {
	base := sourcecoverage.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	publication := preflightPublication{manifest: strings.Repeat("a", 64), generation: "fixture_g1", producer: Producer,
		through: 1, indexHash: strings.Repeat("b", 64), batchHash: strings.Repeat("c", 64)}
	row := preflightFixtureRow("9001", "7001", "1001", "article-a", "assert", 1, base)
	receipts := []preflightSubjectReceipt{
		{receiptHash: strings.Repeat("d", 64), manifest: publication.manifest, subject: base, sparseHash: strings.Repeat("e", 64), eventCount: 1},
		{receiptHash: strings.Repeat("f", 64), manifest: publication.manifest,
			subject:    sourcecoverage.SubjectRef{AuthorityID: "legacy.identity", TenantID: "platform", SubjectID: "1001"},
			sparseHash: strings.Repeat("e", 64), eventCount: 1},
	}
	for _, bad := range []string{"0", "01", "9223372036854775808", "-1"} {
		copy := row
		copy.subject.SubjectID = bad
		report := assessSubjectRefV2(preflightSnapshot{cursor: 1, ods: []preflightODS{copy}})
		if findingCount(report, "invalid_positive_int64_uid") != 1 {
			t.Fatalf("UID %q accepted", bad)
		}
	}
	report := assessSubjectRefV2(preflightSnapshot{cursor: 1, ods: []preflightODS{row}, publications: []preflightPublication{publication}, receipts: receipts})
	if findingCount(report, "projected_subject_receipt_collision") != 1 || findingCount(report, "exceptional_legacy_slot_requires_review") != 1 {
		t.Fatalf("receipt projection merged distinct old slots: %+v", report.Findings)
	}
	receipts[1].manifest = strings.Repeat("0", 64)
	report = assessSubjectRefV2(preflightSnapshot{cursor: 1, ods: []preflightODS{row}, publications: []preflightPublication{publication}, receipts: receipts})
	if findingCount(report, "subject_receipt_reference_mismatch") == 0 {
		t.Fatal("orphan manifest reference accepted")
	}
}

func TestSubjectRefV2ImmutableEvidenceAndPrefixCounterexamples(t *testing.T) {
	subject := sourcecoverage.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	row := preflightFixtureRow("9001", "7001", "1001", "article-a", "assert", 1, subject)
	changed := row
	changed.sourceHash = strings.Repeat("0", 64)
	report := assessSubjectRefV2(preflightSnapshot{cursor: 1, ods: []preflightODS{changed}})
	if findingCount(report, "immutable_event_or_receipt_mismatch") == 0 {
		t.Fatal("changed source hash accepted")
	}
	for _, mutate := range []func(*preflightODS){
		func(r *preflightODS) { r.eventType = "rtw.favorite.retract" },
		func(r *preflightODS) { r.aggregateID = "9999" },
		func(r *preflightODS) { r.eventTime = r.eventTime.Add(time.Hour) },
		func(r *preflightODS) { r.availableAt = r.availableAt.Add(time.Hour) },
		func(r *preflightODS) { r.receivedAt = r.receivedAt.Add(time.Hour) },
	} {
		changed = row
		mutate(&changed)
		report = assessSubjectRefV2(preflightSnapshot{cursor: 1, ods: []preflightODS{changed}})
		if findingCount(report, "immutable_event_or_receipt_mismatch") == 0 {
			t.Fatal("materialized ODS field changed without EventSpec/receipt was accepted")
		}
	}
	changed = row
	changed.predecessor = "unrelated.v1"
	report = assessSubjectRefV2(preflightSnapshot{cursor: 1, ods: []preflightODS{changed}})
	if findingCount(report, "favorite_version_chain_mismatch") == 0 {
		t.Fatal("assert predecessor accepted")
	}
	retract := preflightFixtureRow("9001", "7001", "1001", "article-a", "retract", 2, subject)
	retract.predecessor = "unrelated.v1"
	report = assessSubjectRefV2(preflightSnapshot{cursor: 2, ods: []preflightODS{row, retract}})
	if findingCount(report, "favorite_predecessor_mismatch") == 0 {
		t.Fatal("orphan retract accepted")
	}
	cleanSHA := assessSubjectRefV2(preflightSnapshot{cursor: 1, ods: []preflightODS{row}}).SnapshotSHA256
	changedSHA := assessSubjectRefV2(preflightSnapshot{cursor: 2, ods: []preflightODS{row}}).SnapshotSHA256
	if cleanSHA == changedSHA {
		t.Fatal("cursor omitted from snapshot fingerprint")
	}
	gap := row
	gap.offset = 2
	report = assessSubjectRefV2(preflightSnapshot{cursor: 2, ods: []preflightODS{gap}})
	if findingCount(report, "ods_prefix_or_producer") == 0 {
		t.Fatal("missing origin accepted")
	}
	publication := preflightPublication{manifest: strings.Repeat("a", 64), generation: "fixture_g1", producer: Producer,
		through: 2, indexHash: strings.Repeat("b", 64), batchHash: strings.Repeat("c", 64)}
	report = assessSubjectRefV2(preflightSnapshot{cursor: 1, ods: []preflightODS{row}, publications: []preflightPublication{publication}})
	if findingCount(report, "published_prefix_reference_mismatch") == 0 {
		t.Fatal("manifest through uncommitted offset accepted")
	}
}

func TestSubjectRefV2PGTimestampQuantization(t *testing.T) {
	source := "2026-09-15T00:00:00.000000500Z"
	stored, _ := time.Parse(time.RFC3339Nano,"2026-09-15T00:00:00Z")
	if !pgTimestampMatches(stored,source) { t.Fatal("pgx-truncated source timestamp rejected") }
	if pgTimestampMatches(stored.Add(time.Microsecond),source) { t.Fatal("adjacent PG microsecond accepted") }
}
