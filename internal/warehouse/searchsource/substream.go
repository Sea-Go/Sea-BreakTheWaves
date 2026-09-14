package searchsource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const GroupingSchema = "sea.search.qrel-grouping.v1"
const SubstreamSchema = "sea.search.qrel-substream.v1"
const SubstreamPartition = "ridethewind.knowledge:qrel-substream:v1"

// GroupingPolicy is an explicit, versioned mapping. RTW's judgment event has
// no query family or near-duplicate cluster; neither is inferred from text.
type GroupingPolicy struct {
	SchemaVersion     string `json:"schema_version"`
	PolicyID          string `json:"policy_id"`
	DataKind          string `json:"data_kind"` // v1 exporter accepts synthetic fixtures only
	FixtureProvenance string `json:"fixture_provenance"`
	Assignments       []struct {
		SearchID             string `json:"search_id"`
		QueryTextSHA256      string `json:"query_text_sha256"`
		QueryFamilyID        string `json:"query_family_id"`
		NearDuplicateCluster string `json:"near_duplicate_cluster_id"`
	} `json:"assignments"`
}

type CoverageRow struct {
	SourceOffset         int64   `json:"source_offset"`
	EventID              string  `json:"event_id"`
	EventType            string  `json:"event_type"`
	Status               string  `json:"status"`
	DCInputHash          string  `json:"dc_input_hash"`
	DCReceiptID          string  `json:"dc_receipt_id"`
	AuthoritySHA256      *string `json:"authority_event_sha256"`
	QrelOrdinal          *int64  `json:"qrel_ordinal"`
	LandingPayloadSHA256 *string `json:"landing_payload_sha256"`
	LeafSHA256           string  `json:"leaf_sha256"`
	PreviousRoot         string  `json:"previous_root"`
	CoverageRoot         string  `json:"coverage_root"`
}

type SubstreamManifest struct {
	SchemaVersion         string `json:"schema_version"`
	Producer              string `json:"producer"`
	Consumer              string `json:"consumer"`
	FromOffset            int64  `json:"from_offset"`
	ThroughOffset         int64  `json:"through_offset"`
	ODSCommittedOffset    int64  `json:"ods_committed_offset"`
	CoverageRoot          string `json:"coverage_root"`
	CoverageSHA256        string `json:"coverage_sha256"`
	LandingSHA256         string `json:"landing_sha256"`
	LandingBatchID        string `json:"landing_batch_id"`
	QrelCount             int64  `json:"qrel_count"`
	TechnicalSkipCount    int64  `json:"technical_skip_count"`
	GroupingPolicyID      string `json:"grouping_policy_id"`
	GroupingPolicySHA256  string `json:"grouping_policy_sha256"`
	DataKind              string `json:"data_kind"`
	FixtureProvenance     string `json:"fixture_provenance"`
	Activation            string `json:"activation"`
	JudgmentScopeComplete bool   `json:"judgment_scope_complete"`
	SourcePartition       string `json:"source_partition"`
}

type SubstreamBundle struct {
	Manifest      SubstreamManifest
	CoverageJSONL []byte
	LandingJSONL  []byte
}

type grouping struct {
	family  string
	cluster string
	textSHA string
}

func ParseGroupingPolicy(raw []byte) (GroupingPolicy, string, error) {
	var policy GroupingPolicy
	if len(raw) == 0 || len(raw) > 1<<20 {
		return policy, "", ErrContract
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&policy) != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) ||
		policy.SchemaVersion != GroupingSchema || policy.PolicyID == "" ||
		policy.DataKind != "synthetic" || policy.FixtureProvenance == "" || len(policy.Assignments) == 0 {
		return GroupingPolicy{}, "", ErrContract
	}
	seen := map[string]bool{}
	for _, assignment := range policy.Assignments {
		if assignment.SearchID == "" || !shaPattern.MatchString(assignment.QueryTextSHA256) ||
			assignment.QueryFamilyID == "" || assignment.NearDuplicateCluster == "" || seen[assignment.SearchID] {
			return GroupingPolicy{}, "", ErrContract
		}
		seen[assignment.SearchID] = true
	}
	encoded, err := canonical(raw)
	if err != nil {
		return GroupingPolicy{}, "", err
	}
	return policy, digest(encoded), nil
}

type odsSubstreamRow struct {
	offset       int64
	eventID      string
	eventType    string
	eventSpec    []byte
	dcInputHash  string
	receiptJSON  []byte
	receivedAt   time.Time
	status       string
	authority    []byte
	authoritySHA *string
	judgmentID   *string
	revisionID   *string
	revision     *int
	baseID       *string
	state        *string
	payload      []byte
}

// ExportSyntheticSubstream reads one repeatable-read ODS snapshot and rejects an
// incomplete producer prefix. Every original DC offset enters the coverage
// chain; only qrel revisions receive the separate contiguous ordinal. It is
// intentionally fixture-only: no real human source is relabeled synthetic.
// An observed export needs a separate trusted provenance/approval contract.
func ExportSyntheticSubstream(ctx context.Context, db *pgxpool.Pool, through int64,
	policy GroupingPolicy, policySHA string) (SubstreamBundle, error) {
	var out SubstreamBundle
	if db == nil || through < 1 || through > 100_000 || !shaPattern.MatchString(policySHA) ||
		policy.SchemaVersion != GroupingSchema || policy.PolicyID == "" ||
		policy.DataKind != "synthetic" || policy.FixtureProvenance == "" {
		return out, ErrContract
	}
	groups := map[string]grouping{}
	for _, assignment := range policy.Assignments {
		if assignment.SearchID == "" || groups[assignment.SearchID].family != "" ||
			!shaPattern.MatchString(assignment.QueryTextSHA256) || assignment.QueryFamilyID == "" ||
			assignment.NearDuplicateCluster == "" {
			return out, ErrContract
		}
		groups[assignment.SearchID] = grouping{assignment.QueryFamilyID,
			assignment.NearDuplicateCluster, assignment.QueryTextSHA256}
	}
	policyRaw, err := json.Marshal(policy)
	if err != nil {
		return out, err
	}
	policyCanonical, err := canonical(policyRaw)
	if err != nil || digest(policyCanonical) != policySHA {
		return out, ErrContract
	}
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var committed int64
	if err := tx.QueryRow(ctx, `SELECT committed_offset FROM warehouse_search_source.consumer_cursor
 WHERE consumer=$1 AND producer=$2`, DefaultConsumer, Producer).Scan(&committed); err != nil || committed < through {
		return out, ErrContract
	}
	rows, err := tx.Query(ctx, `SELECT source_offset,event_id,event_type,event_spec,dc_input_hash,dc_receipt,
 dc_received_at,status,authority_event_json,authority_event_sha256,judgment_id,judgment_revision_id,
 judgment_revision,base_revision_id,judgment_state,judgment_payload
 FROM warehouse_search_source.ods_event WHERE producer=$1 AND source_offset BETWEEN 1 AND $2
 ORDER BY source_offset`, Producer, through)
	if err != nil {
		return out, err
	}
	var coverage bytes.Buffer
	var landing []map[string]any
	root := digest([]byte(SubstreamSchema + ":" + Producer))
	ordinal := int64(0)
	skips := int64(0)
	for rows.Next() {
		var item odsSubstreamRow
		if err := rows.Scan(&item.offset, &item.eventID, &item.eventType, &item.eventSpec,
			&item.dcInputHash, &item.receiptJSON, &item.receivedAt, &item.status,
			&item.authority, &item.authoritySHA, &item.judgmentID, &item.revisionID,
			&item.revision, &item.baseID, &item.state, &item.payload); err != nil {
			rows.Close()
			return out, err
		}
		if item.offset != ordinal+skips+1 {
			rows.Close()
			return out, fmt.Errorf("%w: original DC producer offset gap", ErrContract)
		}
		coverageRow, qrel, err := projectSubstreamRow(item, groups, ordinal+1)
		if err != nil {
			rows.Close()
			return out, err
		}
		if qrel != nil {
			payload, err := json.Marshal(qrel)
			if err != nil {
				rows.Close()
				return out, err
			}
			canonicalPayload, err := canonical(payload)
			if err != nil {
				rows.Close()
				return out, err
			}
			landingSHA := digest(canonicalPayload)
			coverageRow.LandingPayloadSHA256 = &landingSHA
			ordinal++
			landing = append(landing, qrel)
		} else {
			skips++
		}
		coverageRow.PreviousRoot = root
		leaf, err := json.Marshal(struct {
			SourceOffset         int64   `json:"source_offset"`
			EventID              string  `json:"event_id"`
			EventType            string  `json:"event_type"`
			Status               string  `json:"status"`
			DCInputHash          string  `json:"dc_input_hash"`
			DCReceiptID          string  `json:"dc_receipt_id"`
			AuthoritySHA256      *string `json:"authority_event_sha256"`
			QrelOrdinal          *int64  `json:"qrel_ordinal"`
			LandingPayloadSHA256 *string `json:"landing_payload_sha256"`
		}{coverageRow.SourceOffset, coverageRow.EventID, coverageRow.EventType,
			coverageRow.Status, coverageRow.DCInputHash, coverageRow.DCReceiptID,
			coverageRow.AuthoritySHA256, coverageRow.QrelOrdinal,
			coverageRow.LandingPayloadSHA256})
		if err != nil {
			rows.Close()
			return out, err
		}
		leafCanonical, err := canonical(leaf)
		if err != nil {
			rows.Close()
			return out, err
		}
		coverageRow.LeafSHA256 = digest(leafCanonical)
		root = digest([]byte(root + ":" + coverageRow.LeafSHA256))
		coverageRow.CoverageRoot = root
		encoded, err := json.Marshal(coverageRow)
		if err != nil {
			rows.Close()
			return out, err
		}
		coverage.Write(encoded)
		coverage.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	if ordinal+skips != through || ordinal == 0 {
		return out, ErrContract
	}
	if err := tx.Commit(ctx); err != nil {
		return out, err
	}
	batchID := "rtw-qrel-" + root[:24]
	var landingBytes bytes.Buffer
	for _, event := range landing {
		event["batch_id"] = batchID
		payload := make(map[string]any, len(event))
		for key, value := range event {
			if key != "batch_id" {
				payload[key] = value
			}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return out, err
		}
		canonicalPayload, err := canonical(raw)
		if err != nil {
			return out, err
		}
		event["payload_hash"] = digest(canonicalPayload)
		encoded, err := json.Marshal(event)
		if err != nil {
			return out, err
		}
		landingBytes.Write(encoded)
		landingBytes.WriteByte('\n')
	}
	out.CoverageJSONL, out.LandingJSONL = coverage.Bytes(), landingBytes.Bytes()
	out.Manifest = SubstreamManifest{SchemaVersion: SubstreamSchema, Producer: Producer,
		Consumer: DefaultConsumer, FromOffset: 1, ThroughOffset: through,
		ODSCommittedOffset: committed, CoverageRoot: root,
		CoverageSHA256: digest(out.CoverageJSONL), LandingSHA256: digest(out.LandingJSONL),
		LandingBatchID: batchID, QrelCount: ordinal, TechnicalSkipCount: skips,
		GroupingPolicyID: policy.PolicyID, GroupingPolicySHA256: policySHA,
		DataKind: policy.DataKind, FixtureProvenance: policy.FixtureProvenance,
		Activation: "none", JudgmentScopeComplete: false, SourcePartition: SubstreamPartition}
	return out, nil
}

func projectSubstreamRow(item odsSubstreamRow, groups map[string]grouping, nextOrdinal int64) (
	CoverageRow, map[string]any, error) {
	coverage := CoverageRow{SourceOffset: item.offset, EventID: item.eventID, EventType: item.eventType,
		Status: item.status, DCInputHash: item.dcInputHash, AuthoritySHA256: item.authoritySHA}
	if item.eventID == "" || !shaPattern.MatchString(item.dcInputHash) || item.receivedAt.IsZero() {
		return coverage, nil, ErrContract
	}
	eventCanonical, err := canonical(item.eventSpec)
	if err != nil || digest(eventCanonical) != item.dcInputHash {
		return coverage, nil, ErrContract
	}
	var event eventing.Event
	var receipt eventing.Receipt
	if json.Unmarshal(item.eventSpec, &event) != nil || json.Unmarshal(item.receiptJSON, &receipt) != nil ||
		event.EventID != item.eventID || event.EventType != item.eventType || event.Producer != Producer ||
		event.SchemaVersion != 1 ||
		receipt.EventID != item.eventID || receipt.Producer != Producer || receipt.Offset != item.offset ||
		receipt.InputHash != item.dcInputHash || receipt.ReceiptID == "" ||
		receipt.TechnicalStatus != "accepted" {
		return coverage, nil, ErrContract
	}
	receivedAt, err := time.Parse(time.RFC3339Nano, receipt.ReceivedAt)
	if err != nil || !receivedAt.Equal(item.receivedAt) {
		return coverage, nil, ErrContract
	}
	coverage.DCReceiptID = receipt.ReceiptID
	if item.status == "technical_skip" {
		if isJudgment(item.eventType) || item.authoritySHA != nil || item.judgmentID != nil ||
			item.revisionID != nil || item.payload != nil {
			return coverage, nil, ErrContract
		}
		return coverage, nil, nil
	}
	if item.status != "qrel_revision" || !isJudgment(item.eventType) || item.authoritySHA == nil ||
		item.judgmentID == nil || item.revisionID == nil || item.revision == nil || item.state == nil {
		return coverage, nil, ErrContract
	}
	judgment, err := parseJudgment(event, item.authority, *item.authoritySHA, item.dcInputHash)
	if err != nil || judgment.JudgmentID != *item.judgmentID || judgment.RevisionID != *item.revisionID ||
		judgment.JudgmentRevision != *item.revision || judgment.State != *item.state ||
		item.baseID == nil || judgment.BaseRevisionID != *item.baseID {
		return coverage, nil, ErrContract
	}
	var stored Judgment
	if json.Unmarshal(item.payload, &stored) != nil {
		return coverage, nil, ErrContract
	}
	storedJSON, err := json.Marshal(stored)
	if err != nil {
		return coverage, nil, err
	}
	parsedJSON, err := json.Marshal(judgment)
	if err != nil {
		return coverage, nil, err
	}
	storedCanonical, err := canonical(storedJSON)
	if err != nil {
		return coverage, nil, err
	}
	parsedCanonical, err := canonical(parsedJSON)
	if err != nil || !bytes.Equal(storedCanonical, parsedCanonical) {
		return coverage, nil, ErrContract
	}
	group, ok := groups[judgment.SearchID]
	if !ok || group.textSHA != judgment.QueryTextSHA256 {
		return coverage, nil, ErrContract
	}
	coverage.QrelOrdinal = &nextOrdinal
	state := "active"
	var grade any
	judgedMask := true
	var revoked any
	if item.eventType == Withdrawn {
		state, judgedMask, revoked = "retracted", false, item.receivedAt.UTC().Format(time.RFC3339Nano)
	} else {
		grade = *judgment.Grade
	}
	landing := map[string]any{
		"event_id": item.eventID, "judgment_id": judgment.JudgmentID,
		"judgment_revision": judgment.JudgmentRevision, "status": state,
		"query_id": judgment.SearchID, "query_family_id": group.family,
		"near_duplicate_cluster_id": group.cluster,
		"query_text":                judgment.QueryText, "query_text_sha256": judgment.QueryTextSHA256,
		"document_id": judgment.ContentID, "document_revision": judgment.ContentRevisionID,
		"chunk_id": judgment.ChunkID, "chunk_text": judgment.ChunkText,
		"chunk_text_sha256": judgment.ChunkTextSHA256,
		"relevance_grade":   grade, "judged_mask": judgedMask,
		"judgment_source":      "synthetic_fixture",
		"judgment_source_ref":  "synthetic-rtw-dc-pg/" + item.eventID,
		"judgment_source_hash": *item.authoritySHA,
		"query_time":           judgment.QueryTime, "content_available_at": judgment.ContentAvailableAt,
		"judged_at":        judgment.JudgedAt,
		"available_at":     item.receivedAt.UTC().Format(time.RFC3339Nano),
		"revoked_at":       revoked,
		"source_partition": SubstreamPartition, "source_sequence": nextOrdinal,
	}
	return coverage, landing, nil
}

func ValidateCoverage(bundle SubstreamBundle) error {
	manifest := bundle.Manifest
	if manifest.SchemaVersion != SubstreamSchema || manifest.Producer != Producer ||
		manifest.Consumer != DefaultConsumer || manifest.FromOffset != 1 || manifest.ThroughOffset < 1 ||
		manifest.ODSCommittedOffset < manifest.ThroughOffset || manifest.QrelCount < 1 ||
		manifest.QrelCount+manifest.TechnicalSkipCount != manifest.ThroughOffset ||
		manifest.DataKind != "synthetic" || manifest.FixtureProvenance == "" ||
		manifest.Activation != "none" || manifest.JudgmentScopeComplete ||
		manifest.GroupingPolicyID == "" || !shaPattern.MatchString(manifest.GroupingPolicySHA256) ||
		!shaPattern.MatchString(manifest.CoverageRoot) ||
		manifest.LandingBatchID != "rtw-qrel-"+manifest.CoverageRoot[:24] ||
		manifest.SourcePartition != SubstreamPartition ||
		digest(bundle.CoverageJSONL) != manifest.CoverageSHA256 ||
		digest(bundle.LandingJSONL) != manifest.LandingSHA256 {
		return ErrContract
	}
	root := digest([]byte(SubstreamSchema + ":" + Producer))
	ordinal := int64(0)
	qrelCoverage := make([]CoverageRow, 0, manifest.QrelCount)
	reader := bytes.NewReader(bundle.CoverageJSONL)
	for i := int64(1); i <= manifest.ThroughOffset; i++ {
		line, err := readLine(reader)
		if err != nil {
			return ErrContract
		}
		var row CoverageRow
		if json.Unmarshal(line, &row) != nil || row.SourceOffset != i || row.PreviousRoot != root ||
			row.EventID == "" || row.DCReceiptID == "" {
			return ErrContract
		}
		if row.Status == "qrel_revision" {
			ordinal++
			if row.QrelOrdinal == nil || *row.QrelOrdinal != ordinal || row.AuthoritySHA256 == nil ||
				row.LandingPayloadSHA256 == nil {
				return ErrContract
			}
			qrelCoverage = append(qrelCoverage, row)
		} else if row.Status != "technical_skip" || row.QrelOrdinal != nil ||
			row.AuthoritySHA256 != nil || row.LandingPayloadSHA256 != nil {
			return ErrContract
		}
		leaf, err := json.Marshal(struct {
			SourceOffset         int64   `json:"source_offset"`
			EventID              string  `json:"event_id"`
			EventType            string  `json:"event_type"`
			Status               string  `json:"status"`
			DCInputHash          string  `json:"dc_input_hash"`
			DCReceiptID          string  `json:"dc_receipt_id"`
			AuthoritySHA256      *string `json:"authority_event_sha256"`
			QrelOrdinal          *int64  `json:"qrel_ordinal"`
			LandingPayloadSHA256 *string `json:"landing_payload_sha256"`
		}{row.SourceOffset, row.EventID, row.EventType, row.Status, row.DCInputHash,
			row.DCReceiptID, row.AuthoritySHA256, row.QrelOrdinal,
			row.LandingPayloadSHA256})
		if err != nil {
			return err
		}
		leafCanonical, err := canonical(leaf)
		if err != nil || digest(leafCanonical) != row.LeafSHA256 {
			return ErrContract
		}
		root = digest([]byte(root + ":" + row.LeafSHA256))
		if row.CoverageRoot != root {
			return ErrContract
		}
	}
	if reader.Len() != 0 || root != manifest.CoverageRoot || ordinal != manifest.QrelCount {
		return ErrContract
	}
	landingRows := bytes.Split(bytes.TrimSuffix(bundle.LandingJSONL, []byte{'\n'}), []byte{'\n'})
	if len(landingRows) != int(manifest.QrelCount) {
		return ErrContract
	}
	for i, line := range landingRows {
		var event map[string]any
		if json.Unmarshal(line, &event) != nil || event["batch_id"] != manifest.LandingBatchID ||
			event["source_partition"] != SubstreamPartition || event["source_sequence"] != float64(i+1) ||
			event["event_id"] != qrelCoverage[i].EventID ||
			event["judgment_source_hash"] != *qrelCoverage[i].AuthoritySHA256 ||
			event["judgment_source"] != "synthetic_fixture" {
			return ErrContract
		}
		payloadHash, ok := event["payload_hash"].(string)
		if !ok || !shaPattern.MatchString(payloadHash) {
			return ErrContract
		}
		if payloadHash != *qrelCoverage[i].LandingPayloadSHA256 {
			return ErrContract
		}
		delete(event, "batch_id")
		delete(event, "payload_hash")
		encoded, err := json.Marshal(event)
		if err != nil {
			return err
		}
		canonicalPayload, err := canonical(encoded)
		if err != nil || digest(canonicalPayload) != payloadHash {
			return ErrContract
		}
		if qrelCoverage[i].EventType == Withdrawn {
			if event["status"] != "retracted" || event["relevance_grade"] != nil ||
				event["judged_mask"] != false {
				return ErrContract
			}
		} else if qrelCoverage[i].EventType != Revised || event["status"] != "active" ||
			event["relevance_grade"] == nil || event["judged_mask"] != true {
			return ErrContract
		}
	}
	return nil
}

func readLine(reader *bytes.Reader) ([]byte, error) {
	var line []byte
	for {
		value, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		if value == '\n' {
			return line, nil
		}
		line = append(line, value)
	}
}
