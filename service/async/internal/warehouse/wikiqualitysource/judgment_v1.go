package wikiqualitysource

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
)

const RevisionSchemaV1 = "rtw.wiki.quality-judgment.v1"
const RubricV1 = "sea.wiki.fact-coverage.v1"
const AdminCaptureV1 = "human_admin_jwt_allowlist"

var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)

// JudgmentV1 is the frozen single-fact revision carried by an RTW event. One
// judgment never proves the complete expected FactSet of a page or SourceScope.
type JudgmentV1 struct {
	SchemaVersion       string `json:"schema_version"`
	JudgmentSource      string `json:"judgment_source"`
	JudgmentID          string `json:"judgment_id"`
	FactID              string `json:"fact_id"`
	JudgeRevisionID     string `json:"judge_revision_id"`
	JudgeRevision       string `json:"judge_revision"`
	BaseJudgeRevisionID string `json:"base_judge_revision_id"`
	ModuleID            string `json:"module_id"`
	PageID              string `json:"page_id"`
	WikiRevisionID      string `json:"wiki_revision_id"`
	BaseWikiRevisionID  string `json:"base_wiki_revision_id"`
	WikiOriginKind      string `json:"wiki_origin_kind"`
	OriginCompileID     string `json:"origin_compile_id"`
	WikiContentSHA256   string `json:"wiki_content_sha256"`
	SourceRevisionID    string `json:"source_revision_id"`
	SourceContentSHA256 string `json:"source_content_sha256"`
	Locator             string `json:"locator"`
	SourceByteStart     string `json:"source_byte_start"`
	SourceByteEnd       string `json:"source_byte_end"`
	SourceQuote         string `json:"source_quote"`
	SourceQuoteSHA256   string `json:"source_quote_sha256"`
	WikiClaimText       string `json:"wiki_claim_text"`
	WikiClaimSHA256     string `json:"wiki_claim_sha256"`
	CitationPresent     bool   `json:"citation_present"`
	Assessment          string `json:"assessment"`
	Grade               *int   `json:"grade"`
	RubricVersion       string `json:"rubric_version"`
	Reason              string `json:"reason"`
	ActorID             string `json:"actor_id"`
	JudgedAt            string `json:"judged_at"`
	SourceWithdrawn     bool   `json:"source_withdrawn"`
	WikiWithdrawn       bool   `json:"wiki_withdrawn"`
}

type literalKind byte

const (
	stringKind literalKind = '"'
	boolKind   literalKind = 'b'
	numberKind literalKind = '0'
	objectKind literalKind = '{'
	arrayKind  literalKind = '['
	gradeKind  literalKind = 'g'
)

var eventKeysV1 = map[string]literalKind{
	"aggregate_id": stringKind, "aggregate_version": numberKind,
	"event_id": stringKind, "event_type": stringKind,
	"occurred_at": stringKind, "operation_id": stringKind,
	"payload": objectKind, "producer": stringKind, "schema_version": numberKind,
}

var judgmentKeysV1 = map[string]literalKind{
	"schema_version": stringKind, "judgment_source": stringKind,
	"judgment_id": stringKind, "fact_id": stringKind,
	"judge_revision_id": stringKind, "judge_revision": stringKind,
	"base_judge_revision_id": stringKind, "module_id": stringKind,
	"page_id": stringKind, "wiki_revision_id": stringKind,
	"base_wiki_revision_id": stringKind, "wiki_origin_kind": stringKind,
	"origin_compile_id": stringKind, "wiki_content_sha256": stringKind,
	"source_revision_id": stringKind, "source_content_sha256": stringKind,
	"locator": stringKind, "source_byte_start": stringKind,
	"source_byte_end": stringKind, "source_quote": stringKind,
	"source_quote_sha256": stringKind, "wiki_claim_text": stringKind,
	"wiki_claim_sha256": stringKind, "citation_present": boolKind,
	"assessment": stringKind, "grade": gradeKind,
	"rubric_version": stringKind, "reason": stringKind,
	"actor_id": stringKind, "judged_at": stringKind,
	"source_withdrawn": boolKind, "wiki_withdrawn": boolKind,
}

// exactObject rejects omitted, extra or duplicate literal keys and mismatched
// JSON types. A typed json.Unmarshal alone silently discards these mistakes.
func exactObject(raw []byte, expected map[string]literalKind) error {
	return exactObjectSized(raw, 32<<10, expected)
}

func exactObjectSized(raw []byte, maxBytes int, expected map[string]literalKind) error {
	if len(raw) < 2 || len(raw) > maxBytes {
		return ErrContract
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return ErrContract
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		kind, found := expected[name]
		if err != nil || !ok || !found || seen[name] {
			return ErrContract
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return ErrContract
		}
		value = bytes.TrimSpace(value)
		if len(value) == 0 || !matchKind(value, kind) {
			return ErrContract
		}
		seen[name] = true
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || len(seen) != len(expected) {
		return ErrContract
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrContract
	}
	return nil
}

func matchKind(value []byte, kind literalKind) bool {
	switch kind {
	case stringKind:
		return value[0] == '"'
	case boolKind:
		return bytes.Equal(value, []byte("true")) || bytes.Equal(value, []byte("false"))
	case numberKind:
		return value[0] >= '0' && value[0] <= '9'
	case objectKind:
		return value[0] == '{'
	case arrayKind:
		return value[0] == '['
	case gradeKind:
		return bytes.Equal(value, []byte("null")) || value[0] >= '0' && value[0] <= '9'
	default:
		return false
	}
}

func optionalID(id string) bool { return id == "" || idPattern.MatchString(id) }

// RTW's present administrator Auth claim is a bounded userId literal, not a
// UserCenter UID. Reject controls while preserving admitted UTF-8 names.
func validCaptureActor(actor string) bool {
	if strings.TrimSpace(actor) == "" || len(actor) > 200 {
		return false
	}
	for _, ch := range actor {
		if ch < 0x20 || ch == 0x7f {
			return false
		}
	}
	return true
}

func canonicalDecimal(value string, positive bool) (int64, bool) {
	n, err := strconv.ParseInt(value, 10, 64)
	return n, err == nil && n >= 0 && (!positive || n > 0) &&
		strconv.FormatInt(n, 10) == value
}

// ParseJudgmentV1 validates exact RTW v1 literals after the consumer has
// proven RTW's original Event bytes and DC's whole-JCS input hash. RTW's
// Worker original-event read also checks the immutable Wiki/Source objects;
// this parser cannot substitute for RTW's source-of-truth FactSet inventory.
func ParseJudgmentV1(event eventing.Event, original []byte) (JudgmentV1, error) {
	var value JudgmentV1
	dcRaw, err := json.Marshal(event)
	if err != nil {
		return value, ErrContract
	}
	dcCanonical, dcErr := canonical(dcRaw)
	originalCanonical, originalErr := canonical(original)
	if !qualityEvent(event.EventType) || event.SchemaVersion != 1 || event.Producer != Producer ||
		event.AggregateVersion < 1 || !idPattern.MatchString(event.AggregateID) ||
		!idPattern.MatchString(event.EventID) || event.OperationID == "" ||
		dcErr != nil || originalErr != nil || !bytes.Equal(dcCanonical, originalCanonical) ||
		exactObject(original, eventKeysV1) != nil ||
		exactObject(event.Payload, judgmentKeysV1) != nil ||
		json.Unmarshal(event.Payload, &value) != nil {
		return JudgmentV1{}, ErrContract
	}
	if value.SchemaVersion != RevisionSchemaV1 || value.JudgmentSource != AdminCaptureV1 ||
		value.RubricVersion != RubricV1 || value.ModuleID != event.AggregateID ||
		!idPattern.MatchString(value.JudgmentID) ||
		!idPattern.MatchString(value.JudgeRevisionID) || !idPattern.MatchString(value.WikiRevisionID) ||
		!idPattern.MatchString(value.SourceRevisionID) || !validCaptureActor(value.ActorID) ||
		!optionalID(value.BaseJudgeRevisionID) || !optionalID(value.BaseWikiRevisionID) ||
		!optionalID(value.OriginCompileID) || len(value.PageID) == 0 || len(value.PageID) > 200 ||
		!utf8.ValidString(value.PageID) || strings.TrimSpace(value.PageID) != value.PageID ||
		!shaPattern.MatchString(value.WikiContentSHA256) ||
		!shaPattern.MatchString(value.SourceContentSHA256) ||
		!shaPattern.MatchString(value.SourceQuoteSHA256) ||
		value.FactID != "fact_"+digest([]byte(value.SourceRevisionID+"\x00"+
			value.Locator+"\x00"+value.SourceQuoteSHA256)) ||
		!utf8.ValidString(value.SourceQuote) || len(value.SourceQuote) < 1 ||
		len(value.SourceQuote) > 4096 || strings.TrimSpace(value.SourceQuote) == "" ||
		strings.ContainsRune(value.SourceQuote, 0) ||
		digest([]byte(value.SourceQuote)) != value.SourceQuoteSHA256 ||
		!utf8.ValidString(value.Reason) || len(value.Reason) < 1 || len(value.Reason) > 2000 ||
		strings.TrimSpace(value.Reason) == "" || strings.ContainsRune(value.Reason, 0) {
		return JudgmentV1{}, ErrContract
	}
	if !strings.HasPrefix(value.Locator, "paragraph:") {
		return JudgmentV1{}, ErrContract
	}
	ordinal, ok := canonicalDecimal(strings.TrimPrefix(value.Locator, "paragraph:"), true)
	if !ok || ordinal < 1 {
		return JudgmentV1{}, ErrContract
	}
	revision, ok := canonicalDecimal(value.JudgeRevision, true)
	if !ok || revision < 1 || (revision == 1 && value.BaseJudgeRevisionID != "") ||
		(revision > 1 && value.BaseJudgeRevisionID == "") {
		return JudgmentV1{}, ErrContract
	}
	start, ok := canonicalDecimal(value.SourceByteStart, false)
	if !ok {
		return JudgmentV1{}, ErrContract
	}
	end, ok := canonicalDecimal(value.SourceByteEnd, true)
	if !ok || end <= start || end-start != int64(len(value.SourceQuote)) {
		return JudgmentV1{}, ErrContract
	}
	if (value.WikiOriginKind == "manual_revision" && value.OriginCompileID != "") ||
		(value.WikiOriginKind == "ai_accepted" && value.OriginCompileID == "") ||
		(value.WikiOriginKind != "manual_revision" && value.WikiOriginKind != "ai_accepted") {
		return JudgmentV1{}, ErrContract
	}
	if value.WikiClaimText == "" {
		if value.WikiClaimSHA256 != "" || value.Assessment == "covered" || value.Assessment == "conflict" {
			return JudgmentV1{}, ErrContract
		}
	} else if !utf8.ValidString(value.WikiClaimText) || len(value.WikiClaimText) > 4096 ||
		!shaPattern.MatchString(value.WikiClaimSHA256) ||
		digest([]byte(value.WikiClaimText)) != value.WikiClaimSHA256 ||
		strings.ContainsRune(value.WikiClaimText, 0) ||
		value.Assessment == "missing" {
		return JudgmentV1{}, ErrContract
	}
	switch value.Assessment {
	case "undetermined":
		if value.Grade != nil {
			return JudgmentV1{}, ErrContract
		}
	case "missing", "conflict":
		if value.Grade == nil || *value.Grade != 0 {
			return JudgmentV1{}, ErrContract
		}
	case "covered":
		if value.Grade == nil || *value.Grade < 1 || *value.Grade > 3 ||
			(*value.Grade >= 2 && !value.CitationPresent) {
			return JudgmentV1{}, ErrContract
		}
	default:
		return JudgmentV1{}, ErrContract
	}
	if (value.SourceWithdrawn || value.WikiWithdrawn) && value.Assessment != "undetermined" {
		return JudgmentV1{}, ErrContract
	}
	if _, err := time.Parse(time.RFC3339Nano, value.JudgedAt); err != nil {
		return JudgmentV1{}, ErrContract
	}
	if _, err := time.Parse(time.RFC3339Nano, event.OccurredAt); err != nil {
		return JudgmentV1{}, ErrContract
	}
	return value, nil
}

// V1Verifier does not infer current administrator eligibility or a complete
// page FactSet. Consumer still requires the RTW original-event Authority.
type V1Verifier struct{}

func (V1Verifier) VerifyQualityEvent(ctx context.Context, event eventing.Event, original []byte) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrContract
	}
	_, err := ParseJudgmentV1(event, original)
	return err
}
