package sourceproof

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

type fixturePrefixAuthority struct {
	badACK    bool
	badCommit bool
	badHash   bool
}

// This fixture exercises the typed seam; it is NOT DC/ODS source authority.
func (f fixturePrefixAuthority) VerifyPrefix(_ context.Context, p PrefixProof,
	indexSHA string) (PrefixReceipt, error) {
	through := p.ThroughOffset
	committed := p.ThroughOffset
	if f.badACK {
		through = "9"
	}
	if f.badCommit {
		committed = "9"
	}
	if f.badHash {
		indexSHA = strings.Repeat("0", 64)
	}
	return PrefixReceipt{Producer: Producer, Consumer: p.Consumer,
		CoverageIndexJCSSHA256: indexSHA, CommittedThroughOffset: committed,
		AcknowledgedThroughOffset: through,
		AuthorityReceiptSHA256:    strings.Repeat("f", 64)}, nil
}

func fixtureEvent(t *testing.T, id, offset string, raw []byte) EventProof {
	t.Helper()
	canonical, err := jcsRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	return EventProof{EventID: id, DCOffset: offset,
		OriginalRaw: raw, RawSHA256: digest(raw), JCSSHA256: digest(canonical)}
}

func jcsRaw(raw []byte) ([]byte, error) {
	return jsoncanonicalizer.Transform(raw)
}

func sourceProofFixture(t *testing.T) Input {
	t.Helper()
	sourceA, sourceB := "source-a", "source-b"
	quoteA, quoteB, quoteOptional := strings.Repeat("c", 64),
		strings.Repeat("d", 64), strings.Repeat("e", 64)
	factA := CatalogFact{FactID: factID(sourceA, "paragraph:1", quoteA),
		SourceRevisionID: sourceA, Locator: "paragraph:1",
		SourceContentSHA256: strings.Repeat("a", 64), SourceQuoteSHA256: quoteA,
		Required: true}
	factB := CatalogFact{FactID: factID(sourceB, "paragraph:2", quoteB),
		SourceRevisionID: sourceB, Locator: "paragraph:2",
		SourceContentSHA256: strings.Repeat("b", 64), SourceQuoteSHA256: quoteB,
		Required: true}
	optional := CatalogFact{FactID: factID(sourceA, "paragraph:2", quoteOptional),
		SourceRevisionID: sourceA, Locator: "paragraph:2",
		SourceContentSHA256: strings.Repeat("a", 64), SourceQuoteSHA256: quoteOptional}
	payload, payloadSHA, err := jcs(map[string]any{
		"schema_version": "synthetic.catalog.only", "fact_ids": []string{factA.FactID, factB.FactID, optional.FactID}})
	if err != nil {
		t.Fatal(err)
	}
	catalogEvent := fixtureEvent(t, "evt-catalog-5", "5",
		[]byte(`{"type": "fixture.catalog", "producer":"ridethewind.knowledge"}`))
	firstEvent := fixtureEvent(t, "evt-judgment-6", "6",
		[]byte(`{"type":"fixture.judgment","fact_id":"`+factA.FactID+`"}`))
	secondEvent := fixtureEvent(t, "evt-judgment-8", "8",
		[]byte(`{"type":"fixture.judgment","fact_id":"`+factB.FactID+`"}`))
	wikiID, scope := "wiki-revision-a", "scope_"+strings.Repeat("1", 64)
	return Input{Catalog: CatalogProof{ModuleID: "module-a", PageID: "《外部知识》",
		WikiRevisionID: wikiID, SourceScopeRevision: scope,
		FactSetRevisionID: "fact-set-revision-a", FactSetPayloadJCSSHA256: payloadSHA,
		FactSetPayloadJCS: payload, Event: catalogEvent,
		Sources: []CatalogSource{{RevisionID: sourceA, ContentSHA256: strings.Repeat("a", 64)},
			{RevisionID: sourceB, ContentSHA256: strings.Repeat("b", 64)}},
		Facts: []CatalogFact{optional, factB, factA}, FactsCompleteDeclared: true,
		DeclarationSource: Declaration, RTWActorID: "test-admin"},
		Judgments: []JudgmentProof{{WikiRevisionID: wikiID, SourceScopeRevision: scope,
			FactID: factB.FactID, JudgeRevisionID: "judge-revision-b",
			HeadAtCutoffRevisionID: "judge-revision-b", RTWActorID: "管理员乙",
			Event: secondEvent},
			{WikiRevisionID: wikiID, SourceScopeRevision: scope,
				FactID: factA.FactID, JudgeRevisionID: "judge-revision-a",
				HeadAtCutoffRevisionID: "judge-revision-a", RTWActorID: "test-admin",
				Event: firstEvent}},
		Prefix: PrefixProof{Producer: Producer, Consumer: "btw-warehouse-wiki-quality",
			ThroughOffset: "10", CutoffOffset: "8",
			Batches: []PrefixBatch{
				{FromOffset: "1", ToOffset: "4", BatchJCSSHA256: strings.Repeat("1", 64),
					ODSReceiptSHA256: strings.Repeat("2", 64)},
				{FromOffset: "5", ToOffset: "10", BatchJCSSHA256: strings.Repeat("3", 64),
					ODSReceiptSHA256: strings.Repeat("4", 64)},
			},
			Selected: []PrefixEvent{
				{DCOffset: "5", EventID: catalogEvent.EventID, JCSSHA256: catalogEvent.JCSSHA256},
				{DCOffset: "6", EventID: firstEvent.EventID, JCSSHA256: firstEvent.JCSSHA256},
				{DCOffset: "8", EventID: secondEvent.EventID, JCSSHA256: secondEvent.JCSSHA256},
			}},
		Withdrawals: WithdrawalSnapshot{AtCutoffOffset: "8", WikiState: Available,
			Sources: []SourceStatus{{SourceRevisionID: sourceA, State: Available},
				{SourceRevisionID: sourceB, State: Available}}}}
}

func TestAggregateTwoSourcesCatalogAndDistinctJudgmentsGolden(t *testing.T) {
	input := sourceProofFixture(t)
	frozen, err := Freeze(context.Background(), input, fixturePrefixAuthority{})
	if err != nil || frozen.Receipt.QualityState != NotEvaluable ||
		frozen.Receipt.EvidenceLevel != SourceOnly || frozen.Receipt.Activation != "none" ||
		len(frozen.Receipt.RequiredFactIDs) != 2 ||
		len(frozen.Receipt.Judgments) != 2 ||
		frozen.Receipt.Judgments[0].FactID >= frozen.Receipt.Judgments[1].FactID ||
		frozen.Receipt.Catalog.OriginalRaw != nil ||
		frozen.Receipt.Judgments[0].Event.OriginalRaw != nil ||
		frozen.RootSHA256 != digest(frozen.ReceiptJCS) ||
		frozen.RootSHA256 == input.Catalog.Event.RawSHA256 ||
		frozen.RootSHA256 == input.Catalog.FactSetPayloadJCSSHA256 {
		t.Fatalf("two-source source-proof root promoted quality or mixed hash domains: %+v %v", frozen.Receipt, err)
	}
	if read, err := DecodeReceipt(frozen.ReceiptJCS, frozen.RootSHA256); err != nil ||
		read.FactSetRevisionID != input.Catalog.FactSetRevisionID {
		t.Fatalf("independent source-only root reader rejected typed JCS: %+v %v", read, err)
	}
	promoted := frozen.Receipt
	promoted.QualityState = "observed"
	promotedJCS, promotedSHA, err := jcs(promoted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReceipt(promotedJCS, promotedSHA); !errors.Is(err, ErrProof) {
		t.Fatalf("rehashed source-only root self-approved D07 quality: %v", err)
	}
	unknown := append(bytes.TrimSuffix(append([]byte(nil), frozen.ReceiptJCS...), []byte("}")),
		[]byte(`,"tenant_id":"invented"}`)...)
	if _, err := DecodeReceipt(unknown, digest(unknown)); !errors.Is(err, ErrProof) {
		t.Fatalf("quality source proof accepted an unknown tenant wire key: %v", err)
	}
	permuted := sourceProofFixture(t)
	permuted.Judgments[0], permuted.Judgments[1] = permuted.Judgments[1], permuted.Judgments[0]
	permuted.Catalog.Facts[0], permuted.Catalog.Facts[2] =
		permuted.Catalog.Facts[2], permuted.Catalog.Facts[0]
	other, err := Freeze(context.Background(), permuted, fixturePrefixAuthority{})
	if err != nil || other.RootSHA256 != frozen.RootSHA256 ||
		!bytes.Equal(other.ReceiptJCS, frozen.ReceiptJCS) {
		t.Fatalf("FactID enumeration changed JCS root: %s/%s %v", frozen.RootSHA256, other.RootSHA256, err)
	}
	const golden = "5e81c4a5bbacf398d1b3a91e930be36012fd6fdd821bc7a9259e7c15202723c0"
	if frozen.RootSHA256 != golden {
		t.Fatalf("sourceproof JCS Golden changed: %s", frozen.RootSHA256)
	}
}

func TestAggregateRejectsMissingDuplicateCrossTargetAndForgedRaw(t *testing.T) {
	for _, trial := range []struct {
		name   string
		change func(*Input)
	}{
		{"missing_required_fact", func(i *Input) { i.Judgments = i.Judgments[:1]; i.Prefix.Selected = i.Prefix.Selected[:2] }},
		{"duplicate_fact", func(i *Input) { i.Judgments[1].FactID = i.Judgments[0].FactID }},
		{"duplicate_event_id", func(i *Input) { i.Judgments[1].Event.EventID = i.Judgments[0].Event.EventID }},
		{"cross_wiki_target", func(i *Input) { i.Judgments[0].WikiRevisionID = "wiki-revision-b" }},
		{"cross_scope", func(i *Input) { i.Judgments[0].SourceScopeRevision = "scope_" + strings.Repeat("9", 64) }},
		{"old_judge_head_at_cutoff", func(i *Input) { i.Judgments[0].HeadAtCutoffRevisionID = "judge-revision-old" }},
		{"forged_original_raw_sha", func(i *Input) { i.Judgments[0].Event.RawSHA256 = strings.Repeat("0", 64) }},
		{"forged_original_jcs_sha", func(i *Input) { i.Catalog.Event.JCSSHA256 = strings.Repeat("0", 64) }},
		{"forged_fact_id", func(i *Input) { i.Catalog.Facts[0].FactID = "fact_" + strings.Repeat("0", 64) }},
		{"cross_source_scope_sha", func(i *Input) { i.Catalog.Facts[0].SourceContentSHA256 = strings.Repeat("0", 64) }},
		{"false_complete", func(i *Input) { i.Catalog.FactsCompleteDeclared = false }},
		{"actor_control", func(i *Input) { i.Catalog.RTWActorID = "test-admin\nforged" }},
	} {
		t.Run(trial.name, func(t *testing.T) {
			input := sourceProofFixture(t)
			trial.change(&input)
			if _, err := Freeze(context.Background(), input, fixturePrefixAuthority{}); !errors.Is(err, ErrProof) {
				t.Fatalf("bad RTW Catalog/Fact source crossed aggregation: %v", err)
			}
		})
	}
}

func TestAggregateRejectsSparsePrefixBadCutoffAndUntrustedACK(t *testing.T) {
	for _, trial := range []struct {
		name      string
		change    func(*Input)
		authority PrefixAuthority
	}{
		{"missing_authority", nil, nil},
		{"sparse_range", func(i *Input) { i.Prefix.Batches[1].FromOffset = "6" }, fixturePrefixAuthority{}},
		{"selected_only", func(i *Input) { i.Prefix.Batches = nil }, fixturePrefixAuthority{}},
		{"cutoff_before_second_judgment", func(i *Input) { i.Prefix.CutoffOffset = "7"; i.Withdrawals.AtCutoffOffset = "7" }, fixturePrefixAuthority{}},
		{"forged_selected_membership", func(i *Input) { i.Prefix.Selected[2].EventID = "old-head-event" }, fixturePrefixAuthority{}},
		{"bad_authority_index", nil, fixturePrefixAuthority{badHash: true}},
		{"unacked_through", nil, fixturePrefixAuthority{badACK: true}},
		{"uncommitted_through", nil, fixturePrefixAuthority{badCommit: true}},
	} {
		t.Run(trial.name, func(t *testing.T) {
			input := sourceProofFixture(t)
			if trial.change != nil {
				trial.change(&input)
			}
			if _, err := Freeze(context.Background(), input, trial.authority); !errors.Is(err, ErrPrefix) {
				t.Fatalf("sparse or untrusted DC/ODS prefix minted a root: %v", err)
			}
		})
	}
}

func TestAggregateWithdrawalCutoffRemainsNotEvaluable(t *testing.T) {
	input := sourceProofFixture(t)
	input.Withdrawals.Sources[1].State = Withdrawn
	frozen, err := Freeze(context.Background(), input, fixturePrefixAuthority{})
	if err != nil || frozen.Receipt.QualityState != NotEvaluable ||
		frozen.Receipt.QualityReason != "withdrawal_or_qualification_at_cutoff_unresolved" ||
		frozen.Receipt.WithdrawalSnapshot.AtCutoffOffset != input.Prefix.CutoffOffset {
		t.Fatalf("historical withdrawal at cutoff became an observed quality grade: %+v %v", frozen.Receipt, err)
	}
}
