package sourceproof

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// DecodeReceipt lets a warehouse reader reject a rehashed but promoted
// source-only artifact. It still must use the official RTW/DC read ports to
// verify the original Event and complete prefix named by these hashes.
func DecodeReceipt(raw []byte, expectedSHA string) (Receipt, error) {
	var value Receipt
	if len(raw) < 2 || len(raw) > 1<<20 || !validSHA(expectedSHA) ||
		digest(raw) != expectedSHA {
		return value, ErrProof
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&value) != nil || d.Decode(new(any)) != io.EOF {
		return Receipt{}, ErrProof
	}
	canonical, _, err := jcs(value)
	if err != nil || !bytes.Equal(canonical, raw) ||
		value.SchemaVersion != Schema || value.Producer != Producer ||
		value.EvidenceLevel != SourceOnly || value.QualityState != NotEvaluable ||
		value.Activation != "none" || value.DeclarationSource != Declaration ||
		!value.FactsCompleteDeclared || !actor(value.RTWActorID) ||
		!idPattern.MatchString(value.WikiRevisionID) ||
		!strings.HasPrefix(value.SourceScopeRevision, "scope_") ||
		!validSHA(strings.TrimPrefix(value.SourceScopeRevision, "scope_")) ||
		!idPattern.MatchString(value.FactSetRevisionID) ||
		!validSHA(value.FactSetPayloadJCSSHA256) ||
		!validSHA(value.PrefixIndexJCSSHA256) || !validSHA(value.PrefixAuthoritySHA256) ||
		(value.QualityReason != "administrator_complete_catalog_is_a_declaration_not_quality_truth" &&
			value.QualityReason != "withdrawal_or_qualification_at_cutoff_unresolved") ||
		len(value.RequiredFactIDs) < 1 || len(value.RequiredFactIDs) > 128 ||
		len(value.Judgments) < 1 || len(value.Judgments) > 128 {
		return Receipt{}, ErrProof
	}
	cutoff, ok := decimal(value.CutoffOffset, true)
	if !ok || value.WithdrawalSnapshot.AtCutoffOffset != value.CutoffOffset ||
		!validState(value.WithdrawalSnapshot.WikiState) {
		return Receipt{}, ErrProof
	}
	catalogOffset, ok := decimal(value.Catalog.DCOffset, true)
	if !ok || catalogOffset > cutoff ||
		!idPattern.MatchString(value.Catalog.EventID) ||
		!validSHA(value.Catalog.RawSHA256) || !validSHA(value.Catalog.JCSSHA256) ||
		len(value.Sources) < 1 || len(value.Sources) > 64 ||
		len(value.Sources) != len(value.WithdrawalSnapshot.Sources) {
		return Receipt{}, ErrProof
	}
	previousSource := ""
	allAvailable := value.WithdrawalSnapshot.WikiState == Available
	for i, source := range value.Sources {
		status := value.WithdrawalSnapshot.Sources[i]
		if !idPattern.MatchString(source.RevisionID) || source.RevisionID <= previousSource ||
			!validSHA(source.ContentSHA256) || status.SourceRevisionID != source.RevisionID ||
			!validState(status.State) {
			return Receipt{}, ErrProof
		}
		if status.State != Available {
			allAvailable = false
		}
		previousSource = source.RevisionID
	}
	if allAvailable && value.QualityReason != "administrator_complete_catalog_is_a_declaration_not_quality_truth" ||
		!allAvailable && value.QualityReason != "withdrawal_or_qualification_at_cutoff_unresolved" {
		return Receipt{}, ErrProof
	}
	previousFact, eventIDs := "", map[string]bool{value.Catalog.EventID: true}
	offsets := map[int64]bool{catalogOffset: true}
	judged := map[string]bool{}
	for _, j := range value.Judgments {
		offset, ok := decimal(j.Event.DCOffset, true)
		if !ok || offset > cutoff || j.FactID <= previousFact ||
			!strings.HasPrefix(j.FactID, "fact_") ||
			!validSHA(strings.TrimPrefix(j.FactID, "fact_")) ||
			j.WikiRevisionID != value.WikiRevisionID ||
			j.SourceScopeRevision != value.SourceScopeRevision ||
			!idPattern.MatchString(j.JudgeRevisionID) ||
			j.HeadAtCutoffRevisionID != j.JudgeRevisionID ||
			!actor(j.RTWActorID) || !idPattern.MatchString(j.Event.EventID) ||
			!validSHA(j.Event.RawSHA256) || !validSHA(j.Event.JCSSHA256) ||
			eventIDs[j.Event.EventID] || offsets[offset] {
			return Receipt{}, ErrProof
		}
		previousFact = j.FactID
		judged[j.FactID], eventIDs[j.Event.EventID], offsets[offset] = true, true, true
	}
	previousFact = ""
	for _, factID := range value.RequiredFactIDs {
		if factID <= previousFact || !judged[factID] ||
			!strings.HasPrefix(factID, "fact_") ||
			!validSHA(strings.TrimPrefix(factID, "fact_")) {
			return Receipt{}, ErrProof
		}
		previousFact = factID
	}
	return value, nil
}
