// Command search-summary-usage-acceptance joins one RTW accepted answer to the
// exact captured DataCenter model request using its fixed SearchID. It reads
// only a disposable local PostgreSQL test database and never emits credentials.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type runtime struct {
	SchemaVersion        string `json:"schema_version"`
	Endpoint             string `json:"endpoint"`
	AccessToken          string `json:"access_token"`
	LogicalModel         string `json:"logical_model"`
	PhysicalModel        string `json:"physical_model"`
	ModelConfigurationID string `json:"model_configuration_id"`
	UserID               string `json:"user_id"`
	PostgresDSN          string `json:"postgres_dsn"`
	ReadyAt              string `json:"ready_at"`
}

type accepted struct {
	SchemaVersion            string `json:"schema_version"`
	DeliveryExecution        string `json:"delivery_execution"`
	SearchID                 string `json:"search_id"`
	AnswerID                 string `json:"answer_id"`
	Answer                   string `json:"answer"`
	EvidenceID               string `json:"evidence_id"`
	Quote                    string `json:"quote"`
	QuoteHash                string `json:"quote_hash"`
	CitationReceiptRef       string `json:"citation_receipt_ref"`
	EndToEndLatencyMS        int64  `json:"end_to_end_latency_ms"`
	RTWAnswerRows            int    `json:"rtw_answer_rows"`
	RTWAnswerCitationRows    int    `json:"rtw_answer_citation_rows"`
	RTWSearchCitationRows    int    `json:"rtw_search_citation_rows"`
	RTWProductHistoryPresent bool   `json:"rtw_product_history_present"`
	RealUserCenter           bool   `json:"real_user_center"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type report struct {
	SchemaVersion                string `json:"schema_version"`
	Status                       string `json:"status"`
	SearchID                     string `json:"search_id"`
	AnswerID                     string `json:"answer_id"`
	RTWAnswerSHA256              string `json:"rtw_answer_sha256"`
	EvidenceQuote                string `json:"evidence_quote"`
	EvidenceQuoteSHA256          string `json:"evidence_quote_sha256"`
	RTWAcceptedAnswer            string `json:"rtw_accepted_answer"`
	CitationReceiptRef           string `json:"citation_receipt_ref"`
	ModelConfigurationID         string `json:"model_configuration_id"`
	PhysicalModel                string `json:"physical_model"`
	ModelProvider                string `json:"model_provider"`
	ModelInteractionID           string `json:"model_interaction_id"`
	ModelResponseSHA256          string `json:"model_response_sha256"`
	ModelLatencyMS               int64  `json:"model_latency_ms"`
	EndToEndLatencyMS            int64  `json:"end_to_end_latency_ms"`
	ProviderUsage                *usage `json:"provider_usage"`
	RTWDurableAnswerRows         int    `json:"rtw_durable_answer_rows"`
	RTWDurableAnswerCitationRows int    `json:"rtw_durable_answer_citation_rows"`
	RTWDurableSearchCitationRows int    `json:"rtw_durable_search_citation_rows"`
	RTWProductHistoryPresent     bool   `json:"rtw_product_history_present"`
	RealUserCenter               bool   `json:"real_user_center"`
	QualityGate                  string `json:"quality_gate"`
	RelevanceState               string `json:"relevance_state"`
	Activation                   string `json:"activation"`
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func readJSON(path string, out any) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return nil, errors.New("acceptance input must be a private bounded regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("acceptance input JSON differs")
	}
	return raw, nil
}

func run(ctx context.Context, runtimePath, answerPath string) (report, error) {
	var out report
	var dc runtime
	if _, err := readJSON(runtimePath, &dc); err != nil {
		return out, err
	}
	var rtw accepted
	raw, err := readJSON(answerPath, &rtw)
	if err != nil {
		return out, err
	}
	readyAt, err := time.Parse(time.RFC3339Nano, dc.ReadyAt)
	if err != nil || dc.SchemaVersion != "sea.dc.local-chat-consumer.v1" || dc.LogicalModel != "agent" ||
		dc.PhysicalModel == "" || dc.ModelConfigurationID == "" || dc.UserID == "" || dc.PostgresDSN == "" ||
		rtw.SchemaVersion != "sea.search.live-summary-rtw.v1" || rtw.DeliveryExecution != "observed" ||
		rtw.SearchID == "" || rtw.AnswerID == "" || strings.TrimSpace(rtw.Answer) == "" ||
		rtw.EvidenceID == "" || rtw.Quote == "" || digest([]byte(rtw.Quote)) != rtw.QuoteHash ||
		rtw.CitationReceiptRef == "" || rtw.EndToEndLatencyMS < 1 ||
		rtw.RTWAnswerRows != 1 || rtw.RTWAnswerCitationRows != 1 || rtw.RTWSearchCitationRows != 1 ||
		!rtw.RTWProductHistoryPresent || !rtw.RealUserCenter {
		return out, errors.New("live DataCenter/RTW summary receipt scope differs")
	}
	pool, err := pgxpool.New(ctx, dc.PostgresDSN)
	if err != nil {
		return out, err
	}
	defer pool.Close()
	rows, err := pool.Query(ctx, `SELECT i.id::text,i.model_configuration_id::text,i.model,i.provider,i.outcome,
 i.response_status,i.latency_ms,c.body FROM telemetry.model_interaction i
 JOIN telemetry.model_interaction_response_chunk c ON c.interaction_id=i.id AND c.sequence=0
 WHERE i.user_id=$1 AND i.requested_at >= $2 AND position($3 in convert_from(i.request_body,'UTF8'))>0
 ORDER BY i.requested_at`, dc.UserID, readyAt, rtw.SearchID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	var matches int
	for rows.Next() {
		matches++
		var id, configID, model, provider, outcome string
		var status *int
		var latency *int64
		var response []byte
		if err := rows.Scan(&id, &configID, &model, &provider, &outcome, &status, &latency, &response); err != nil {
			return out, err
		}
		var envelope struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage *usage `json:"usage"`
		}
		if json.Unmarshal(response, &envelope) != nil || len(envelope.Choices) != 1 ||
			configID != dc.ModelConfigurationID || model != dc.PhysicalModel || outcome != "completed" ||
			status == nil || *status != 200 || latency == nil || *latency < 1 {
			return out, errors.New("DataCenter captured model interaction did not complete under the active configuration")
		}
		var completion struct {
			Answer    string   `json:"answer"`
			Citations []string `json:"citations"`
		}
		decoder := json.NewDecoder(strings.NewReader(envelope.Choices[0].Message.Content))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&completion) != nil || decoder.Decode(new(any)) != io.EOF ||
			completion.Answer != rtw.Answer || len(completion.Citations) != 1 ||
			completion.Citations[0] != rtw.EvidenceID {
			return out, errors.New("DataCenter model output differs from RTW accepted answer or citation")
		}
		if envelope.Usage != nil && (envelope.Usage.PromptTokens < 1 || envelope.Usage.CompletionTokens < 1 ||
			envelope.Usage.TotalTokens != envelope.Usage.PromptTokens+envelope.Usage.CompletionTokens) {
			return out, errors.New("provider usage totals differ")
		}
		out = report{SchemaVersion: "sea.search.live-summary-usage.v1", Status: "functional_chain_passed",
			SearchID: rtw.SearchID, AnswerID: rtw.AnswerID, RTWAnswerSHA256: digest(raw),
			EvidenceQuote: rtw.Quote, EvidenceQuoteSHA256: rtw.QuoteHash,
			RTWAcceptedAnswer: rtw.Answer, CitationReceiptRef: rtw.CitationReceiptRef,
			ModelConfigurationID: configID, PhysicalModel: model, ModelProvider: provider,
			ModelInteractionID: id, ModelResponseSHA256: digest(response), ModelLatencyMS: *latency,
			EndToEndLatencyMS: rtw.EndToEndLatencyMS, ProviderUsage: envelope.Usage,
			RTWDurableAnswerRows: 1, RTWDurableAnswerCitationRows: 1, RTWDurableSearchCitationRows: 1,
			RTWProductHistoryPresent: true, RealUserCenter: true,
			QualityGate: "not_passed_unsupported_interpretation", RelevanceState: "not_evaluable_no_qrels",
			Activation: "none"}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if matches != 1 {
		return out, errors.New("expected exactly one captured DataCenter model call for RTW SearchID")
	}
	return out, nil
}

func main() {
	var runtimePath, answerPath, outputPath string
	flag.StringVar(&runtimePath, "dc-runtime", "", "private local DataCenter gateway runtime")
	flag.StringVar(&answerPath, "rtw-answer", "", "private RTW durable answer test receipt")
	flag.StringVar(&outputPath, "output", "", "new immutable combined evidence path")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if runtimePath == "" || answerPath == "" || outputPath == "" {
		logger.Error("DC runtime, RTW answer and new output are required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := run(ctx, runtimePath, answerPath)
	if err != nil {
		logger.Error("real summary model/answer binding failed", "error", err)
		os.Exit(1)
	}
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		logger.Error("encode real summary evidence", "error", err)
		os.Exit(1)
	}
	file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		logger.Error("create immutable summary evidence", "error", err)
		os.Exit(1)
	}
	if _, err := file.Write(append(body, '\n')); err != nil {
		_ = file.Close()
		logger.Error("write immutable summary evidence", "error", err)
		os.Exit(1)
	}
	if err := file.Close(); err != nil {
		logger.Error("close immutable summary evidence", "error", err)
		os.Exit(1)
	}
	logger.Info("DataCenter model output bound to RTW durable summary", "search_id", result.SearchID,
		"model_configuration_id", result.ModelConfigurationID, "report", outputPath,
		"quality_gate", result.QualityGate)
}
