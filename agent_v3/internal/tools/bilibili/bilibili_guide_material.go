package bilibili

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

	"agent_v3/internal/config"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

const (
	BilibiliGuideStatusAccepted = "accepted"
	BilibiliGuideStatusReview   = "review"
	BilibiliGuideStatusRejected = "rejected"
	BilibiliGuideStatusSelected = "selected"
)

type BilibiliGuideQuery struct {
	Query  string `json:"query"`
	Intent string `json:"intent"`
}

type BilibiliGuideSource struct {
	Query  string `json:"query"`
	Intent string `json:"intent"`
}

type BilibiliGuideCandidate struct {
	Title         string                `json:"title"`
	URL           string                `json:"url"`
	BVID          string                `json:"bvid"`
	AuthorName    string                `json:"author_name"`
	Summary       string                `json:"summary"`
	ViewCount     int64                 `json:"view_count"`
	DanmakuCount  int64                 `json:"danmaku_count"`
	LikeCount     int64                 `json:"like_count"`
	FavoriteCount int64                 `json:"favorite_count"`
	PublishTime   int64                 `json:"publish_time"`
	Duration      string                `json:"duration"`
	CoverURL      string                `json:"cover_url"`
	Sources       []BilibiliGuideSource `json:"sources"`
	SourceQuery   string                `json:"source_query"`
	SourceIntent  string                `json:"source_intent"`
	URLHash       string                `json:"url_hash"`
	Score         float64               `json:"score"`
	Status        string                `json:"status"`
	Reasons       []string              `json:"reasons"`
	Raw           *BilibiliSearchItem   `json:"raw,omitempty"`
}

type BilibiliGuideStats struct {
	RawCount      int `json:"raw_count"`
	DedupedCount  int `json:"deduped_count"`
	AcceptedCount int `json:"accepted_count"`
	ReviewCount   int `json:"review_count"`
	RejectedCount int `json:"rejected_count"`
	SelectedCount int `json:"selected_count"`
}

type BilibiliGuideRun struct {
	RunID              string                    `json:"run_id"`
	Topic              string                    `json:"topic"`
	QueryPlan          []BilibiliGuideQuery      `json:"query_plan"`
	RawCandidates      []BilibiliGuideCandidate  `json:"raw_candidates"`
	FilteredCandidates []BilibiliGuideCandidate  `json:"filtered_candidates"`
	ReviewPool         []BilibiliGuideCandidate  `json:"review_pool"`
	SelectedForLLM     BilibiliGuideLLMInput     `json:"selected_for_llm"`
	Stats              BilibiliGuideStats        `json:"stats"`
	Options            BilibiliGuideRunOptions   `json:"options"`
	Errors             []BilibiliGuideQueryError `json:"errors,omitempty"`
}

type BilibiliGuideLLMInput struct {
	Topic         string                  `json:"topic"`
	SelectedCount int                     `json:"selected_count"`
	Items         []BilibiliGuideLLMVideo `json:"items"`
}

type BilibiliGuideLLMVideo struct {
	Intent        string   `json:"intent"`
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	BVID          string   `json:"bvid"`
	AuthorName    string   `json:"author_name"`
	Summary       string   `json:"summary"`
	ContentBrief  string   `json:"content_brief"`
	KeyPoints     []string `json:"key_points"`
	SourceSignals []string `json:"source_signals"`
	Score         float64  `json:"score"`
	Evidence      []string `json:"evidence"`
}

type BilibiliGuideQueryError struct {
	Query string `json:"query"`
	Error string `json:"error"`
}

type BilibiliGuideRunOptions struct {
	QueryCount         int      `json:"query_count"`
	PerQueryCount      int      `json:"per_query_count"`
	ReviewPoolSize     int      `json:"review_pool_size"`
	SelectedVideoCount int      `json:"selected_video_count"`
	AcceptScore        float64  `json:"accept_score"`
	ReviewScore        float64  `json:"review_score"`
	MinSummaryChars    int      `json:"min_summary_chars"`
	MinViewCount       int64    `json:"min_view_count"`
	MaxAgeDays         int      `json:"max_age_days"`
	MustKeywords       []string `json:"must_keywords"`
	ShouldKeywords     []string `json:"should_keywords"`
	NegativeKeywords   []string `json:"negative_keywords"`
	BlockedAuthors     []string `json:"blocked_authors"`
	TrustedAuthors     []string `json:"trusted_authors"`
}

type bilibiliGuideSearcher interface {
	Search(context.Context, BilibiliSearchInput) (BilibiliSearchResult, error)
}

type BilibiliGuideMaterialInput struct {
	Topic              string   `json:"topic" jsonschema:"description=攻略主题，例如 成都旅游攻略、重庆美食、杭州景点"`
	QueryCount         int      `json:"query_count,omitempty" jsonschema:"description=搜索词数量，默认使用配置，建议 8-12"`
	PerQueryCount      int      `json:"per_query_count,omitempty" jsonschema:"description=每个搜索词返回数量，范围 1-20，默认 10"`
	ReviewPoolSize     int      `json:"review_pool_size,omitempty" jsonschema:"description=进入人工审核池的候选数量，默认 30"`
	SelectedVideoCount int      `json:"selected_video_count,omitempty" jsonschema:"description=最终进入大模型的素材视频数量，默认 12"`
	MustKeywords       []string `json:"must_keywords,omitempty" jsonschema:"description=必须相关的核心关键词，不传则从 topic 推断"`
	ShouldKeywords     []string `json:"should_keywords,omitempty" jsonschema:"description=加分关键词，例如 美食、景点、路线、避坑"`
	NegativeKeywords   []string `json:"negative_keywords,omitempty" jsonschema:"description=负面关键词，命中后拒绝"`
}

type BilibiliGuideMaterialResult struct {
	RunID            string                    `json:"run_id"`
	Topic            string                    `json:"topic"`
	QueryCount       int                       `json:"query_count"`
	RawCount         int                       `json:"raw_count"`
	DedupedCount     int                       `json:"deduped_count"`
	ReviewPoolCount  int                       `json:"review_pool_count"`
	SelectedCount    int                       `json:"selected_count"`
	SelectedForLLM   BilibiliGuideLLMInput     `json:"selected_for_llm"`
	ReviewCandidates []BilibiliGuideCandidate  `json:"review_candidates,omitempty"`
	QueryPlan        []BilibiliGuideQuery      `json:"query_plan"`
	Errors           []BilibiliGuideQueryError `json:"errors,omitempty"`
	Message          string                    `json:"message"`
}

func CollectBilibiliGuideMaterial(ctx context.Context, cfg config.BilibiliConfig, topic string) (BilibiliGuideRun, error) {
	runtime := newBilibiliRuntime(cfg)
	return collectBilibiliGuideMaterial(ctx, runtime, topic, bilibiliGuideOptionsFromConfig(cfg.GuideMaterial))
}

func newBilibiliGuideMaterialTool(runtime *bilibiliRuntime) tool.Tool {
	return function.NewFunctionTool(runtime.GuideMaterial,
		function.WithName("bilibili_guide_material"),
		function.WithDescription("围绕旅游攻略主题多轮搜索 B 站视频，自动完成候选视频获取、去重、过滤、评分、选择，并生成可供大模型使用的 selected_for_llm 素材。"),
	)
}

func (r *bilibiliRuntime) GuideMaterial(ctx context.Context, in BilibiliGuideMaterialInput) (BilibiliGuideMaterialResult, error) {
	opts := bilibiliGuideOptionsFromConfig(r.cfg.GuideMaterial)
	opts = mergeBilibiliGuideMaterialInput(opts, in)
	run, err := collectBilibiliGuideMaterial(ctx, r, in.Topic, opts)
	if err != nil {
		return BilibiliGuideMaterialResult{}, err
	}
	return buildBilibiliGuideMaterialResult(run), nil
}

func GenerateBilibiliGuideQueryPlan(topic string, queryCount int) []BilibiliGuideQuery {
	topic = strings.TrimSpace(topic)
	templates := []BilibiliGuideQuery{
		{Query: topic, Intent: "overview"},
		{Query: topic + " 攻略", Intent: "overview"},
		{Query: topic + " vlog", Intent: "overview"},
		{Query: topic + " 自由行", Intent: "itinerary"},
		{Query: topic + " 路线", Intent: "itinerary"},
		{Query: topic + " 景点", Intent: "attraction"},
		{Query: topic + " 美食", Intent: "food"},
		{Query: topic + " 住宿", Intent: "hotel_area"},
		{Query: topic + " 交通", Intent: "transport"},
		{Query: topic + " 避坑", Intent: "pitfall"},
		{Query: topic + " 预算", Intent: "budget"},
		{Query: topic + " 几天合适", Intent: "itinerary"},
		{Query: topic + " 第一次", Intent: "overview"},
	}
	seen := map[string]bool{}
	out := make([]BilibiliGuideQuery, 0, queryCount)
	for _, item := range templates {
		query := compactBilibiliSpace(item.Query)
		if query == "" || seen[query] {
			continue
		}
		seen[query] = true
		out = append(out, BilibiliGuideQuery{Query: query, Intent: item.Intent})
		if len(out) >= queryCount {
			break
		}
	}
	return out
}

func collectBilibiliGuideMaterial(ctx context.Context, searcher bilibiliGuideSearcher, topic string, opts BilibiliGuideRunOptions) (BilibiliGuideRun, error) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return BilibiliGuideRun{}, fmt.Errorf("topic cannot be empty")
	}
	opts = normalizeBilibiliGuideOptions(opts)
	queryPlan := GenerateBilibiliGuideQueryPlan(topic, opts.QueryCount)
	run := BilibiliGuideRun{
		RunID:     BuildBilibiliGuideRunID(topic, time.Now()),
		Topic:     topic,
		QueryPlan: queryPlan,
		Options:   opts,
	}

	for _, query := range queryPlan {
		result, err := searcher.Search(ctx, BilibiliSearchInput{Query: query.Query, Count: opts.PerQueryCount})
		if err != nil {
			run.Errors = append(run.Errors, BilibiliGuideQueryError{Query: query.Query, Error: err.Error()})
			continue
		}
		if result.Code != 0 {
			run.Errors = append(run.Errors, BilibiliGuideQueryError{Query: query.Query, Error: result.Message})
			continue
		}
		for _, item := range result.Items {
			run.RawCandidates = append(run.RawCandidates, bilibiliCandidateFromSearchItem(item, query))
		}
	}

	run.Stats.RawCount = len(run.RawCandidates)
	run.FilteredCandidates = scoreBilibiliGuideCandidates(dedupBilibiliGuideCandidates(run.RawCandidates), topic, opts)
	run.Stats = computeBilibiliGuideStats(run.Stats.RawCount, run.FilteredCandidates)
	run.ReviewPool = selectBilibiliGuideReviewPool(run.FilteredCandidates, opts.ReviewPoolSize)
	selected := selectBilibiliGuideVideos(run.ReviewPool, opts.SelectedVideoCount)
	run.SelectedForLLM = buildBilibiliGuideLLMInput(topic, selected)
	run.Stats.SelectedCount = len(run.SelectedForLLM.Items)
	return run, nil
}

func bilibiliGuideOptionsFromConfig(cfg config.BilibiliGuideMaterialConfig) BilibiliGuideRunOptions {
	cfg = cfg.WithDefaults()
	return BilibiliGuideRunOptions{
		QueryCount:         cfg.QueryCount,
		PerQueryCount:      cfg.PerQueryCount,
		ReviewPoolSize:     cfg.ReviewPoolSize,
		SelectedVideoCount: cfg.SelectedVideoCount,
		AcceptScore:        cfg.AcceptScore,
		ReviewScore:        cfg.ReviewScore,
		MinSummaryChars:    cfg.MinSummaryChars,
		MinViewCount:       cfg.MinViewCount,
		MaxAgeDays:         cfg.MaxAgeDays,
		MustKeywords:       cfg.MustKeywords,
		ShouldKeywords:     cfg.ShouldKeywords,
		NegativeKeywords:   cfg.NegativeKeywords,
		BlockedAuthors:     cfg.BlockedAuthors,
		TrustedAuthors:     cfg.TrustedAuthors,
	}
}

func normalizeBilibiliGuideOptions(opts BilibiliGuideRunOptions) BilibiliGuideRunOptions {
	return bilibiliGuideOptionsFromConfig(config.BilibiliGuideMaterialConfig{
		QueryCount:         opts.QueryCount,
		PerQueryCount:      opts.PerQueryCount,
		ReviewPoolSize:     opts.ReviewPoolSize,
		SelectedVideoCount: opts.SelectedVideoCount,
		AcceptScore:        opts.AcceptScore,
		ReviewScore:        opts.ReviewScore,
		MinSummaryChars:    opts.MinSummaryChars,
		MinViewCount:       opts.MinViewCount,
		MaxAgeDays:         opts.MaxAgeDays,
		MustKeywords:       opts.MustKeywords,
		ShouldKeywords:     opts.ShouldKeywords,
		NegativeKeywords:   opts.NegativeKeywords,
		BlockedAuthors:     opts.BlockedAuthors,
		TrustedAuthors:     opts.TrustedAuthors,
	})
}

func mergeBilibiliGuideMaterialInput(opts BilibiliGuideRunOptions, in BilibiliGuideMaterialInput) BilibiliGuideRunOptions {
	if in.QueryCount > 0 {
		opts.QueryCount = in.QueryCount
	}
	if in.PerQueryCount > 0 {
		opts.PerQueryCount = in.PerQueryCount
	}
	if in.ReviewPoolSize > 0 {
		opts.ReviewPoolSize = in.ReviewPoolSize
	}
	if in.SelectedVideoCount > 0 {
		opts.SelectedVideoCount = in.SelectedVideoCount
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
	return normalizeBilibiliGuideOptions(opts)
}

func bilibiliCandidateFromSearchItem(item BilibiliSearchItem, source BilibiliGuideQuery) BilibiliGuideCandidate {
	normalizedURL := normalizeBilibiliURL(item.URL)
	if normalizedURL == "" && item.BVID != "" {
		normalizedURL = "https://www.bilibili.com/video/" + item.BVID
	}
	raw := item
	return BilibiliGuideCandidate{
		Title:         strings.TrimSpace(item.Title),
		URL:           strings.TrimSpace(item.URL),
		BVID:          strings.TrimSpace(item.BVID),
		AuthorName:    strings.TrimSpace(item.AuthorName),
		Summary:       strings.TrimSpace(item.Summary),
		ViewCount:     item.ViewCount,
		DanmakuCount:  item.DanmakuCount,
		LikeCount:     item.LikeCount,
		FavoriteCount: item.FavoriteCount,
		PublishTime:   item.PublishTime,
		Duration:      item.Duration,
		CoverURL:      item.CoverURL,
		Sources:       []BilibiliGuideSource{{Query: source.Query, Intent: source.Intent}},
		SourceQuery:   source.Query,
		SourceIntent:  source.Intent,
		URLHash:       hashBilibiliString(normalizedURL),
		Raw:           &raw,
	}
}

func dedupBilibiliGuideCandidates(items []BilibiliGuideCandidate) []BilibiliGuideCandidate {
	byURL := map[string]int{}
	out := make([]BilibiliGuideCandidate, 0, len(items))
	for _, item := range items {
		key := item.URLHash
		if key == "" {
			key = hashBilibiliString(normalizeBilibiliURL(item.URL))
		}
		if idx, ok := byURL[key]; ok {
			existing := &out[idx]
			existing.Sources = appendMissingBilibiliSources(existing.Sources, item.Sources)
			if roughBilibiliCandidateStrength(item) > roughBilibiliCandidateStrength(*existing) {
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

func scoreBilibiliGuideCandidates(items []BilibiliGuideCandidate, topic string, opts BilibiliGuideRunOptions) []BilibiliGuideCandidate {
	out := make([]BilibiliGuideCandidate, 0, len(items))
	for _, item := range items {
		item.Score, item.Status, item.Reasons = scoreBilibiliGuideCandidate(item, topic, opts)
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return bilibiliStatusRank(out[i].Status) < bilibiliStatusRank(out[j].Status)
		}
		return out[i].Score > out[j].Score
	})
	return out
}

func scoreBilibiliGuideCandidate(item BilibiliGuideCandidate, topic string, opts BilibiliGuideRunOptions) (float64, string, []string) {
	reasons := []string{}
	if item.Title == "" {
		return 0, BilibiliGuideStatusRejected, []string{"标题为空"}
	}
	if item.URL == "" && item.BVID == "" {
		return 0, BilibiliGuideStatusRejected, []string{"URL 和 BVID 为空"}
	}
	if containsAnyBilibili(item.AuthorName, opts.BlockedAuthors) {
		return 0, BilibiliGuideStatusRejected, []string{"作者命中黑名单"}
	}
	text := strings.ToLower(item.Title + " " + item.Summary)
	if containsAnyBilibili(text, opts.NegativeKeywords) {
		return 0, BilibiliGuideStatusRejected, []string{"命中负面关键词"}
	}

	score := 0.0
	mustKeywords := append([]string{}, opts.MustKeywords...)
	if len(mustKeywords) == 0 {
		mustKeywords = bilibiliKeywordTokens(topic)
	}
	matchedMust := countContainsBilibili(text, mustKeywords)
	if matchedMust == 0 && len(mustKeywords) > 0 {
		reasons = append(reasons, "核心词未命中")
	} else {
		part := math.Min(30, float64(matchedMust)*12)
		score += part
		reasons = append(reasons, fmt.Sprintf("主题相关性 +%.1f", part))
	}
	shouldMatched := countContainsBilibili(text, opts.ShouldKeywords)
	if shouldMatched > 0 {
		part := math.Min(10, float64(shouldMatched)*2)
		score += part
		reasons = append(reasons, fmt.Sprintf("扩展关键词 +%.1f", part))
	}

	intent := classifyBilibiliGuideIntent(item)
	if intent != "other" {
		score += 22
		reasons = append(reasons, "攻略维度 "+intent+" +22.0")
	} else {
		score += 6
		reasons = append(reasons, "攻略维度不明确 +6.0")
	}

	engagement := math.Min(22,
		math.Log10(float64(maxBilibiliInt64(item.ViewCount, 0)+1))*6+
			math.Log10(float64(maxBilibiliInt64(item.LikeCount, 0)+1))*5+
			math.Log10(float64(maxBilibiliInt64(item.FavoriteCount, 0)+1))*4+
			math.Log10(float64(maxBilibiliInt64(item.DanmakuCount, 0)+1))*2,
	)
	score += engagement
	reasons = append(reasons, fmt.Sprintf("互动质量 +%.1f", engagement))

	freshness := bilibiliFreshnessScore(item.PublishTime, opts.MaxAgeDays)
	score += freshness
	reasons = append(reasons, fmt.Sprintf("新鲜度 +%.1f", freshness))

	readability := bilibiliReadabilityScore(item)
	score += readability
	reasons = append(reasons, fmt.Sprintf("可读性 +%.1f", readability))
	if containsAnyBilibili(item.AuthorName, opts.TrustedAuthors) {
		score += 5
		reasons = append(reasons, "作者命中白名单 +5.0")
	}

	status := BilibiliGuideStatusAccepted
	if len([]rune(item.Summary)) < opts.MinSummaryChars {
		status = BilibiliGuideStatusReview
		reasons = append(reasons, "简介过短，需人工复核")
	}
	if item.ViewCount < opts.MinViewCount {
		status = BilibiliGuideStatusReview
		reasons = append(reasons, "播放量低，需人工复核")
	}
	if matchedMust == 0 && len(mustKeywords) > 0 {
		status = BilibiliGuideStatusReview
	}
	if score < opts.ReviewScore {
		status = BilibiliGuideStatusRejected
	} else if score < opts.AcceptScore {
		status = BilibiliGuideStatusReview
	}
	return math.Round(score*10) / 10, status, reasons
}

func selectBilibiliGuideReviewPool(items []BilibiliGuideCandidate, size int) []BilibiliGuideCandidate {
	if size <= 0 {
		return nil
	}
	pool := make([]BilibiliGuideCandidate, 0, size)
	for _, item := range items {
		if item.Status == BilibiliGuideStatusRejected {
			continue
		}
		pool = append(pool, item)
		if len(pool) >= size {
			break
		}
	}
	return pool
}

func selectBilibiliGuideVideos(pool []BilibiliGuideCandidate, limit int) []BilibiliGuideCandidate {
	if limit <= 0 {
		return nil
	}
	selected := make([]BilibiliGuideCandidate, 0, limit)
	used := map[string]bool{}
	intentCount := map[string]int{}
	for _, item := range pool {
		intent := classifyBilibiliGuideIntent(item)
		if intent == "other" || intentCount[intent] >= 2 {
			continue
		}
		item.Status = BilibiliGuideStatusSelected
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
		item.Status = BilibiliGuideStatusSelected
		selected = append(selected, item)
		if len(selected) >= limit {
			break
		}
	}
	return selected
}

func buildBilibiliGuideLLMInput(topic string, selected []BilibiliGuideCandidate) BilibiliGuideLLMInput {
	input := BilibiliGuideLLMInput{Topic: topic, SelectedCount: len(selected)}
	for _, item := range selected {
		evidence := item.Reasons
		if len(evidence) > 4 {
			evidence = evidence[:4]
		}
		input.Items = append(input.Items, BilibiliGuideLLMVideo{
			Intent:        classifyBilibiliGuideIntent(item),
			Title:         item.Title,
			URL:           item.URL,
			BVID:          item.BVID,
			AuthorName:    item.AuthorName,
			Summary:       item.Summary,
			ContentBrief:  buildBilibiliContentBrief(item),
			KeyPoints:     buildBilibiliKeyPoints(item),
			SourceSignals: buildBilibiliSourceSignals(item),
			Score:         item.Score,
			Evidence:      append([]string(nil), evidence...),
		})
	}
	return input
}

func buildBilibiliGuideMaterialResult(run BilibiliGuideRun) BilibiliGuideMaterialResult {
	reviewCandidates := run.ReviewPool
	if len(reviewCandidates) > 10 {
		reviewCandidates = reviewCandidates[:10]
	}
	message := fmt.Sprintf("已完成 B 站旅游素材获取和过滤：原始 %d 条，去重 %d 条，审核池 %d 条，最终选择 %d 条。",
		run.Stats.RawCount,
		run.Stats.DedupedCount,
		len(run.ReviewPool),
		run.Stats.SelectedCount,
	)
	if len(run.Errors) > 0 {
		message += fmt.Sprintf(" 有 %d 个 query 失败，详见 errors。", len(run.Errors))
	}
	return BilibiliGuideMaterialResult{
		RunID:            run.RunID,
		Topic:            run.Topic,
		QueryCount:       len(run.QueryPlan),
		RawCount:         run.Stats.RawCount,
		DedupedCount:     run.Stats.DedupedCount,
		ReviewPoolCount:  len(run.ReviewPool),
		SelectedCount:    run.Stats.SelectedCount,
		SelectedForLLM:   run.SelectedForLLM,
		ReviewCandidates: append([]BilibiliGuideCandidate(nil), reviewCandidates...),
		QueryPlan:        run.QueryPlan,
		Errors:           run.Errors,
		Message:          message,
	}
}

func computeBilibiliGuideStats(rawCount int, items []BilibiliGuideCandidate) BilibiliGuideStats {
	stats := BilibiliGuideStats{RawCount: rawCount, DedupedCount: len(items)}
	for _, item := range items {
		switch item.Status {
		case BilibiliGuideStatusAccepted:
			stats.AcceptedCount++
		case BilibiliGuideStatusReview:
			stats.ReviewCount++
		default:
			stats.RejectedCount++
		}
	}
	return stats
}

func BuildBilibiliGuideRunID(topic string, now time.Time) string {
	slug := regexp.MustCompile(`[^a-zA-Z0-9\p{Han}]+`).ReplaceAllString(topic, "_")
	slug = strings.Trim(slug, "_")
	if len([]rune(slug)) > 24 {
		slug = string([]rune(slug)[:24])
	}
	if slug == "" {
		slug = "bilibili"
	}
	return now.Format("20060102_150405") + "_" + slug
}

func normalizeBilibiliURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func classifyBilibiliGuideIntent(item BilibiliGuideCandidate) string {
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
	}
	return "other"
}

func bilibiliFreshnessScore(publishTime int64, maxAgeDays int) float64 {
	if publishTime <= 0 {
		return 3
	}
	days := time.Since(time.Unix(publishTime, 0)).Hours() / 24
	switch {
	case maxAgeDays > 0 && days > float64(maxAgeDays):
		return 1
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

func bilibiliReadabilityScore(item BilibiliGuideCandidate) float64 {
	score := 0.0
	titleLen := len([]rune(item.Title))
	summaryLen := len([]rune(item.Summary))
	if titleLen >= 6 && titleLen <= 80 {
		score += 3
	}
	if summaryLen >= 20 && summaryLen <= 500 {
		score += 3
	}
	return score
}

func bilibiliKeywordTokens(s string) []string {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) > 0 {
		return fields
	}
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return []string{strings.TrimSpace(s)}
}

func countContainsBilibili(text string, needles []string) int {
	count := 0
	for _, needle := range needles {
		needle = strings.ToLower(strings.TrimSpace(needle))
		if needle != "" && strings.Contains(text, needle) {
			count++
		}
	}
	return count
}

func containsAnyBilibili(text string, needles []string) bool {
	text = strings.ToLower(text)
	for _, needle := range needles {
		needle = strings.ToLower(strings.TrimSpace(needle))
		if needle != "" && strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func appendMissingBilibiliSources(existing, incoming []BilibiliGuideSource) []BilibiliGuideSource {
	seen := map[string]bool{}
	for _, source := range existing {
		seen[source.Query+"|"+source.Intent] = true
	}
	for _, source := range incoming {
		key := source.Query + "|" + source.Intent
		if !seen[key] {
			existing = append(existing, source)
			seen[key] = true
		}
	}
	return existing
}

func roughBilibiliCandidateStrength(item BilibiliGuideCandidate) float64 {
	return float64(item.ViewCount) + float64(item.LikeCount)*3 + float64(item.FavoriteCount)*4 + float64(len([]rune(item.Summary)))/20
}

func bilibiliStatusRank(status string) int {
	switch status {
	case BilibiliGuideStatusAccepted:
		return 0
	case BilibiliGuideStatusReview:
		return 1
	default:
		return 2
	}
}

func hashBilibiliString(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func compactBilibiliSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func maxBilibiliInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func buildBilibiliContentBrief(item BilibiliGuideCandidate) string {
	parts := []string{}
	if strings.TrimSpace(item.Title) != "" {
		parts = append(parts, "Title: "+strings.TrimSpace(item.Title))
	}
	if strings.TrimSpace(item.Summary) != "" {
		parts = append(parts, "Content: "+strings.TrimSpace(item.Summary))
	}
	if strings.TrimSpace(item.Duration) != "" {
		parts = append(parts, "Duration: "+strings.TrimSpace(item.Duration))
	}
	return truncateBilibiliRunes(compactBilibiliSpace(strings.Join(parts, " | ")), 900)
}

func buildBilibiliKeyPoints(item BilibiliGuideCandidate) []string {
	return firstBilibiliTextFragments(item.Summary, 4, 160)
}

func buildBilibiliSourceSignals(item BilibiliGuideCandidate) []string {
	signals := []string{}
	if item.ViewCount > 0 {
		signals = append(signals, fmt.Sprintf("views=%d", item.ViewCount))
	}
	if item.LikeCount > 0 {
		signals = append(signals, fmt.Sprintf("likes=%d", item.LikeCount))
	}
	if item.FavoriteCount > 0 {
		signals = append(signals, fmt.Sprintf("favorites=%d", item.FavoriteCount))
	}
	if item.DanmakuCount > 0 {
		signals = append(signals, fmt.Sprintf("danmaku=%d", item.DanmakuCount))
	}
	if strings.TrimSpace(item.Duration) != "" {
		signals = append(signals, "duration="+strings.TrimSpace(item.Duration))
	}
	if len(item.Sources) > 1 {
		signals = append(signals, fmt.Sprintf("matched_queries=%d", len(item.Sources)))
	}
	return signals
}

func firstBilibiliTextFragments(text string, limit int, maxRunes int) []string {
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
		part = compactBilibiliSpace(part)
		if part == "" {
			continue
		}
		out = append(out, truncateBilibiliRunes(part, maxRunes))
		if len(out) >= limit {
			return out
		}
	}
	if len(out) == 0 {
		out = append(out, truncateBilibiliRunes(compactBilibiliSpace(text), maxRunes))
	}
	return out
}

func truncateBilibiliRunes(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return strings.TrimSpace(string(runes[:maxRunes])) + "..."
}
