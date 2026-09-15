package wiki_quality

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

func qualityFact(t *testing.T, id string, source Source, locator, quote, group string,
	required bool) Fact {
	t.Helper()
	start, end, ok := OriginalParagraphBytes(source.Content, locator)
	if !ok {
		t.Fatalf("fixture paragraph unavailable: %s", locator)
	}
	position := bytes.Index([]byte(source.Content)[start:end], []byte(quote))
	if position < 0 {
		t.Fatalf("fixture quote unavailable: %s", quote)
	}
	start += position
	return Fact{FactID: id, SourceRevisionID: source.RevisionID,
		Locator: locator, OriginalByteStart: start, OriginalByteEnd: start + len([]byte(quote)),
		Quote: quote, OriginalByteSHA256: Digest([]byte(quote)), Required: required,
		ConflictGroup: group}
}

type fixtureFactAuthority struct{ receipt AuthorityReceipt }

func (f fixtureFactAuthority) Verify(context.Context, Scope, []Source, Review) (AuthorityReceipt, error) {
	return f.receipt, nil
}

type fixtureDCCostAuthority struct{}

func (fixtureDCCostAuthority) Verify(context.Context, Scope, CostReceipt) error { return nil }

func TestWikiQualityRealAuthorityAndDCCostRemainSeparate(t *testing.T) {
	input := qualityFixture(t)
	input.Review.DataKind, input.Review.ReviewerUID = HumanAdmin, "rtw-admin-uid-1"
	input.Review.SourceProvenance = &SourceProvenance{Producer: "ridethewind.knowledge",
		EventID:           "evt_11111111-1111-4111-8111-111111111111",
		RTWEventJCSSHA256: strings.Repeat("9", 64), DCOffset: "42"}
	without, err := EvaluateCase(context.Background(), input, Verifiers{})
	if err != nil || without.Report.State != NotEvaluable ||
		without.Report.Reason != "rtw_human_authority_not_verified" ||
		without.Report.RequiredCoverage.State != NotEvaluable {
		t.Fatalf("self-declared human grade gained approval without RTW reader: %+v %v", without.Report, err)
	}
	reviewSHA, err := reviewDigest(input)
	if err != nil {
		t.Fatal(err)
	}
	sourceSHA, err := sourceScopeDigest(input)
	if err != nil {
		t.Fatal(err)
	}
	receipt := AuthorityReceipt{ReviewJCSSHA256: reviewSHA,
		SourceScopeJCSSHA256: sourceSHA, FactsComplete: true,
		LabelsComplete: true, RevisionsQualified: true,
		AuthorityEvidenceSHA256: input.Review.SourceProvenance.RTWEventJCSSHA256,
		RTWEventJCSSHA256:       input.Review.SourceProvenance.RTWEventJCSSHA256,
		DCOffset:                input.Review.SourceProvenance.DCOffset}
	bad := receipt
	bad.DCOffset = "43"
	if frozen, err := EvaluateCase(context.Background(), input,
		Verifiers{Facts: fixtureFactAuthority{receipt: bad}}); err != nil ||
		frozen.Report.State != NotEvaluable {
		t.Fatalf("mismatched true DC source offset became human quality: %+v %v", frozen.Report, err)
	}
	good, err := EvaluateCase(context.Background(), input,
		Verifiers{Facts: fixtureFactAuthority{receipt: receipt}})
	if err != nil || good.Report.State != Observed ||
		good.Report.AuthorityEvidenceSHA256 != receipt.AuthorityEvidenceSHA256 ||
		good.Report.Cost.State != NotEvaluable ||
		good.Report.Cost.Reason != "dc_usage_authority_not_verified" ||
		good.Report.Activation != "none" {
		t.Fatalf("RTW human facts were mixed with unverified DC token counts: %+v %v", good.Report, err)
	}
	withCost, err := EvaluateCase(context.Background(), input,
		Verifiers{Facts: fixtureFactAuthority{receipt: receipt}, Cost: fixtureDCCostAuthority{}})
	if err != nil || withCost.Report.State != Observed ||
		withCost.Report.Cost.State != Observed || withCost.Report.Cost.TotalTokens != 768 ||
		withCost.ManifestSHA256 != good.ManifestSHA256 ||
		withCost.ReportSHA256 == good.ReportSHA256 {
		// The CASE excludes a verifier/derived cost state; identical CaseSHA is
		// correct. Only the ReportSHA may change when DC proof is supplied.
		t.Fatalf("DC separate verifier failed to change only cost report evidence: case=%s/%s report=%s/%s",
			withCost.ManifestSHA256, good.ManifestSHA256,
			withCost.ReportSHA256, good.ReportSHA256)
	}
}

func qualityJudgment(t *testing.T, factID string, disposition Disposition,
	grade, wiki, claim, reason string) FactJudgment {
	t.Helper()
	j := FactJudgment{FactID: factID, Disposition: disposition,
		Grade: grade, Reason: reason}
	if claim != "" {
		position := bytes.Index([]byte(wiki), []byte(claim))
		if position < 0 {
			t.Fatalf("fixture Wiki claim unavailable: %s", claim)
		}
		j.WikiByteStart, j.WikiByteEnd, j.WikiText =
			position, position+len([]byte(claim)), claim
	}
	return j
}

func qualityFixture(t *testing.T) Input {
	t.Helper()
	first := "海风来自东侧。\r\n    code: 7\r\n\r\n潮汐每日两次。"
	second := "港口每周一开放。\r\n\r\n港口每周二开放。\r\n\r\n航道深度未测。"
	sources := []Source{
		{RevisionID: "source-r1", ModuleID: "module-1", Kind: "source",
			Content: first, ContentSHA256: Digest([]byte(first)), QualifiedAtReview: true},
		{RevisionID: "source-r2", ModuleID: "module-1", Kind: "source",
			Content: second, ContentSHA256: Digest([]byte(second)), QualifiedAtReview: true},
	}
	wiki := "港口知识\r\n\r\n海风来自东侧。\r\n\r\n海风来自东侧。\r\n\r\n港口每周一开放。\r\n港口每周三开放。"
	refs := []SourceRef{
		{RevisionID: "source-r1", Locator: "paragraph:1"},
		{RevisionID: "source-r2", Locator: "paragraph:1"},
		{RevisionID: "source-r2", Locator: "paragraph:2"},
	}
	facts := []Fact{
		qualityFact(t, "fact-5", sources[1], "paragraph:3", "航道深度未测。", "", false),
		qualityFact(t, "fact-4", sources[1], "paragraph:2", "港口每周二开放。", "port-open", true),
		qualityFact(t, "fact-3", sources[1], "paragraph:1", "港口每周一开放。", "port-open", true),
		qualityFact(t, "fact-2", sources[0], "paragraph:2", "潮汐每日两次。", "", true),
		qualityFact(t, "fact-1", sources[0], "paragraph:1", "海风来自东侧。", "", true),
	}
	judgments := []FactJudgment{
		qualityJudgment(t, "fact-5", Undetermined, "", wiki, "", "来源深度数值未知，人工暂不判。"),
		qualityJudgment(t, "fact-4", Conflict, "0", wiki, "港口每周三开放。", "与冻结第二段相矛盾。"),
		qualityJudgment(t, "fact-3", Covered, "2", wiki, "港口每周一开放。", "事实与引用正确，条件略短。"),
		qualityJudgment(t, "fact-2", Missing, "0", wiki, "", "Wiki漏掉潮汐事实。"),
		qualityJudgment(t, "fact-1", Covered, "3", wiki, "海风来自东侧。", "完整事实与原资料位置可读。"),
	}
	return Input{Scope: Scope{ModuleID: "module-1", PageID: "《外部知识》",
		CompileID: "compile-1", Generation: 1, BaseRevisionID: "wiki-base-1",
		SourceRevisionIDs:    []string{"source-r1", "source-r2"},
		ModelConfigurationID: "11111111-1111-4111-8111-111111111111",
		PromptVersion:        "wiki-draft-v1",
		Hashes: HashDomains{CompileInputSHA256: strings.Repeat("a", 64),
			DCJobInputSHA256:        strings.Repeat("b", 64),
			RTWAcceptResultSHA256:   strings.Repeat("c", 64),
			TechnicalManifestSHA256: strings.Repeat("d", 64),
			DCTechnicalResultSHA256: strings.Repeat("e", 64)}},
		Sources: sources,
		Target: WikiVersion{Kind: AIAccepted, RevisionID: "wiki-ai-1",
			ModuleID: "module-1", PageID: "《外部知识》", BaseRevisionID: "wiki-base-1",
			Content: wiki, ContentSHA256: Digest([]byte(wiki)), SourceRefs: refs},
		Candidate: &Candidate{Content: wiki, ContentSHA256: Digest([]byte(wiki)), SourceRefs: refs},
		Review: Review{ReviewID: "review-1", ReviewVersion: "judge-r1",
			RubricVersion: RubricVersion, WikiRevisionID: "wiki-ai-1",
			DataKind: SyntheticFixture, FactsComplete: true, LabelsComplete: true,
			Facts: facts, Judgments: judgments},
		Cost: &CostReceipt{DCUsageReceiptSHA256: strings.Repeat("f", 64),
			ModelConfigurationID: "11111111-1111-4111-8111-111111111111",
			PromptTokens:         297, CompletionTokens: 471, TotalTokens: 768, WallMillis: 15000}}
}

func manualQualityFixture(t *testing.T, ai Input) Input {
	t.Helper()
	manual := ai
	manual.Scope.CompileID, manual.Scope.Generation = "", 0
	manual.Scope.ModelConfigurationID, manual.Scope.PromptVersion = "", ""
	manual.Scope.BaseRevisionID, manual.Scope.Hashes = ai.Target.RevisionID, HashDomains{}
	manual.PreviousAI = &ai.Target
	manual.Candidate, manual.Cost = nil, nil
	manualText := "## 港口知识\r\n\r\n海风来自东侧。\r\n\r\n潮汐每日两次。\r\n\r\n港口每周一开放。"
	manual.Target = WikiVersion{Kind: ManualRevisionKind, RevisionID: "wiki-human-2",
		ModuleID: ai.Target.ModuleID, PageID: ai.Target.PageID,
		BaseRevisionID: ai.Target.RevisionID, Content: manualText,
		ContentSHA256: Digest([]byte(manualText)), SourceRefs: []SourceRef{
			{RevisionID: "source-r1", Locator: "paragraph:1"},
			{RevisionID: "source-r1", Locator: "paragraph:2"},
			{RevisionID: "source-r2", Locator: "paragraph:1"}}}
	manual.Review = ai.Review
	manual.Review.WikiRevisionID, manual.Review.ReviewID, manual.Review.ReviewVersion =
		manual.Target.RevisionID, "review-2", "judge-r2"
	manual.Review.Judgments = []FactJudgment{
		qualityJudgment(t, "fact-1", Covered, "3", manualText, "海风来自东侧。", "人工保留完整事实。"),
		qualityJudgment(t, "fact-2", Covered, "2", manualText, "潮汐每日两次。", "人工补上引用，限定仍较短。"),
		qualityJudgment(t, "fact-3", Covered, "3", manualText, "港口每周一开放。", "人工清楚限定港口开放。"),
		qualityJudgment(t, "fact-4", Missing, "0", manualText, "", "冲突旧句不写入本版。"),
		qualityJudgment(t, "fact-5", Undetermined, "", manualText, "", "深度仍待人工核实。"),
	}
	return manual
}

func TestWikiQualityComplementaryConflictCRLFAndManualDataset(t *testing.T) {
	ai := qualityFixture(t)
	start, end, ok := OriginalParagraphBytes(ai.Sources[0].Content, "paragraph:1")
	if !ok || string([]byte(ai.Sources[0].Content)[start:end]) !=
		"海风来自东侧。\r\n    code: 7" {
		t.Fatal("RTW paragraph:1 changed CRLF or stripped four-space code")
	}
	first, err := EvaluateCase(context.Background(), ai, Verifiers{})
	if err != nil || first.Report.State != Observed ||
		first.Report.EvidenceKind != SyntheticFixture || first.Report.Activation != "none" ||
		first.Report.FactCounts.Total != 5 || first.Report.FactCounts.Required != 4 ||
		first.Report.FactCounts.Covered != 2 || first.Report.FactCounts.Missing != 1 ||
		first.Report.FactCounts.Conflict != 1 || first.Report.FactCounts.Undetermined != 1 ||
		first.Report.FactCounts.DeclaredConflictGroups != 1 ||
		first.Report.RequiredCoverage != (Fraction{State: Observed, Numerator: 2, Denominator: 4}) ||
		first.Report.CitationFidelity != (Fraction{State: Observed, Numerator: 2, Denominator: 2}) ||
		first.Report.Structure.RepeatedParagraphs != 1 ||
		first.Report.Structure.ATXHeadingLines != 0 ||
		first.Report.Cost.TotalTokens != 768 || first.Report.Edit.State != NotApplicable {
		t.Fatalf("complementary/conflicting facts changed offline counts: %+v err=%v", first.Report, err)
	}
	if first.Report.GradeHistogram[0].Count != 2 ||
		first.Report.GradeHistogram[2].Count != 1 ||
		first.Report.GradeHistogram[3].Count != 1 {
		t.Fatalf("RTW 0–3 rubric histogram changed: %+v", first.Report.GradeHistogram)
	}
	canonical, err := jsoncanonicalizer.Transform(first.ManifestJCS)
	if err != nil || !bytes.Equal(canonical, first.ManifestJCS) ||
		first.ManifestSHA256 != Digest(first.ManifestJCS) ||
		first.ReportSHA256 != Digest(first.ReportJCS) {
		t.Fatalf("quality Case/Report did not freeze separate exact JCS hashes: %v", err)
	}
	manual := manualQualityFixture(t, ai)
	second, err := EvaluateCase(context.Background(), manual, Verifiers{})
	if err != nil || second.Report.State != Observed ||
		second.Report.WikiOriginKind != ManualRevisionKind ||
		second.Report.Edit.State != Observed || second.Report.Edit.ByteAddedWindow < 1 ||
		second.Report.Edit.ExactLinesAdded < 1 ||
		second.Report.Edit.ATXHeadingsBefore != 0 ||
		second.Report.Edit.ATXHeadingsAfter != 1 ||
		second.Report.Cost.State != NotApplicable ||
		second.Report.RequiredCoverage.Numerator != 3 {
		t.Fatalf("manual Wiki revision was folded into AI case or edit delta lost: %+v err=%v", second.Report, err)
	}
	left, err := FreezeDataset("human-compare-r1", []FrozenCase{first, second})
	right, rightErr := FreezeDataset("human-compare-r1", []FrozenCase{second, first})
	if err != nil || rightErr != nil || left.RootSHA256 != right.RootSHA256 ||
		!bytes.Equal(left.JCS, right.JCS) || left.Manifest.Activation != "none" ||
		len(left.Manifest.Entries) != 2 ||
		first.ManifestSHA256 == ai.Target.ContentSHA256 ||
		first.ManifestSHA256 == ai.Scope.Hashes.TechnicalManifestSHA256 ||
		left.RootSHA256 == first.ManifestSHA256 {
		t.Fatalf("Dataset root/metadata mixed AI/manual revisions or SHA domains: root=%s err=%v %v",
			left.RootSHA256, err, rightErr)
	}
	// Golden values are pinned after the first red/green byte-level run.
	const caseGolden = "6ceeda9bc97639684a9d2ea29a8f490130b9e209f8d3b3d08ad1ec54ff8e78a8"
	const datasetGolden = "e995ec05366b6d910854a7d5f4ef0112e73822b85fc054f7b58fdacfa69efa2c"
	if first.ManifestSHA256 != caseGolden || left.RootSHA256 != datasetGolden {
		t.Fatalf("quality JCS golden differs: case=%s dataset=%s", first.ManifestSHA256, left.RootSHA256)
	}
}

func TestWikiQualityRefusesIncompleteOrUnqualifiedEvidence(t *testing.T) {
	base := qualityFixture(t)
	for _, trial := range []struct {
		name   string
		change func(*Input)
		reason string
	}{
		{"no-fact-inventory", func(i *Input) {
			i.Review.Facts, i.Review.Judgments = nil, nil
			i.Review.FactsComplete, i.Review.LabelsComplete = false, false
		}, "complete_fact_inventory_missing"},
		{"partial-labels", func(i *Input) { i.Review.LabelsComplete = false; i.Review.Judgments = i.Review.Judgments[:4] }, "human_fact_labels_incomplete"},
		{"withdrawn-source", func(i *Input) { i.Sources[1].WithdrawnAtReview = true }, "source_revision_unavailable_or_unqualified"},
		{"unqualified-source", func(i *Input) { i.Sources[1].QualifiedAtReview = false }, "source_revision_unavailable_or_unqualified"},
		{"withdrawn-target", func(i *Input) { i.Target.Withdrawn = true }, "target_revision_unavailable_or_withdrawn"},
		{"human-without-RTW-event", func(i *Input) { i.Review.DataKind = HumanAdmin }, "rtw_event_or_dc_offset_missing"},
	} {
		t.Run(trial.name, func(t *testing.T) {
			input := base
			input.Sources = append([]Source(nil), base.Sources...)
			input.Review.Facts = append([]Fact(nil), base.Review.Facts...)
			input.Review.Judgments = append([]FactJudgment(nil), base.Review.Judgments...)
			trial.change(&input)
			frozen, err := EvaluateCase(context.Background(), input, Verifiers{})
			if err != nil || frozen.Report.State != NotEvaluable || frozen.Report.Reason != trial.reason ||
				frozen.Report.RequiredCoverage.State != NotEvaluable ||
				frozen.Report.FactCounts.Total != 0 || frozen.Manifest.EvaluationState != NotEvaluable {
				t.Fatalf("unqualified/incomplete Wiki case gained numeric quality: %+v err=%v", frozen.Report, err)
			}
		})
	}
	// An already functional single-source 14-byte source-copy run is still a
	// not_evaluable quality input without a signed COMPLETE human fact set.
	const sourceCopy = "one fixed fact"
	const sourceCopySHA = "146f522cc16f69581c5c3e3cd21e594f8435da6e79cdf104a25ca5d3dc93367d"
	if Digest([]byte(sourceCopy)) != sourceCopySHA {
		t.Fatal("historical L4 source-copy raw byte SHA changed")
	}
	copyRun := Input{Scope: Scope{ModuleID: "module-copy", PageID: "《历史短页》",
		BaseRevisionID: "wiki-base", SourceRevisionIDs: []string{"source-copy"}},
		Sources: []Source{{RevisionID: "source-copy", ModuleID: "module-copy",
			Kind: "source", Content: sourceCopy, ContentSHA256: sourceCopySHA,
			QualifiedAtReview: true}},
		Target: WikiVersion{Kind: AIAccepted, RevisionID: "wiki-copy-1",
			ModuleID: "module-copy", PageID: "《历史短页》", BaseRevisionID: "wiki-base",
			Content: sourceCopy, ContentSHA256: sourceCopySHA,
			SourceRefs: []SourceRef{{RevisionID: "source-copy", Locator: "paragraph:1"}}},
		Candidate: &Candidate{Content: sourceCopy, ContentSHA256: sourceCopySHA,
			SourceRefs: []SourceRef{{RevisionID: "source-copy", Locator: "paragraph:1"}}},
		Review: Review{ReviewID: "review-copy", ReviewVersion: "judge-copy-v1",
			RubricVersion: RubricVersion, WikiRevisionID: "wiki-copy-1", DataKind: HumanAdmin}}
	if frozen, err := EvaluateCase(context.Background(), copyRun, Verifiers{}); err != nil ||
		frozen.Report.State != NotEvaluable || frozen.Report.FactCounts.Total != 0 ||
		frozen.Report.Reason != "complete_fact_inventory_missing" {
		t.Fatalf("functional source-copy example became quality evidence: %+v %v", frozen.Report, err)
	}
}

func TestWikiQualityRejectsChangedRawSourceQuoteGradeAndOldBase(t *testing.T) {
	base := qualityFixture(t)
	badSource := base
	badSource.Sources = append([]Source(nil), base.Sources...)
	badSource.Sources[0].Content += "更新"
	if _, err := EvaluateCase(context.Background(), badSource, Verifiers{}); !errors.Is(err, ErrEvidence) {
		t.Fatalf("changed SourceRevision bytes preserved old SHA: %v", err)
	}
	newRevision := base
	newRevision.Sources = append([]Source(nil), base.Sources...)
	newRevision.Scope.SourceRevisionIDs = append([]string(nil), base.Scope.SourceRevisionIDs...)
	newRevision.Sources[1].RevisionID, newRevision.Scope.SourceRevisionIDs[1] =
		"source-r2-new", "source-r2-new"
	newRevision.Target.SourceRefs = append([]SourceRef(nil), base.Target.SourceRefs...)
	for i := range newRevision.Target.SourceRefs {
		if newRevision.Target.SourceRefs[i].RevisionID == "source-r2" {
			newRevision.Target.SourceRefs[i].RevisionID = "source-r2-new"
		}
	}
	newRevision.Candidate = &Candidate{Content: newRevision.Target.Content,
		ContentSHA256: newRevision.Target.ContentSHA256,
		SourceRefs:    newRevision.Target.SourceRefs}
	if _, err := EvaluateCase(context.Background(), newRevision, Verifiers{}); !errors.Is(err, ErrScope) {
		t.Fatalf("new SourceRevision stole older RTW human facts: %v", err)
	}
	badQuote := base
	badQuote.Review.Facts = append([]Fact(nil), base.Review.Facts...)
	badQuote.Review.Facts[0].OriginalByteStart++
	if _, err := EvaluateCase(context.Background(), badQuote, Verifiers{}); !errors.Is(err, ErrEvidence) {
		t.Fatalf("human quote no longer original paragraph substring: %v", err)
	}
	badGrade := base
	badGrade.Review.Judgments = append([]FactJudgment(nil), base.Review.Judgments...)
	badGrade.Review.Judgments[1].Grade = "3" // conflict cannot carry a 3.
	if _, err := EvaluateCase(context.Background(), badGrade, Verifiers{}); !errors.Is(err, ErrEvidence) {
		t.Fatalf("RTW grade/disposition contradictory: %v", err)
	}
	manual := manualQualityFixture(t, base)
	manual.Target.BaseRevisionID = "other-old-head"
	if _, err := EvaluateCase(context.Background(), manual, Verifiers{}); !errors.Is(err, ErrScope) {
		t.Fatalf("manual old-base mutation crossed accepted AI base: %v", err)
	}
	manual = manualQualityFixture(t, base)
	manual.Scope.Hashes = base.Scope.Hashes
	if _, err := EvaluateCase(context.Background(), manual, Verifiers{}); !errors.Is(err, ErrScope) {
		t.Fatalf("manual Wiki revision inherited a different AI technical Job hash: %v", err)
	}
}

func TestWikiQualityMissingPinnedCitationStaysVisible(t *testing.T) {
	input := qualityFixture(t)
	input.Target.SourceRefs = append([]SourceRef(nil), input.Target.SourceRefs[1:]...)
	input.Candidate = &Candidate{Content: input.Target.Content,
		ContentSHA256: input.Target.ContentSHA256, SourceRefs: input.Target.SourceRefs}
	input.Review.Judgments = append([]FactJudgment(nil), input.Review.Judgments...)
	input.Review.Judgments[4].Grade = "1" // Human says covered, citation incomplete.
	frozen, err := EvaluateCase(context.Background(), input, Verifiers{})
	if err != nil || frozen.Report.State != Observed ||
		frozen.Report.FactCounts.CoveredWithoutPinnedCitation != 1 ||
		frozen.Report.CitationFidelity != (Fraction{State: Observed, Numerator: 1, Denominator: 2}) ||
		frozen.Report.GradeHistogram[1].Count != 1 {
		t.Fatalf("multi-source missing citation was hidden or promoted to faithful: %+v %v", frozen.Report, err)
	}
	input.Review.Judgments[4].Grade = "3" // Human high grade contradicts absent ref.
	if bad, err := EvaluateCase(context.Background(), input, Verifiers{}); err != nil ||
		bad.Report.State != NotEvaluable || bad.Report.Reason != "high_grade_without_pinned_citation" ||
		bad.Report.RequiredCoverage.State != NotEvaluable {
		t.Fatalf("unsupported high-grade citation became evaluable: %+v %v", bad.Report, err)
	}
}

func TestWikiQualityRequiredUndeterminedDoesNotBecomeZeroCoverage(t *testing.T) {
	input := qualityFixture(t)
	input.Review.Facts = append([]Fact(nil), input.Review.Facts...)
	input.Review.Facts[0].Required = true // The fifth source fact is human-undetermined.
	frozen, err := EvaluateCase(context.Background(), input, Verifiers{})
	if err != nil || frozen.Report.State != Observed ||
		frozen.Report.FactCounts.Required != 5 ||
		frozen.Report.FactCounts.Undetermined != 1 ||
		frozen.Report.RequiredCoverage.State != NotEvaluable ||
		frozen.Report.RequiredCoverage.Reason != "required_human_fact_undetermined" ||
		frozen.Report.RequiredCoverage.Denominator != 0 {
		t.Fatalf("required pending human fact was counted as coverage failure: %+v %v",
			frozen.Report, err)
	}
}

func TestWikiQualityJCSReadersRejectTamperAndKeepReviewVersions(t *testing.T) {
	firstInput := qualityFixture(t)
	first, err := EvaluateCase(context.Background(), firstInput, Verifiers{})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := DecodeCaseManifest(first.ManifestJCS, first.ManifestSHA256); err != nil ||
		value.CaseID != first.Manifest.CaseID {
		t.Fatalf("immutable Case JCS failed independent reader: %+v %v", value, err)
	}
	secondInput := qualityFixture(t)
	secondInput.Review.ReviewID, secondInput.Review.ReviewVersion = "review-r2", "judge-r2"
	second, err := EvaluateCase(context.Background(), secondInput, Verifiers{})
	if err != nil || second.Manifest.CaseID == first.Manifest.CaseID {
		t.Fatalf("new human grade version overwrote prior immutable Wiki case: %v", err)
	}
	dataset, err := FreezeDataset("same-wiki-two-judges", []FrozenCase{second, first})
	if err != nil || len(dataset.Manifest.Entries) != 2 ||
		dataset.Manifest.Entries[0].WikiRevisionID != first.Manifest.Target.RevisionID ||
		dataset.Manifest.Entries[1].WikiRevisionID != first.Manifest.Target.RevisionID {
		t.Fatalf("same WikiRevision distinct judge versions cannot coexist: %+v %v", dataset.Manifest, err)
	}
	if value, err := DecodeDatasetManifest(dataset.JCS, dataset.RootSHA256); err != nil ||
		len(value.Entries) != 2 {
		t.Fatalf("Dataset root JCS independent reader failed: %+v %v", value, err)
	}
	tamperedCase := bytes.Replace(first.ManifestJCS, []byte(first.Manifest.CaseID),
		[]byte("wiki-quality-"+strings.Repeat("a", 64)), 1)
	if _, err := DecodeCaseManifest(tamperedCase, Digest(tamperedCase)); !errors.Is(err, ErrDataset) {
		t.Fatalf("rehashed but forged CaseID passed quality reader: %v", err)
	}
	withUnknown := append(bytes.TrimSuffix(append([]byte(nil), first.ManifestJCS...), []byte("}")),
		[]byte(`,"unknown":"x"}`)...)
	if _, err := DecodeCaseManifest(withUnknown, Digest(withUnknown)); !errors.Is(err, ErrDataset) {
		t.Fatalf("unknown source case key passed typed JCS reader: %v", err)
	}
	trailing := append(append([]byte(nil), dataset.JCS...), '\n')
	if _, err := DecodeDatasetManifest(trailing, Digest(trailing)); !errors.Is(err, ErrDataset) {
		t.Fatalf("trailing non-JCS bytes passed dataset root reader: %v", err)
	}
	if _, err := FreezeDataset("bad-duplicate", []FrozenCase{first, first}); !errors.Is(err, ErrDataset) {
		t.Fatalf("duplicate target/review entry promoted into Dataset root: %v", err)
	}
}
