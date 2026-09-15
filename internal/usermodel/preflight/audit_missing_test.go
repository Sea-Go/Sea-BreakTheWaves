package preflight

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func oldAuditClean(t *testing.T, schema string) int64 {
	t.Helper()
	oldBin := os.Getenv("USERMODEL_PREFLIGHT_OLD_BIN")
	if oldBin == "" {
		t.Fatal("old 1150534 CLI is required for P1 baseline proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, oldBin, "--run", "--dsn-env", "USERMODEL_PREFLIGHT_TEST_POSTGRES_DSN", "--schema", schema)
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatal("old 1150534 preflight unexpectedly blocked fixture")
	}
	var old struct {
		L1 int64 `json:"l1"`
		L2 int64 `json:"l2"`
		L3 int64 `json:"l3"`
	}
	if err := json.Unmarshal(stdout, &old); err != nil {
		t.Fatal("old report is not JSON")
	}
	if old.L1 != 0 || old.L2 != 0 {
		t.Fatalf("old 1150534 fixture was not a true blind spot: L1=%d L2=%d", old.L1, old.L2)
	}
	return old.L3
}

func requireNewP1(t *testing.T, conn *pgx.Conn, schema, rule, table string) {
	t.Helper()
	oldAuditClean(t, schema)
	report, err := Run(context.Background(), conn, schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.L1 != 0 || report.L2 != 1 || findingCount(report, rule, table) != 1 {
		t.Fatalf("new preflight did not isolate %s: L1=%d L2=%d findings=%+v", rule, report.L1, report.L2, report.Findings)
	}
	t.Logf("old115 L1/L2=0; new %s=1; contentSHA=%s", rule, report.ReportSHA256)
}

func insertState(t *testing.T, conn *pgx.Conn, uid string, version int64) {
	t.Helper()
	_, err := conn.Exec(context.Background(), `INSERT INTO usermodel_subject_state(authority_id,tenant_id,subject_id,state_version)
		VALUES('rtw.identity','platform',$1,$2)`, uid, version)
	if err != nil {
		t.Fatal(err)
	}
}

func insertAcceptedEvent(t *testing.T, conn *pgx.Conn, uid, eventID string, sequence any) {
	t.Helper()
	_, err := conn.Exec(context.Background(), `INSERT INTO usermodel_events
		(authority_id,tenant_id,subject_id,producer,event_id,normalized_hash,event_body,action,semantic_kind,
		 occurred_at,observed_at,source_partition,source_sequence,status,initial_status,initial_version,accepted_version)
		VALUES('rtw.identity','platform',$1,'fixture',$2,repeat('a',64),'{}','assert','reading',
		 now(),now(),'partition',$3,'accepted','accepted',1,1)`, uid, eventID, sequence)
	if err != nil {
		t.Fatal(err)
	}
}

func insertPendingEvent(t *testing.T, conn *pgx.Conn, uid, eventID string, initialVersion int64, sequence any) {
	t.Helper()
	_, err := conn.Exec(context.Background(), `INSERT INTO usermodel_events
		(authority_id,tenant_id,subject_id,producer,event_id,normalized_hash,event_body,action,semantic_kind,
		 occurred_at,observed_at,source_partition,source_sequence,supersedes_producer,supersedes_event_id,
		 status,initial_status,initial_version,accepted_version)
		VALUES('rtw.identity','platform',$1,'fixture',$2,repeat('b',64),'{}','correct','reading',
		 now(),now(),'partition',$3,'fixture','missing-predecessor','pending_dependency','pending_dependency',$4,NULL)`,
		uid, eventID, sequence, initialVersion)
	if err != nil {
		t.Fatal(err)
	}
}

func insertOutbox(t *testing.T, conn *pgx.Conn, uid, eventID string, version int64) {
	t.Helper()
	_, err := conn.Exec(context.Background(), `INSERT INTO usermodel_outbox
		(authority_id,tenant_id,subject_id,state_version,event_type,producer,event_id,payload)
		VALUES('rtw.identity','platform',$1,$2,'usermodel.fact.accepted','fixture',$3,'{}')`, uid, version, eventID)
	if err != nil {
		t.Fatal(err)
	}
}

func insertActive(t *testing.T, conn *pgx.Conn, uid, eventID string) {
	t.Helper()
	_, err := conn.Exec(context.Background(), `INSERT INTO usermodel_active_facts
		(authority_id,tenant_id,subject_id,producer,event_id,activated_version)
		VALUES('rtw.identity','platform',$1,'fixture',$2,1)`, uid, eventID)
	if err != nil {
		t.Fatal(err)
	}
}

func TestP1OutboxMin(t *testing.T) {
	conn, schema := fixtureDB(t)
	insertState(t, conn, "101", 2)
	insertOutbox(t, conn, "101", "first", 0)
	insertOutbox(t, conn, "101", "second", 2)
	requireNewP1(t, conn, schema, "outbox_version_gap", "usermodel_outbox")
}

func TestP1CoveragePending(t *testing.T) {
	conn, schema := fixtureDB(t)
	insertState(t, conn, "102", 0)
	insertPendingEvent(t, conn, "102", "pending", 0, nil)
	_, err := conn.Exec(context.Background(), `INSERT INTO usermodel_coverage_prefix
		(manifest_sha256,producer,binding_policy_id,through_offset,event_index_sha256,batch_evidence_sha256,ref_body)
		VALUES(repeat('c',64),'fixture','policy',1,repeat('d',64),repeat('e',64),'{}')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(context.Background(), `INSERT INTO usermodel_coverage_event
		(manifest_sha256,source_offset,producer,event_id,input_hash,authority_id,tenant_id,subject_id,
		 normalized_hash,accepted_version,row_body)
		VALUES(repeat('c',64),1,'fixture','pending',repeat('f',64),'rtw.identity','platform','102',
		 repeat('b',64),1,'{}')`)
	if err != nil {
		t.Fatal(err)
	}
	requireNewP1(t, conn, schema, "coverage_event_version", "usermodel_coverage_event")
}

func TestP1WatermarkMissing(t *testing.T) {
	conn, schema := fixtureDB(t)
	insertState(t, conn, "103", 1)
	insertAcceptedEvent(t, conn, "103", "accepted", int64(1))
	insertOutbox(t, conn, "103", "accepted", 1)
	insertActive(t, conn, "103", "accepted")
	requireNewP1(t, conn, schema, "missing_source_watermark", "usermodel_events")
}

func insertBoundUnmapped(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	insertState(t, conn, "104", 0)
	insertState(t, conn, "105", 0)
	_, err := conn.Exec(context.Background(), `INSERT INTO usermodel_unmapped_events
		(authority_id,tenant_id,external_subject_id,producer,event_id,normalized_hash,event_body,
		 bound_subject_id,bound_version,bound_status)
		VALUES('rtw.identity','platform','external-alias','fixture','parked',repeat('a',64),'{}',
		 '104',0,'accepted')`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestP1BoundMismatch(t *testing.T) {
	conn, schema := fixtureDB(t)
	insertBoundUnmapped(t, conn)
	_, err := conn.Exec(context.Background(), `INSERT INTO usermodel_subject_bindings
		(authority_id,tenant_id,external_subject_id,subject_id)
		VALUES('rtw.identity','platform','external-alias','105')`)
	if err != nil {
		t.Fatal(err)
	}
	requireNewP1(t, conn, schema, "bound_unmapped_binding_disagreement", "usermodel_unmapped_events")
}

func TestP1BoundMissing(t *testing.T) {
	conn, schema := fixtureDB(t)
	insertBoundUnmapped(t, conn)
	requireNewP1(t, conn, schema, "bound_unmapped_binding_disagreement", "usermodel_unmapped_events")
}

func TestP1ActiveMissing(t *testing.T) {
	conn, schema := fixtureDB(t)
	insertState(t, conn, "106", 1)
	insertAcceptedEvent(t, conn, "106", "accepted", nil)
	insertOutbox(t, conn, "106", "accepted", 1)
	requireNewP1(t, conn, schema, "missing_current_active_fact", "usermodel_events")
}

func TestP3PendingGapIsValid(t *testing.T) {
	conn, schema := fixtureDB(t)
	insertState(t, conn, "107", 1)
	insertAcceptedEvent(t, conn, "107", "accepted", int64(1))
	insertPendingEvent(t, conn, "107", "pending", 1, int64(3))
	insertOutbox(t, conn, "107", "accepted", 1)
	insertActive(t, conn, "107", "accepted")
	_, err := conn.Exec(context.Background(), `INSERT INTO usermodel_watermarks
		(authority_id,tenant_id,subject_id,producer,source_partition,contiguous_sequence,max_seen_sequence)
		VALUES('rtw.identity','platform','107','fixture','partition',1,3)`)
	if err != nil {
		t.Fatal(err)
	}
	oldAuditClean(t, schema)
	report, err := Run(context.Background(), conn, schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.L1+report.L2+report.L3 != 0 {
		t.Fatalf("pending gap was falsely rejected: %+v", report.Findings)
	}
}

func TestP3OntologyMissing(t *testing.T) {
	conn, schema := fixtureDB(t)
	insertState(t, conn, "108", 0)
	_, err := conn.Exec(context.Background(), `INSERT INTO usermodel_ontology_definitions
		(authority_id,tenant_id,definition_version,definition_hash,definition_body)
		VALUES('rtw.identity','platform',1,repeat('a',64),'{}')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(context.Background(), `INSERT INTO usermodel_ontology_heads
		(authority_id,tenant_id,definition_version) VALUES('rtw.identity','platform',1)`)
	if err != nil {
		t.Fatal(err)
	}
	if oldAuditClean(t, schema) != 0 {
		t.Fatal("old preflight unexpectedly found the missing ontology projection")
	}
	report, err := Run(context.Background(), conn, schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.L1+report.L2 != 0 || findingCount(report, "ontology_projection_missing", "usermodel_subject_state") != 1 {
		t.Fatalf("missing ontology projection was not an L3 rebuild candidate: %+v", report.Findings)
	}
}

func TestAcceptedSuccessorAndRetractNeedNoActiveRow(t *testing.T) {
	conn, schema := fixtureDB(t)
	insertState(t, conn, "110", 3)
	insertAcceptedEvent(t, conn, "110", "assert", nil)
	for _, successor := range []struct {
		eventID, predecessor, action string
		version                      int64
	}{
		{"correct", "assert", "correct", 2}, {"retract", "correct", "retract", 3},
	} {
		_, err := conn.Exec(context.Background(), `INSERT INTO usermodel_events
			(authority_id,tenant_id,subject_id,producer,event_id,normalized_hash,event_body,action,semantic_kind,
			 occurred_at,observed_at,source_partition,supersedes_producer,supersedes_event_id,
			 status,initial_status,initial_version,accepted_version)
			VALUES('rtw.identity','platform','110','fixture',$1,repeat('b',64),'{}',$2,'reading',
			 now(),now(),'partition','fixture',$3,'accepted','accepted',$4,$4)`,
			successor.eventID, successor.action, successor.predecessor, successor.version)
		if err != nil {
			t.Fatal(err)
		}
		insertOutbox(t, conn, "110", successor.eventID, successor.version)
	}
	insertOutbox(t, conn, "110", "assert", 1)
	oldAuditClean(t, schema)
	report, err := Run(context.Background(), conn, schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.L1+report.L2+report.L3 != 0 {
		t.Fatalf("accepted successor/retract chain falsely requires active row: %+v", report.Findings)
	}
}

func TestP2RowsAndContentHash(t *testing.T) {
	conn, schema := fixtureDB(t)
	empty, err := Run(context.Background(), conn, schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	insertState(t, conn, "109", 0)
	stateZero, err := Run(context.Background(), conn, schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.TotalRows != 0 || stateZero.TotalRows != 1 || len(stateZero.TableRows) != 23 ||
		findingCount(stateZero, "invalid_legacy_subject", "usermodel_subject_state") != 0 ||
		empty.ReportSHA256 == stateZero.ReportSHA256 || !stateZero.RowAuditComplete {
		t.Fatal("scanned row counts did not distinguish empty and valid state-version-zero snapshots")
	}
	var stateRows int64
	for _, scan := range stateZero.TableRows {
		if scan.Table == "usermodel_subject_state" {
			stateRows = scan.Rows
		}
	}
	if stateRows != 1 {
		t.Fatal("subject state table count is missing")
	}
	copy := stateZero
	copy.ReportSHA256 = ""
	compact, _ := json.Marshal(copy)
	contentSHA := sha256.Sum256(compact)
	if stateZero.ReportSHA256 != hex.EncodeToString(contentSHA[:]) {
		t.Fatal("content SHA does not match compact JSON excluding its own field")
	}
	var output bytes.Buffer
	if err := json.NewEncoder(&output).Encode(stateZero); err != nil {
		t.Fatal(err)
	}
	fileSHA := sha256.Sum256(output.Bytes())
	if stateZero.ReportSHA256 == hex.EncodeToString(fileSHA[:]) {
		t.Fatal("content SHA was incorrectly presented as stdout file byte SHA")
	}
}
