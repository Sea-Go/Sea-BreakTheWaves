// Command search-summary-matrix-witness projects exactly one RTW+DataCenter
// live summary receipt into an honest 12-path execution witness. It does not
// mix another release or pretend unjudged relevance is a numeric score.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/evaluation"
)

const schema = "sea.search.live-summary-matrix.v1"

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type modelUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type boundReport struct {
	SchemaVersion                string      `json:"schema_version"`
	Status                       string      `json:"status"`
	SearchID                     string      `json:"search_id"`
	AnswerID                     string      `json:"answer_id"`
	RTWAnswerSHA256              string      `json:"rtw_answer_sha256"`
	EvidenceQuote                string      `json:"evidence_quote"`
	EvidenceQuoteSHA256          string      `json:"evidence_quote_sha256"`
	RTWAcceptedAnswer            string      `json:"rtw_accepted_answer"`
	CitationReceiptRef           string      `json:"citation_receipt_ref"`
	ModelConfigurationID         string      `json:"model_configuration_id"`
	PhysicalModel                string      `json:"physical_model"`
	ModelProvider                string      `json:"model_provider"`
	ModelInteractionID           string      `json:"model_interaction_id"`
	ModelResponseSHA256          string      `json:"model_response_sha256"`
	ModelLatencyMS               int64       `json:"model_latency_ms"`
	EndToEndLatencyMS            int64       `json:"end_to_end_latency_ms"`
	ProviderUsage                *modelUsage `json:"provider_usage"`
	RTWDurableAnswerRows         int         `json:"rtw_durable_answer_rows"`
	RTWDurableAnswerCitationRows int         `json:"rtw_durable_answer_citation_rows"`
	RTWDurableSearchCitationRows int         `json:"rtw_durable_search_citation_rows"`
	RTWProductHistoryPresent     bool        `json:"rtw_product_history_present"`
	RealUserCenter               bool        `json:"real_user_center"`
	QualityGate                  string      `json:"quality_gate"`
	RelevanceState               string      `json:"relevance_state"`
	Activation                   string      `json:"activation"`
}

type rtwReport struct {
	SchemaVersion            string `json:"schema_version"`
	DeliveryExecution        string `json:"delivery_execution"`
	Depth                    string `json:"depth"`
	Intelligence             string `json:"intelligence"`
	Delivery                 string `json:"delivery"`
	SearchID                 string `json:"search_id"`
	AnswerID                 string `json:"answer_id"`
	SubjectAuthorityID       string `json:"subject_authority_id"`
	SubjectTenantID          string `json:"subject_tenant_id"`
	SubjectID                string `json:"subject_id"`
	SessionID                string `json:"session_id"`
	Answer                   string `json:"answer"`
	CitationReceiptRef       string `json:"citation_receipt_ref"`
	EvidenceID               string `json:"evidence_id"`
	Quote                    string `json:"quote"`
	QuoteHash                string `json:"quote_hash"`
	EndToEndLatencyMS        int64  `json:"end_to_end_latency_ms"`
	RTWAnswerRows            int    `json:"rtw_answer_rows"`
	RTWAnswerCitationRows    int    `json:"rtw_answer_citation_rows"`
	RTWSearchCitationRows    int    `json:"rtw_search_citation_rows"`
	RTWProductHistoryPresent bool   `json:"rtw_product_history_present"`
	RealUserCenter           bool   `json:"real_user_center"`
	ModelGateway             string `json:"model_gateway"`
	QrelComplete             bool   `json:"qrel_complete"`
}

type metric struct {
	State string   `json:"state"`
	Value *float64 `json:"value"`
}

type cell struct {
	Variant            evaluation.SearchVariant `json:"variant"`
	DeliveryExecution  string                   `json:"delivery_execution"`
	ProductStatus      string                   `json:"product_status"`
	Reason             string                   `json:"reason,omitempty"`
	SearchID           string                   `json:"search_id,omitempty"`
	AnswerID           string                   `json:"answer_id,omitempty"`
	EvidenceOrder      []string                 `json:"evidence_order,omitempty"`
	CitationReceiptRef string                   `json:"citation_receipt_ref,omitempty"`
	ModelInteractionID string                   `json:"model_interaction_id,omitempty"`
	ProviderUsage      *modelUsage              `json:"provider_usage"`
	ModelLatencyMS     *int64                   `json:"model_latency_ms"`
	EndToEndLatencyMS  *int64                   `json:"end_to_end_latency_ms"`
	Metrics            map[string]metric        `json:"metrics"`
}

type report struct {
	SchemaVersion       string   `json:"schema_version"`
	SourceUsageSHA256   string   `json:"source_usage_sha256"`
	SourceRTWSHA256     string   `json:"source_rtw_sha256"`
	RTWEvidencePackRef  string   `json:"rtw_evidence_pack_ref"`
	SourceScope         string   `json:"source_scope"`
	ActualDeliveryCells int      `json:"actual_delivery_cells"`
	NotExecutedCells    int      `json:"not_executed_cells"`
	QualityGate         string   `json:"quality_gate"`
	RelevanceState      string   `json:"relevance_state"`
	Recommendation      string   `json:"recommendation"`
	EvidenceQuote       string   `json:"evidence_quote"`
	RTWAcceptedAnswer   string   `json:"rtw_accepted_answer"`
	Cells               []cell   `json:"cells"`
	Limitations         []string `json:"limitations"`
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func read(path string, target any) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() < 1 || info.Size() > 1<<20 {
		return nil, errors.New("live summary source must be a private bounded regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("live summary source JSON contract differs")
	}
	return raw, nil
}

func project(usage boundReport, usageSHA string, rtw rtwReport, rtwSHA string) (report, error) {
	var out report
	if usage.SchemaVersion != "sea.search.live-summary-usage.v1" || usage.Status != "functional_chain_passed" ||
		usage.SearchID == "" || usage.AnswerID == "" || usage.RTWAnswerSHA256 != rtwSHA ||
		usage.SearchID != rtw.SearchID || usage.AnswerID != rtw.AnswerID ||
		usage.RTWAcceptedAnswer != rtw.Answer || usage.EvidenceQuote != rtw.Quote ||
		usage.EvidenceQuoteSHA256 != rtw.QuoteHash || digest([]byte(rtw.Quote)) != rtw.QuoteHash ||
		usage.CitationReceiptRef != rtw.CitationReceiptRef || !hashPattern.MatchString(usage.ModelResponseSHA256) ||
		usage.ModelConfigurationID == "" || usage.ModelInteractionID == "" || usage.PhysicalModel == "" ||
		usage.ModelLatencyMS < 1 || usage.EndToEndLatencyMS < usage.ModelLatencyMS ||
		usage.EndToEndLatencyMS != rtw.EndToEndLatencyMS ||
		usage.ProviderUsage == nil || usage.ProviderUsage.PromptTokens < 1 ||
		usage.ProviderUsage.CompletionTokens < 1 ||
		usage.ProviderUsage.TotalTokens != usage.ProviderUsage.PromptTokens+usage.ProviderUsage.CompletionTokens ||
		usage.RTWDurableAnswerRows != 1 || usage.RTWDurableAnswerCitationRows != 1 ||
		usage.RTWDurableSearchCitationRows != 1 || !usage.RTWProductHistoryPresent || !usage.RealUserCenter ||
		usage.QualityGate != "not_passed_unsupported_interpretation" ||
		usage.RelevanceState != "not_evaluable_no_qrels" || usage.Activation != "none" ||
		rtw.SchemaVersion != "sea.search.live-summary-rtw.v1" || rtw.DeliveryExecution != "observed" ||
		rtw.Depth != "fast" || rtw.Intelligence != "low" || rtw.Delivery != "summary" ||
		rtw.EvidenceID == "" || rtw.SubjectAuthorityID != "rtw.identity" || rtw.SubjectTenantID == "" ||
		rtw.SubjectID == "" || rtw.SessionID == "" || rtw.ModelGateway != "datacenter-local-ollama" ||
		rtw.QrelComplete || rtw.RTWAnswerRows != 1 || rtw.RTWAnswerCitationRows != 1 ||
		rtw.RTWSearchCitationRows != 1 || !rtw.RTWProductHistoryPresent || !rtw.RealUserCenter {
		return out, errors.New("RTW/DC one-cell summary receipts differ or quality was promoted")
	}
	const prefix = "search-citations/sha256/"
	if !strings.HasPrefix(rtw.CitationReceiptRef, prefix) ||
		!hashPattern.MatchString(strings.TrimPrefix(rtw.CitationReceiptRef, prefix)) {
		return out, errors.New("RTW durable evidence pack reference invalid")
	}
	out = report{SchemaVersion: schema, SourceUsageSHA256: usageSHA, SourceRTWSHA256: rtwSHA,
		RTWEvidencePackRef: rtw.CitationReceiptRef, SourceScope: "one_RTW_durable_evidence_pack_not_full_qrel_corpus",
		ActualDeliveryCells: 1, NotExecutedCells: 11,
		QualityGate: usage.QualityGate, RelevanceState: usage.RelevanceState,
		Recommendation: "incomplete", EvidenceQuote: rtw.Quote, RTWAcceptedAnswer: rtw.Answer,
		Cells: make([]cell, 0, 12), Limitations: []string{
			"fast/low/summary is an actual RTW+DataCenter functional delivery with a failed semantic quality gate; activation remains none",
			"RTW citation pack is one published-snapshot receipt, not a frozen fully judged candidate corpus",
			"S09/S11/S12 are not_evaluable without approved qrels and judged candidate coverage; no zero or uplift is implied",
			"the other 11 paths were not executed in this RTW snapshot; an earlier Tools witness belongs to another snapshot and is excluded",
			"model and end-to-end timings are local functional observations, not a performance SLO acceptance",
		}}
	for _, variant := range evaluation.SearchVariants() {
		cell := cell{Variant: variant, DeliveryExecution: "not_executed", ProductStatus: "not_executed",
			Reason: "profile_or_delivery_dependency_not_executed",
			Metrics: map[string]metric{"S09.recall_at_k": {State: "not_evaluable"},
				"S11.mrr_at_k": {State: "not_evaluable"}, "S12.ndcg_at_k": {State: "not_evaluable"}}}
		if variant == (evaluation.SearchVariant{Depth: "fast", Intelligence: "low", Delivery: "summary"}) {
			cell.DeliveryExecution, cell.ProductStatus, cell.Reason = "observed", "functional_success_quality_failed", ""
			cell.SearchID, cell.AnswerID = rtw.SearchID, rtw.AnswerID
			cell.EvidenceOrder = []string{rtw.EvidenceID}
			cell.CitationReceiptRef = rtw.CitationReceiptRef
			cell.ModelInteractionID, cell.ProviderUsage = usage.ModelInteractionID, usage.ProviderUsage
			cell.ModelLatencyMS, cell.EndToEndLatencyMS = &usage.ModelLatencyMS, &usage.EndToEndLatencyMS
		}
		out.Cells = append(out.Cells, cell)
	}
	return out, nil
}

func main() {
	var usagePath, rtwPath, outputPath string
	flag.StringVar(&usagePath, "usage-report", "", "private DC/RTW joined model usage report")
	flag.StringVar(&rtwPath, "rtw-report", "", "private original RTW accepted answer report")
	flag.StringVar(&outputPath, "output", "", "new immutable one-snapshot matrix witness")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if usagePath == "" || rtwPath == "" || outputPath == "" {
		logger.Error("usage, RTW report and output are required")
		os.Exit(2)
	}
	var usage boundReport
	usageRaw, err := read(usagePath, &usage)
	if err != nil {
		logger.Error("load DC/RTW usage", "error", err)
		os.Exit(1)
	}
	var rtw rtwReport
	rtwRaw, err := read(rtwPath, &rtw)
	if err != nil {
		logger.Error("load RTW durable answer", "error", err)
		os.Exit(1)
	}
	result, err := project(usage, digest(usageRaw), rtw, digest(rtwRaw))
	if err != nil {
		logger.Error("one-cell summary projection rejected", "error", err)
		os.Exit(1)
	}
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		logger.Error("encode one-cell witness", "error", err)
		os.Exit(1)
	}
	file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		logger.Error("create immutable one-cell witness", "error", err)
		os.Exit(1)
	}
	if _, err := file.Write(append(body, '\n')); err != nil {
		_ = file.Close()
		logger.Error("write one-cell witness", "error", err)
		os.Exit(1)
	}
	if err := file.Close(); err != nil {
		logger.Error("close one-cell witness", "error", err)
		os.Exit(1)
	}
	logger.Info("RTW/DC live summary mapped to separate incomplete matrix",
		"output", outputPath, "observed", result.ActualDeliveryCells,
		"not_executed", result.NotExecutedCells, "quality_gate", result.QualityGate)
}
