// Command search-matrix-acceptance runs the local, default-off H10.b test
// split through actual BGE-M3-backed Go exact lanes and the search Graph.
// Summary, Tool delivery, RTW citations and model spend remain unobserved.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	parity "github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/three_lane_parity"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/evaluation"
	search "github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/search"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	frameworktrace "trpc.group/trpc-go/trpc-agent-go/telemetry/trace"
)

type sourceCase struct {
	QueryID                string `json:"query_id"`
	QueryFamilyID          string `json:"query_family_id"`
	NearDuplicateClusterID string `json:"near_duplicate_cluster_id"`
	QueryText              string `json:"query_text"`
	QueryTime              string `json:"query_time"`
	Judgments              []struct {
		ChunkKey string `json:"chunk_key"`
		Grade    int    `json:"grade"`
	} `json:"judgments"`
}

type sourceFile struct {
	SchemaVersion         string `json:"schema_version"`
	DatasetManifestSHA256 string `json:"dataset_manifest_sha256"`
	DataKind              string `json:"data_kind"`
	ReaderStatus          string `json:"reader_status"`
	Split                 string `json:"split"`
	SplitWindows          []struct {
		Name       string `json:"name"`
		Start      string `json:"start"`
		End        string `json:"end"`
		Rows       int    `json:"rows"`
		ShardCount int    `json:"shard_count"`
	} `json:"split_windows"`
	EvaluationCutoff      string       `json:"evaluation_cutoff"`
	JudgmentScopeComplete bool         `json:"judgment_scope_complete"`
	Cases                 []sourceCase `json:"cases"`
}

type caseRun struct {
	QueryID            string              `json:"query_id"`
	TraceID            string              `json:"trace_id"`
	NativeSpans        []string            `json:"native_spans"`
	RetrievalStatus    string              `json:"retrieval_status"`
	StopReason         string              `json:"stop_reason"`
	CandidateOrder     []string            `json:"candidate_order"`
	UsedSubqueries     int                 `json:"used_subqueries"`
	Batches            int                 `json:"batches"`
	LaneStatus         []search.LaneStatus `json:"lane_status"`
	ModelTokenCost     *int                `json:"model_token_cost"`
	CitationWriteCount *int                `json:"citation_write_count"`
}

type cellRun struct {
	Variant           evaluation.SearchVariant `json:"variant"`
	DeliveryExecution string                   `json:"delivery_execution"`
	Cases             []caseRun                `json:"cases"`
}

type report struct {
	SchemaVersion                string                        `json:"schema_version"`
	Status                       string                        `json:"status"`
	RepresentationManifestSHA256 string                        `json:"representation_manifest_sha256"`
	PythonScorerSHA256           string                        `json:"python_scorer_sha256"`
	QrelManifestSHA256           string                        `json:"qrel_manifest_sha256"`
	QrelCaseExportSHA256         string                        `json:"qrel_case_export_sha256"`
	DataKind                     string                        `json:"data_kind"`
	TestCaseCount                int                           `json:"test_case_count"`
	ExactFullTokenRows           int                           `json:"exact_full_token_rows"`
	ExactParity                  parity.ParityReport           `json:"exact_parity"`
	SearchMatrix                 evaluation.SearchMatrixReport `json:"search_matrix"`
	Cells                        []cellRun                     `json:"cells"`
	Limitations                  []string                      `json:"limitations"`
}

func readSource(path, expectedHash string) (sourceFile, error) {
	var value sourceFile
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 || hash(raw) != expectedHash {
		return value, errors.New("verified H10.b case export missing or SHA changed")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF ||
		value.SchemaVersion != "sea.search.matrix-source.v1" || value.DataKind != "synthetic" ||
		value.ReaderStatus != "validated_synthetic_fixture" || value.Split != "test" ||
		value.JudgmentScopeComplete || len(value.Cases) == 0 || len(value.SplitWindows) != 3 {
		return sourceFile{}, errors.New("H10.b case export contract differs")
	}
	for i, name := range []string{"train", "validation", "test"} {
		if value.SplitWindows[i].Name != name {
			return sourceFile{}, errors.New("H10.b split sequence differs")
		}
	}
	return value, nil
}

func hash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func parseTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }

func fixedInputs(source sourceFile, scorer parity.ScorerReport, frozen parity.Frozen) (
	evaluation.SearchMatrixInput, map[string]sourceCase, map[string]parity.ScorerQuery, error) {
	var input evaluation.SearchMatrixInput
	if source.DatasetManifestSHA256 != frozen.Manifest.DatasetManifestSHA256 ||
		scorer.QrelManifestSHA256 != source.DatasetManifestSHA256 || len(source.Cases) != len(scorer.Queries) {
		return input, nil, nil, errors.New("source, scorer and representation snapshot differ")
	}
	var err error
	input.EvaluationCutoff, err = parseTime(source.EvaluationCutoff)
	if err != nil {
		return input, nil, nil, err
	}
	input.SplitPlan = evaluation.SplitPlan{Revision: "h10b-" + source.DatasetManifestSHA256,
		EmbargoSeconds: 0}
	input.SplitPlan.TrainEnd, err = parseTime(source.SplitWindows[0].End)
	if err != nil {
		return input, nil, nil, err
	}
	input.SplitPlan.ValidationEnd, err = parseTime(source.SplitWindows[1].End)
	if err != nil {
		return input, nil, nil, err
	}
	input.SplitPlan.TestEnd, err = parseTime(source.SplitWindows[2].End)
	if err != nil {
		return input, nil, nil, err
	}
	input.BenchmarkRevision = "h10b-synthetic-12-path-v1"
	input.CorpusSnapshot = frozen.ManifestSHA256
	input.K = scorer.TopK
	input.Qrels = evaluation.QrelSet{Revision: "h10b-synthetic-v2", Source: "strict_h10b_case_export",
		SourceHash: source.DatasetManifestSHA256, SourceKind: "synthetic_fixture",
		AvailableAt: input.EvaluationCutoff, CorpusSnapshot: input.CorpusSnapshot}
	queries := make(map[string]parity.QueryRow, len(frozen.Queries))
	for _, query := range frozen.Queries {
		queries[query.QueryID] = query
	}
	scored := make(map[string]parity.ScorerQuery, len(scorer.Queries))
	for _, query := range scorer.Queries {
		scored[query.QueryID] = query
	}
	cases := make(map[string]sourceCase, len(source.Cases))
	for _, item := range source.Cases {
		encoded, ok := queries[item.QueryID]
		found, foundOK := scored[item.QueryID]
		if !ok || !foundOK || item.QueryID == "" || cases[item.QueryID].QueryID != "" ||
			encoded.Split != "test" || item.QueryText != encoded.Text ||
			item.QueryFamilyID != encoded.QueryFamilyID ||
			item.NearDuplicateClusterID != encoded.NearDuplicateClusterID ||
			item.QueryFamilyID != found.QueryFamilyID || len(item.Judgments) == 0 {
			return input, nil, nil, errors.New("case export query differs from frozen BGE/scorer")
		}
		judgments := make([]evaluation.Judgment, 0, len(item.Judgments))
		allowed := map[string]bool{}
		for _, key := range found.CandidateSet {
			allowed[key] = true
		}
		for _, judgment := range item.Judgments {
			if !allowed[judgment.ChunkKey] || judgment.Grade < 0 || judgment.Grade > 3 {
				return input, nil, nil, errors.New("case export judgment outside fixed candidate set")
			}
			judgments = append(judgments, evaluation.Judgment{ItemID: judgment.ChunkKey,
				Relevance: judgment.Grade})
		}
		// H10.b lacks an all-candidates-judged receipt, even if the small
		// synthetic TopK happens to be judged. Never promote these grades.
		input.Qrels.Cases = append(input.Qrels.Cases, evaluation.QrelCase{CaseID: item.QueryID,
			Complete: false, Judgments: judgments})
		cases[item.QueryID] = item
	}
	return input, cases, scored, nil
}

func run(ctx context.Context, representationPath, scorerPath, scorerHash,
	sourcePath, sourceHash, profilesPath, lockPath, artifactDir string) (report, error) {
	out := report{SchemaVersion: "sea.search.matrix-execution.v1", Status: "failed"}
	frozen, err := parity.LoadFrozen(representationPath, profilesPath, lockPath)
	if err != nil {
		return out, err
	}
	scorer, _, err := parity.LoadScorer(scorerPath, scorerHash, frozen)
	if err != nil {
		return out, err
	}
	source, err := readSource(sourcePath, sourceHash)
	if err != nil {
		return out, err
	}
	input, cases, scored, err := fixedInputs(source, scorer, frozen)
	if err != nil {
		return out, err
	}
	out.RepresentationManifestSHA256 = frozen.ManifestSHA256
	out.PythonScorerSHA256 = scorerHash
	out.QrelManifestSHA256 = source.DatasetManifestSHA256
	out.QrelCaseExportSHA256 = sourceHash
	out.DataKind = source.DataKind
	out.TestCaseCount = len(cases)
	allowed := map[string][]string{}
	for id, query := range scored {
		allowed[id] = query.CandidateSet
	}
	_, err = parity.RunExactWith(ctx, frozen, artifactDir, allowed, func(live parity.LiveExact) error {
		out.ExactFullTokenRows = live.FullTokenRows
		return executeMatrix(ctx, &out, &input, cases, scored, live)
	})
	if err != nil {
		return out, err
	}
	out.ExactParity, err = parity.Compare(ctx, frozen, scorer, scorerHash, artifactDir)
	if err != nil {
		return out, fmt.Errorf("locked BGE-M3 exact-lane parity: %w", err)
	}
	if out.ExactParity.Status != "passed" {
		return out, errors.New("locked BGE-M3 exact-lane parity did not pass")
	}
	out.Status = "retrieval_graph_passed_delivery_blocked"
	out.Limitations = []string{
		"H10.b revision 2 is synthetic and has no all-candidates-judged receipt; Recall/MRR/nDCG remain not_evaluable",
		"all 12 Graph retrieval invocations use the same fixed query text; medium/high have different policy budgets but no live LLM planner or query rewrite",
		"summary and tools delivery were not executed: this local fixture has no RTW same-revision source/citation receipt or DC model session",
		"lane indexes use locked BGE-M3 frozen values and local exact search; there is no new model call, online ANN, HTTP, or production traffic",
		"case export was read from the immutable local H10.b Parquet copy; this run did not reopen a live SeaweedFS S3 endpoint",
		"case SubjectRef is a declared synthetic query-local fixture identity and is not RTW/WhaleHall/DataCenter account linkage",
	}
	return out, nil
}

func executeMatrix(ctx context.Context, out *report, input *evaluation.SearchMatrixInput,
	cases map[string]sourceCase, scored map[string]parity.ScorerQuery, live parity.LiveExact) error {
	policy := search.Policy{Version: "h10b-synthetic-matrix-v1", Profiles: map[search.Depth]map[search.Intelligence]search.Limits{
		search.Fast: {
			search.Low:    {MaxBatches: 1, MaxSubqueries: 1, TopKPerLane: 1, MaxEvidence: 1, WallTime: 20 * time.Second},
			search.Medium: {MaxBatches: 1, MaxSubqueries: 2, TopKPerLane: 2, MaxEvidence: 2, WallTime: 20 * time.Second},
			search.High:   {MaxBatches: 1, MaxSubqueries: 3, TopKPerLane: 3, MaxEvidence: 3, WallTime: 20 * time.Second},
		},
		search.Detailed: {
			search.Low:    {MaxBatches: 2, MaxSubqueries: 2, TopKPerLane: 2, MaxEvidence: 2, WallTime: 20 * time.Second},
			search.Medium: {MaxBatches: 3, MaxSubqueries: 3, TopKPerLane: 3, MaxEvidence: 3, WallTime: 20 * time.Second},
			search.High:   {MaxBatches: 4, MaxSubqueries: 4, TopKPerLane: 4, MaxEvidence: 4, WallTime: 20 * time.Second},
		},
	}}
	service, err := search.New(live.Dense, live.Sparse, live.MultiVector,
		search.PlanFunc(func(_ context.Context, in search.PlanInput) ([]string, error) {
			if in.Round > 1 {
				return nil, nil // no extra validated H10.b query text exists
			}
			return []string{in.Query}, nil
		}), search.CheckFunc(func(_ context.Context, snap search.Snapshot, chunk corpus.Chunk) (bool, error) {
			for _, revision := range snap.ValidRevisionIDs {
				if revision == chunk.RevisionID {
					return true, nil
				}
			}
			return false, nil
		}), policy)
	if err != nil {
		return err
	}
	ag, err := search.NewSearchGraphAgent(service)
	if err != nil {
		return err
	}
	run := runner.NewRunner("h10b-search-matrix", ag)
	defer run.Close()
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	oldTracer, oldProvider := frameworktrace.Tracer, frameworktrace.TracerProvider
	frameworktrace.TracerProvider = provider
	frameworktrace.Tracer = provider.Tracer("trpc.agent.go")
	defer func() {
		frameworktrace.Tracer, frameworktrace.TracerProvider = oldTracer, oldProvider
		_ = provider.Shutdown(context.Background())
	}()
	keys := make([]string, 0, len(cases))
	for id := range cases {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	byRevision := map[string]string{}
	for key, revision := range live.Revisions {
		byRevision[revision] = key
	}
	for _, variant := range evaluation.SearchVariants() {
		cell := cellRun{Variant: variant, DeliveryExecution: "not_executed_missing_RTW_DC_dependencies"}
		evaluated := evaluation.SearchMatrixRun{Variant: variant,
			RunRevision: "h10b-exact-graph-" + variant.Depth + "-" + variant.Intelligence + "-" + variant.Delivery}
		for _, id := range keys {
			item, scoredCase := cases[id], scored[id]
			revisions := make([]string, 0, len(scoredCase.CandidateSet))
			for _, key := range scoredCase.CandidateSet {
				revision := live.Revisions[key]
				if revision == "" {
					return errors.New("scored candidate missing from exact index")
				}
				revisions = append(revisions, revision)
			}
			sort.Strings(revisions)
			snapshot := search.Snapshot{ModuleID: live.ModuleID, ReleaseID: live.ReleaseID,
				Generation: live.Generation, PublicationRevision: input.CorpusSnapshot,
				Indexes: map[search.Lane]corpus.Ref{search.Dense: live.DenseRef,
					search.Sparse: live.SparseRef, search.MultiVector: live.MultiRef},
				ValidRevisionIDs: revisions}
			request := search.Request{Query: item.QueryText, Depth: search.Depth(variant.Depth),
				Intelligence: search.Intelligence(variant.Intelligence), Snapshot: snapshot}
			option, err := search.SearchGraphRunOption(request)
			if err != nil {
				return err
			}
			parentCtx, parent := provider.Tracer("search-matrix-acceptance").Start(ctx, "search-matrix-case")
			stream, err := run.Run(parentCtx, "fixture-"+id, "session-"+id+"-"+variant.Delivery,
				model.NewUserMessage("execute fixed synthetic query"), option)
			if err != nil {
				parent.End()
				return err
			}
			var result search.Result
			var graphDone, runnerDone int
			for event := range stream {
				if event.IsTerminalError() {
					parent.End()
					return fmt.Errorf("native search Graph terminal error for %s: %v", id, event.Error)
				}
				if event.IsRunnerCompletion() {
					runnerDone++
				}
				candidate, complete, decodeErr := search.SearchGraphResultFromCompletion(event, request)
				if decodeErr != nil {
					parent.End()
					return decodeErr
				}
				if complete {
					graphDone++
					result = candidate
				}
			}
			traceID := parent.SpanContext().TraceID()
			parent.End()
			if graphDone != 1 || runnerDone != 1 {
				return fmt.Errorf("native Search Graph completion missing for %s", id)
			}
			seen := map[string]bool{}
			for _, span := range spans.Ended() {
				if span.SpanContext().TraceID() == traceID && span.InstrumentationScope().Name == "trpc.agent.go" {
					seen[span.Name()] = true
				}
			}
			for _, required := range []string{"invoke_agent search_execute", "workflow execute_graph search_execute",
				"workflow execute_function_node execute_search"} {
				if !seen[required] {
					return fmt.Errorf("native framework span missing: %s", required)
				}
			}
			when, err := parseTime(item.QueryTime)
			if err != nil {
				return err
			}
			identity := evaluation.CaseIdentity{CaseID: id,
				Subject:   evaluation.SubjectRef{AuthorityID: "sea.synthetic.qrel", TenantID: "fixture", SubjectID: id},
				SessionID: "session-" + id, QueryFamily: item.QueryFamilyID, QueryFamilyScope: "subject",
				Split: "test", RequestedAt: when, FeatureAvailableAt: when}
			order := make([]string, 0, len(result.Verified))
			results := make([]evaluation.SearchResult, 0, len(result.Verified))
			for _, candidate := range result.Verified {
				key := byRevision[candidate.Key.RevisionID]
				if key == "" {
					return errors.New("Graph candidate is outside frozen H10.b corpus")
				}
				order = append(order, key)
				results = append(results, evaluation.SearchResult{ItemID: key})
			}
			outcome := "partial" // retrieval happened, product delivery did not
			evaluated.Cases = append(evaluated.Cases, evaluation.SearchCase{Identity: identity,
				Outcome: outcome, Results: results})
			spanNames := make([]string, 0, len(seen))
			for name := range seen {
				spanNames = append(spanNames, name)
			}
			sort.Strings(spanNames)
			cell.Cases = append(cell.Cases, caseRun{QueryID: id, TraceID: traceID.String(),
				NativeSpans: spanNames, RetrievalStatus: result.Status, StopReason: result.StopReason,
				CandidateOrder: order, UsedSubqueries: result.UsedSubqueries,
				Batches: len(result.Batches), LaneStatus: result.LaneStatus})
		}
		input.Runs = append(input.Runs, evaluated)
		out.Cells = append(out.Cells, cell)
	}
	out.SearchMatrix, err = evaluation.EvaluateSearchMatrix(*input)
	if err != nil {
		return err
	}
	if len(out.SearchMatrix.Cells) != 12 {
		return errors.New("formal search matrix did not include all 12 paths")
	}
	for _, cell := range out.SearchMatrix.Cells {
		for _, metric := range cell.Report.Metrics {
			if metric.State != evaluation.NotEvaluable || metric.Value != nil {
				return errors.New("synthetic incomplete judgments became formal metric")
			}
		}
	}
	return nil
}

func main() {
	var representationPath, scorerPath, scorerHash, sourcePath, sourceHash string
	var profilesPath, lockPath, artifactDir, outputPath string
	flag.StringVar(&representationPath, "representation-manifest", "", "locked BGE representation manifest")
	flag.StringVar(&scorerPath, "scorer", "", "independent Python scorer report")
	flag.StringVar(&scorerHash, "scorer-sha256", "", "exact scorer bytes SHA-256")
	flag.StringVar(&sourcePath, "case-export", "", "strict H10.b reader case export")
	flag.StringVar(&sourceHash, "case-export-sha256", "", "exact case export bytes SHA-256")
	flag.StringVar(&profilesPath, "profiles", "training/serving/bge_m3/profiles.json", "pinned BGE profiles")
	flag.StringVar(&lockPath, "model-lock", "training/serving/bge_m3/model.lock.json", "pinned BGE model lock")
	flag.StringVar(&artifactDir, "artifact-dir", "", "new local exact-index object directory")
	flag.StringVar(&outputPath, "output", "", "new machine-readable report path")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if representationPath == "" || scorerPath == "" || scorerHash == "" || sourcePath == "" ||
		sourceHash == "" || artifactDir == "" || outputPath == "" {
		logger.Error("missing explicit frozen matrix inputs")
		os.Exit(2)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		logger.Error("output path must not exist", "error", err)
		os.Exit(2)
	}
	if err := os.MkdirAll(artifactDir, 0700); err != nil {
		logger.Error("create exact index artifact directory", "error", err)
		os.Exit(1)
	}
	result, err := run(context.Background(), representationPath, scorerPath, scorerHash,
		sourcePath, sourceHash, profilesPath, lockPath, artifactDir)
	if err != nil {
		logger.Error("search matrix execution failed", "error", err)
		os.Exit(1)
	}
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		logger.Error("encode matrix report", "error", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0700); err != nil {
		logger.Error("create report directory", "error", err)
		os.Exit(1)
	}
	file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		logger.Error("create immutable matrix report", "error", err)
		os.Exit(1)
	}
	if _, err := file.Write(append(body, '\n')); err != nil {
		logger.Error("write matrix report", "error", err)
		_ = file.Close()
		os.Exit(1)
	}
	if err := file.Close(); err != nil {
		logger.Error("close matrix report", "error", err)
		os.Exit(1)
	}
	logger.Info("search matrix retrieval Graph run completed", "report", outputPath,
		"cells", len(result.Cells), "cases", len(result.Cells)*result.TestCaseCount,
		"status", result.Status)
}
