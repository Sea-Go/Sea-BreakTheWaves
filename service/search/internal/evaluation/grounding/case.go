// Package grounding freezes RTW/DC source and trace evidence for an offline,
// human-reviewed answer-grounding gate. It never decides entailment from text.
package grounding

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

const CaseSchema = "sea.search.answer-grounding-case.v1"
const ReviewPolicy = "sea.search.answer-grounding-human-review.v1"
const citationRefPrefix = "search-citations/sha256/"

var ErrEvidence = errors.New("answer grounding source evidence mismatch")
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var tracePattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Evidence struct {
	ID          string `json:"evidence_id"`
	Quote       string `json:"quote"`
	QuoteSHA256 string `json:"quote_sha256"`
}

// Case contains exactly the accepted answer, its cited quote and the source
// receipts. TraceScope is RTW structured-log correlation, not OTLP proof.
type Case struct {
	SchemaVersion           string     `json:"schema_version"`
	CaseID                  string     `json:"case_id"`
	PolicyRevision          string     `json:"policy_revision"`
	ReviewStatus            string     `json:"review_status"`
	Activation              string     `json:"activation"`
	SearchID                string     `json:"search_id"`
	AnswerID                string     `json:"answer_id"`
	ModelInteractionID      string     `json:"model_interaction_id"`
	ModelResponseSHA256     string     `json:"model_response_sha256"`
	RTWAnswerReportSHA256   string     `json:"rtw_answer_report_sha256"`
	DCUsageReportSHA256     string     `json:"dc_usage_report_sha256"`
	RTWTraceLogSHA256       string     `json:"rtw_trace_log_sha256"`
	RTWTraceID              string     `json:"rtw_trace_id"`
	TraceScope              string     `json:"trace_scope"`
	CitationPackRef         string     `json:"citation_pack_ref"`
	Evidence                []Evidence `json:"evidence"`
	AcceptedAnswer          string     `json:"accepted_answer"`
	AcceptedAnswerSHA256    string     `json:"accepted_answer_sha256"`
	PriorQualityObservation string     `json:"prior_quality_observation"`
}

type usageReport struct {
	SchemaVersion       string `json:"schema_version"`
	Status              string `json:"status"`
	SearchID            string `json:"search_id"`
	AnswerID            string `json:"answer_id"`
	RTWAnswerSHA256     string `json:"rtw_answer_sha256"`
	EvidenceQuote       string `json:"evidence_quote"`
	EvidenceQuoteSHA256 string `json:"evidence_quote_sha256"`
	RTWAcceptedAnswer   string `json:"rtw_accepted_answer"`
	CitationReceiptRef  string `json:"citation_receipt_ref"`
	ModelInteractionID  string `json:"model_interaction_id"`
	ModelResponseSHA256 string `json:"model_response_sha256"`
	QualityGate         string `json:"quality_gate"`
	Activation          string `json:"activation"`
}

type rtwReport struct {
	SchemaVersion            string `json:"schema_version"`
	DeliveryExecution        string `json:"delivery_execution"`
	SearchID                 string `json:"search_id"`
	AnswerID                 string `json:"answer_id"`
	Answer                   string `json:"answer"`
	EvidenceID               string `json:"evidence_id"`
	Quote                    string `json:"quote"`
	QuoteHash                string `json:"quote_hash"`
	CitationReceiptRef       string `json:"citation_receipt_ref"`
	RTWAnswerRows            int    `json:"rtw_answer_rows"`
	RTWAnswerCitationRows    int    `json:"rtw_answer_citation_rows"`
	RTWSearchCitationRows    int    `json:"rtw_search_citation_rows"`
	RTWProductHistoryPresent bool   `json:"rtw_product_history_present"`
}

func Digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// citationRef is RTW's durable lookup identity. It is derived from the
// search ID and is deliberately independent from the evidence-pack hash.
func citationRef(searchID string) string {
	return citationRefPrefix + Digest([]byte(searchID))
}

func readPrivate(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 ||
		info.Size() < 1 || info.Size() > limit {
		return nil, ErrEvidence
	}
	return os.ReadFile(path)
}

func readJSON(path string, limit int64, target any) ([]byte, error) {
	raw, err := readPrivate(path, limit)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, ErrEvidence
	}
	return raw, nil
}

func traceID(raw []byte, searchID, answerID string) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	needed := []string{"knowledge.product.search.succeeded",
		"knowledge.search.citations.accept.succeeded", "knowledge.answer.accept.succeeded"}
	counts := map[string]map[string]int{}
	for scanner.Scan() {
		var event struct {
			Event       string `json:"event"`
			SearchID    string `json:"search_id"`
			AnswerID    string `json:"answer_id"`
			OperationID string `json:"operation_id"`
			TraceID     string `json:"trace_id"`
			Outcome     string `json:"outcome"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue // unrelated framework lines are not accepted as receipts
		}
		wanted := false
		for _, name := range needed {
			wanted = wanted || name == event.Event
		}
		if !wanted || event.Outcome != "succeeded" {
			continue
		}
		matched := false
		switch event.Event {
		case "knowledge.product.search.succeeded", "knowledge.answer.accept.succeeded":
			matched = event.SearchID == searchID && event.AnswerID == answerID
		case "knowledge.search.citations.accept.succeeded":
			matched = event.SearchID == searchID && event.OperationID == "search-citations:"+searchID
		}
		if !matched {
			continue
		}
		if !tracePattern.MatchString(event.TraceID) {
			return "", ErrEvidence
		}
		if counts[event.TraceID] == nil {
			counts[event.TraceID] = map[string]int{}
		}
		counts[event.TraceID][event.Event]++
	}
	if scanner.Err() != nil {
		return "", ErrEvidence
	}
	var complete []string
	for id, events := range counts {
		if events[needed[0]] == 1 && events[needed[1]] == 1 && events[needed[2]] == 1 {
			complete = append(complete, id)
		}
	}
	if len(complete) != 1 {
		return "", fmt.Errorf("%w: expected one same-trace product/citation/answer success", ErrEvidence)
	}
	return complete[0], nil
}

// FreezeCase binds exact private source bytes and the three same-trace RTW
// success events. The prior model-quality observation is NOT a human label.
func FreezeCase(usagePath, rtwPath, tracePath string) (Case, error) {
	var out Case
	var usage usageReport
	usageRaw, err := readJSON(usagePath, 1<<20, &usage)
	if err != nil {
		return out, err
	}
	var rtw rtwReport
	rtwRaw, err := readJSON(rtwPath, 1<<20, &rtw)
	if err != nil {
		return out, err
	}
	logRaw, err := readPrivate(tracePath, 20<<20)
	if err != nil {
		return out, err
	}
	if usage.SchemaVersion != "sea.search.live-summary-usage.v1" || usage.Status != "functional_chain_passed" ||
		usage.SearchID == "" || usage.AnswerID == "" || usage.SearchID != rtw.SearchID ||
		usage.AnswerID != rtw.AnswerID || usage.RTWAnswerSHA256 != Digest(rtwRaw) ||
		usage.EvidenceQuote != rtw.Quote || usage.EvidenceQuoteSHA256 != rtw.QuoteHash ||
		usage.RTWAcceptedAnswer != rtw.Answer || usage.CitationReceiptRef != rtw.CitationReceiptRef ||
		!hashPattern.MatchString(usage.ModelResponseSHA256) || usage.ModelInteractionID == "" ||
		usage.QualityGate == "" || usage.Activation != "none" ||
		rtw.SchemaVersion != "sea.search.live-summary-rtw.v1" || rtw.DeliveryExecution != "observed" ||
		rtw.EvidenceID == "" || rtw.Quote == "" || Digest([]byte(rtw.Quote)) != rtw.QuoteHash ||
		strings.TrimSpace(rtw.Answer) == "" || rtw.RTWAnswerRows != 1 ||
		rtw.RTWAnswerCitationRows != 1 || rtw.RTWSearchCitationRows != 1 ||
		!rtw.RTWProductHistoryPresent || rtw.CitationReceiptRef != citationRef(rtw.SearchID) {
		return out, ErrEvidence
	}
	id, err := traceID(logRaw, usage.SearchID, usage.AnswerID)
	if err != nil {
		return out, err
	}
	out = Case{SchemaVersion: CaseSchema, PolicyRevision: ReviewPolicy,
		ReviewStatus: "pending_human_review", Activation: "none", SearchID: usage.SearchID,
		AnswerID: usage.AnswerID, ModelInteractionID: usage.ModelInteractionID,
		ModelResponseSHA256:   usage.ModelResponseSHA256,
		RTWAnswerReportSHA256: Digest(rtwRaw), DCUsageReportSHA256: Digest(usageRaw),
		RTWTraceLogSHA256: Digest(logRaw), RTWTraceID: id,
		TraceScope:      "rtw_structured_log_not_otlp_collector",
		CitationPackRef: rtw.CitationReceiptRef,
		Evidence:        []Evidence{{ID: rtw.EvidenceID, Quote: rtw.Quote, QuoteSHA256: rtw.QuoteHash}},
		AcceptedAnswer:  rtw.Answer, AcceptedAnswerSHA256: Digest([]byte(rtw.Answer)),
		PriorQualityObservation: usage.QualityGate}
	identity, err := json.Marshal(out)
	if err != nil {
		return Case{}, err
	}
	out.CaseID = "grounding-" + Digest(identity)
	return out, nil
}
