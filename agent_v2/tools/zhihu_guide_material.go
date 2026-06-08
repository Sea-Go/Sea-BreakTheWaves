package tools

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"agent_v2/config"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

const (
	ZhihuGuideStatusAccepted = "accepted"
	ZhihuGuideStatusReview   = "review"
	ZhihuGuideStatusRejected = "rejected"
	ZhihuGuideStatusSelected = "selected"
)

type ZhihuGuideQuery struct {
	Query  string `json:"query"`
	Intent string `json:"intent"`
}

type ZhihuGuideSource struct {
	Query  string `json:"query"`
	Intent string `json:"intent"`
	Scope  string `json:"scope"`
}

type ZhihuGuideCandidate struct {
	Title        string              `json:"title"`
	URL          string              `json:"url"`
	AuthorName   string              `json:"author_name"`
	Summary      string              `json:"summary"`
	VoteUpCount  int64               `json:"vote_up_count"`
	CommentCount int64               `json:"comment_count"`
	EditTime     int64               `json:"edit_time"`
	Sources      []ZhihuGuideSource  `json:"sources"`
	SourceQuery  string              `json:"source_query"`
	SourceIntent string              `json:"source_intent"`
	SearchScope  string              `json:"search_scope"`
	URLHash      string              `json:"url_hash"`
	IsArticle    bool                `json:"is_article"`
	Score        float64             `json:"score"`
	Status       string              `json:"status"`
	Reasons      []string            `json:"reasons"`
	Raw          *ZhihuSearchItem    `json:"raw,omitempty"`
	IntentScores map[string]struct{} `json:"-"`
}

type ZhihuGuideStats struct {
	RawCount             int `json:"raw_count"`
	ZhihuSearchRawCount  int `json:"zhihu_search_raw_count"`
	GlobalSearchRawCount int `json:"global_search_raw_count"`
	DedupedCount         int `json:"deduped_count"`
	AcceptedCount        int `json:"accepted_count"`
	ReviewCount          int `json:"review_count"`
	RejectedCount        int `json:"rejected_count"`
	SelectedCount        int `json:"selected_count"`
}

type ZhihuGuideRun struct {
	RunID              string                 `json:"run_id"`
	Topic              string                 `json:"topic"`
	QueryPlan          []ZhihuGuideQuery      `json:"query_plan"`
	RawCandidates      []ZhihuGuideCandidate  `json:"raw_candidates"`
	FilteredCandidates []ZhihuGuideCandidate  `json:"filtered_candidates"`
	ReviewPool         []ZhihuGuideCandidate  `json:"review_pool"`
	SelectedForLLM     ZhihuGuideLLMInput     `json:"selected_for_llm"`
	Stats              ZhihuGuideStats        `json:"stats"`
	Options            ZhihuGuideRunOptions   `json:"options"`
	Errors             []ZhihuGuideQueryError `json:"errors,omitempty"`
}

type ZhihuGuideLLMInput struct {
	Topic         string                 `json:"topic"`
	SelectedCount int                    `json:"selected_count"`
	Items         []ZhihuGuideLLMArticle `json:"items"`
}

type ZhihuGuideLLMArticle struct {
	Intent        string   `json:"intent"`
	SearchScope   string   `json:"search_scope"`
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	AuthorName    string   `json:"author_name"`
	Summary       string   `json:"summary"`
	ContentBrief  string   `json:"content_brief"`
	KeyPoints     []string `json:"key_points"`
	SourceSignals []string `json:"source_signals"`
	Score         float64  `json:"score"`
	Evidence      []string `json:"evidence"`
}

type ZhihuGuideQueryError struct {
	Query string `json:"query"`
	Error string `json:"error"`
}

type ZhihuGuideReviewDecisionFile struct {
	RunID                string                     `json:"run_id"`
	Topic                string                     `json:"topic"`
	SelectedArticleCount int                        `json:"selected_article_count"`
	Instructions         []string                   `json:"instructions"`
	Decisions            []ZhihuGuideReviewDecision `json:"decisions"`
}

type ZhihuGuideReviewDecision struct {
	URLHash         string   `json:"url_hash"`
	URL             string   `json:"url"`
	Title           string   `json:"title"`
	AuthorName      string   `json:"author_name"`
	Intent          string   `json:"intent"`
	Score           float64  `json:"score"`
	SuggestedStatus string   `json:"suggested_status"`
	Decision        string   `json:"decision"`
	HumanNote       string   `json:"human_note"`
	Reasons         []string `json:"reasons"`
}

type ZhihuGuideRunOptions struct {
	QueryCount           int      `json:"query_count"`
	PerQueryCount        int      `json:"per_query_count"`
	ReviewPoolSize       int      `json:"review_pool_size"`
	SelectedArticleCount int      `json:"selected_article_count"`
	ArticleOnly          bool     `json:"article_only"`
	AcceptScore          float64  `json:"accept_score"`
	ReviewScore          float64  `json:"review_score"`
	MinSummaryChars      int      `json:"min_summary_chars"`
	MinVoteUpCount       int64    `json:"min_vote_up_count"`
	MaxAgeDays           int      `json:"max_age_days"`
	MustKeywords         []string `json:"must_keywords"`
	ShouldKeywords       []string `json:"should_keywords"`
	NegativeKeywords     []string `json:"negative_keywords"`
	BlockedAuthors       []string `json:"blocked_authors"`
	TrustedAuthors       []string `json:"trusted_authors"`
}

type zhihuGuideSearcher interface {
	Search(context.Context, ZhihuSearchInput) (ZhihuSearchResult, error)
}

type zhihuGuideGlobalSearcher interface {
	GlobalSearch(context.Context, ZhihuSearchInput) (ZhihuSearchResult, error)
}

type ZhihuGuideMaterialInput struct {
	Topic                string   `json:"topic" jsonschema:"description=攻略主题，例如 大阪旅游攻略、东京亲子游"`
	QueryCount           int      `json:"query_count,omitempty" jsonschema:"description=搜索词数量，默认使用配置，建议 8-12"`
	PerQueryCount        int      `json:"per_query_count,omitempty" jsonschema:"description=每个搜索词返回数量，范围 1-10，默认 10"`
	ReviewPoolSize       int      `json:"review_pool_size,omitempty" jsonschema:"description=进入人工审核池的候选数量，默认 30"`
	SelectedArticleCount int      `json:"selected_article_count,omitempty" jsonschema:"description=最终进入大模型的素材数量，默认 12"`
	ArticleOnly          *bool    `json:"article_only,omitempty" jsonschema:"description=是否只保留知乎专栏文章，默认 false"`
	MustKeywords         []string `json:"must_keywords,omitempty" jsonschema:"description=必须相关的核心关键词，不传则从 topic 推断"`
	ShouldKeywords       []string `json:"should_keywords,omitempty" jsonschema:"description=加分关键词，例如 交通、美食、避坑"`
	NegativeKeywords     []string `json:"negative_keywords,omitempty" jsonschema:"description=负面关键词，命中后拒绝"`
}

type ZhihuGuideMaterialResult struct {
	RunID                string                       `json:"run_id"`
	Topic                string                       `json:"topic"`
	QueryCount           int                          `json:"query_count"`
	RawCount             int                          `json:"raw_count"`
	ZhihuSearchRawCount  int                          `json:"zhihu_search_raw_count"`
	GlobalSearchRawCount int                          `json:"global_search_raw_count"`
	DedupedCount         int                          `json:"deduped_count"`
	ReviewPoolCount      int                          `json:"review_pool_count"`
	SelectedCount        int                          `json:"selected_count"`
	SelectedForLLM       ZhihuGuideLLMInput           `json:"selected_for_llm"`
	ReviewCandidates     []ZhihuGuideCandidate        `json:"review_candidates,omitempty"`
	ReviewDecisions      ZhihuGuideReviewDecisionFile `json:"review_decisions"`
	QueryPlan            []ZhihuGuideQuery            `json:"query_plan"`
	Errors               []ZhihuGuideQueryError       `json:"errors,omitempty"`
	Message              string                       `json:"message"`
}

func CollectZhihuGuideMaterial(ctx context.Context, zhihuCfg config.ZhihuConfig, topic string) (ZhihuGuideRun, error) {
	runtime := newZhihuRuntime(zhihuCfg)
	return collectZhihuGuideMaterial(ctx, runtime, topic, guideOptionsFromConfig(zhihuCfg.GuideMaterial))
}

func newZhihuGuideMaterialTool(runtime *zhihuRuntime) tool.Tool {
	return function.NewFunctionTool(runtime.GuideMaterial,
		function.WithName("zhihu_guide_material"),
		function.WithDescription("围绕攻略主题多轮调用知乎搜索，自动完成候选文章/回答获取、去重、过滤、评分、选择，并生成可供大模型使用的 selected_for_llm 素材。"),
	)
}

func (r *zhihuRuntime) GuideMaterial(ctx context.Context, in ZhihuGuideMaterialInput) (ZhihuGuideMaterialResult, error) {
	opts := guideOptionsFromConfig(r.cfg.GuideMaterial)
	opts = mergeGuideMaterialInput(opts, in)
	run, err := collectZhihuGuideMaterial(ctx, r, in.Topic, opts)
	if err != nil {
		return ZhihuGuideMaterialResult{}, err
	}

	return buildZhihuGuideMaterialResult(run), nil
}

func GenerateZhihuGuideQueryPlan(topic string, queryCount int) []ZhihuGuideQuery {
	topic = strings.TrimSpace(topic)
	templates := []ZhihuGuideQuery{
		{Query: topic, Intent: "overview"},
		{Query: topic + " 攻略", Intent: "overview"},
		{Query: topic + " 自由行", Intent: "itinerary"},
		{Query: topic + " 路线", Intent: "itinerary"},
		{Query: topic + " 住哪里", Intent: "hotel_area"},
		{Query: topic + " 交通", Intent: "transport"},
		{Query: topic + " 美食", Intent: "food"},
		{Query: topic + " 避坑", Intent: "pitfall"},
		{Query: topic + " 预算", Intent: "budget"},
		{Query: topic + " 几天合适", Intent: "itinerary"},
		{Query: topic + " 景点推荐", Intent: "attraction"},
		{Query: topic + " 季节", Intent: "season"},
		{Query: topic + " 亲子", Intent: "scene"},
		{Query: topic + " 情侣", Intent: "scene"},
		{Query: topic + " 第一次", Intent: "overview"},
	}
	seen := map[string]bool{}
	out := make([]ZhihuGuideQuery, 0, queryCount)
	for _, item := range templates {
		query := compactSpace(item.Query)
		if query == "" || seen[query] {
			continue
		}
		seen[query] = true
		out = append(out, ZhihuGuideQuery{Query: query, Intent: item.Intent})
		if len(out) >= queryCount {
			break
		}
	}
	return out
}

func collectZhihuGuideMaterial(ctx context.Context, searcher zhihuGuideSearcher, topic string, opts ZhihuGuideRunOptions) (ZhihuGuideRun, error) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return ZhihuGuideRun{}, fmt.Errorf("topic cannot be empty")
	}
	opts = normalizeGuideOptions(opts)
	queryPlan := GenerateZhihuGuideQueryPlan(topic, opts.QueryCount)
	run := ZhihuGuideRun{
		RunID:     BuildZhihuGuideRunID(topic, time.Now()),
		Topic:     topic,
		QueryPlan: queryPlan,
		Options:   opts,
	}

	for _, query := range queryPlan {
		result, err := searcher.Search(ctx, ZhihuSearchInput{Query: query.Query, Count: opts.PerQueryCount})
		if err != nil {
			run.Errors = append(run.Errors, ZhihuGuideQueryError{Query: query.Query, Error: "zhihu_search: " + err.Error()})
		} else if result.Code != 0 {
			run.Errors = append(run.Errors, ZhihuGuideQueryError{Query: query.Query, Error: "zhihu_search: " + result.Message})
		} else {
			for _, item := range result.Items {
				candidate := candidateFromSearchItem(item, query, "zhihu_search")
				run.RawCandidates = append(run.RawCandidates, candidate)
			}
		}

		globalSearcher, ok := searcher.(zhihuGuideGlobalSearcher)
		if !ok {
			continue
		}
		result, err = globalSearcher.GlobalSearch(ctx, ZhihuSearchInput{Query: query.Query, Count: opts.PerQueryCount})
		if err != nil {
			run.Errors = append(run.Errors, ZhihuGuideQueryError{Query: query.Query, Error: "global_search: " + err.Error()})
			continue
		}
		if result.Code != 0 {
			run.Errors = append(run.Errors, ZhihuGuideQueryError{Query: query.Query, Error: "global_search: " + result.Message})
			continue
		}
		for _, item := range result.Items {
			candidate := candidateFromSearchItem(item, query, "global_search")
			run.RawCandidates = append(run.RawCandidates, candidate)
		}
	}

	run.Stats.RawCount = len(run.RawCandidates)
	run.Stats.ZhihuSearchRawCount, run.Stats.GlobalSearchRawCount = countZhihuGuideRawScopes(run.RawCandidates)
	run.FilteredCandidates = scoreZhihuGuideCandidates(dedupZhihuGuideCandidates(run.RawCandidates), topic, opts)
	run.Stats = computeZhihuGuideStats(run.Stats.RawCount, run.FilteredCandidates)
	run.Stats.ZhihuSearchRawCount, run.Stats.GlobalSearchRawCount = countZhihuGuideRawScopes(run.RawCandidates)
	run.ReviewPool = selectZhihuGuideReviewPool(run.FilteredCandidates, opts.ReviewPoolSize)
	selected := selectZhihuGuideArticles(run.ReviewPool, opts.SelectedArticleCount)
	run.SelectedForLLM = buildZhihuGuideLLMInput(topic, selected)
	run.Stats.SelectedCount = len(run.SelectedForLLM.Items)
	return run, nil
}

func BuildZhihuGuideReviewDecisions(run ZhihuGuideRun) ZhihuGuideReviewDecisionFile {
	selected := map[string]bool{}
	for _, item := range run.SelectedForLLM.Items {
		selected[hashString(normalizeZhihuURL(item.URL))] = true
	}
	out := ZhihuGuideReviewDecisionFile{
		RunID:                run.RunID,
		Topic:                run.Topic,
		SelectedArticleCount: run.Options.SelectedArticleCount,
		Instructions: []string{
			"把 decision 改成 selected 表示最终喂给大模型。",
			"把 decision 改成 rejected 表示人工排除。",
			"保持 pending 表示暂不人工指定，apply-review 时会按分数和维度自动补足。",
		},
	}
	for _, item := range run.ReviewPool {
		decision := "pending"
		if selected[item.URLHash] {
			decision = ZhihuGuideStatusSelected
		}
		reasons := item.Reasons
		if len(reasons) > 5 {
			reasons = reasons[:5]
		}
		out.Decisions = append(out.Decisions, ZhihuGuideReviewDecision{
			URLHash:         item.URLHash,
			URL:             item.URL,
			Title:           item.Title,
			AuthorName:      item.AuthorName,
			Intent:          classifyGuideIntent(item),
			Score:           item.Score,
			SuggestedStatus: item.Status,
			Decision:        decision,
			Reasons:         append([]string(nil), reasons...),
		})
	}
	return out
}

func ApplyZhihuGuideReviewDecisions(topic string, reviewPool []ZhihuGuideCandidate, decisionFile ZhihuGuideReviewDecisionFile, limit int) (ZhihuGuideLLMInput, []string) {
	if strings.TrimSpace(topic) == "" {
		topic = decisionFile.Topic
	}
	if limit <= 0 {
		limit = decisionFile.SelectedArticleCount
	}
	if limit <= 0 {
		limit = 12
	}

	byHash := map[string]ZhihuGuideCandidate{}
	byURL := map[string]ZhihuGuideCandidate{}
	for _, item := range reviewPool {
		byHash[item.URLHash] = item
		byURL[normalizeZhihuURL(item.URL)] = item
	}

	selected := make([]ZhihuGuideCandidate, 0, limit)
	rejected := map[string]bool{}
	used := map[string]bool{}
	warnings := []string{}

	for _, decision := range decisionFile.Decisions {
		item, ok := byHash[decision.URLHash]
		if !ok {
			item, ok = byURL[normalizeZhihuURL(decision.URL)]
		}
		if !ok {
			if strings.TrimSpace(decision.Decision) != "" && decision.Decision != "pending" {
				warnings = append(warnings, "审核决策未匹配候选："+decision.Title)
			}
			continue
		}
		key := item.URLHash
		switch normalizeDecision(decision.Decision) {
		case ZhihuGuideStatusSelected:
			if used[key] {
				continue
			}
			item.Status = ZhihuGuideStatusSelected
			if strings.TrimSpace(decision.HumanNote) != "" {
				item.Reasons = append([]string{"人工备注：" + strings.TrimSpace(decision.HumanNote)}, item.Reasons...)
			}
			selected = append(selected, item)
			used[key] = true
		case ZhihuGuideStatusRejected:
			rejected[key] = true
		}
	}

	if len(selected) > limit {
		warnings = append(warnings, fmt.Sprintf("人工选择 %d 篇，超过限制 %d 篇，已按审核文件顺序截断", len(selected), limit))
		selected = selected[:limit]
	}
	if len(selected) < limit {
		for _, item := range selectZhihuGuideArticles(reviewPool, limit) {
			if used[item.URLHash] || rejected[item.URLHash] {
				continue
			}
			selected = append(selected, item)
			used[item.URLHash] = true
			if len(selected) >= limit {
				break
			}
		}
	}
	return buildZhihuGuideLLMInput(topic, selected), warnings
}

func BuildZhihuGuideReviewMarkdown(run ZhihuGuideRun) string {
	var b strings.Builder
	b.WriteString("# 知乎攻略素材审核报告\n\n")
	b.WriteString("## 本次运行\n\n")
	b.WriteString(fmt.Sprintf("- run_id：`%s`\n", run.RunID))
	b.WriteString(fmt.Sprintf("- topic：`%s`\n", run.Topic))
	b.WriteString(fmt.Sprintf("- query 数：`%d`\n", len(run.QueryPlan)))
	b.WriteString(fmt.Sprintf("- 原始候选：`%d`\n", run.Stats.RawCount))
	b.WriteString(fmt.Sprintf("- 去重后候选：`%d`\n", run.Stats.DedupedCount))
	b.WriteString(fmt.Sprintf("- accepted：`%d`\n", run.Stats.AcceptedCount))
	b.WriteString(fmt.Sprintf("- review：`%d`\n", run.Stats.ReviewCount))
	b.WriteString(fmt.Sprintf("- rejected：`%d`\n", run.Stats.RejectedCount))
	b.WriteString(fmt.Sprintf("- selected：`%d`\n\n", run.Stats.SelectedCount))

	if len(run.Errors) > 0 {
		b.WriteString("## 取数错误\n\n")
		b.WriteString("| Query | Error |\n")
		b.WriteString("|---|---|\n")
		for _, item := range run.Errors {
			b.WriteString(fmt.Sprintf("| %s | %s |\n", escapeGuideMarkdownCell(item.Query), escapeGuideMarkdownCell(item.Error)))
		}
		b.WriteString("\n")
	}

	b.WriteString("## Query Plan\n\n")
	b.WriteString("| # | Intent | Query |\n")
	b.WriteString("|---:|---|---|\n")
	for i, item := range run.QueryPlan {
		b.WriteString(fmt.Sprintf("| %d | `%s` | %s |\n", i+1, escapeGuideMarkdownCell(item.Intent), escapeGuideMarkdownCell(item.Query)))
	}
	b.WriteString("\n")

	b.WriteString("## 推荐进入大模型的素材\n\n")
	writeGuideCandidateTable(&b, selectedCandidatesFromRun(run))

	b.WriteString("## 人工审核池\n\n")
	writeGuideCandidateTable(&b, run.ReviewPool)

	b.WriteString("## 说明\n\n")
	b.WriteString("- 大模型生成攻略时只使用返回结果中的 `selected_for_llm`。\n")
	b.WriteString("- 如需人工调整，修改返回结果中 `review_decisions` 的 `decision` 字段，再由 agent 重建 `selected_for_llm`。\n")
	b.WriteString("- `decision` 可选：`selected`、`rejected`、`pending`；`pending` 会在 apply-review 时按分数和维度自动补足。\n")
	b.WriteString("- `raw_candidates` 用于追溯 API 返回；`filtered_candidates` 用于调试筛选规则。\n")
	return b.String()
}

func BuildZhihuGuideAppliedReviewMarkdown(decisions ZhihuGuideReviewDecisionFile, input ZhihuGuideLLMInput, warnings []string) string {
	var b strings.Builder
	b.WriteString("# 知乎攻略素材人工审核应用结果\n\n")
	b.WriteString(fmt.Sprintf("- run_id：`%s`\n", decisions.RunID))
	b.WriteString(fmt.Sprintf("- topic：`%s`\n", input.Topic))
	b.WriteString(fmt.Sprintf("- selected：`%d`\n\n", input.SelectedCount))
	if len(warnings) > 0 {
		b.WriteString("## 警告\n\n")
		for _, warning := range warnings {
			b.WriteString("- " + warning + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## 最终进入大模型的素材\n\n")
	b.WriteString("| # | 维度 | 分数 | 标题 | 作者 | 证据 |\n")
	b.WriteString("|---:|---|---:|---|---|---|\n")
	for i, item := range input.Items {
		title := item.Title
		if item.URL != "" {
			title = fmt.Sprintf("[%s](%s)", escapeGuideMarkdownCell(item.Title), item.URL)
		}
		b.WriteString(fmt.Sprintf(
			"| %d | `%s` | %.1f | %s | %s | %s |\n",
			i+1,
			escapeGuideMarkdownCell(item.Intent),
			item.Score,
			title,
			escapeGuideMarkdownCell(item.AuthorName),
			escapeGuideMarkdownCell(strings.Join(item.Evidence, "; ")),
		))
	}
	b.WriteString("\n")
	return b.String()
}

func guideOptionsFromConfig(cfg config.ZhihuGuideMaterialConfig) ZhihuGuideRunOptions {
	cfg = cfg.WithDefaults()
	return ZhihuGuideRunOptions{
		QueryCount:           cfg.QueryCount,
		PerQueryCount:        cfg.PerQueryCount,
		ReviewPoolSize:       cfg.ReviewPoolSize,
		SelectedArticleCount: cfg.SelectedArticleCount,
		ArticleOnly:          cfg.ArticleOnly,
		AcceptScore:          cfg.AcceptScore,
		ReviewScore:          cfg.ReviewScore,
		MinSummaryChars:      cfg.MinSummaryChars,
		MinVoteUpCount:       cfg.MinVoteUpCount,
		MaxAgeDays:           cfg.MaxAgeDays,
		MustKeywords:         cfg.MustKeywords,
		ShouldKeywords:       cfg.ShouldKeywords,
		NegativeKeywords:     cfg.NegativeKeywords,
		BlockedAuthors:       cfg.BlockedAuthors,
		TrustedAuthors:       cfg.TrustedAuthors,
	}
}

func normalizeGuideOptions(opts ZhihuGuideRunOptions) ZhihuGuideRunOptions {
	cfg := config.ZhihuGuideMaterialConfig{
		QueryCount:           opts.QueryCount,
		PerQueryCount:        opts.PerQueryCount,
		ReviewPoolSize:       opts.ReviewPoolSize,
		SelectedArticleCount: opts.SelectedArticleCount,
		ArticleOnly:          opts.ArticleOnly,
		AcceptScore:          opts.AcceptScore,
		ReviewScore:          opts.ReviewScore,
		MinSummaryChars:      opts.MinSummaryChars,
		MinVoteUpCount:       opts.MinVoteUpCount,
		MaxAgeDays:           opts.MaxAgeDays,
		MustKeywords:         opts.MustKeywords,
		ShouldKeywords:       opts.ShouldKeywords,
		NegativeKeywords:     opts.NegativeKeywords,
		BlockedAuthors:       opts.BlockedAuthors,
		TrustedAuthors:       opts.TrustedAuthors,
	}.WithDefaults()
	return guideOptionsFromConfig(config.ZhihuGuideMaterialConfig{
		QueryCount:           cfg.QueryCount,
		PerQueryCount:        cfg.PerQueryCount,
		ReviewPoolSize:       cfg.ReviewPoolSize,
		SelectedArticleCount: cfg.SelectedArticleCount,
		ArticleOnly:          cfg.ArticleOnly,
		AcceptScore:          cfg.AcceptScore,
		ReviewScore:          cfg.ReviewScore,
		MinSummaryChars:      cfg.MinSummaryChars,
		MinVoteUpCount:       cfg.MinVoteUpCount,
		MaxAgeDays:           cfg.MaxAgeDays,
		MustKeywords:         cfg.MustKeywords,
		ShouldKeywords:       cfg.ShouldKeywords,
		NegativeKeywords:     cfg.NegativeKeywords,
		BlockedAuthors:       cfg.BlockedAuthors,
		TrustedAuthors:       cfg.TrustedAuthors,
	})
}

func candidateFromSearchItem(item ZhihuSearchItem, source ZhihuGuideQuery, scope string) ZhihuGuideCandidate {
	normalizedURL := normalizeZhihuURL(item.URL)
	raw := item
	return ZhihuGuideCandidate{
		Title:        strings.TrimSpace(item.Title),
		URL:          strings.TrimSpace(item.URL),
		AuthorName:   strings.TrimSpace(item.AuthorName),
		Summary:      strings.TrimSpace(item.Summary),
		VoteUpCount:  item.VoteUpCount,
		CommentCount: item.CommentCount,
		EditTime:     item.EditTime,
		Sources:      []ZhihuGuideSource{{Query: source.Query, Intent: source.Intent, Scope: scope}},
		SourceQuery:  source.Query,
		SourceIntent: source.Intent,
		SearchScope:  scope,
		URLHash:      hashString(normalizedURL),
		IsArticle:    isZhihuArticleURL(item.URL),
		Raw:          &raw,
	}
}

func dedupZhihuGuideCandidates(items []ZhihuGuideCandidate) []ZhihuGuideCandidate {
	byURL := map[string]int{}
	out := make([]ZhihuGuideCandidate, 0, len(items))
	for _, item := range items {
		key := item.URLHash
		if key == "" {
			key = hashString(normalizeZhihuURL(item.URL))
		}
		if idx, ok := byURL[key]; ok {
			existing := &out[idx]
			existing.Sources = appendMissingSources(existing.Sources, item.Sources)
			if roughCandidateStrength(item) > roughCandidateStrength(*existing) {
				item.Sources = existing.Sources
				out[idx] = item
			}
			continue
		}
		byURL[key] = len(out)
		out = append(out, item)
	}
	return out
}

func scoreZhihuGuideCandidates(items []ZhihuGuideCandidate, topic string, opts ZhihuGuideRunOptions) []ZhihuGuideCandidate {
	out := make([]ZhihuGuideCandidate, 0, len(items))
	for _, item := range items {
		item.Score, item.Status, item.Reasons = scoreZhihuGuideCandidate(item, topic, opts)
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return statusRank(out[i].Status) < statusRank(out[j].Status)
		}
		return out[i].Score > out[j].Score
	})
	return out
}

func scoreZhihuGuideCandidate(item ZhihuGuideCandidate, topic string, opts ZhihuGuideRunOptions) (float64, string, []string) {
	reasons := []string{}
	if item.Title == "" {
		return 0, ZhihuGuideStatusRejected, []string{"标题为空"}
	}
	if item.URL == "" {
		return 0, ZhihuGuideStatusRejected, []string{"URL 为空"}
	}
	if containsAny(item.AuthorName, opts.BlockedAuthors) {
		return 0, ZhihuGuideStatusRejected, []string{"作者命中黑名单"}
	}
	text := strings.ToLower(item.Title + " " + item.Summary)
	if containsAny(text, opts.NegativeKeywords) {
		return 0, ZhihuGuideStatusRejected, []string{"命中负面关键词"}
	}

	score := 0.0
	topicTokens := keywordTokens(topic)
	mustKeywords := append([]string{}, opts.MustKeywords...)
	if len(mustKeywords) == 0 {
		mustKeywords = topicTokens
	}
	matchedMust := countContains(text, mustKeywords)
	if matchedMust == 0 && len(mustKeywords) > 0 {
		reasons = append(reasons, "核心词未命中")
	} else {
		part := math.Min(30, float64(matchedMust)*12)
		score += part
		reasons = append(reasons, fmt.Sprintf("主题相关性 +%.1f", part))
	}
	shouldMatched := countContains(text, opts.ShouldKeywords)
	if shouldMatched > 0 {
		part := math.Min(8, float64(shouldMatched)*2)
		score += part
		reasons = append(reasons, fmt.Sprintf("扩展关键词 +%.1f", part))
	}

	intent := classifyGuideIntent(item)
	if intent != "other" {
		score += 20
		reasons = append(reasons, "攻略维度 "+intent+" +20.0")
	} else {
		score += 6
		reasons = append(reasons, "攻略维度不明确 +6.0")
	}

	engagement := math.Min(20, math.Log10(float64(maxInt64(item.VoteUpCount, 0)+1))*8+math.Log10(float64(maxInt64(item.CommentCount, 0)+1))*3)
	score += engagement
	reasons = append(reasons, fmt.Sprintf("互动质量 +%.1f", engagement))

	freshness := zhihuFreshnessScore(item.EditTime)
	score += freshness
	reasons = append(reasons, fmt.Sprintf("新鲜度 +%.1f", freshness))

	if item.IsArticle {
		score += 8
		reasons = append(reasons, "专栏文章 +8.0")
	} else {
		score += 3
		reasons = append(reasons, "非专栏内容 +3.0")
	}
	if item.SearchScope == "global_search" {
		score += 6
		reasons = append(reasons, "global_search supplement +6.0")
	}
	if containsAny(item.AuthorName, opts.TrustedAuthors) {
		score += 5
		reasons = append(reasons, "作者命中白名单 +5.0")
	}
	readability := readabilityScore(item)
	score += readability
	reasons = append(reasons, fmt.Sprintf("可读性 +%.1f", readability))

	status := ZhihuGuideStatusAccepted
	if len([]rune(item.Summary)) < opts.MinSummaryChars {
		status = ZhihuGuideStatusReview
		reasons = append(reasons, "摘要过短，需人工复核")
	}
	if item.VoteUpCount < opts.MinVoteUpCount {
		status = ZhihuGuideStatusReview
		reasons = append(reasons, "赞同数低，需人工复核")
	}
	if opts.ArticleOnly && !item.IsArticle {
		status = ZhihuGuideStatusReview
		reasons = append(reasons, "非专栏文章，需人工复核")
	}
	if matchedMust == 0 && len(mustKeywords) > 0 {
		status = ZhihuGuideStatusReview
	}
	if score < opts.ReviewScore {
		status = ZhihuGuideStatusRejected
	} else if score < opts.AcceptScore {
		status = ZhihuGuideStatusReview
	}
	return math.Round(score*10) / 10, status, reasons
}

func selectZhihuGuideReviewPool(items []ZhihuGuideCandidate, size int) []ZhihuGuideCandidate {
	if size <= 0 {
		return nil
	}
	pool := make([]ZhihuGuideCandidate, 0, size)
	for _, item := range items {
		if item.Status == ZhihuGuideStatusRejected {
			continue
		}
		pool = append(pool, item)
		if len(pool) >= size {
			break
		}
	}
	return pool
}

func selectZhihuGuideArticles(pool []ZhihuGuideCandidate, limit int) []ZhihuGuideCandidate {
	if limit <= 0 {
		return nil
	}
	selected := make([]ZhihuGuideCandidate, 0, limit)
	used := map[string]bool{}
	intentCount := map[string]int{}
	for _, item := range pool {
		intent := classifyGuideIntent(item)
		if intent == "other" || intentCount[intent] >= 2 {
			continue
		}
		item.Status = ZhihuGuideStatusSelected
		selected = append(selected, item)
		used[item.URLHash] = true
		intentCount[intent]++
		if len(selected) >= limit {
			return selected
		}
	}
	for _, item := range pool {
		if used[item.URLHash] {
			continue
		}
		item.Status = ZhihuGuideStatusSelected
		selected = append(selected, item)
		if len(selected) >= limit {
			break
		}
	}
	return selected
}

func buildZhihuGuideLLMInput(topic string, selected []ZhihuGuideCandidate) ZhihuGuideLLMInput {
	input := ZhihuGuideLLMInput{Topic: topic, SelectedCount: len(selected)}
	for _, item := range selected {
		evidence := item.Reasons
		if len(evidence) > 4 {
			evidence = evidence[:4]
		}
		input.Items = append(input.Items, ZhihuGuideLLMArticle{
			Intent:        classifyGuideIntent(item),
			SearchScope:   item.SearchScope,
			Title:         zhihuLLMTitle(item),
			URL:           item.URL,
			AuthorName:    item.AuthorName,
			Summary:       item.Summary,
			ContentBrief:  buildGuideContentBrief(item),
			KeyPoints:     buildGuideKeyPoints(item),
			SourceSignals: buildZhihuSourceSignals(item),
			Score:         item.Score,
			Evidence:      append([]string(nil), evidence...),
		})
	}
	return input
}

func buildZhihuGuideMaterialResult(run ZhihuGuideRun) ZhihuGuideMaterialResult {
	reviewCandidates := run.ReviewPool
	if len(reviewCandidates) > 10 {
		reviewCandidates = reviewCandidates[:10]
	}
	message := fmt.Sprintf("已完成知乎素材获取和过滤：原始 %d 条，去重 %d 条，审核池 %d 条，最终选择 %d 条。",
		run.Stats.RawCount,
		run.Stats.DedupedCount,
		len(run.ReviewPool),
		run.Stats.SelectedCount,
	)
	if len(run.Errors) > 0 {
		message += fmt.Sprintf(" 有 %d 个 query 失败，详见 errors。", len(run.Errors))
	}
	return ZhihuGuideMaterialResult{
		RunID:                run.RunID,
		Topic:                run.Topic,
		QueryCount:           len(run.QueryPlan),
		RawCount:             run.Stats.RawCount,
		ZhihuSearchRawCount:  run.Stats.ZhihuSearchRawCount,
		GlobalSearchRawCount: run.Stats.GlobalSearchRawCount,
		DedupedCount:         run.Stats.DedupedCount,
		ReviewPoolCount:      len(run.ReviewPool),
		SelectedCount:        run.Stats.SelectedCount,
		SelectedForLLM:       run.SelectedForLLM,
		ReviewCandidates:     append([]ZhihuGuideCandidate(nil), reviewCandidates...),
		ReviewDecisions:      BuildZhihuGuideReviewDecisions(run),
		QueryPlan:            run.QueryPlan,
		Errors:               run.Errors,
		Message:              message,
	}
}

func mergeGuideMaterialInput(opts ZhihuGuideRunOptions, in ZhihuGuideMaterialInput) ZhihuGuideRunOptions {
	if in.QueryCount > 0 {
		opts.QueryCount = in.QueryCount
	}
	if in.PerQueryCount > 0 {
		opts.PerQueryCount = in.PerQueryCount
	}
	if in.ReviewPoolSize > 0 {
		opts.ReviewPoolSize = in.ReviewPoolSize
	}
	if in.SelectedArticleCount > 0 {
		opts.SelectedArticleCount = in.SelectedArticleCount
	}
	if in.ArticleOnly != nil {
		opts.ArticleOnly = *in.ArticleOnly
	}
	if len(in.MustKeywords) > 0 {
		opts.MustKeywords = in.MustKeywords
	}
	if len(in.ShouldKeywords) > 0 {
		opts.ShouldKeywords = in.ShouldKeywords
	}
	if len(in.NegativeKeywords) > 0 {
		opts.NegativeKeywords = in.NegativeKeywords
	}
	return normalizeGuideOptions(opts)
}

func computeZhihuGuideStats(rawCount int, items []ZhihuGuideCandidate) ZhihuGuideStats {
	stats := ZhihuGuideStats{RawCount: rawCount, DedupedCount: len(items)}
	for _, item := range items {
		switch item.Status {
		case ZhihuGuideStatusAccepted:
			stats.AcceptedCount++
		case ZhihuGuideStatusReview:
			stats.ReviewCount++
		default:
			stats.RejectedCount++
		}
	}
	return stats
}

func countZhihuGuideRawScopes(items []ZhihuGuideCandidate) (zhihuCount, globalCount int) {
	for _, item := range items {
		switch item.SearchScope {
		case "global_search":
			globalCount++
		default:
			zhihuCount++
		}
	}
	return zhihuCount, globalCount
}

func BuildZhihuGuideRunID(topic string, now time.Time) string {
	slug := regexp.MustCompile(`[^a-zA-Z0-9\p{Han}]+`).ReplaceAllString(topic, "_")
	slug = strings.Trim(slug, "_")
	if len([]rune(slug)) > 24 {
		slug = string([]rune(slug)[:24])
	}
	if slug == "" {
		slug = "zhihu"
	}
	return now.Format("20060102_150405") + "_" + slug
}

func normalizeZhihuURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func isZhihuArticleURL(raw string) bool {
	u := strings.ToLower(raw)
	return strings.Contains(u, "zhuanlan.zhihu.com/p/") || strings.Contains(u, "zhihu.com/p/")
}

func classifyGuideIntent(item ZhihuGuideCandidate) string {
	for _, source := range item.Sources {
		if source.Intent != "" && source.Intent != "other" {
			return source.Intent
		}
	}
	text := strings.ToLower(item.Title + " " + item.Summary)
	switch {
	case strings.Contains(text, "住") || strings.Contains(text, "酒店") || strings.Contains(text, "住宿"):
		return "hotel_area"
	case strings.Contains(text, "交通") || strings.Contains(text, "地铁") || strings.Contains(text, "机场"):
		return "transport"
	case strings.Contains(text, "美食") || strings.Contains(text, "餐厅") || strings.Contains(text, "吃"):
		return "food"
	case strings.Contains(text, "避坑") || strings.Contains(text, "注意") || strings.Contains(text, "坑"):
		return "pitfall"
	case strings.Contains(text, "预算") || strings.Contains(text, "花费") || strings.Contains(text, "费用"):
		return "budget"
	case strings.Contains(text, "路线") || strings.Contains(text, "行程") || strings.Contains(text, "几天"):
		return "itinerary"
	case strings.Contains(text, "景点") || strings.Contains(text, "玩法") || strings.Contains(text, "推荐"):
		return "attraction"
	case strings.Contains(text, "季节") || strings.Contains(text, "樱花") || strings.Contains(text, "冬天") || strings.Contains(text, "夏天"):
		return "season"
	}
	return "other"
}

func zhihuFreshnessScore(editTime int64) float64 {
	if editTime <= 0 {
		return 3
	}
	days := time.Since(time.Unix(editTime, 0)).Hours() / 24
	switch {
	case days <= 30:
		return 15
	case days <= 90:
		return 12
	case days <= 365:
		return 9
	case days <= 1095:
		return 5
	default:
		return 2
	}
}

func readabilityScore(item ZhihuGuideCandidate) float64 {
	score := 0.0
	titleLen := len([]rune(item.Title))
	summaryLen := len([]rune(item.Summary))
	if titleLen >= 8 && titleLen <= 60 {
		score += 2.5
	}
	if summaryLen >= 40 && summaryLen <= 300 {
		score += 2.5
	}
	return score
}

func keywordTokens(s string) []string {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) > 0 {
		return fields
	}
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return []string{strings.TrimSpace(s)}
}

func countContains(text string, needles []string) int {
	count := 0
	for _, needle := range needles {
		needle = strings.ToLower(strings.TrimSpace(needle))
		if needle != "" && strings.Contains(text, needle) {
			count++
		}
	}
	return count
}

func containsAny(text string, needles []string) bool {
	text = strings.ToLower(text)
	for _, needle := range needles {
		needle = strings.ToLower(strings.TrimSpace(needle))
		if needle != "" && strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func appendMissingSources(existing, incoming []ZhihuGuideSource) []ZhihuGuideSource {
	seen := map[string]bool{}
	for _, source := range existing {
		seen[source.Query+"|"+source.Intent+"|"+source.Scope] = true
	}
	for _, source := range incoming {
		key := source.Query + "|" + source.Intent + "|" + source.Scope
		if !seen[key] {
			existing = append(existing, source)
			seen[key] = true
		}
	}
	return existing
}

func roughCandidateStrength(item ZhihuGuideCandidate) float64 {
	return float64(item.VoteUpCount)*2 + float64(item.CommentCount) + float64(len([]rune(item.Summary)))/20
}

func statusRank(status string) int {
	switch status {
	case ZhihuGuideStatusAccepted:
		return 0
	case ZhihuGuideStatusReview:
		return 1
	default:
		return 2
	}
}

func normalizeDecision(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "selected", "approved", "approve", "yes", "y":
		return ZhihuGuideStatusSelected
	case "rejected", "reject", "no", "n":
		return ZhihuGuideStatusRejected
	default:
		return "pending"
	}
}

func selectedCandidatesFromRun(run ZhihuGuideRun) []ZhihuGuideCandidate {
	byURL := map[string]ZhihuGuideCandidate{}
	for _, item := range run.ReviewPool {
		byURL[item.URL] = item
	}
	out := make([]ZhihuGuideCandidate, 0, len(run.SelectedForLLM.Items))
	for _, item := range run.SelectedForLLM.Items {
		candidate := byURL[item.URL]
		if candidate.URL == "" {
			candidate = ZhihuGuideCandidate{
				Title:      item.Title,
				URL:        item.URL,
				AuthorName: item.AuthorName,
				Summary:    item.Summary,
				Score:      item.Score,
				Status:     ZhihuGuideStatusSelected,
			}
		}
		candidate.Status = ZhihuGuideStatusSelected
		out = append(out, candidate)
	}
	return out
}

func writeGuideCandidateTable(b *strings.Builder, items []ZhihuGuideCandidate) {
	if len(items) == 0 {
		b.WriteString("暂无候选。\n\n")
		return
	}
	b.WriteString("| # | 状态 | 分数 | 维度 | 标题 | 作者 | 赞同 | 评论 | 原因 |\n")
	b.WriteString("|---:|---|---:|---|---|---|---:|---:|---|\n")
	for i, item := range items {
		reasons := item.Reasons
		if len(reasons) > 3 {
			reasons = reasons[:3]
		}
		title := item.Title
		if item.URL != "" {
			title = fmt.Sprintf("[%s](%s)", escapeGuideMarkdownCell(item.Title), item.URL)
		}
		b.WriteString(fmt.Sprintf(
			"| %d | `%s` | %.1f | `%s` | %s | %s | %d | %d | %s |\n",
			i+1,
			escapeGuideMarkdownCell(item.Status),
			item.Score,
			escapeGuideMarkdownCell(firstGuideIntent(item)),
			title,
			escapeGuideMarkdownCell(item.AuthorName),
			item.VoteUpCount,
			item.CommentCount,
			escapeGuideMarkdownCell(strings.Join(reasons, "; ")),
		))
	}
	b.WriteString("\n")
}

func firstGuideIntent(item ZhihuGuideCandidate) string {
	if item.SourceIntent != "" {
		return item.SourceIntent
	}
	if len(item.Sources) > 0 {
		return item.Sources[0].Intent
	}
	return "other"
}

func zhihuLLMTitle(item ZhihuGuideCandidate) string {
	if item.SearchScope != "global_search" {
		return item.Title
	}
	return "[global_search] " + item.Title
}

func buildGuideContentBrief(item ZhihuGuideCandidate) string {
	parts := []string{}
	if strings.TrimSpace(item.Title) != "" {
		parts = append(parts, "Title: "+strings.TrimSpace(item.Title))
	}
	if strings.TrimSpace(item.Summary) != "" {
		parts = append(parts, "Content: "+strings.TrimSpace(item.Summary))
	}
	return truncateRunes(compactSpace(strings.Join(parts, " | ")), 900)
}

func buildGuideKeyPoints(item ZhihuGuideCandidate) []string {
	return firstTextFragments(item.Summary, 4, 160)
}

func buildZhihuSourceSignals(item ZhihuGuideCandidate) []string {
	signals := []string{}
	if item.SearchScope != "" {
		signals = append(signals, "source="+item.SearchScope)
	}
	if item.VoteUpCount > 0 {
		signals = append(signals, fmt.Sprintf("votes=%d", item.VoteUpCount))
	}
	if item.CommentCount > 0 {
		signals = append(signals, fmt.Sprintf("comments=%d", item.CommentCount))
	}
	if item.IsArticle {
		signals = append(signals, "type=article")
	} else {
		signals = append(signals, "type=answer_or_web")
	}
	if len(item.Sources) > 1 {
		signals = append(signals, fmt.Sprintf("matched_queries=%d", len(item.Sources)))
	}
	return signals
}

func firstTextFragments(text string, limit int, maxRunes int) []string {
	text = strings.TrimSpace(text)
	if text == "" || limit <= 0 {
		return nil
	}
	splitter := func(r rune) bool {
		return r == '。' || r == '！' || r == '？' || r == '；' || r == ';' || r == '\n'
	}
	rawParts := strings.FieldsFunc(text, splitter)
	out := make([]string, 0, limit)
	for _, part := range rawParts {
		part = compactSpace(part)
		if part == "" {
			continue
		}
		out = append(out, truncateRunes(part, maxRunes))
		if len(out) >= limit {
			return out
		}
	}
	if len(out) == 0 {
		out = append(out, truncateRunes(compactSpace(text), maxRunes))
	}
	return out
}

func truncateRunes(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return strings.TrimSpace(string(runes[:maxRunes])) + "..."
}

func escapeGuideMarkdownCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func hashString(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func compactSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
