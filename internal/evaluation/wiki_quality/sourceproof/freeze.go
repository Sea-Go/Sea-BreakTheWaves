package sourceproof

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

func validateCatalog(c CatalogProof) (map[string]CatalogFact, []string, int64, error) {
	if !idPattern.MatchString(c.ModuleID) || strings.TrimSpace(c.PageID) == "" ||
		!utf8.ValidString(c.PageID) || len(c.PageID) > 200 ||
		!idPattern.MatchString(c.WikiRevisionID) ||
		!strings.HasPrefix(c.SourceScopeRevision, "scope_") ||
		!validSHA(strings.TrimPrefix(c.SourceScopeRevision, "scope_")) ||
		!idPattern.MatchString(c.FactSetRevisionID) ||
		!validSHA(c.FactSetPayloadJCSSHA256) ||
		!c.FactsCompleteDeclared || c.DeclarationSource != Declaration || !actor(c.RTWActorID) ||
		len(c.Sources) < 1 || len(c.Sources) > 64 ||
		len(c.Facts) < 1 || len(c.Facts) > 128 ||
		len(c.FactSetPayloadJCS) < 2 || len(c.FactSetPayloadJCS) > 768<<10 {
		return nil, nil, 0, ErrProof
	}
	canonical, err := jsoncanonicalizer.Transform(c.FactSetPayloadJCS)
	if err != nil || !bytes.Equal(canonical, c.FactSetPayloadJCS) ||
		digest(canonical) != c.FactSetPayloadJCSSHA256 {
		return nil, nil, 0, ErrProof
	}
	rootOffset, err := event(c.Event)
	if err != nil {
		return nil, nil, 0, err
	}
	facts := make(map[string]CatalogFact, len(c.Facts))
	sources := make(map[string]string, len(c.Sources))
	previousSource := ""
	for _, source := range c.Sources {
		if !idPattern.MatchString(source.RevisionID) || !validSHA(source.ContentSHA256) ||
			source.RevisionID <= previousSource {
			return nil, nil, 0, ErrProof
		}
		sources[source.RevisionID] = source.ContentSHA256
		previousSource = source.RevisionID
	}
	required := make([]string, 0, len(c.Facts))
	for _, fact := range c.Facts {
		if !idPattern.MatchString(fact.SourceRevisionID) ||
			!locatorPattern.MatchString(fact.Locator) ||
			!validSHA(fact.SourceContentSHA256) || !validSHA(fact.SourceQuoteSHA256) ||
			sources[fact.SourceRevisionID] != fact.SourceContentSHA256 ||
			fact.FactID != factID(fact.SourceRevisionID, fact.Locator, fact.SourceQuoteSHA256) ||
			facts[fact.FactID].FactID != "" {
			return nil, nil, 0, ErrProof
		}
		facts[fact.FactID] = fact
		if fact.Required {
			required = append(required, fact.FactID)
		}
	}
	if len(required) == 0 {
		return nil, nil, 0, ErrProof
	}
	sort.Strings(required)
	return facts, required, rootOffset, nil
}

func validateJudgments(c CatalogProof, facts map[string]CatalogFact,
	judgments []JudgmentProof) ([]JudgmentProof, map[int64]EventProof, error) {
	if len(judgments) < 1 || len(judgments) > 128 {
		return nil, nil, ErrProof
	}
	seenFacts := make(map[string]bool, len(judgments))
	seenEvents := map[string]bool{c.Event.EventID: true}
	byOffset := make(map[int64]EventProof, len(judgments)+1)
	rootOffset, _ := decimal(c.Event.DCOffset, true)
	byOffset[rootOffset] = c.Event
	ordered := append([]JudgmentProof(nil), judgments...)
	for _, j := range ordered {
		if _, ok := facts[j.FactID]; !ok || seenFacts[j.FactID] ||
			j.WikiRevisionID != c.WikiRevisionID ||
			j.SourceScopeRevision != c.SourceScopeRevision ||
			!idPattern.MatchString(j.JudgeRevisionID) ||
			j.HeadAtCutoffRevisionID != j.JudgeRevisionID ||
			!actor(j.RTWActorID) {
			return nil, nil, ErrProof
		}
		offset, err := event(j.Event)
		if err != nil || byOffset[offset].EventID != "" || seenEvents[j.Event.EventID] {
			return nil, nil, ErrProof
		}
		seenFacts[j.FactID] = true
		seenEvents[j.Event.EventID] = true
		byOffset[offset] = j.Event
	}
	for _, fact := range facts {
		if fact.Required && !seenFacts[fact.FactID] {
			return nil, nil, ErrProof
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].FactID < ordered[j].FactID })
	return ordered, byOffset, nil
}

func validatePrefix(p PrefixProof, eventByOffset map[int64]EventProof) (int64, string, error) {
	if p.Producer != Producer || !idPattern.MatchString(p.Consumer) ||
		len(p.Batches) < 1 || len(p.Batches) > 4096 ||
		len(p.Selected) != len(eventByOffset) {
		return 0, "", ErrPrefix
	}
	through, ok := decimal(p.ThroughOffset, true)
	if !ok {
		return 0, "", ErrPrefix
	}
	cutoff, ok := decimal(p.CutoffOffset, true)
	if !ok || cutoff > through {
		return 0, "", ErrPrefix
	}
	next := int64(1)
	for _, batch := range p.Batches {
		from, fromOK := decimal(batch.FromOffset, true)
		to, toOK := decimal(batch.ToOffset, true)
		if !fromOK || !toOK || from != next || to < from ||
			!validSHA(batch.BatchJCSSHA256) || !validSHA(batch.ODSReceiptSHA256) ||
			to == int64(^uint64(0)>>1) {
			return 0, "", ErrPrefix
		}
		next = to + 1
	}
	if next != through+1 {
		return 0, "", ErrPrefix
	}
	previous := int64(0)
	for _, selected := range p.Selected {
		offset, ok := decimal(selected.DCOffset, true)
		if !ok || offset <= previous || offset > cutoff ||
			eventByOffset[offset].EventID != selected.EventID ||
			eventByOffset[offset].JCSSHA256 != selected.JCSSHA256 {
			return 0, "", ErrPrefix
		}
		previous = offset
	}
	_, hash, err := jcs(p)
	if err != nil {
		return 0, "", ErrPrefix
	}
	return cutoff, hash, nil
}

func validState(value RevisionStatus) bool {
	return value == Available || value == Withdrawn || value == Unknown
}

func validateWithdrawals(w WithdrawalSnapshot, c CatalogProof,
	cutoff int64) (bool, error) {
	when, ok := decimal(w.AtCutoffOffset, true)
	if !ok || when != cutoff || !validState(w.WikiState) {
		return false, ErrProof
	}
	sourceIDs := map[string]bool{}
	for _, source := range c.Sources {
		sourceIDs[source.RevisionID] = true
	}
	if len(w.Sources) != len(sourceIDs) {
		return false, ErrProof
	}
	allAvailable := w.WikiState == Available
	previous := ""
	for _, source := range w.Sources {
		if !sourceIDs[source.SourceRevisionID] ||
			source.SourceRevisionID <= previous || !validState(source.State) {
			return false, ErrProof
		}
		if source.State != Available {
			allAvailable = false
		}
		previous = source.SourceRevisionID
	}
	return allAvailable, nil
}

// Freeze only builds a SOURCE proof after the injected prefix reader confirms
// the full DC producer index and PG/ACK watermark. Catalog/label semantics,
// human actor eligibility and truth require future RTW/DC readers. Even a
// successful synthetic verifier NEVER produces an observed D07 quality case.
func Freeze(ctx context.Context, in Input, authority PrefixAuthority) (Frozen, error) {
	if ctx == nil || ctx.Err() != nil || authority == nil {
		return Frozen{}, ErrPrefix
	}
	facts, required, _, err := validateCatalog(in.Catalog)
	if err != nil {
		return Frozen{}, err
	}
	judgments, eventByOffset, err := validateJudgments(in.Catalog, facts, in.Judgments)
	if err != nil {
		return Frozen{}, err
	}
	cutoff, prefixSHA, err := validatePrefix(in.Prefix, eventByOffset)
	if err != nil {
		return Frozen{}, err
	}
	allAvailable, err := validateWithdrawals(in.Withdrawals, in.Catalog, cutoff)
	if err != nil {
		return Frozen{}, err
	}
	verified, err := authority.VerifyPrefix(ctx, in.Prefix, prefixSHA)
	if err != nil || verified.Producer != Producer || verified.Consumer != in.Prefix.Consumer ||
		verified.CoverageIndexJCSSHA256 != prefixSHA ||
		!validSHA(verified.AuthorityReceiptSHA256) {
		return Frozen{}, ErrPrefix
	}
	committed, ok := decimal(verified.CommittedThroughOffset, true)
	if !ok {
		return Frozen{}, ErrPrefix
	}
	acked, ok := decimal(verified.AcknowledgedThroughOffset, true)
	if !ok || committed < lenPrefix(in.Prefix) || acked < lenPrefix(in.Prefix) {
		return Frozen{}, ErrPrefix
	}
	reason := "administrator_complete_catalog_is_a_declaration_not_quality_truth"
	if !allAvailable {
		reason = "withdrawal_or_qualification_at_cutoff_unresolved"
	}
	catalogEvent := in.Catalog.Event
	catalogEvent.OriginalRaw = nil
	for i := range judgments {
		judgments[i].Event.OriginalRaw = nil
	}
	receipt := Receipt{SchemaVersion: Schema, Producer: Producer,
		WikiRevisionID:          in.Catalog.WikiRevisionID,
		SourceScopeRevision:     in.Catalog.SourceScopeRevision,
		FactSetRevisionID:       in.Catalog.FactSetRevisionID,
		Catalog:                 catalogEvent,
		FactSetPayloadJCSSHA256: in.Catalog.FactSetPayloadJCSSHA256,
		Sources:                 append([]CatalogSource(nil), in.Catalog.Sources...),
		RequiredFactIDs:         required, Judgments: judgments,
		PrefixIndexJCSSHA256:  prefixSHA,
		PrefixAuthoritySHA256: verified.AuthorityReceiptSHA256,
		CutoffOffset:          in.Prefix.CutoffOffset,
		WithdrawalSnapshot:    in.Withdrawals,
		FactsCompleteDeclared: true, DeclarationSource: Declaration,
		RTWActorID: in.Catalog.RTWActorID, EvidenceLevel: SourceOnly,
		QualityState: NotEvaluable, QualityReason: reason, Activation: "none"}
	raw, rootSHA, err := jcs(receipt)
	if err != nil {
		return Frozen{}, ErrProof
	}
	if _, err := DecodeReceipt(raw, rootSHA); err != nil {
		return Frozen{}, ErrProof
	}
	return Frozen{Receipt: receipt, ReceiptJCS: raw, RootSHA256: rootSHA}, nil
}

func lenPrefix(p PrefixProof) int64 { n, _ := decimal(p.ThroughOffset, true); return n }
