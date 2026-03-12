package zlog

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// 运行：go ChatTest -run TestRecoAgent_GenRecE2ETrace -v
// 目的：用“生成式推荐 agent”的业务方式，完整模拟一条链路：
// invoke -> intent -> policy(route, why) -> retrieval(vector db) -> tool(call, result) -> rank -> chat(gen) -> validate -> side_effect
func TestRecoAgent_GenRecE2ETrace(t *testing.T) {
	// -------- 1) 用 buffer 接住 JSON 日志，最后 t.Log 打出来（方便你看）--------
	var buf bytes.Buffer
	encCfg := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "linenum",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.FullCallerEncoder,
		EncodeName:     zapcore.FullNameEncoder,
	}
	core := zapcore.NewCore(zapcore.NewJSONEncoder(encCfg), zapcore.AddSync(&buf), zapcore.InfoLevel)
	zlog = zap.New(core, zap.AddCaller(), zap.Development(), zap.Fields(zap.String("serviceName", "RecommandService")))
	t.Cleanup(func() { zlog = nil })

	// -------- 2) 一些“业务模拟”的 mock：向量库检索 / 工具调用 / LLM 生成 / 校验 --------

	type Article struct {
		ID          string
		Title       string
		PublishedAt time.Time
	}

	// 向量库里“最近新番资讯文章”（模拟）
	vectorDB := []Article{
		{ID: "doc_001", Title: "2026冬季新番榜单：口碑Top10", PublishedAt: time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)},
		{ID: "doc_002", Title: "2026年2月新番更新追更指南", PublishedAt: time.Date(2026, 2, 22, 0, 0, 0, 0, time.UTC)},
		{ID: "doc_003", Title: "近期热门：番剧讨论热度趋势", PublishedAt: time.Date(2026, 2, 21, 0, 0, 0, 0, time.UTC)},
		{ID: "doc_004", Title: "新番推荐：悬疑/恋爱/热血分区精选", PublishedAt: time.Date(2026, 2, 18, 0, 0, 0, 0, time.UTC)},
	}

	// 向量检索：返回 doc hashes(top5) + coverage_score（摘要）
	vectorSearch := func(query string, topK int) (docIDs []string, docHashesTop5 []string, coverage float64) {
		_ = query
		if topK <= 0 {
			topK = 10
		}
		// 简化：直接返回前 3 篇当“相关结果”
		docIDs = []string{vectorDB[1].ID, vectorDB[0].ID, vectorDB[2].ID}
		for i := 0; i < len(docIDs) && i < 5; i++ {
			h := HashID(docIDs[i]) // "h_ab12cd"
			docHashesTop5 = append(docHashesTop5, "d_"+strings.TrimPrefix(h, "h_"))
		}
		coverage = 0.67
		return
	}

	// 工具：查询“最新更新/放送表/热度”（模拟外部工具）
	type AnimeToolResult struct {
		ItemsReturned int
		IsStale       bool
		ErrorClass    string
	}

	animeUpdateTool := func(region string, since time.Time) (AnimeToolResult, error) {
		_ = region
		_ = since
		// 模拟成功返回
		return AnimeToolResult{
			ItemsReturned: 6,
			IsStale:       false,
			ErrorClass:    "NONE",
		}, nil
	}

	// LLM 生成（只返回 tokens 和 response_ref，正文不落日志）
	llmGenerate := func(promptRef string, ctxRef string) (tokensIn, tokensOut int, responseRef string) {
		_ = promptRef
		_ = ctxRef
		return 1650, 410, "blob://traces/demo/gen/resp/g_001"
	}

	// 校验：必须引用 + grounded 一致性（模拟）
	validateOutput := func(mustCite bool, returnedDocCount int) (q Quality) {
		q.SchemaValid = true
		if mustCite {
			// 有检索才算 citation_valid=true（这里简化）
			q.CitationValid = returnedDocCount > 0
		} else {
			q.CitationValid = true
		}
		if mustCite && returnedDocCount == 0 {
			q.ClaimGroundingCheck = "FAIL_CLAIMED_BUT_EMPTY"
			q.Violations = []string{"CITATION_MISSING", "GROUNDED_CLAIM_INCONSISTENT"}
			return
		}
		q.ClaimGroundingCheck = "PASS"
		q.Violations = []string{}
		return
	}

	// -------- 3) 真实“业务链路”模拟：生成式推荐 agent --------

	// 入参（模拟用户请求）
	userQuery := "最近有什么好看的新番？"
	surface := "home_feed"
	agentName := "reco_agent"
	recRequestID := "rec_9a1c2f8d"
	userID := "user_123"
	sessionID := "s_77c1aa"
	exp := []ExpID{{Name: "genrec_router_v3", Variant: "B"}}

	// 入口：创建 trace ctx
	ctx := NewTrace(context.Background(), recRequestID, surface, agentName, userID, sessionID, exp)

	// (root) invoke_agent
	LogRootInvoke(ctx, StatusOK, nil, surface)

	// ChatTest 内部：为了让 span_id 更像示例（0001/0002/…），给一个小工具固定 span_id
	fixSpan := func(newCtx context.Context, sp *Span, fixed string, latencyMS int64) context.Context {
		sp.spanID = fixed
		newCtx = UpdateSpan(newCtx, fixed)
		sp.ctx = newCtx
		sp.start = time.Now().Add(-time.Duration(latencyMS) * time.Millisecond)
		return newCtx
	}

	// 1) intent.inferred：意图推断
	ctx1, sp1 := StartSpan(ctx, "intent.inferred")
	ctx1 = fixSpan(ctx1, sp1, "0001", 12)
	intent := Intent{
		Label:            "explore_new_items",
		Confidence:       0.82,
		ConfidenceBucket: ConfidenceBucket(0.82),
		Signals:          []string{"SIG_RECENT_VIEW_CATEGORY:anime", "SIG_FRESHNESS_SENSITIVE:high"},
	}
	sp1.End(StatusOK, nil,
		zap.Any("intent", intent),
		zap.Any("model", ModelInfo{ID: "intent_model_v5", Version: "2026-02-10"}),
		zap.String("user_query", userQuery), // 业务里你可以不落（或改成 hash/ref）；这里为了演示
	)

	// 2) policy.routed：路由/策略（包含“为什么要检索/为什么要调用工具”的决策）
	ctx2, sp2 := StartSpan(ctx1, "policy.routed")
	ctx2 = fixSpan(ctx2, sp2, "0002", 3)

	// 决策：为什么要用 RAG + TOOL
	// - RAG：需要 grounding（从向量库拿“新番相关文章”）
	// - TOOL：需要 freshness（外部工具拿“最新更新/热度/放送表”，避免“把现在当两年前”）
	mustCite := true
	maxToolCalls := 2
	policyDecision := Decision{
		Type:        "policy.route",
		Chosen:      "RAG+TOOL",
		Confidence:  0.74,
		ReasonCodes: []string{"NEED_GROUNDING", "NEED_FRESHNESS"},
		Constraints: map[string]any{
			"must_cite_sources": mustCite,
			"max_tool_calls":    maxToolCalls,
			"max_latency_ms":    1200,
		},
		Signals: map[string]any{
			"intent_label":        intent.Label,
			"time_skew_detected":  false,
			"freshness_sensitive": "high",
		},
		Alternatives: []map[string]any{
			{"action": "GEN_ONLY", "score": 0.38, "reject_code": "HALLUCINATION_RISK"},
			{"action": "RAG_ONLY", "score": 0.62, "reject_code": "FRESHNESS_RISK"},
		},
		ArtifactsRef: map[string]string{
			"prompt_ref": "blob://traces/demo/prompt/p_001",
		},
	}
	sp2.End(StatusOK, nil, zap.Any("decision", policyDecision))

	// 3) retrieval.user_profile：用户画像/偏好检索（模拟）
	ctx3, sp3 := StartSpan(ctx2, "retrieval.user_profile")
	ctx3 = fixSpan(ctx3, sp3, "0003", 22)
	sp3.End(StatusOK, nil,
		zap.Any("retrieval", map[string]any{
			"returned_doc_count": 5,
			"empty":              false,
		}),
	)

	// 4) retrieval.completed：向量库检索相关文章（核心：returned_doc_count/coverage/top hashes）
	ctx4, sp4 := StartSpan(ctx2, "retrieval.completed")
	ctx4 = fixSpan(ctx4, sp4, "0004", 48)

	docIDs, docHashesTop5, coverage := vectorSearch(userQuery, 20)
	ret := Retrieval{
		Source:           "knowledge_base",
		QueryCount:       1,
		RequestedTopK:    20,
		ReturnedDocCount: len(docIDs),
		Empty:            len(docIDs) == 0,
		CoverageScore:    coverage,
		DocIDHashesTop5:  docHashesTop5,
	}
	retDecision := Decision{
		Type:        "retrieve",
		Chosen:      "vector",
		ReasonCodes: []string{"NEED_GROUNDING"},
		Signals:     map[string]any{"empty_retrieval": ret.Empty},
		ArtifactsRef: map[string]string{
			"retrieval_ref": "blob://traces/demo/retrieval/r_001",
		},
	}
	sp4.End(StatusOK, nil, zap.Any("retrieval", ret), zap.Any("decision", retDecision))

	// 5) tool.called + tool.result：模拟“调用工具拿最新更新/热度”（用 goroutine 模拟异步/队列）
	// 关键点：传播 BaseContext（context propagation），保证 trace_id/parent_span 拼得起来
	baseForTool, _ := BaseFrom(ctx2)
	done := make(chan struct{}, 1)

	go func() {
		ctxTool := WithBase(context.Background(), baseForTool)

		// tool.called
		ctx5, sp5 := StartSpan(ctxTool, "tool.called")
		ctx5 = fixSpan(ctx5, sp5, "0005", 1)

		toolSelect := Decision{
			Type:        "tool_select",
			Chosen:      "anime_update_lookup",
			ReasonCodes: []string{"NEED_FRESHNESS", "AVOID_TIME_SKEW"},
		}
		sp5.End(StatusOK, nil,
			zap.Any("tool", ToolCall{
				Name:            "anime_update_lookup",
				IOSchemaVersion: "2026-02-01",
				ArgsSummary:     map[string]any{"region": "SG", "since_hours": 48},
				ArgsRef:         "blob://traces/demo/tool/io/t_001#args",
			}),
			zap.Any("decision", toolSelect),
		)

		// tool.result
		_, sp6 := StartSpan(ctx5, "tool.result")
		ctx6 := fixSpan(ctx5, sp6, "0006", 160)

		toolRes, toolErr := animeUpdateTool("SG", time.Now().Add(-48*time.Hour))
		if toolErr != nil {
			sp6.End(StatusError, toolErr,
				zap.Any("tool", ToolCall{
					Name:       "anime_update_lookup",
					Outcome:    "ERROR",
					ErrorClass: ErrorClass(toolErr),
				}),
			)
			done <- struct{}{}
			return
		}

		_ = ctx6
		sp6.End(StatusOK, nil,
			zap.Any("tool", ToolCall{
				Name: "anime_update_lookup",
				ResultSummary: map[string]any{
					"items_returned": toolRes.ItemsReturned,
					"is_stale":       toolRes.IsStale,
					"error_class":    toolRes.ErrorClass,
				},
				ResultRef: "blob://traces/demo/tool/io/t_001#result",
				Outcome:   "OK",
			}),
		)

		done <- struct{}{}
	}()

	<-done

	// 6) rank.completed：排序/重排（生成式推荐的候选->最终列表）
	ctx7, sp7 := StartSpan(ctx2, "rank.completed")
	ctx7 = fixSpan(ctx7, sp7, "0007", 35)
	sp7.End(StatusOK, nil,
		zap.Any("rank", map[string]any{
			"candidate_in":  120,
			"candidate_out": 20,
			"rerank_model":  map[string]any{"id": "rerank_v3", "version": "2026-02-15"},
			"scores_summary": map[string]any{
				"min":  0.12,
				"max":  0.91,
				"mean": 0.57,
			},
		}),
	)

	// 7) chat.generated：LLM 生成推荐解释/理由（response 不落日志，只落 ref）
	ctx8, sp8 := StartSpan(ctx7, "chat.generated")
	ctx8 = fixSpan(ctx8, sp8, "0008", 120)
	tIn, tOut, respRef := llmGenerate("blob://traces/demo/prompt/p_001", "blob://traces/demo/retrieval/r_001")
	sp8.End(StatusOK, nil,
		zap.Any("model", ModelInfo{ID: "gpt-x", Version: "2026-02-20"}),
		zap.Any("gen", Gen{
			TokensIn:    tIn,
			TokensOut:   tOut,
			StopReason:  "stop",
			ResponseRef: respRef,
		}),
	)

	// 8) validate.output：质量校验（把“答得好不好”写回 trace）
	ctx9, sp9 := StartSpan(ctx2, "validate.output")
	ctx9 = fixSpan(ctx9, sp9, "0009", 12)

	q := validateOutput(mustCite, ret.ReturnedDocCount)
	sp9.End(StatusOK, nil, zap.Any("quality", q))

	// 9) side_effect.exposure_log：有副作用才打（曝光/埋点/写入等）
	ctx10, sp10 := StartSpan(ctx9, "side_effect.exposure_log")
	_ = ctx10
	ctx9 = fixSpan(ctx9, sp10, "0010", 17)
	sp10.End(StatusOK, nil,
		zap.Any("side_effect", map[string]any{
			"type":    "exposure_log",
			"outcome": "OK",
		}),
	)

	// -------- 4) 把整条链路的日志按行打印出来（go ChatTest -v 可见）--------
	out := strings.TrimSpace(buf.String())
	t.Log("============================================================")
	t.Log("SIMULATED AGENT TRACE LOGS (JSON lines)")
	t.Log("============================================================")
	for _, line := range strings.Split(out, "\n") {
		t.Log(line)
	}
}

func TestSpanEnd_LogsErrorLevelForStatusError(t *testing.T) {
	var buf bytes.Buffer
	zlog = newTestLogger(&buf)
	t.Cleanup(func() { zlog = nil })

	ctx := NewTrace(context.Background(), "rec_err", "content_search", "content_search_agent", "", "", nil)
	_, sp := StartSpan(ctx, "rerank.milvus_cohere")
	sp.start = time.Now().Add(-50 * time.Millisecond)
	sp.End(StatusError, context.DeadlineExceeded)

	entry := lastJSONLogLine(t, &buf)
	if got := entry["level"]; got != "error" {
		t.Fatalf("expected error level, got %v", got)
	}
	if got := entry["status"]; got != "500" {
		t.Fatalf("expected status 500, got %v", got)
	}
	if got := entry["event_type"]; got != "rerank.milvus_cohere" {
		t.Fatalf("expected rerank event_type, got %v", got)
	}
}

func TestLogRootInvoke_LogsInfoLevelForStatusOK(t *testing.T) {
	var buf bytes.Buffer
	zlog = newTestLogger(&buf)
	t.Cleanup(func() { zlog = nil })

	ctx := NewTrace(context.Background(), "rec_ok", "content_search", "content_search_agent", "", "", nil)
	LogRootInvoke(ctx, StatusOK, nil, "content_search")

	entry := lastJSONLogLine(t, &buf)
	if got := entry["level"]; got != "info" {
		t.Fatalf("expected info level, got %v", got)
	}
	if got := entry["status"]; got != "200" {
		t.Fatalf("expected status 200, got %v", got)
	}
	if got := entry["event_type"]; got != "invoke_agent" {
		t.Fatalf("expected invoke_agent event_type, got %v", got)
	}
}

func newTestLogger(buf *bytes.Buffer) *zap.Logger {
	encCfg := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "linenum",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.FullCallerEncoder,
		EncodeName:     zapcore.FullNameEncoder,
	}
	core := zapcore.NewCore(zapcore.NewJSONEncoder(encCfg), zapcore.AddSync(buf), zapcore.InfoLevel)
	return zap.New(core, zap.AddCaller(), zap.Development(), zap.Fields(zap.String("serviceName", "RecommandService")))
}

func lastJSONLogLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	out := strings.TrimSpace(buf.String())
	if out == "" {
		t.Fatal("expected log output, got empty buffer")
	}
	lines := strings.Split(out, "\n")
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil {
		t.Fatalf("failed to parse log line: %v", err)
	}
	return entry
}
