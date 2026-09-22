package wiki_quality

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"unicode/utf8"
)

const RubricVersion = "sea.wiki.fact-coverage.v1"

type qualification struct {
	State   State
	Reason  string
	Receipt AuthorityReceipt
}

func validateHashDomains(h HashDomains) bool {
	values := []string{h.CompileInputSHA256, h.DCJobInputSHA256,
		h.RTWAcceptResultSHA256, h.TechnicalManifestSHA256,
		h.DCTechnicalResultSHA256}
	for _, value := range values {
		if value != "" && !validHash(value) {
			return false
		}
	}
	return true
}

func validateWikiVersion(value WikiVersion, scope Scope, expected WikiKind) error {
	if value.Kind != expected || !idPattern.MatchString(value.RevisionID) ||
		value.ModuleID != scope.ModuleID || value.PageID != scope.PageID ||
		value.BaseRevisionID != scope.BaseRevisionID {
		return ErrScope
	}
	if !utf8.ValidString(value.Content) || len(value.Content) > 48<<10 ||
		!validHash(value.ContentSHA256) ||
		(value.Content != "" && Digest([]byte(value.Content)) != value.ContentSHA256) {
		return ErrEvidence
	}
	if value.Content != "" && strings.TrimSpace(value.Content) == "" {
		return ErrEvidence
	}
	allowed := make(map[string]bool, len(scope.SourceRevisionIDs))
	for _, id := range scope.SourceRevisionIDs {
		allowed[id] = true
	}
	if len(value.SourceRefs) > 64 || expected == AIAccepted && len(value.SourceRefs) == 0 {
		return ErrScope
	}
	seen := map[SourceRef]bool{}
	for _, ref := range value.SourceRefs {
		if !allowed[ref.RevisionID] || !paragraphPattern.MatchString(ref.Locator) || seen[ref] {
			return ErrScope
		}
		seen[ref] = true
	}
	return nil
}

func validateInputShape(input Input) error {
	scope := input.Scope
	if !idPattern.MatchString(scope.ModuleID) || !validText(scope.PageID, 128) ||
		(scope.CompileID != "" && !idPattern.MatchString(scope.CompileID)) ||
		(input.Target.Kind == AIAccepted && scope.Generation < 0) ||
		(input.Target.Kind == ManualRevisionKind && scope.Generation < 0) ||
		(scope.BaseRevisionID != "" && !idPattern.MatchString(scope.BaseRevisionID)) ||
		len(scope.SourceRevisionIDs) < 1 || len(scope.SourceRevisionIDs) > 16 ||
		len(scope.SourceRevisionIDs) != len(input.Sources) ||
		!validateHashDomains(scope.Hashes) {
		return ErrScope
	}
	if input.Target.Kind == AIAccepted {
		if (scope.ModelConfigurationID != "" && !idPattern.MatchString(scope.ModelConfigurationID)) ||
			(scope.PromptVersion != "" && !idPattern.MatchString(scope.PromptVersion)) ||
			input.PreviousAI != nil {
			return ErrScope
		}
	} else if input.Target.Kind == ManualRevisionKind {
		if scope.CompileID != "" || scope.ModelConfigurationID != "" ||
			scope.PromptVersion != "" || input.Candidate != nil ||
			scope.Hashes != (HashDomains{}) {
			return ErrScope
		}
	} else {
		return ErrScope
	}
	seenSource := map[string]bool{}
	for i, id := range scope.SourceRevisionIDs {
		source := input.Sources[i]
		if !idPattern.MatchString(id) || seenSource[id] ||
			source.RevisionID != id || source.ModuleID != scope.ModuleID ||
			source.Kind != "source" || !validHash(source.ContentSHA256) {
			return ErrScope
		}
		seenSource[id] = true
		if source.Content != "" &&
			(!utf8.ValidString(source.Content) || len(source.Content) > 64<<10 ||
				Digest([]byte(source.Content)) != source.ContentSHA256) {
			return ErrEvidence
		}
	}
	if err := validateWikiVersion(input.Target, scope, input.Target.Kind); err != nil {
		// A withdrawn target is not yet readable/eligible, but raw evidence
		// supplied here must still be byte-consistent.
		return err
	}
	sourceByID := make(map[string]Source, len(input.Sources))
	for _, source := range input.Sources {
		sourceByID[source.RevisionID] = source
	}
	for _, ref := range input.Target.SourceRefs {
		if source := sourceByID[ref.RevisionID]; source.Content != "" {
			if _, _, ok := OriginalParagraphBytes(source.Content, ref.Locator); !ok {
				return ErrEvidence
			}
		}
	}
	if input.Candidate != nil {
		candidate := input.Candidate
		if !utf8.ValidString(candidate.Content) || len(candidate.Content) > 48<<10 ||
			candidate.ContentSHA256 != input.Target.ContentSHA256 ||
			Digest([]byte(candidate.Content)) != candidate.ContentSHA256 ||
			candidate.Content != input.Target.Content ||
			len(candidate.SourceRefs) != len(input.Target.SourceRefs) {
			return ErrEvidence
		}
		for i, ref := range candidate.SourceRefs {
			if ref != input.Target.SourceRefs[i] {
				return ErrEvidence
			}
		}
	}
	if input.PreviousAI != nil {
		previous := *input.PreviousAI
		if input.Target.Kind != ManualRevisionKind ||
			input.Target.BaseRevisionID != previous.RevisionID ||
			previous.Kind != AIAccepted || previous.ModuleID != scope.ModuleID ||
			previous.PageID != scope.PageID || !validHash(previous.ContentSHA256) ||
			strings.TrimSpace(previous.Content) == "" ||
			Digest([]byte(previous.Content)) != previous.ContentSHA256 ||
			!utf8.ValidString(previous.Content) || len(previous.Content) > 48<<10 {
			return ErrScope
		}
	}
	if input.Review.WikiRevisionID != input.Target.RevisionID ||
		!idPattern.MatchString(input.Review.ReviewID) ||
		!idPattern.MatchString(input.Review.ReviewVersion) ||
		input.Review.RubricVersion != RubricVersion ||
		(input.Review.DataKind != SyntheticFixture && input.Review.DataKind != HumanAdmin) {
		return ErrScope
	}
	if input.Review.RTWActorID != "" && !validRTWActorID(input.Review.RTWActorID) {
		return ErrEvidence
	}
	if provenance := input.Review.SourceProvenance; provenance != nil {
		offset, err := strconv.ParseUint(provenance.DCOffset, 10, 64)
		if provenance.Producer != "ridethewind.knowledge" ||
			!idPattern.MatchString(provenance.EventID) ||
			!validHash(provenance.RTWEventJCSSHA256) ||
			err != nil || offset < 1 || strconv.FormatUint(offset, 10) != provenance.DCOffset {
			return ErrEvidence
		}
	}
	if input.Cost != nil {
		cost := input.Cost
		if !validHash(cost.DCUsageReceiptSHA256) ||
			!idPattern.MatchString(cost.ModelConfigurationID) ||
			(input.Target.Kind == AIAccepted && scope.ModelConfigurationID != "" &&
				cost.ModelConfigurationID != scope.ModelConfigurationID) ||
			cost.PromptTokens < 0 || cost.CompletionTokens < 0 ||
			cost.TotalTokens < 1 || cost.TotalTokens < cost.PromptTokens+cost.CompletionTokens ||
			cost.WallMillis < 1 {
			return ErrEvidence
		}
	}
	return nil
}

func validateFacts(input Input) (map[string]Fact, map[string]FactJudgment, error) {
	sources := make(map[string]Source, len(input.Sources))
	for _, source := range input.Sources {
		sources[source.RevisionID] = source
	}
	facts := make(map[string]Fact, len(input.Review.Facts))
	for _, fact := range input.Review.Facts {
		source, ok := sources[fact.SourceRevisionID]
		if !ok || !idPattern.MatchString(fact.FactID) || facts[fact.FactID].FactID != "" ||
			!paragraphPattern.MatchString(fact.Locator) ||
			!validHash(fact.OriginalByteSHA256) ||
			fact.OriginalByteSHA256 != Digest([]byte(fact.Quote)) ||
			(fact.ConflictGroup != "" && !idPattern.MatchString(fact.ConflictGroup)) {
			return nil, nil, ErrScope
		}
		if source.Content != "" && !sourceQuote(fact, source) {
			return nil, nil, ErrEvidence
		}
		facts[fact.FactID] = fact
	}
	judgments := make(map[string]FactJudgment, len(input.Review.Judgments))
	for _, judgment := range input.Review.Judgments {
		if _, ok := facts[judgment.FactID]; !ok || judgments[judgment.FactID].FactID != "" {
			return nil, nil, ErrScope
		}
		if !validText(judgment.Reason, 2048) ||
			(judgment.BaseJudgeRevisionID != "" && !idPattern.MatchString(judgment.BaseJudgeRevisionID)) {
			return nil, nil, ErrEvidence
		}
		if judgment.WikiClaimText == "" {
			if judgment.WikiClaimSHA256 != "" || judgment.WikiSpanProvenance != "" {
				return nil, nil, ErrEvidence
			}
		} else if !validHash(judgment.WikiClaimSHA256) ||
			Digest([]byte(judgment.WikiClaimText)) != judgment.WikiClaimSHA256 ||
			judgment.WikiSpanProvenance != DerivedFirstMatch {
			return nil, nil, ErrEvidence
		}
		switch judgment.Disposition {
		case Missing:
			if judgment.Grade != "0" || judgment.WikiClaimText != "" ||
				judgment.WikiByteStart != 0 || judgment.WikiByteEnd != 0 {
				return nil, nil, ErrEvidence
			}
		case Conflict:
			if judgment.Grade != "0" || !wikiSpan(input.Target.Content, judgment) {
				return nil, nil, ErrEvidence
			}
		case Covered:
			if (judgment.Grade != "1" && judgment.Grade != "2" && judgment.Grade != "3") ||
				!wikiSpan(input.Target.Content, judgment) {
				return nil, nil, ErrEvidence
			}
		case Undetermined:
			if judgment.Grade != "" ||
				(judgment.WikiClaimText != "" && !wikiSpan(input.Target.Content, judgment)) ||
				(judgment.WikiClaimText == "" && (judgment.WikiByteStart != 0 || judgment.WikiByteEnd != 0)) {
				return nil, nil, ErrEvidence
			}
		default:
			return nil, nil, ErrEvidence
		}
		judgments[judgment.FactID] = judgment
	}
	return facts, judgments, nil
}

func wikiSpan(content string, judgment FactJudgment) bool {
	raw := []byte(content)
	first := bytes.Index(raw, []byte(judgment.WikiClaimText))
	return first >= 0 && judgment.WikiSpanProvenance == DerivedFirstMatch &&
		judgment.WikiByteStart == first && judgment.WikiByteEnd == first+len([]byte(judgment.WikiClaimText)) &&
		judgment.WikiByteStart >= 0 && judgment.WikiByteEnd <= len(raw) &&
		judgment.WikiByteEnd > judgment.WikiByteStart &&
		string(raw[judgment.WikiByteStart:judgment.WikiByteEnd]) == judgment.WikiClaimText &&
		utf8.Valid(raw[:judgment.WikiByteStart]) && utf8.Valid(raw[:judgment.WikiByteEnd]) &&
		strings.TrimSpace(judgment.WikiClaimText) != ""
}

func validRTWActorID(actor string) bool {
	if strings.TrimSpace(actor) == "" || len(actor) > 200 || !utf8.ValidString(actor) {
		return false
	}
	for _, ch := range actor {
		if ch < 0x20 || ch == 0x7f {
			return false
		}
	}
	return true
}

func reviewDigest(input Input) (string, error) {
	review := input.Review
	review.Facts = sortedFacts(review.Facts)
	review.Judgments = sortedJudgments(review.Judgments)
	_, digest, err := JCS(review)
	return digest, err
}

func sourceScopeDigest(input Input) (string, error) {
	type identity struct {
		Scope          Scope    `json:"scope"`
		Sources        []Source `json:"sources"`
		WikiRevisionID string   `json:"wiki_revision_id"`
	}
	_, digest, err := JCS(identity{Scope: input.Scope, Sources: input.Sources,
		WikiRevisionID: input.Target.RevisionID})
	return digest, err
}

func qualify(ctx context.Context, input Input, verifier AuthorityVerifier,
	facts map[string]Fact, judgments map[string]FactJudgment) (qualification, error) {
	q := qualification{State: Observed}
	if input.Target.Withdrawn || input.Target.Content == "" {
		q.State, q.Reason = NotEvaluable, "target_revision_unavailable_or_withdrawn"
		return q, nil
	}
	for _, source := range input.Sources {
		if source.Content == "" || source.WithdrawnAtReview || !source.QualifiedAtReview {
			q.State, q.Reason = NotEvaluable, "source_revision_unavailable_or_unqualified"
			return q, nil
		}
	}
	if !input.Review.FactsComplete || len(facts) == 0 {
		q.State, q.Reason = NotEvaluable, "complete_fact_inventory_missing"
		return q, nil
	}
	if !input.Review.LabelsComplete || len(judgments) != len(facts) {
		q.State, q.Reason = NotEvaluable, "human_fact_labels_incomplete"
		return q, nil
	}
	if input.Target.Kind == AIAccepted {
		h := input.Scope.Hashes
		if input.Scope.CompileID == "" || input.Scope.Generation < 1 ||
			input.Scope.ModelConfigurationID == "" || input.Scope.PromptVersion == "" ||
			input.Candidate == nil || h.CompileInputSHA256 == "" ||
			h.DCJobInputSHA256 == "" || h.RTWAcceptResultSHA256 == "" ||
			h.TechnicalManifestSHA256 == "" || h.DCTechnicalResultSHA256 == "" {
			q.State, q.Reason = NotEvaluable, "ai_version_or_model_provenance_incomplete"
			return q, nil
		}
	}
	if input.Review.DataKind == SyntheticFixture {
		return q, nil
	}
	if input.Review.SourceProvenance == nil {
		q.State, q.Reason = NotEvaluable, "rtw_event_or_dc_offset_missing"
		return q, nil
	}
	if input.Review.RTWActorID == "" {
		q.State, q.Reason = NotEvaluable, "rtw_actor_id_missing"
		return q, nil
	}
	if verifier == nil {
		q.State, q.Reason = NotEvaluable, "rtw_human_authority_not_verified"
		return q, nil
	}
	reviewSHA, err := reviewDigest(input)
	if err != nil {
		return qualification{}, err
	}
	scopeSHA, err := sourceScopeDigest(input)
	if err != nil {
		return qualification{}, err
	}
	receipt, err := verifier.Verify(ctx, input.Scope, input.Sources, input.Review)
	if err != nil || receipt.ReviewJCSSHA256 != reviewSHA ||
		receipt.SourceScopeJCSSHA256 != scopeSHA ||
		!idPattern.MatchString(receipt.FactCatalogRevisionID) ||
		!validHash(receipt.FactCatalogJCSSHA256) ||
		receipt.FactCatalogJCSSHA256 == receipt.RTWEventJCSSHA256 ||
		receipt.RTWEventJCSSHA256 != input.Review.SourceProvenance.RTWEventJCSSHA256 ||
		receipt.DCOffset != input.Review.SourceProvenance.DCOffset ||
		!receipt.FactsComplete || !receipt.LabelsComplete ||
		!receipt.RevisionsQualified || !validHash(receipt.AuthorityEvidenceSHA256) {
		q.State, q.Reason = NotEvaluable, "rtw_human_authority_not_verified"
		return q, nil
	}
	q.Receipt = receipt
	return q, nil
}
