package search

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
	"go.opentelemetry.io/otel/sdk/trace"
)

const toolsMatrixWitnessSchema = "sea.search.tools-delivery-witness.v1"

// toolMatrixWitness is deliberately test-only. The signed RTW scope and the
// durable readback, not a synthetic answer, are its two sources of identity.
type toolMatrixWitness struct {
	SchemaVersion      string                           `json:"schema_version"`
	DeliveryExecution  string                           `json:"delivery_execution"`
	Depth              searchdomain.Depth               `json:"depth"`
	Intelligence       searchdomain.Intelligence        `json:"intelligence"`
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
	Snapshot           searchdomain.Snapshot            `json:"snapshot"`
	EvidencePack       searchdomain.EvidencePack        `json:"evidence_pack"`
	CitationReceipt    searchdomain.CitationReceipt     `json:"citation_receipt"`
	DurableReadback    ridethewind.SearchCitationRecord `json:"durable_readback"`
	SourceReadAttempts int32                            `json:"source_read_attempts"`
	CitationWriteCount int32                            `json:"citation_write_count"`
	ModelTokenCost     *int                             `json:"model_token_cost"`
	NativeSpanNames    []string                         `json:"native_span_names"`
	NativeTraceID      string                           `json:"native_trace_id"`
	RelevanceEvaluable bool                             `json:"relevance_evaluable"`
}

type toolWitnessRecorder struct {
	sync.Mutex
	value toolMatrixWitness
}

func (r *toolWitnessRecorder) scope(scope TrustedToolsScope, body ToolsRequest) {
	r.Lock()
	defer r.Unlock()
	r.value.SchemaVersion = toolsMatrixWitnessSchema
	r.value.DeliveryExecution = "observed"
	r.value.Depth, r.value.Intelligence, r.value.Delivery = body.Depth, body.Intelligence, "tools"
	r.value.SubjectAuthorityID = scope.Subject.AuthorityID
	r.value.SubjectTenantID = scope.Subject.TenantID
	r.value.SubjectID = scope.Subject.SubjectID
	r.value.SessionID, r.value.OperationID, r.value.SearchID = scope.SessionID, scope.OperationID, scope.SearchID
	r.value.Query, r.value.RequestedAt = body.Query, time.Now().UTC()
	r.value.SnapshotRef, r.value.Snapshot = scope.SnapshotRef, scope.Snapshot
}

func (r *toolWitnessRecorder) accepted(pack searchdomain.EvidencePack,
	receipt searchdomain.CitationReceipt, durable ridethewind.SearchCitationRecord) {
	r.Lock()
	defer r.Unlock()
	r.value.EvidencePack, r.value.CitationReceipt, r.value.DurableReadback = pack, receipt, durable
}

func (r *toolWitnessRecorder) write(directory string, sourceReads, citationWrites int32,
	spans []trace.ReadOnlySpan) (string, error) {
	r.Lock()
	value := r.value
	r.Unlock()
	if directory == "" {
		return "", nil
	}
	if value.SchemaVersion != toolsMatrixWitnessSchema || value.DeliveryExecution != "observed" ||
		value.Depth != searchdomain.Fast || value.Intelligence != searchdomain.Low ||
		value.SearchID == "" || value.CitationReceipt.SearchID != value.SearchID ||
		value.EvidencePack.SearchID != value.SearchID || value.DurableReadback.SearchId != value.SearchID ||
		value.DurableReadback.PackHash != value.CitationReceipt.PackHash ||
		value.DurableReadback.DurableRef != value.CitationReceipt.DurableRef ||
		len(value.EvidencePack.Evidence) != 1 || len(value.DurableReadback.Evidence) != 1 ||
		value.EvidencePack.Evidence[0].ID != value.DurableReadback.Evidence[0].EvidenceId ||
		value.EvidencePack.Evidence[0].QuoteHash != value.DurableReadback.Evidence[0].QuoteHash ||
		sourceReads != 1 || citationWrites != 1 {
		return "", errors.New("RTW Tools durable delivery witness incomplete")
	}
	packHash, err := value.EvidencePack.Hash()
	if err != nil || packHash != value.CitationReceipt.PackHash {
		return "", errors.New("RTW Tools pack hash differs from durable receipt")
	}
	value.SourceReadAttempts, value.CitationWriteCount = sourceReads, citationWrites
	for _, span := range spans {
		if span.InstrumentationScope().Name != "trpc.agent.go" {
			continue
		}
		value.NativeSpanNames = append(value.NativeSpanNames, span.Name())
		if span.Name() == "invoke_agent search_tools_root" {
			value.NativeTraceID = span.SpanContext().TraceID().String()
		}
	}
	sort.Strings(value.NativeSpanNames)
	for _, required := range []string{"invoke_agent search_tools_root", "workflow execute_graph search_tools_root",
		"workflow execute_function_node search_and_accept_tool_evidence"} {
		found := false
		for _, name := range value.NativeSpanNames {
			found = found || name == required
		}
		if !found {
			return "", errors.New("native Tools Agent/Graph span missing")
		}
	}
	if value.NativeTraceID == "" {
		return "", errors.New("native Tools trace ID missing")
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("Tools witness output must be a private existing directory")
	}
	sum := sha256.Sum256([]byte(value.SearchID))
	path := filepath.Join(directory, "tools-"+hex.EncodeToString(sum[:])+".json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	if _, err := file.Write(append(body, '\n')); err != nil {
		_ = file.Close()
		return "", err
	}
	return path, file.Close()
}
