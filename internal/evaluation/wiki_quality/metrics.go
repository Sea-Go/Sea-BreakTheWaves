package wiki_quality

import (
	"bytes"
	"strconv"
	"strings"
)

type Fraction struct {
	State       State  `json:"state"`
	Reason      string `json:"reason,omitempty"`
	Numerator   int    `json:"numerator"`
	Denominator int    `json:"denominator"`
}

type GradeCount struct {
	Grade   string `json:"grade"`
	Meaning string `json:"meaning"`
	Count   int    `json:"count"`
}

type FactOutcome struct {
	FactID             string      `json:"fact_id"`
	SourceRevisionID   string      `json:"source_revision_id"`
	Locator            string      `json:"locator"`
	OriginalByteSHA256 string      `json:"original_byte_sha256"`
	Required           bool        `json:"required"`
	Disposition        Disposition `json:"disposition"`
	Grade              string      `json:"grade,omitempty"`
	HasPinnedCitation  bool        `json:"has_pinned_citation"`
}

type FactCounts struct {
	Total                        int `json:"total"`
	Required                     int `json:"required"`
	Covered                      int `json:"covered"`
	Missing                      int `json:"missing"`
	Conflict                     int `json:"conflict"`
	Undetermined                 int `json:"undetermined"`
	PinnedCitations              int `json:"pinned_citations"`
	CoveredWithoutPinnedCitation int `json:"covered_without_pinned_citation"`
	DeclaredConflictGroups       int `json:"declared_conflict_groups"`
}

type StructureStats struct {
	Paragraphs         int `json:"paragraphs"`
	RepeatedParagraphs int `json:"repeated_paragraphs"`
	ATXHeadingLines    int `json:"atx_heading_lines"`
	FourSpaceCodeLines int `json:"four_space_code_lines"`
	CRLFSequences      int `json:"crlf_sequences"`
}

type EditStats struct {
	State                  State  `json:"state"`
	PreviousWikiRevisionID string `json:"previous_wiki_revision_id,omitempty"`
	ByteAddedWindow        int    `json:"byte_added_window"`
	ByteRemovedWindow      int    `json:"byte_removed_window"`
	ExactLinesAdded        int    `json:"exact_lines_added"`
	ExactLinesRemoved      int    `json:"exact_lines_removed"`
	ATXHeadingsBefore      int    `json:"atx_headings_before"`
	ATXHeadingsAfter       int    `json:"atx_headings_after"`
}

type CostStats struct {
	State            State  `json:"state"`
	Reason           string `json:"reason,omitempty"`
	ReceiptSHA256    string `json:"receipt_sha256,omitempty"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
	WallMillis       int64  `json:"wall_millis"`
}

// A Report counts signed HUMAN statuses; grade meanings are the RTW rubric,
// not model confidence or a Release threshold. Fraction retains integer
// numerator/denominator so downstream ADS can choose its measured threshold.
type Report struct {
	SchemaVersion           string         `json:"schema_version"`
	MetricRevision          string         `json:"metric_revision"`
	RubricVersion           string         `json:"rubric_version"`
	WikiRevisionID          string         `json:"wiki_revision_id"`
	WikiOriginKind          WikiKind       `json:"wiki_origin_kind"`
	CaseID                  string         `json:"case_id"`
	CaseManifestSHA256      string         `json:"case_manifest_sha256"`
	State                   State          `json:"state"`
	Reason                  string         `json:"reason,omitempty"`
	EvidenceKind            DataKind       `json:"evidence_kind"`
	AuthorityEvidenceSHA256 string         `json:"authority_evidence_sha256,omitempty"`
	FactCounts              FactCounts     `json:"fact_counts"`
	GradeHistogram          []GradeCount   `json:"grade_histogram"`
	RequiredCoverage        Fraction       `json:"required_coverage"`
	CitationFidelity        Fraction       `json:"citation_fidelity"`
	Outcomes                []FactOutcome  `json:"fact_outcomes"`
	Structure               StructureStats `json:"structure"`
	Edit                    EditStats      `json:"edit"`
	Cost                    CostStats      `json:"cost"`
	Activation              string         `json:"activation"` // Always none; RTW admin owns manual Release.
}

func gradeHistogram() []GradeCount {
	return []GradeCount{
		{Grade: "0", Meaning: "missing_or_conflict", Count: 0},
		{Grade: "1", Meaning: "covered_expression_or_citation_incomplete_or_ambiguous", Count: 0},
		{Grade: "2", Meaning: "covered_fact_and_citation_faithful_context_incomplete", Count: 0},
		{Grade: "3", Meaning: "covered_complete_context_and_faithful_citation_maintainable", Count: 0},
	}
}

func fixedParagraphs(content string) []string {
	blocks := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n\n")
	out := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if strings.TrimSpace(block) != "" {
			out = append(out, block)
		}
	}
	return out
}

func atxHeading(line string) bool {
	spaces := 0
	for spaces < len(line) && spaces < 4 && line[spaces] == ' ' {
		spaces++
	}
	if spaces > 3 {
		return false
	}
	line = line[spaces:]
	count := 0
	for count < len(line) && count < 7 && line[count] == '#' {
		count++
	}
	return count >= 1 && count <= 6 &&
		(count == len(line) || line[count] == ' ' || line[count] == '\t')
}

func structure(content string) StructureStats {
	blocks := fixedParagraphs(content)
	seen := map[string]bool{}
	stats := StructureStats{Paragraphs: len(blocks),
		CRLFSequences: bytes.Count([]byte(content), []byte("\r\n"))}
	for _, block := range blocks {
		if seen[block] {
			stats.RepeatedParagraphs++
		}
		seen[block] = true
	}
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "    ") {
			stats.FourSpaceCodeLines++
		}
		if atxHeading(line) {
			stats.ATXHeadingLines++
		}
	}
	return stats
}

func editStats(target WikiVersion, previous *WikiVersion) EditStats {
	if target.Kind != ManualRevisionKind || previous == nil {
		return EditStats{State: NotApplicable}
	}
	before, after := []byte(previous.Content), []byte(target.Content)
	prefix := 0
	for prefix < len(before) && prefix < len(after) && before[prefix] == after[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(before)-prefix && suffix < len(after)-prefix &&
		before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}
	// This byte change WINDOW is stable and does not pretend to be a semantic
	// edit distance. Exact line multiset deltas expose reordering separately.
	result := EditStats{State: Observed, PreviousWikiRevisionID: previous.RevisionID,
		ByteAddedWindow:   len(after) - prefix - suffix,
		ByteRemovedWindow: len(before) - prefix - suffix,
		ATXHeadingsBefore: structure(previous.Content).ATXHeadingLines,
		ATXHeadingsAfter:  structure(target.Content).ATXHeadingLines}
	lineCounts := map[string]int{}
	for _, line := range strings.Split(previous.Content, "\n") {
		lineCounts[line]++
	}
	for _, line := range strings.Split(target.Content, "\n") {
		lineCounts[line]--
	}
	for _, delta := range lineCounts {
		if delta > 0 {
			result.ExactLinesRemoved += delta
		} else {
			result.ExactLinesAdded -= delta
		}
	}
	return result
}

func costStats(target WikiVersion, receipt *CostReceipt, verified bool) CostStats {
	if target.Kind == ManualRevisionKind {
		return CostStats{State: NotApplicable}
	}
	if receipt == nil {
		return CostStats{State: NotEvaluable, Reason: "dc_usage_receipt_missing"}
	}
	if !verified {
		return CostStats{State: NotEvaluable, Reason: "dc_usage_authority_not_verified"}
	}
	return CostStats{State: Observed, ReceiptSHA256: receipt.DCUsageReceiptSHA256,
		PromptTokens: receipt.PromptTokens, CompletionTokens: receipt.CompletionTokens,
		TotalTokens: receipt.TotalTokens, WallMillis: receipt.WallMillis}
}

func hasRef(target WikiVersion, fact Fact) bool {
	for _, ref := range target.SourceRefs {
		if ref.RevisionID == fact.SourceRevisionID && ref.Locator == fact.Locator {
			return true
		}
	}
	return false
}

func factMetrics(input Input, judgments map[string]FactJudgment,
	q qualification, costVerified bool) Report {
	report := Report{SchemaVersion: ReportSchema, MetricRevision: MetricRevision,
		RubricVersion: RubricVersion, WikiRevisionID: input.Target.RevisionID,
		WikiOriginKind: input.Target.Kind, State: q.State, Reason: q.Reason,
		EvidenceKind: input.Review.DataKind, Activation: "none",
		GradeHistogram: gradeHistogram(), Structure: structure(input.Target.Content),
		Edit: editStats(input.Target, input.PreviousAI), Cost: costStats(input.Target, input.Cost, costVerified),
		RequiredCoverage: Fraction{State: NotEvaluable},
		CitationFidelity: Fraction{State: NotEvaluable}}
	if q.State != Observed {
		return report
	}
	report.AuthorityEvidenceSHA256 = q.Receipt.AuthorityEvidenceSHA256
	groups := map[string]int{}
	for _, fact := range sortedFacts(input.Review.Facts) {
		judgment := judgments[fact.FactID]
		cited := hasRef(input.Target, fact)
		if judgment.Disposition == Covered && (judgment.Grade == "2" || judgment.Grade == "3") && !cited {
			// RTW's rubric requires citation fidelity at these grades. This is a
			// deterministic cross-check, not a model-driven entailment decision.
			report.State, report.Reason = NotEvaluable, "high_grade_without_pinned_citation"
			report.FactCounts, report.Outcomes = FactCounts{}, nil
			return report
		}
		report.Outcomes = append(report.Outcomes, FactOutcome{FactID: fact.FactID,
			SourceRevisionID: fact.SourceRevisionID, Locator: fact.Locator,
			OriginalByteSHA256: fact.OriginalByteSHA256, Required: fact.Required,
			Disposition: judgment.Disposition, Grade: judgment.Grade,
			HasPinnedCitation: cited})
		report.FactCounts.Total++
		if fact.Required {
			report.FactCounts.Required++
		}
		if fact.ConflictGroup != "" {
			groups[fact.ConflictGroup]++
		}
		switch judgment.Disposition {
		case Covered:
			report.FactCounts.Covered++
			if !cited {
				report.FactCounts.CoveredWithoutPinnedCitation++
			}
		case Missing:
			report.FactCounts.Missing++
		case Conflict:
			report.FactCounts.Conflict++
		case Undetermined:
			report.FactCounts.Undetermined++
		}
		if cited {
			report.FactCounts.PinnedCitations++
		}
		if judgment.Grade != "" {
			grade, _ := strconv.Atoi(judgment.Grade)
			report.GradeHistogram[grade].Count++
		}
	}
	for _, count := range groups {
		if count > 1 {
			report.FactCounts.DeclaredConflictGroups++
		}
	}
	requiredCovered := 0
	requiredUndetermined := 0
	for _, outcome := range report.Outcomes {
		if outcome.Required && outcome.Disposition == Covered {
			requiredCovered++
		}
		if outcome.Required && outcome.Disposition == Undetermined {
			requiredUndetermined++
		}
	}
	if requiredUndetermined > 0 {
		report.RequiredCoverage = Fraction{State: NotEvaluable,
			Reason: "required_human_fact_undetermined"}
	} else if report.FactCounts.Required > 0 {
		report.RequiredCoverage = Fraction{State: Observed,
			Numerator: requiredCovered, Denominator: report.FactCounts.Required}
	} else {
		report.RequiredCoverage = Fraction{State: NotApplicable}
	}
	if report.FactCounts.Covered > 0 {
		report.CitationFidelity = Fraction{State: Observed,
			Numerator:   report.FactCounts.Covered - report.FactCounts.CoveredWithoutPinnedCitation,
			Denominator: report.FactCounts.Covered}
	} else {
		report.CitationFidelity = Fraction{State: NotApplicable}
	}
	return report
}
