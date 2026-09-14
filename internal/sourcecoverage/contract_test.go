package sourcecoverage

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

const receiptTime = "2026-09-15T00:00:00Z"

func indexRow(offset int, subject SubjectRef) EventIndexRow {
	event := json.RawMessage(fmt.Sprintf(`{"producer":"rtw.community.favorite","event_id":"event-%d","payload":{"favorite_id":"9007199254740995"}}`, offset))
	canonical, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	// The contract hashes the JCS EventSpec, not a Go map's field order.
	canonical, err = jsoncanonicalizer.Transform(canonical)
	if err != nil {
		panic(err)
	}
	digest := hash(canonical)
	return EventIndexRow{Producer: "rtw.community.favorite", Offset: fmt.Sprint(offset),
		EventID: fmt.Sprintf("event-%d", offset), InputHash: digest, RTWSourceHash: digest,
		ReceiptID: fmt.Sprintf("receipt-%d", offset), ReceivedAt: receiptTime, Subject: subject, EventSpec: event}
}

func TestCompletePrefixDerivesExactSparseAndEmptySlices(t *testing.T) {
	u1 := SubjectRef{"rtw.identity", "platform", "1001"}
	u2 := SubjectRef{"rtw.identity", "platform", "1002"}
	u3 := SubjectRef{"rtw.identity", "platform", "1003"}
	rows := []EventIndexRow{indexRow(1, u1), indexRow(2, u2), indexRow(3, u1)}
	full, root, err := EventIndexJSONL("rtw.community.favorite", 3, rows)
	if err != nil || strings.Count(string(full), "\n") != 3 || !hashPattern.MatchString(root) {
		t.Fatalf("complete prefix: root=%s err=%v", root, err)
	}
	for _, tc := range []struct {
		name    string
		subject SubjectRef
		count   int64
		offsets []string
	}{
		{"first", u1, 2, []string{`"offset":"1"`, `"offset":"3"`}},
		{"second", u2, 1, []string{`"offset":"2"`}},
		{"absent", u3, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, digest, count, err := SubjectIndexJSONL(tc.subject, rows)
			if err != nil || count != tc.count || digest != hash(body) {
				t.Fatalf("subject slice: count=%d hash=%s err=%v", count, digest, err)
			}
			for _, offset := range tc.offsets {
				if !strings.Contains(string(body), offset) {
					t.Fatalf("missing %s from %q", offset, body)
				}
			}
			if tc.count == 0 && len(body) != 0 {
				t.Fatalf("absent subject has events: %q", body)
			}
		})
	}
	if _, _, err := EventIndexJSONL("rtw.community.favorite", 3, rows[:2]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing global offset accepted: %v", err)
	}
	wrong := append([]EventIndexRow{}, rows...)
	wrong[1].Offset = "3"
	if _, _, err := EventIndexJSONL("rtw.community.favorite", 3, wrong); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate/gap accepted: %v", err)
	}
	tampered := append([]EventIndexRow{}, rows...)
	tampered[1].Subject = u1
	tampered[1].RTWSourceHash = strings.Repeat("f", 64)
	if _, _, err := EventIndexJSONL("rtw.community.favorite", 3, tampered); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong source hash accepted: %v", err)
	}
}

func TestBatchWindowsAndSelfHashes(t *testing.T) {
	batch, batchHash, err := BatchEvidenceJSONL([]BatchEvidence{
		{FromOffset: "1", ToOffset: "2", BatchHash: strings.Repeat("a", 64)},
		{FromOffset: "3", ToOffset: "3", BatchHash: strings.Repeat("b", 64)},
	}, 3)
	if err != nil || len(batch) == 0 || batchHash != hash(batch) {
		t.Fatalf("batch chain: %s %v", batchHash, err)
	}
	if _, _, err := BatchEvidenceJSONL([]BatchEvidence{{FromOffset: "1", ToOffset: "1", BatchHash: strings.Repeat("a", 64)},
		{FromOffset: "3", ToOffset: "3", BatchHash: strings.Repeat("b", 64)}}, 3); !errors.Is(err, ErrInvalid) {
		t.Fatalf("batch gap accepted: %v", err)
	}
	global := GlobalPrefixRef{SchemaVersion: SchemaVersion, Producer: "rtw.community.favorite", Origin: "1",
		ThroughOffset: "3", BindingPolicyID: "rtw.favorite.authority.v1", WarehouseConsumer: "btw-warehouse-favorite",
		WarehouseGeneration: "favorite-g1", EventIndexURL: "s3://test/full.jsonl", EventIndexSHA256: strings.Repeat("c", 64),
		BatchEvidenceURL: "s3://test/batches.jsonl", BatchEvidenceSHA256: batchHash}
	global.ManifestSHA256, err = GlobalManifestHash(global)
	if err != nil {
		t.Fatal(err)
	}
	ref := SubjectCoverageRef{SchemaVersion: SchemaVersion, Subject: SubjectRef{"rtw.identity", "platform", "1001"},
		Prefix: global, SparseIndexURL: "s3://test/u1.jsonl", SparseIndexSHA256: strings.Repeat("d", 64), EventCount: 2}
	ref.ReceiptSHA256, err = SubjectReceiptHash(ref)
	if err != nil || !hashPattern.MatchString(ref.ReceiptSHA256) {
		t.Fatalf("subject receipt: %s %v", ref.ReceiptSHA256, err)
	}
	ref.Prefix.ManifestSHA256 = strings.Repeat("0", 64)
	if _, err := SubjectReceiptHash(ref); !errors.Is(err, ErrInvalid) {
		t.Fatalf("forged global root accepted: %v", err)
	}
}
