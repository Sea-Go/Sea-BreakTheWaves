package rerank

import (
	"context"
	"encoding/json"
	"errors"
	"sea/embedding/service"
	"sea/metrics"
	"sort"
	"strconv"
	"strings"
	"time"

	"sea/config"
	"sea/infra"
	"sea/storage"
	"sea/zlog"

	"github.com/openai/openai-go/v3"
	"go.uber.org/zap"
)

// ToolAIRerank：用大模型对候选文章做精排序（融合长期/短期/周期记忆）。
type ToolAIRerank struct {
	articleRepo     *storage.ArticleRepo
	memoryRepo      *storage.MemoryRepo
	memoryChunkRepo *storage.MemoryChunkRepo
	client          *openai.Client
}

func New(articleRepo *storage.ArticleRepo, memoryRepo *storage.MemoryRepo, memoryChunkRepo *storage.MemoryChunkRepo) *ToolAIRerank {
	// client 在项目中已实现（infra.NewAIClient）。这里在 skill 初始化阶段构造一次并复用，
	// 避免每次 Invoke 都重新初始化导致额外开销与冗余 span。
	return &ToolAIRerank{
		articleRepo:     articleRepo,
		memoryRepo:      memoryRepo,
		memoryChunkRepo: memoryChunkRepo,
		client:          infra.NewAIClient(),
	}
}

func (t *ToolAIRerank) Name() string { return "ai_rerank_articles" }
func (t *ToolAIRerank) Description() string {
	return "使用大模型对候选文章进行精排序：输入候选 article_id 列表，结合用户长期/短期/周期记忆输出最终排序与置信度。"
}
func (t *ToolAIRerank) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"user_id":     map[string]any{"type": "string"},
			"user_intent": map[string]any{"type": "string", "description": "本次推荐的意图描述（来自 intent.parse / policy.route 等）"},
			"candidate_article_ids": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"topk": map[string]any{"type": "integer", "default": 20},
		},
		"required": []string{"user_id", "candidate_article_ids"},
	}
}

type rerankArgs struct {
	UserID              string   `json:"user_id"`
	UserIntent          string   `json:"user_intent"`
	CandidateArticleIDs []string `json:"candidate_article_ids"`
	TopK                int      `json:"topk"`
}

type RankedItem struct {
	ArticleID string  `json:"article_id"`
	Score     float64 `json:"score"`
	Reason    string  `json:"reason"`
}

type RerankResult struct {
	OverallConfidence float64      `json:"overall_confidence"`
	Ranked            []RankedItem `json:"ranked"`
}

func (t *ToolAIRerank) Invoke(ctx context.Context, argsRaw json.RawMessage) (res any, err error) {
	// ✅ 总 span：覆盖整个 rerank；trace/log 里直接看到总耗时
	ctxTotal, totalSp := zlog.StartSpan(ctx, "skills.rerank.completed")
	ctx = ctxTotal

	// breakdown（写入 total span，便于一眼看出每步耗时）
	var (
		msArgsUnmarshal  int64
		msArgsValidate   int64
		msFetchArticles  int64
		msGetMemLong     int64
		msGetMemShort    int64
		msGetMemPeriodic int64
		msBuildCandLines int64
		msBuildMemory    int64
		msComposePrompt  int64
		msInitClient     int64
		msBuildRequest   int64
		msCallLLM        int64
		msExtractOutput  int64
		msParseJSON      int64
		msResultValidate int64

		llmTokensIn   int
		llmTokensOut  int
		llmStopReason string
		llmModel      string

		promptBytes      int
		candidateLineCnt int
		metaCnt          int

		memLongFound     bool
		memShortFound    bool
		memPeriodicFound bool
		memDegraded      bool
	)

	// 0) 参数解析
	var args rerankArgs
	{
		stepStart := time.Now()
		ctx2, sp := zlog.StartSpan(ctx, "skills.rerank.args.unmarshal")
		ctx = ctx2

		if e := json.Unmarshal(argsRaw, &args); e != nil {
			msArgsUnmarshal = time.Since(stepStart).Milliseconds()
			sp.End(zlog.StatusError, e, zap.Int64("step_ms", msArgsUnmarshal))
			err = e
			totalSp.End(zlog.StatusError, err)
			return nil, err
		}
		msArgsUnmarshal = time.Since(stepStart).Milliseconds()
		sp.End(zlog.StatusOK, nil,
			zap.Int64("step_ms", msArgsUnmarshal),
			zap.Int("candidate_in", len(args.CandidateArticleIDs)),
			zap.Int("topk", args.TopK),
		)
	}

	// 0.1) 参数校验/默认值
	{
		stepStart := time.Now()
		ctx2, sp := zlog.StartSpan(ctx, "skills.rerank.args.validate")
		ctx = ctx2

		if t.articleRepo == nil || t.memoryRepo == nil {
			err = errors.New("ArticleRepo/MemoryRepo 未注入")
			msArgsValidate = time.Since(stepStart).Milliseconds()
			sp.End(zlog.StatusError, err, zap.Int64("step_ms", msArgsValidate))
			totalSp.End(zlog.StatusError, err)
			return nil, err
		}
		if args.TopK <= 0 {
			args.TopK = 20
		}
		if len(args.CandidateArticleIDs) == 0 {
			err = errors.New("candidate_article_ids 不能为空")
			msArgsValidate = time.Since(stepStart).Milliseconds()
			sp.End(zlog.StatusError, err, zap.Int64("step_ms", msArgsValidate))
			totalSp.End(zlog.StatusError, err)
			return nil, err
		}

		msArgsValidate = time.Since(stepStart).Milliseconds()
		sp.End(zlog.StatusOK, nil,
			zap.Int64("step_ms", msArgsValidate),
			zap.Int("candidate_in", len(args.CandidateArticleIDs)),
			zap.Int("topk", args.TopK),
		)
	}

	// 1) 取文章元信息
	var metas []storage.ArticleMeta
	{
		stepStart := time.Now()
		ctx2, sp := zlog.StartSpan(ctx, "skills.rerank.articles.fetch")
		ctx = ctx2

		metas, err = t.articleRepo.GetArticlesByIDs(ctx, args.CandidateArticleIDs)
		msFetchArticles = time.Since(stepStart).Milliseconds()
		if err != nil {
			sp.End(zlog.StatusError, err,
				zap.Int64("step_ms", msFetchArticles),
				zap.Int("candidate_in", len(args.CandidateArticleIDs)),
			)
			totalSp.End(zlog.StatusError, err,
				zap.Any("breakdown_ms", map[string]any{"articles_fetch": msFetchArticles}),
			)
			return nil, err
		}
		metaCnt = len(metas)
		sp.End(zlog.StatusOK, nil,
			zap.Int64("step_ms", msFetchArticles),
			zap.Int("candidate_in", len(args.CandidateArticleIDs)),
			zap.Int("meta_out", metaCnt),
		)
	}

	metas = prefilterCandidateMetas(metas, args.UserIntent, args.TopK)
	metaCnt = len(metas)

	// 2) 取记忆（任一桶失败：标记 DEGRADED，但不中断 rerank；内容置空）
	var longMem storage.UserMemory
	var shortMem storage.UserMemory
	var periodicMem storage.UserMemory
	{
		stepStart := time.Now()
		ctx2, sp := zlog.StartSpan(ctx, "skills.rerank.memory.get_long")
		ctx = ctx2

		var e error
		longMem, memLongFound, e = t.memoryRepo.Get(ctx, args.UserID, storage.MemoryLongTerm, "")
		msGetMemLong = time.Since(stepStart).Milliseconds()
		if e != nil {
			longMem = storage.UserMemory{}
			memDegraded = true
			sp.End(zlog.StatusDegraded, e,
				zap.Int64("step_ms", msGetMemLong),
				zap.String("memory_type", string(storage.MemoryLongTerm)),
			)
		} else {
			sp.End(zlog.StatusOK, nil,
				zap.Int64("step_ms", msGetMemLong),
				zap.Bool("found", memLongFound),
				zap.Int("content_len", len(longMem.Content)),
			)
		}
	}
	{
		stepStart := time.Now()
		ctx2, sp := zlog.StartSpan(ctx, "skills.rerank.memory.get_short")
		ctx = ctx2

		var e error
		shortMem, memShortFound, e = t.memoryRepo.Get(ctx, args.UserID, storage.MemoryShortTerm, "")
		msGetMemShort = time.Since(stepStart).Milliseconds()
		if e != nil {
			shortMem = storage.UserMemory{}
			memDegraded = true
			sp.End(zlog.StatusDegraded, e,
				zap.Int64("step_ms", msGetMemShort),
				zap.String("memory_type", string(storage.MemoryShortTerm)),
			)
		} else {
			sp.End(zlog.StatusOK, nil,
				zap.Int64("step_ms", msGetMemShort),
				zap.Bool("found", memShortFound),
				zap.Int("content_len", len(shortMem.Content)),
			)
		}
	}
	{
		stepStart := time.Now()
		ctx2, sp := zlog.StartSpan(ctx, "skills.rerank.memory.get_periodic")
		ctx = ctx2

		var e error
		periodicMem, memPeriodicFound, e = t.memoryRepo.Get(ctx, args.UserID, storage.MemoryPeriodic, "d1")
		msGetMemPeriodic = time.Since(stepStart).Milliseconds()
		if e != nil {
			periodicMem = storage.UserMemory{}
			memDegraded = true
			sp.End(zlog.StatusDegraded, e,
				zap.Int64("step_ms", msGetMemPeriodic),
				zap.String("memory_type", string(storage.MemoryPeriodic)),
				zap.String("period_bucket", "d1"),
			)
		} else {
			sp.End(zlog.StatusOK, nil,
				zap.Int64("step_ms", msGetMemPeriodic),
				zap.Bool("found", memPeriodicFound),
				zap.String("period_bucket", "d1"),
				zap.Int("content_len", len(periodicMem.Content)),
			)
		}
	}

	// 3) 组装 prompt（避免高基数：只传必要字段）
	var candLines []string
	{
		stepStart := time.Now()
		ctx2, sp := zlog.StartSpan(ctx, "skills.rerank.prompt.build_candidates")
		ctx = ctx2

		candLines = make([]string, 0, len(metas))
		for _, m := range metas {
			candLines = append(candLines,
				"- "+m.ArticleID+" | "+m.Title+" | type:"+m.TypeTags+" | tags:"+m.Tags+" | score:"+fmtFloat(float64(m.Score)),
			)
		}
		msBuildCandLines = time.Since(stepStart).Milliseconds()
		candidateLineCnt = len(candLines)
		sp.End(zlog.StatusOK, nil,
			zap.Int64("step_ms", msBuildCandLines),
			zap.Int("meta_in", metaCnt),
			zap.Int("candidate_lines", candidateLineCnt),
		)
	}

	sys := "你是一个推荐系统的精排序器。你要根据用户意图与记忆，把候选文章排序。只输出 JSON。不要输出多余文字。"

	// 3.1) 记忆片段裁剪/清洗（把这一步从 prompt.compose 剥离出来便于定位）
	var memLongStr, memShortStr, memPeriodicStr string
	{
		stepStart := time.Now()
		ctx2, sp := zlog.StartSpan(ctx, "skills.rerank.prompt.build_memory")
		ctx = ctx2

		memLongStr = strings.TrimSpace(longMem.Content)
		memShortStr = strings.TrimSpace(shortMem.Content)
		memPeriodicStr = strings.TrimSpace(periodicMem.Content)
		if t.memoryChunkRepo != nil {
			queryText := strings.TrimSpace(args.UserIntent)
			if queryText == "" {
				queryText = strings.Join(candidateTextHints(metas), " ")
			}
			if queryText != "" {
				if vec, e := service.TextVector(ctx, queryText); e == nil && len(vec) > 0 {
					if memLongFound {
						if chunks, e := t.memoryChunkRepo.SearchMemoryChunks(ctx, args.UserID, storage.MemoryLongTerm, "", longMem.UpdatedAt, vec, 3); e == nil && len(chunks) > 0 {
							memLongStr = strings.Join(chunks, "\n")
						}
					}
					if memShortFound {
						if chunks, e := t.memoryChunkRepo.SearchMemoryChunks(ctx, args.UserID, storage.MemoryShortTerm, "", shortMem.UpdatedAt, vec, 3); e == nil && len(chunks) > 0 {
							memShortStr = strings.Join(chunks, "\n")
						}
					}
					if memPeriodicFound {
						if chunks, e := t.memoryChunkRepo.SearchMemoryChunks(ctx, args.UserID, storage.MemoryPeriodic, "d1", periodicMem.UpdatedAt, vec, 3); e == nil && len(chunks) > 0 {
							memPeriodicStr = strings.Join(chunks, "\n")
						}
					}
				}
			}
		}

		msBuildMemory = time.Since(stepStart).Milliseconds()
		sp.End(zlog.StatusOK, nil,
			zap.Int64("step_ms", msBuildMemory),
			zap.Int("long_len", len(memLongStr)),
			zap.Int("short_len", len(memShortStr)),
			zap.Int("periodic_len", len(memPeriodicStr)),
		)
	}

	user := ""
	{
		stepStart := time.Now()
		ctx2, sp := zlog.StartSpan(ctx, "skills.rerank.prompt.compose")
		ctx = ctx2

		user = "用户意图：" + strings.TrimSpace(args.UserIntent) + "\n" +
			"长期记忆：" + memLongStr + "\n" +
			"短期记忆：" + memShortStr + "\n" +
			"周期记忆：" + memPeriodicStr + "\n" +
			"候选列表：\n" + strings.Join(candLines, "\n") + "\n\n" +
			"请只输出一个合法 JSON 对象（不要使用 ```json 代码块，不要输出任何解释）。" +
			"格式示例：{\"overall_confidence\":0.82,\"ranked\":[{\"article_id\":\"a1\",\"score\":0.93,\"reason\":\"...\"}]}" +
			"只保留 top " + itoa(args.TopK) + "。reason 要简短。inferred.memory_observations 与 inferred.assumptions[].evidence 必须来自上面的记忆原文（尽量原样短语），不要编造。assumptions 最多 5 条。"

		msComposePrompt = time.Since(stepStart).Milliseconds()
		promptBytes = len(sys) + len(user)
		sp.End(zlog.StatusOK, nil,
			zap.Int64("step_ms", msComposePrompt),
			zap.Int("prompt_bytes", promptBytes),
			zap.Int("topk", args.TopK),
			zap.Bool("mem_long_found", memLongFound),
			zap.Bool("mem_short_found", memShortFound),
			zap.Bool("mem_periodic_found", memPeriodicFound),
		)
	}

	// 4) 复用已初始化的 client（避免每次 Invoke 都创建）
	client := t.client
	if client == nil {
		return nil, errors.New("ai client is nil")
	}

	// 5) 构造 chat completion 请求参数（把构造耗时与网络耗时拆开）
	var req openai.ChatCompletionNewParams
	{
		stepStart := time.Now()
		ctx2, sp := zlog.StartSpan(ctx, "skills.rerank.ai.build_request")
		ctx = ctx2

		req = openai.ChatCompletionNewParams{
			Model: config.Cfg.Agent.Model,
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.SystemMessage(sys),
				openai.UserMessage(user),
			},
			Temperature: openai.Float(config.Cfg.Agent.Temperature),
		}

		msBuildRequest = time.Since(stepStart).Milliseconds()
		sp.End(zlog.StatusOK, nil,
			zap.Int64("step_ms", msBuildRequest),
			zap.String("model", config.Cfg.Agent.Model),
			zap.Float64("temperature", config.Cfg.Agent.Temperature),
			zap.Int("prompt_bytes", promptBytes),
		)
	}

	// 6) 调用大模型（网络）
	var resp *openai.ChatCompletion
	{
		stepStart := time.Now()
		ctx2, sp := zlog.StartSpan(ctx, "skills.rerank.ai.chat_completion")
		ctx = ctx2

		resp, err = client.Chat.Completions.New(ctx, req)
		msCallLLM = time.Since(stepStart).Milliseconds()
		if err != nil {
			sp.End(zlog.StatusError, err,
				zap.Int64("step_ms", msCallLLM),
				zap.String("model", config.Cfg.Agent.Model),
				zap.Float64("temperature", config.Cfg.Agent.Temperature),
				zap.Int("prompt_bytes", promptBytes),
			)
			totalSp.End(zlog.StatusError, err,
				zap.Any("breakdown_ms", map[string]any{
					"args_unmarshal":             msArgsUnmarshal,
					"args_validate":              msArgsValidate,
					"articles_fetch":             msFetchArticles,
					"mem_long":                   msGetMemLong,
					"mem_short":                  msGetMemShort,
					"mem_periodic":               msGetMemPeriodic,
					"prompt_build_candidates":    msBuildCandLines,
					"prompt_build_memory":        msBuildMemory,
					"prompt_compose":             msComposePrompt,
					"ai_client_init":             msInitClient,
					"ai_build_request":           msBuildRequest,
					"ai_chat_completion":         msCallLLM,
					"ai_extract_output":          msExtractOutput,
					"ai_parse_json":              msParseJSON,
					"result_validate_and_filter": msResultValidate,
				}),
			)
			return nil, err
		}

		llmModel = config.Cfg.Agent.Model
		if resp != nil && resp.Model != "" {
			llmModel = resp.Model
		}
		if resp != nil {
			llmTokensIn = int(resp.Usage.PromptTokens)
			llmTokensOut = int(resp.Usage.CompletionTokens)
			if len(resp.Choices) > 0 {
				llmStopReason = string(resp.Choices[0].FinishReason)
			}
			// token metrics（prompt+completion）
			metrics.GenRecAgentTotalTokensMetric.Add(float64(llmTokensIn + llmTokensOut))
		}
		sp.End(zlog.StatusOK, nil,
			zap.Int64("step_ms", msCallLLM),
			zap.String("model", llmModel),
			zap.Float64("temperature", config.Cfg.Agent.Temperature),
			zap.Int("prompt_bytes", promptBytes),
			zap.Int("tokens_in", llmTokensIn),
			zap.Int("tokens_out", llmTokensOut),
			zap.String("stop_reason", llmStopReason),
		)
	}

	// 7) 提取模型输出 content（清洗/选择 choice 的耗时）
	content := ""
	{
		stepStart := time.Now()
		ctx2, sp := zlog.StartSpan(ctx, "skills.rerank.ai.extract_output")
		ctx = ctx2

		if resp != nil && len(resp.Choices) > 0 {
			content = resp.Choices[0].Message.Content
		}
		msExtractOutput = time.Since(stepStart).Milliseconds()
		sp.End(zlog.StatusOK, nil,
			zap.Int64("step_ms", msExtractOutput),
			zap.Int("content_len", len(content)),
		)
	}

	// 8) 解析模型输出 JSON
	var out RerankResult
	{
		stepStart := time.Now()
		ctx2, sp := zlog.StartSpan(ctx, "skills.rerank.ai.parse_json")
		ctx = ctx2

		if e := json.Unmarshal([]byte(content), &out); e != nil {
			msParseJSON = time.Since(stepStart).Milliseconds()
			err = errors.New("解析 rerank 输出失败（模型未按 JSON 输出）")
			sp.End(zlog.StatusError, err,
				zap.String("unmarshal_err", e.Error()),
				zap.Int("content_len", len(content)),
				zap.String("head", previewHead(content, 300)),
				zap.String("tail", previewTail(content, 300)),
			)
			totalSp.End(zlog.StatusError, err,
				zap.Any("breakdown_ms", map[string]any{
					"args_unmarshal":             msArgsUnmarshal,
					"args_validate":              msArgsValidate,
					"articles_fetch":             msFetchArticles,
					"mem_long":                   msGetMemLong,
					"mem_short":                  msGetMemShort,
					"mem_periodic":               msGetMemPeriodic,
					"prompt_build_candidates":    msBuildCandLines,
					"prompt_build_memory":        msBuildMemory,
					"prompt_compose":             msComposePrompt,
					"ai_client_init":             msInitClient,
					"ai_build_request":           msBuildRequest,
					"ai_chat_completion":         msCallLLM,
					"ai_extract_output":          msExtractOutput,
					"ai_parse_json":              msParseJSON,
					"result_validate_and_filter": msResultValidate,
				}),
			)
			return nil, err
		}
		msParseJSON = time.Since(stepStart).Milliseconds()
		sp.End(zlog.StatusOK, nil,
			zap.Int64("step_ms", msParseJSON),
			zap.Int("candidate_out", len(out.Ranked)),
			zap.Float64("overall_confidence", out.OverallConfidence),
		)
	}

	// 9) 结果校验/去重/截断 topk（避免输出无效 article_id）
	{
		stepStart := time.Now()
		ctx2, sp := zlog.StartSpan(ctx, "skills.rerank.result.validate")
		ctx = ctx2

		allowed := make(map[string]struct{}, len(args.CandidateArticleIDs))
		for _, id := range args.CandidateArticleIDs {
			if id == "" {
				continue
			}
			allowed[id] = struct{}{}
		}

		filtered := make([]RankedItem, 0, len(out.Ranked))
		seen := make(map[string]struct{}, len(out.Ranked))
		invalidIDCnt := 0
		dupCnt := 0
		for _, it := range out.Ranked {
			if it.ArticleID == "" {
				invalidIDCnt++
				continue
			}
			if _, ok := allowed[it.ArticleID]; !ok {
				invalidIDCnt++
				continue
			}
			if _, ok := seen[it.ArticleID]; ok {
				dupCnt++
				continue
			}
			seen[it.ArticleID] = struct{}{}
			filtered = append(filtered, it)
			if len(filtered) >= args.TopK {
				break
			}
		}
		out.Ranked = filtered

		msResultValidate = time.Since(stepStart).Milliseconds()
		sp.End(zlog.StatusOK, nil,
			zap.Int64("step_ms", msResultValidate),
			zap.Int("ranked_in", len(seen)+dupCnt+invalidIDCnt),
			zap.Int("ranked_out", len(out.Ranked)),
			zap.Int("invalid_article_id_cnt", invalidIDCnt),
			zap.Int("dup_article_id_cnt", dupCnt),
			zap.Int("topk", args.TopK),
		)
	}

	// ✅ total：skills.rerank.completed（span 自带 latency_ms；这里只补充 breakdown）
	status := zlog.StatusOK
	if memDegraded {
		status = zlog.StatusDegraded
	}
	totalSp.End(status, nil,
		zap.Int("candidate_in", len(args.CandidateArticleIDs)),
		zap.Int("candidate_out", len(out.Ranked)),
		zap.Int("topk", args.TopK),
		zap.Bool("mem_degraded", memDegraded),
		zap.Any("breakdown_ms", map[string]any{
			"args_unmarshal":             msArgsUnmarshal,
			"args_validate":              msArgsValidate,
			"articles_fetch":             msFetchArticles,
			"mem_long":                   msGetMemLong,
			"mem_short":                  msGetMemShort,
			"mem_periodic":               msGetMemPeriodic,
			"prompt_build_candidates":    msBuildCandLines,
			"prompt_build_memory":        msBuildMemory,
			"prompt_compose":             msComposePrompt,
			"ai_client_init":             msInitClient,
			"ai_build_request":           msBuildRequest,
			"ai_chat_completion":         msCallLLM,
			"ai_extract_output":          msExtractOutput,
			"ai_parse_json":              msParseJSON,
			"result_validate_and_filter": msResultValidate,
		}),
		zap.Any("rank", map[string]any{
			"candidate_in":  len(args.CandidateArticleIDs),
			"candidate_out": len(out.Ranked),
			"topk":          args.TopK,
		}),
		zap.Any("decision", map[string]any{
			"type":         "rank",
			"chosen":       "ai_rerank",
			"confidence":   out.OverallConfidence,
			"reason_codes": []string{"FUSE_MEMORY"},
		}),
		zap.Any("llm", map[string]any{
			"model":       llmModel,
			"tokens_in":   llmTokensIn,
			"tokens_out":  llmTokensOut,
			"stop_reason": llmStopReason,
		}),
	)

	res = out
	return res, nil
}

func itoa(i int) string { return strconv.Itoa(i) }
func fmtFloat(f float64) string {
	// 保持简短
	return strconv.FormatFloat(f, 'f', 3, 64)
}
func previewHead(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) == 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func previewTail(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) == 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func prefilterCandidateMetas(metas []storage.ArticleMeta, userIntent string, topK int) []storage.ArticleMeta {
	if len(metas) == 0 {
		return nil
	}
	capSize := topK * 2
	if capSize < 12 {
		capSize = 12
	}
	if capSize > 30 {
		capSize = 30
	}
	if len(metas) <= capSize {
		return metas
	}

	intentTerms := keywordSet(userIntent)
	type bucket struct {
		Meta    storage.ArticleMeta
		Score   float64
		Cluster string
	}
	buckets := make([]bucket, 0, len(metas))
	for idx, meta := range metas {
		cluster := primaryCluster(meta)
		overlap := overlapScore(intentTerms, keywordSet(meta.Title+" "+meta.TypeTags+" "+meta.Tags))
		score := float64(meta.Score)*0.35 + overlap*0.45 + recencyBias(idx, len(metas))*0.20
		buckets = append(buckets, bucket{
			Meta:    meta,
			Score:   score,
			Cluster: cluster,
		})
	}
	sort.SliceStable(buckets, func(i, j int) bool {
		return buckets[i].Score > buckets[j].Score
	})

	byCluster := map[string][]bucket{}
	order := make([]string, 0)
	for _, item := range buckets {
		if _, ok := byCluster[item.Cluster]; !ok {
			order = append(order, item.Cluster)
		}
		byCluster[item.Cluster] = append(byCluster[item.Cluster], item)
	}

	out := make([]storage.ArticleMeta, 0, capSize)
	for len(out) < capSize {
		progressed := false
		for _, cluster := range order {
			items := byCluster[cluster]
			if len(items) == 0 {
				continue
			}
			out = append(out, items[0].Meta)
			byCluster[cluster] = items[1:]
			progressed = true
			if len(out) >= capSize {
				break
			}
		}
		if !progressed {
			break
		}
	}
	return out
}

func candidateTextHints(metas []storage.ArticleMeta) []string {
	out := make([]string, 0, rerankMinInt(6, len(metas)))
	for i := 0; i < len(metas) && i < 6; i++ {
		text := strings.TrimSpace(metas[i].Title + " " + metas[i].TypeTags + " " + metas[i].Tags)
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func primaryCluster(meta storage.ArticleMeta) string {
	parts := strings.Split(meta.TypeTags, ",")
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			return p
		}
	}
	if title := strings.TrimSpace(strings.ToLower(meta.Title)); title != "" {
		return title
	}
	return "default"
}

func keywordSet(text string) map[string]struct{} {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z':
			return false
		case r >= '0' && r <= '9':
			return false
		case r >= 0x4e00 && r <= 0x9fff:
			return false
		default:
			return true
		}
	})
	res := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" || len([]rune(field)) < 2 {
			continue
		}
		res[field] = struct{}{}
	}
	return res
}

func overlapScore(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	match := 0
	for key := range a {
		if _, ok := b[key]; ok {
			match++
		}
	}
	denom := len(a)
	if len(b) > denom {
		denom = len(b)
	}
	if denom == 0 {
		return 0
	}
	return float64(match) / float64(denom)
}

func recencyBias(rank, total int) float64 {
	if rank < 0 || total <= 1 {
		return 1
	}
	return 1 - float64(rank)/float64(total-1)
}

func rerankMinInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
