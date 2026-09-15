package wikiqualitysource

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
)

const FactSetFrozenV1 = "knowledge.wiki.fact-set.frozen.v1"
const FactSetRevisionSchemaV1 = "rtw.wiki.fact-set-revision.v1"
const FactSetDeclarationSourceV1 = "admin_jwt_allowlist_declaration"

var factSetKeysV1 = map[string]literalKind{
	"schema_version": stringKind, "fact_set_id": stringKind,
	"fact_set_revision_id": stringKind, "fact_set_revision": stringKind,
	"base_fact_set_revision_id": stringKind, "module_id": stringKind,
	"page_id": stringKind, "wiki_revision_id": stringKind,
	"base_wiki_revision_id": stringKind, "wiki_origin_kind": stringKind,
	"origin_compile_id": stringKind, "wiki_content_sha256": stringKind,
	"source_scope_revision": stringKind, "source_revisions": arrayKind,
	"facts": arrayKind, "facts_complete": boolKind,
	"declaration_source": stringKind, "actor_id": stringKind,
	"reason": stringKind, "frozen_at": stringKind,
}

var factSetSourceKeysV1 = map[string]literalKind{
	"revision_id": stringKind, "content_sha256": stringKind,
}

var factSetFactKeysV1 = map[string]literalKind{
	"fact_id": stringKind, "source_revision_id": stringKind,
	"source_content_sha256": stringKind, "locator": stringKind,
	"source_byte_start": stringKind, "source_byte_end": stringKind,
	"source_quote": stringKind, "source_quote_sha256": stringKind,
	"required": boolKind, "conflict_group": stringKind,
}

type FactSetSourceV1 struct {
	RevisionID    string `json:"revision_id"`
	ContentSHA256 string `json:"content_sha256"`
}

type FactSetFactV1 struct {
	FactID              string `json:"fact_id"`
	SourceRevisionID    string `json:"source_revision_id"`
	SourceContentSHA256 string `json:"source_content_sha256"`
	Locator             string `json:"locator"`
	SourceByteStart     string `json:"source_byte_start"`
	SourceByteEnd       string `json:"source_byte_end"`
	SourceQuote         string `json:"source_quote"`
	SourceQuoteSHA256   string `json:"source_quote_sha256"`
	Required            bool   `json:"required"`
	ConflictGroup       string `json:"conflict_group"`
}

// FactSetV1 carries the administrator's explicit completeness declaration
// over a pinned, approved Source scope. The declaration is not an objective
// proof that the administrator found every expected fact or a D07 threshold.
type FactSetV1 struct {
	SchemaVersion         string            `json:"schema_version"`
	FactSetID             string            `json:"fact_set_id"`
	FactSetRevisionID     string            `json:"fact_set_revision_id"`
	FactSetRevision       string            `json:"fact_set_revision"`
	BaseFactSetRevisionID string            `json:"base_fact_set_revision_id"`
	ModuleID              string            `json:"module_id"`
	PageID                string            `json:"page_id"`
	WikiRevisionID        string            `json:"wiki_revision_id"`
	BaseWikiRevisionID    string            `json:"base_wiki_revision_id"`
	WikiOriginKind        string            `json:"wiki_origin_kind"`
	OriginCompileID       string            `json:"origin_compile_id"`
	WikiContentSHA256     string            `json:"wiki_content_sha256"`
	SourceScopeRevision   string            `json:"source_scope_revision"`
	SourceRevisions       []FactSetSourceV1 `json:"source_revisions"`
	Facts                 []FactSetFactV1   `json:"facts"`
	FactsComplete         bool              `json:"facts_complete"`
	DeclarationSource     string            `json:"declaration_source"`
	ActorID               string            `json:"actor_id"`
	Reason                string            `json:"reason"`
	FrozenAt              string            `json:"frozen_at"`
}

func factSetArrayObjects(raw json.RawMessage, maxCount int, keys map[string]literalKind) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('[') {
		return ErrContract
	}
	count := 0
	for decoder.More() {
		var element json.RawMessage
		if decoder.Decode(&element) != nil || exactObjectSized(element, 1<<16, keys) != nil {
			return ErrContract
		}
		count++
		if count > maxCount {
			return ErrContract
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim(']') || count == 0 {
		return ErrContract
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrContract
	}
	return nil
}

func validConflictGroupV1(group string) bool {
	if group == "" {
		return true
	}
	if len(group) > 64 {
		return false
	}
	for _, r := range group {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

// ParseFactSetV1 proves exact source Event/FactSet JCS bytes and typed v1
// fields. RTW's private original Event reader must also prove each immutable
// Source object, current eligibility and the actual Wiki/Compile source set.
// The most recent Scope head cannot substitute for this explicit historical
// FactSetRevisionID when evaluating an earlier target Wiki revision.
func ParseFactSetV1(event eventing.Event, original []byte, factSetJCSSHA string) (FactSetV1, error) {
	var value FactSetV1
	if event.EventType != FactSetFrozenV1 || event.SchemaVersion != 1 ||
		event.Producer != Producer || event.AggregateVersion < 1 ||
		!idPattern.MatchString(event.EventID) || !idPattern.MatchString(event.AggregateID) ||
		event.OperationID == "" || !shaPattern.MatchString(factSetJCSSHA) ||
		exactObjectSized(original, 2<<20, eventKeysV1) != nil ||
		exactObjectSized(event.Payload, 2<<20, factSetKeysV1) != nil {
		return value, ErrContract
	}
	originalCanonical, err := canonical(original)
	if err != nil {
		return value, ErrContract
	}
	dcRaw, err := json.Marshal(event)
	if err != nil {
		return value, ErrContract
	}
	dcCanonical, err := canonical(dcRaw)
	if err != nil || !bytes.Equal(originalCanonical, dcCanonical) {
		return value, ErrContract
	}
	factSetCanonical, err := canonical(event.Payload)
	if err != nil || digest(factSetCanonical) != factSetJCSSHA {
		return value, ErrContract
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(event.Payload, &payload) != nil ||
		factSetArrayObjects(payload["source_revisions"], 64, factSetSourceKeysV1) != nil ||
		factSetArrayObjects(payload["facts"], 128, factSetFactKeysV1) != nil ||
		json.Unmarshal(event.Payload, &value) != nil {
		return FactSetV1{}, ErrContract
	}
	if value.SchemaVersion != FactSetRevisionSchemaV1 ||
		value.DeclarationSource != FactSetDeclarationSourceV1 || !value.FactsComplete ||
		value.ModuleID != event.AggregateID || !idPattern.MatchString(value.FactSetID) ||
		!idPattern.MatchString(value.FactSetRevisionID) ||
		!optionalID(value.BaseFactSetRevisionID) ||
		!idPattern.MatchString(value.WikiRevisionID) ||
		!optionalID(value.BaseWikiRevisionID) ||
		!idPattern.MatchString(value.ModuleID) ||
		len(value.PageID) < 1 || len(value.PageID) > 200 ||
		!utf8.ValidString(value.PageID) || strings.TrimSpace(value.PageID) != value.PageID ||
		!shaPattern.MatchString(value.WikiContentSHA256) ||
		!validCaptureActor(value.ActorID) ||
		!utf8.ValidString(value.Reason) || len(value.Reason) < 1 || len(value.Reason) > 2000 ||
		strings.TrimSpace(value.Reason) == "" || strings.ContainsRune(value.Reason, 0) ||
		len(value.SourceRevisions) < 1 || len(value.SourceRevisions) > 64 ||
		len(value.Facts) < 1 || len(value.Facts) > 128 {
		return FactSetV1{}, ErrContract
	}
	revision, ok := canonicalDecimal(value.FactSetRevision, true)
	if !ok || (revision == 1 && value.BaseFactSetRevisionID != "") ||
		(revision > 1 && value.BaseFactSetRevisionID == "") {
		return FactSetV1{}, ErrContract
	}
	if (value.WikiOriginKind == "ai_accepted" && !idPattern.MatchString(value.OriginCompileID)) ||
		(value.WikiOriginKind == "manual_revision" && value.OriginCompileID != "") ||
		(value.WikiOriginKind != "ai_accepted" && value.WikiOriginKind != "manual_revision") {
		return FactSetV1{}, ErrContract
	}
	frozenAt, err := time.Parse(time.RFC3339Nano, value.FrozenAt)
	if err != nil || frozenAt.IsZero() || frozenAt.Location() != time.UTC {
		return FactSetV1{}, ErrContract
	}
	if _, err := time.Parse(time.RFC3339Nano, event.OccurredAt); err != nil {
		return FactSetV1{}, ErrContract
	}
	known := map[string]string{}
	previousSource := ""
	for _, source := range value.SourceRevisions {
		if !idPattern.MatchString(source.RevisionID) ||
			!shaPattern.MatchString(source.ContentSHA256) ||
			(previousSource != "" && source.RevisionID <= previousSource) {
			return FactSetV1{}, ErrContract
		}
		known[source.RevisionID] = source.ContentSHA256
		previousSource = source.RevisionID
	}
	scopeClaim := struct {
		ModuleID        string            `json:"module_id"`
		PageID          string            `json:"page_id"`
		SourceRevisions []FactSetSourceV1 `json:"source_revisions"`
	}{value.ModuleID, value.PageID, value.SourceRevisions}
	scopeRaw, err := json.Marshal(scopeClaim)
	if err != nil {
		return FactSetV1{}, ErrContract
	}
	scopeCanonical, err := canonical(scopeRaw)
	if err != nil || value.SourceScopeRevision != "scope_"+digest(scopeCanonical) {
		return FactSetV1{}, ErrContract
	}
	previousFact := ""
	groups := map[string]int{}
	requiredCount := 0
	for _, fact := range value.Facts {
		if fact.FactID != "fact_"+digest([]byte(fact.SourceRevisionID+"\x00"+
			fact.Locator+"\x00"+fact.SourceQuoteSHA256)) ||
			(previousFact != "" && fact.FactID <= previousFact) ||
			known[fact.SourceRevisionID] == "" ||
			fact.SourceContentSHA256 != known[fact.SourceRevisionID] ||
			!strings.HasPrefix(fact.Locator, "paragraph:") ||
			!shaPattern.MatchString(fact.SourceQuoteSHA256) ||
			!utf8.ValidString(fact.SourceQuote) || len(fact.SourceQuote) < 1 ||
			len(fact.SourceQuote) > 4096 || strings.TrimSpace(fact.SourceQuote) == "" ||
			strings.ContainsRune(fact.SourceQuote, 0) ||
			digest([]byte(fact.SourceQuote)) != fact.SourceQuoteSHA256 ||
			!validConflictGroupV1(fact.ConflictGroup) {
			return FactSetV1{}, ErrContract
		}
		if ordinal, ok := canonicalDecimal(strings.TrimPrefix(fact.Locator, "paragraph:"), true); !ok || ordinal < 1 {
			return FactSetV1{}, ErrContract
		}
		start, ok := canonicalDecimal(fact.SourceByteStart, false)
		if !ok {
			return FactSetV1{}, ErrContract
		}
		end, ok := canonicalDecimal(fact.SourceByteEnd, true)
		if !ok || end <= start || end-start != int64(len(fact.SourceQuote)) {
			return FactSetV1{}, ErrContract
		}
		if fact.Required {
			requiredCount++
		}
		if fact.ConflictGroup != "" {
			groups[fact.ConflictGroup]++
		}
		previousFact = fact.FactID
	}
	if requiredCount < 1 {
		return FactSetV1{}, ErrContract
	}
	for _, count := range groups {
		if count < 2 {
			return FactSetV1{}, ErrContract
		}
	}
	return value, nil
}
