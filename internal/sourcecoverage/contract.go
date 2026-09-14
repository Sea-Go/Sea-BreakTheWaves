// Package sourcecoverage defines the immutable source-index contract shared by
// warehouse publication and user-model verification. It owns no database or
// network client; each domain retains its own consumer and acceptance state.
package sourcecoverage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

const SchemaVersion = 2
const MaxDCOffset int64 = 9007199254740991

var ErrInvalid = errors.New("invalid source coverage contract")
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type SubjectRef struct {
	AuthorityID string `json:"authority_id"`
	TenantID    string `json:"tenant_id"`
	SubjectID   string `json:"subject_id"`
}

func (s SubjectRef) valid() bool {
	return s.AuthorityID != "" && s.TenantID != "" && s.SubjectID != ""
}

// EventIndexRow contains one DC producer position and the RTW-verified owner.
// Offset is a canonical decimal string; a position belongs to the producer,
// never to one SubjectRef's own consecutive sequence.
type EventIndexRow struct {
	Producer      string          `json:"producer"`
	Offset        string          `json:"offset"`
	EventID       string          `json:"event_id"`
	InputHash     string          `json:"input_hash"`
	RTWSourceHash string          `json:"rtw_source_hash"`
	ReceiptID     string          `json:"receipt_id"`
	ReceivedAt    string          `json:"received_at"`
	Subject       SubjectRef      `json:"subject_ref"`
	EventSpec     json.RawMessage `json:"event_spec"`
}

type BatchEvidence struct {
	FromOffset string `json:"from_offset"`
	ToOffset   string `json:"to_offset"`
	BatchHash  string `json:"batch_hash"`
}

// GlobalPrefixRef points to the complete [1,W] event index. The batch chain
// records the publishing consumer's actual read windows; another consumer may
// use different batch sizes and compares EventIndexSHA256 instead.
type GlobalPrefixRef struct {
	SchemaVersion       int    `json:"schema_version"`
	Producer            string `json:"producer"`
	Origin              string `json:"origin"`
	ThroughOffset       string `json:"through_offset"`
	BindingPolicyID     string `json:"binding_policy_id"`
	WarehouseConsumer   string `json:"warehouse_consumer"`
	WarehouseGeneration string `json:"warehouse_generation"`
	EventIndexURL       string `json:"event_index_url"`
	EventIndexSHA256    string `json:"event_index_sha256"`
	BatchEvidenceURL    string `json:"batch_evidence_url"`
	BatchEvidenceSHA256 string `json:"batch_evidence_sha256"`
	ManifestSHA256      string `json:"manifest_sha256"`
}

// SubjectCoverageRef is meaningful only after the full GlobalPrefixRef index
// has been independently verified. A sparse list alone cannot prove absence.
type SubjectCoverageRef struct {
	SchemaVersion     int             `json:"schema_version"`
	Subject           SubjectRef      `json:"subject_ref"`
	Prefix            GlobalPrefixRef `json:"global_prefix"`
	SparseIndexURL    string          `json:"sparse_index_url"`
	SparseIndexSHA256 string          `json:"sparse_index_sha256"`
	EventCount        int64           `json:"event_count"`
	ReceiptSHA256     string          `json:"receipt_sha256"`
}

func positiveDecimal(raw string) (int64, bool) {
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil && n > 0 && n <= MaxDCOffset && strconv.FormatInt(n, 10) == raw
}

func canonical(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return jsoncanonicalizer.Transform(raw)
}

func hash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func validRow(row EventIndexRow) bool {
	if row.Producer == "" || row.EventID == "" || row.ReceiptID == "" || !row.Subject.valid() ||
		!hashPattern.MatchString(row.InputHash) || !hashPattern.MatchString(row.RTWSourceHash) ||
		row.InputHash != row.RTWSourceHash || len(row.EventSpec) == 0 {
		return false
	}
	if _, ok := positiveDecimal(row.Offset); !ok {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, row.ReceivedAt); err != nil {
		return false
	}
	var event struct {
		Producer string `json:"producer"`
		EventID  string `json:"event_id"`
	}
	if json.Unmarshal(row.EventSpec, &event) != nil || event.Producer != row.Producer || event.EventID != row.EventID {
		return false
	}
	canonicalEvent, err := jsoncanonicalizer.Transform(row.EventSpec)
	return err == nil && hash(canonicalEvent) == row.InputHash
}

// EventIndexJSONL verifies and freezes one complete producer prefix. It does
// not sort or silently fill a missing offset; callers must supply [1,W].
func EventIndexJSONL(producer string, through int64, rows []EventIndexRow) ([]byte, string, error) {
	if producer == "" || through < 1 || int64(len(rows)) != through {
		return nil, "", fmt.Errorf("%w: incomplete global prefix", ErrInvalid)
	}
	var out bytes.Buffer
	for i, row := range rows {
		position, ok := positiveDecimal(row.Offset)
		if !ok || position != int64(i+1) || row.Producer != producer || !validRow(row) {
			return nil, "", fmt.Errorf("%w: event index offset %d", ErrInvalid, i+1)
		}
		line, err := canonical(row)
		if err != nil {
			return nil, "", err
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.Bytes(), hash(out.Bytes()), nil
}

// SubjectIndexJSONL derives the *entire* slice for a subject from a validated
// global index. Its empty result is a proof only when tied to that full index.
func SubjectIndexJSONL(subject SubjectRef, full []EventIndexRow) ([]byte, string, int64, error) {
	if !subject.valid() {
		return nil, "", 0, ErrInvalid
	}
	var out bytes.Buffer
	var count int64
	for _, row := range full {
		if row.Subject != subject {
			continue
		}
		line, err := canonical(row)
		if err != nil {
			return nil, "", 0, err
		}
		out.Write(line)
		out.WriteByte('\n')
		count++
	}
	return out.Bytes(), hash(out.Bytes()), count, nil
}

// BatchEvidenceJSONL freezes the publisher's read windows. This hash is
// provenance for that consumer, not a cross-consumer equality condition.
func BatchEvidenceJSONL(batches []BatchEvidence, through int64) ([]byte, string, error) {
	if through < 1 || len(batches) == 0 {
		return nil, "", ErrInvalid
	}
	var out bytes.Buffer
	var next int64 = 1
	for _, batch := range batches {
		from, fromOK := positiveDecimal(batch.FromOffset)
		to, toOK := positiveDecimal(batch.ToOffset)
		if !fromOK || !toOK || from != next || to < from || !hashPattern.MatchString(batch.BatchHash) {
			return nil, "", ErrInvalid
		}
		line, err := canonical(batch)
		if err != nil {
			return nil, "", err
		}
		out.Write(line)
		out.WriteByte('\n')
		next = to + 1
	}
	if next != through+1 {
		return nil, "", ErrInvalid
	}
	return out.Bytes(), hash(out.Bytes()), nil
}

// BatchHash reconstructs the DC eventing batch hash over its actual Item
// shape. A publisher's read-window hash must be verified against the same
// frozen EventSpec rows, rather than trusted because it looks like a digest.
func BatchHash(rows []EventIndexRow) (string, error) {
	if len(rows) == 0 || len(rows) > 128 {
		return "", ErrInvalid
	}
	type item struct {
		Offset    int64           `json:"offset"`
		InputHash string          `json:"input_hash"`
		Event     json.RawMessage `json:"event"`
	}
	items := make([]item, 0, len(rows))
	var previous int64
	for i, row := range rows {
		offset, ok := positiveDecimal(row.Offset)
		if !ok || !validRow(row) || i > 0 && offset != previous+1 {
			return "", ErrInvalid
		}
		items = append(items, item{Offset: offset, InputHash: row.InputHash, Event: row.EventSpec})
		previous = offset
	}
	encoded, err := canonical(items)
	if err != nil {
		return "", err
	}
	return hash(encoded), nil
}

// VerifyBatchEvidence checks every publishing read window against the exact
// EventIndex rows. Consumers with different window sizes compare the complete
// EventIndex root instead of demanding the same batch-evidence root.
func VerifyBatchEvidence(producer string, full []EventIndexRow, batches []BatchEvidence) error {
	if _, _, err := EventIndexJSONL(producer, int64(len(full)), full); err != nil {
		return err
	}
	if _, _, err := BatchEvidenceJSONL(batches, int64(len(full))); err != nil {
		return err
	}
	for _, batch := range batches {
		from, _ := positiveDecimal(batch.FromOffset)
		to, _ := positiveDecimal(batch.ToOffset)
		computed, err := BatchHash(full[int(from-1):int(to)])
		if err != nil || computed != batch.BatchHash {
			return fmt.Errorf("%w: batch hash differs from event index", ErrInvalid)
		}
	}
	return nil
}

func GlobalManifestHash(ref GlobalPrefixRef) (string, error) {
	if ref.SchemaVersion != SchemaVersion || ref.Producer == "" || ref.Origin != "1" ||
		ref.BindingPolicyID == "" || ref.WarehouseConsumer == "" || ref.WarehouseGeneration == "" ||
		ref.EventIndexURL == "" || ref.BatchEvidenceURL == "" ||
		!hashPattern.MatchString(ref.EventIndexSHA256) || !hashPattern.MatchString(ref.BatchEvidenceSHA256) {
		return "", ErrInvalid
	}
	if _, ok := positiveDecimal(ref.ThroughOffset); !ok {
		return "", ErrInvalid
	}
	ref.ManifestSHA256 = ""
	bytes, err := canonical(ref)
	if err != nil {
		return "", err
	}
	return hash(bytes), nil
}

func SubjectReceiptHash(ref SubjectCoverageRef) (string, error) {
	if ref.SchemaVersion != SchemaVersion || !ref.Subject.valid() || ref.SparseIndexURL == "" ||
		!hashPattern.MatchString(ref.SparseIndexSHA256) || ref.EventCount < 0 {
		return "", ErrInvalid
	}
	manifestHash, err := GlobalManifestHash(ref.Prefix)
	if err != nil || manifestHash != ref.Prefix.ManifestSHA256 {
		return "", ErrInvalid
	}
	ref.ReceiptSHA256 = ""
	bytes, err := canonical(ref)
	if err != nil {
		return "", err
	}
	return hash(bytes), nil
}
