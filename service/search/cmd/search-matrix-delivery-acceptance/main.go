// Command search-matrix-delivery-acceptance records one actual RTW-published
// fast/low/Tools delivery cell and marks the other 11 product cells unrun.
// It cannot turn an unjudged fixture into a relevance score.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/evaluation"
	search "github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/search"
)

type witness struct {
	SchemaVersion      string                           `json:"schema_version"`
	DeliveryExecution  string                           `json:"delivery_execution"`
	Depth              search.Depth                     `json:"depth"`
	Intelligence       search.Intelligence              `json:"intelligence"`
	Delivery           string                           `json:"delivery"`
	SubjectAuthorityID string                           `json:"subject_authority_id"`
	SubjectTenantID    string                           `json:"subject_tenant_id"`
	SubjectID          string                           `json:"subject_id"`
	SessionID          string                           `json:"session_id"`
	OperationID        string                           `json:"operation_id"`
	SearchID           string                           `json:"search_id"`
	Query              string                           `json:"query"`
	RequestedAt        time.Time                        `json:"requested_at"`
	SnapshotRef        string                           `json:"snapshot_ref"`
	Snapshot           search.Snapshot                  `json:"snapshot"`
	EvidencePack       search.EvidencePack              `json:"evidence_pack"`
	CitationReceipt    search.CitationReceipt           `json:"citation_receipt"`
	DurableReadback    ridethewind.SearchCitationRecord `json:"durable_readback"`
	SourceReadAttempts int32                            `json:"source_read_attempts"`
	CitationWriteCount int32                            `json:"citation_write_count"`
	ModelTokenCost     *int                             `json:"model_token_cost"`
	NativeSpanNames    []string                         `json:"native_span_names"`
	NativeTraceID      string                           `json:"native_trace_id"`
	RelevanceEvaluable bool                             `json:"relevance_evaluable"`
}

type cell struct {
	Variant            evaluation.SearchVariant `json:"variant"`
	DeliveryExecution  string                   `json:"delivery_execution"`
	Reason             string                   `json:"reason,omitempty"`
	SearchID           string                   `json:"search_id,omitempty"`
	CandidateOrder     []string                 `json:"candidate_order,omitempty"`
	CitationReceipt    *search.CitationReceipt  `json:"citation_receipt,omitempty"`
	SourceReadAttempts *int32                   `json:"source_read_attempts"`
	CitationWriteCount *int32                   `json:"citation_write_count"`
	ModelTokenCost     *int                     `json:"model_token_cost"`
	NativeTraceID      string                   `json:"native_trace_id,omitempty"`
}

type report struct {
	SchemaVersion       string                        `json:"schema_version"`
	Status              string                        `json:"status"`
	WitnessSHA256       string                        `json:"witness_sha256"`
	CorpusSnapshot      string                        `json:"corpus_snapshot"`
	ActualDeliveryCells int                           `json:"actual_delivery_cells"`
	NotExecutedCells    int                           `json:"not_executed_cells"`
	Cells               []cell                        `json:"cells"`
	FormalMatrix        evaluation.SearchMatrixReport `json:"formal_matrix"`
	QualityState        string                        `json:"quality_state"`
	Limitations         []string                      `json:"limitations"`
}

func sha(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func loadWitness(path string) (witness, string, error) {
	var value witness
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return value, "", errors.New("RTW Tools witness missing or too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return witness{}, "", errors.New("RTW Tools witness JSON contract differs")
	}
	if value.SchemaVersion != "sea.search.tools-delivery-witness.v1" ||
		value.DeliveryExecution != "observed" || value.Depth != search.Fast ||
		value.Intelligence != search.Low || value.Delivery != "tools" ||
		value.SubjectAuthorityID != "rtw.identity" || value.SubjectTenantID == "" ||
		value.SubjectID == "" || value.SessionID == "" || value.OperationID == "" ||
		value.Query == "" || value.SearchID == "" || value.RequestedAt.IsZero() ||
		value.SnapshotRef == "" || value.Snapshot.ModuleID == "" ||
		value.EvidencePack.SearchID != value.SearchID ||
		value.CitationReceipt.SearchID != value.SearchID ||
		value.DurableReadback.SearchId != value.SearchID ||
		value.CitationReceipt.PackHash != value.DurableReadback.PackHash ||
		value.CitationReceipt.DurableRef != value.DurableReadback.DurableRef ||
		value.SourceReadAttempts != 1 || value.CitationWriteCount != 1 ||
		value.ModelTokenCost != nil || value.RelevanceEvaluable || len(value.EvidencePack.Evidence) != 1 ||
		len(value.DurableReadback.Evidence) != 1 || value.NativeTraceID == "" {
		return witness{}, "", errors.New("RTW Tools durable witness is incomplete")
	}
	packHash, err := value.EvidencePack.Hash()
	if err != nil || packHash != value.CitationReceipt.PackHash ||
		value.EvidencePack.Evidence[0].ID != value.DurableReadback.Evidence[0].EvidenceId ||
		value.EvidencePack.Evidence[0].QuoteHash != value.DurableReadback.Evidence[0].QuoteHash {
		return witness{}, "", errors.New("RTW Tools durable citation differs from pack")
	}
	for _, required := range []string{"invoke_agent search_tools_root",
		"workflow execute_graph search_tools_root",
		"workflow execute_function_node search_and_accept_tool_evidence"} {
		found := false
		for _, name := range value.NativeSpanNames {
			found = found || name == required
		}
		if !found {
			return witness{}, "", fmt.Errorf("native Tools span missing: %s", required)
		}
	}
	return value, sha(raw), nil
}

func run(value witness, witnessSHA string) (report, error) {
	out := report{SchemaVersion: "sea.search.matrix-delivery.v1",
		Status: "one_actual_delivery_cell_remaining_not_executed", WitnessSHA256: witnessSHA,
		CorpusSnapshot: value.SnapshotRef, ActualDeliveryCells: 1, NotExecutedCells: 11,
		QualityState: "not_evaluable_no_frozen_qrels", Cells: make([]cell, 0, 12),
		Limitations: []string{
			"RTW published same-revision source/citation and native Tools Graph are observed only for fast/low/tools",
			"the RTW fixture has no frozen qrel set, candidate-pool judgment coverage or product split plan; formal relevance is not_evaluable",
			"RTW test selected one published chunk deterministically; this witness does not prove real three-lane recall or ranking",
			"summary remains unexecuted because its existing cross-repository test uses a fixed OpenAI model fixture, not a measured DataCenter model-gateway call",
			"medium/high/detailed remain unexecuted because the formal BTW cmd/api policy/planner and Tools HTTP route currently support only fast/low",
			"model token cost is null, not zero; no model was invoked by the observed Tools cell",
		}}
	requestedAt := value.RequestedAt.UTC()
	identity := evaluation.CaseIdentity{CaseID: value.SearchID,
		Subject: evaluation.SubjectRef{AuthorityID: value.SubjectAuthorityID,
			TenantID: value.SubjectTenantID, SubjectID: value.SubjectID},
		SessionID: value.SessionID, QueryFamily: "single-fixture-query-" + sha([]byte(value.Query)),
		QueryFamilyScope: "subject", Split: "test", RequestedAt: requestedAt,
		FeatureAvailableAt: requestedAt}
	input := evaluation.SearchMatrixInput{BenchmarkRevision: "rtw-single-published-fixture.v1",
		CorpusSnapshot: value.SnapshotRef, EvaluationCutoff: requestedAt.Add(time.Hour), K: 1,
		SplitPlan: evaluation.SplitPlan{Revision: "single-case-fixture-only.v1",
			TrainEnd: requestedAt.Add(-2 * time.Hour), ValidationEnd: requestedAt.Add(-time.Hour),
			TestEnd: requestedAt.Add(time.Hour), EmbargoSeconds: 0}}
	evidence := value.EvidencePack.Evidence[0]
	key, err := json.Marshal([]string{evidence.Key.SourceKind, evidence.Key.ContentID,
		evidence.Key.RevisionID, evidence.Key.ChunkID})
	if err != nil {
		return out, err
	}
	for _, variant := range evaluation.SearchVariants() {
		item := cell{Variant: variant, DeliveryExecution: "not_executed"}
		caseOutcome := "failed"
		var results []evaluation.SearchResult
		if variant == (evaluation.SearchVariant{Depth: "fast", Intelligence: "low", Delivery: "tools"}) {
			item.DeliveryExecution, item.SearchID = "observed", value.SearchID
			item.CandidateOrder = []string{string(key)}
			item.CitationReceipt = &value.CitationReceipt
			item.SourceReadAttempts = &value.SourceReadAttempts
			item.CitationWriteCount = &value.CitationWriteCount
			item.NativeTraceID = value.NativeTraceID
			caseOutcome = "completed"
			results = []evaluation.SearchResult{{ItemID: string(key), CitationVerified: true,
				CitationReceiptID: value.CitationReceipt.DurableRef}}
		} else {
			item.Reason = "profile_or_delivery_dependency_not_executed"
		}
		input.Runs = append(input.Runs, evaluation.SearchMatrixRun{Variant: variant,
			RunRevision: "rtw-fixture-" + variant.Depth + "-" + variant.Intelligence + "-" + variant.Delivery,
			Cases:       []evaluation.SearchCase{{Identity: identity, Outcome: caseOutcome, Results: results}}})
		out.Cells = append(out.Cells, item)
	}
	out.FormalMatrix, err = evaluation.EvaluateSearchMatrix(input)
	if err != nil {
		return out, err
	}
	if len(out.FormalMatrix.Cells) != 12 || out.FormalMatrix.Recommendation != "incomplete" {
		return out, errors.New("formal 12-cell evaluator lost incomplete state")
	}
	for _, item := range out.FormalMatrix.Cells {
		for _, metric := range item.Report.Metrics {
			if metric.State != evaluation.NotEvaluable || metric.Value != nil {
				return out, errors.New("unjudged product fixture produced a relevance score")
			}
		}
	}
	return out, nil
}

func main() {
	var witnessPath, outputPath string
	flag.StringVar(&witnessPath, "witness", "", "single actual RTW Tools delivery witness")
	flag.StringVar(&outputPath, "output", "", "new immutable matrix report path")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if witnessPath == "" || outputPath == "" {
		logger.Error("witness and output are required")
		os.Exit(2)
	}
	value, witnessSHA, err := loadWitness(witnessPath)
	if err != nil {
		logger.Error("load RTW delivery witness", "error", err)
		os.Exit(1)
	}
	result, err := run(value, witnessSHA)
	if err != nil {
		logger.Error("evaluate partial product matrix", "error", err)
		os.Exit(1)
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		logger.Error("encode partial matrix", "error", err)
		os.Exit(1)
	}
	file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		logger.Error("create immutable matrix report", "error", err)
		os.Exit(1)
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		_ = file.Close()
		logger.Error("write immutable matrix report", "error", err)
		os.Exit(1)
	}
	if err := file.Close(); err != nil {
		logger.Error("close immutable matrix report", "error", err)
		os.Exit(1)
	}
	logger.Info("actual RTW Tools delivery mapped into incomplete 12-cell matrix",
		"report", outputPath, "observed_cells", result.ActualDeliveryCells,
		"not_executed_cells", result.NotExecutedCells)
}
