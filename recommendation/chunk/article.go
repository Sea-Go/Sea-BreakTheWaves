package chunk

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	types "sea/type"
)

type Article = types.Article
type Section = types.Section
type Block = types.Block
type Chunk = types.Chunk
type SplitResult = types.SplitResult

func SplitArticle(a Article, maxTokens int, overlapTokens int, keywordTopK int) (SplitResult, error) {
	a.ArticleID = NormalizeArticleID(a.ArticleID, a)
	if strings.TrimSpace(a.Title) == "" {
		return SplitResult{}, errors.New("title cannot be empty")
	}

	keywords := DetectKeywords(a, keywordTopK)
	keywordScore := KeywordCoverageScore(len(keywords))
	coarseRawText := buildCoarseRawText(a, keywords)
	coarseIntro := buildCoarseIntroFallback(a, keywords)
	coarseText := ComposeCoarseText(coarseRawText, coarseIntro, keywordScore)

	var fineChunks []Chunk
	var imageChunks []Chunk
	chunkSeq := 0

	if strings.TrimSpace(a.Cover) != "" {
		chunkSeq++
		imageChunks = append(imageChunks, buildImageChunk(a, chunkSeq, "cover", []string{strings.TrimSpace(a.Cover)}))
	}

	for _, sec := range a.Sections {
		textBody := buildSectionText(sec)
		if textBody != "" {
			parts := SplitByTokenBudget(textBody, maxTokens, overlapTokens)
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				chunkSeq++
				fineChunks = append(fineChunks, Chunk{
					ChunkID:     BuildChunkID(a.ArticleID, chunkSeq, sec.H2, part),
					ArticleID:   a.ArticleID,
					H2:          strings.TrimSpace(sec.H2),
					Content:     part,
					Tokens:      ApproxTokenCount(part),
					ContentType: "text",
				})
			}
		}

		for _, urls := range collectSectionImageGroups(sec) {
			chunkSeq++
			imageChunks = append(imageChunks, buildImageChunk(a, chunkSeq, sec.H2, urls))
		}
	}

	return SplitResult{
		CoarseText:    coarseText,
		CoarseRawText: coarseRawText,
		CoarseIntro:   coarseIntro,
		KeywordScore:  keywordScore,
		FineChunks:    fineChunks,
		ImageChunks:   imageChunks,
		Keywords:      keywords,
	}, nil
}

func buildCoarseRawText(a Article, keywords []string) string {
	h2s := collectH2(a)
	parts := []string{"title: " + strings.TrimSpace(a.Title)}
	if len(a.TypeTags) > 0 {
		parts = append(parts, "type_tags: "+strings.Join(uniqueStrings(a.TypeTags), ", "))
	}
	if len(keywords) > 0 {
		parts = append(parts, "keywords: "+strings.Join(keywords, ", "))
	}
	if len(h2s) > 0 {
		parts = append(parts, "headings: "+strings.Join(h2s, " | "))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func buildCoarseIntroFallback(a Article, keywords []string) string {
	parts := make([]string, 0, 4)
	if title := strings.TrimSpace(a.Title); title != "" {
		parts = append(parts, "This article focuses on \""+title+"\".")
	}
	if len(a.TypeTags) > 0 {
		parts = append(parts, "Its primary types are "+strings.Join(uniqueStrings(a.TypeTags), ", ")+".")
	}
	if len(keywords) > 0 {
		parts = append(parts, "Important keywords include "+strings.Join(limitStrings(keywords, 6), ", ")+".")
	}
	if h2s := collectH2(a); len(h2s) > 0 {
		parts = append(parts, "Key sections cover "+strings.Join(limitStrings(h2s, 4), ", ")+".")
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func ComposeCoarseText(rawText, intro string, keywordScore float32) string {
	parts := make([]string, 0, 3)
	if rawText = strings.TrimSpace(rawText); rawText != "" {
		parts = append(parts, rawText)
	}
	if intro = strings.TrimSpace(intro); intro != "" {
		parts = append(parts, "intro: "+intro)
	}
	parts = append(parts, "keyword_score: "+strconv.FormatFloat(float64(keywordScore), 'f', 1, 32))
	return strings.Join(parts, "\n")
}

func buildSectionText(sec Section) string {
	var b strings.Builder
	if strings.TrimSpace(sec.H2) != "" {
		b.WriteString("heading: ")
		b.WriteString(strings.TrimSpace(sec.H2))
		b.WriteString("\n")
	}
	for _, blk := range sec.Blocks {
		if strings.EqualFold(strings.TrimSpace(blk.Type), "image") {
			continue
		}
		if strings.TrimSpace(blk.Text) == "" {
			continue
		}
		b.WriteString(strings.TrimSpace(blk.Text))
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func collectSectionImageGroups(sec Section) [][]string {
	var groups [][]string
	var current []string

	flush := func() {
		if len(current) == 0 {
			return
		}
		groups = append(groups, append([]string(nil), current...))
		current = nil
	}

	for _, blk := range sec.Blocks {
		if !strings.EqualFold(strings.TrimSpace(blk.Type), "image") {
			flush()
			continue
		}
		url := strings.TrimSpace(blk.ImageURL)
		if url == "" {
			continue
		}
		current = append(current, url)
	}
	flush()

	return groups
}

func buildImageChunk(a Article, seq int, h2 string, urls []string) Chunk {
	cleanURLs := make([]string, 0, len(urls))
	for _, url := range urls {
		url = strings.TrimSpace(url)
		if url != "" {
			cleanURLs = append(cleanURLs, url)
		}
	}

	contentType := "image"
	content := ""
	if len(cleanURLs) > 1 {
		contentType = "multi_images"
		if data, err := json.Marshal(cleanURLs); err == nil {
			content = string(data)
		}
	}
	if content == "" && len(cleanURLs) > 0 {
		content = cleanURLs[0]
	}

	return Chunk{
		ChunkID:     BuildChunkID(a.ArticleID, seq, strings.TrimSpace(h2), strings.Join(cleanURLs, "|")),
		ArticleID:   a.ArticleID,
		H2:          strings.TrimSpace(h2),
		Content:     content,
		Tokens:      len(cleanURLs),
		ContentType: contentType,
		ImageURLs:   cleanURLs,
	}
}

func DetectKeywords(a Article, topK int) []string {
	weightByKey := map[string]float64{}
	textByKey := map[string]string{}
	firstSeen := map[string]int{}
	seenSeq := 0

	add := func(s string, weight float64) {
		s = normalizeKeywordCandidate(s)
		if s == "" {
			return
		}
		key := strings.ToLower(s)
		weightByKey[key] += weight
		if _, ok := textByKey[key]; !ok {
			textByKey[key] = s
		}
		if _, ok := firstSeen[key]; !ok {
			firstSeen[key] = seenSeq
			seenSeq++
		}
	}

	for _, t := range a.TypeTags {
		add(t, 5.0)
	}
	for _, t := range a.Tags {
		add(t, 4.0)
	}

	hashTagRe := regexp.MustCompile(`#([\p{Han}A-Za-z0-9_\-]+)`)
	for _, text := range append([]string{a.Title}, collectH2(a)...) {
		for _, sub := range hashTagRe.FindAllStringSubmatch(text, -1) {
			if len(sub) == 2 {
				add(sub[1], 4.0)
			}
		}
	}

	for _, text := range append([]string{a.Title}, collectH2(a)...) {
		for _, piece := range extractKeywordPieces(text) {
			add(piece, 2.0)
		}
	}

	type keywordRank struct {
		Key    string
		Text   string
		Weight float64
		SeenAt int
	}

	ranked := make([]keywordRank, 0, len(weightByKey))
	for key, weight := range weightByKey {
		ranked = append(ranked, keywordRank{
			Key:    key,
			Text:   textByKey[key],
			Weight: weight,
			SeenAt: firstSeen[key],
		})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Weight == ranked[j].Weight {
			if len([]rune(ranked[i].Text)) == len([]rune(ranked[j].Text)) {
				return ranked[i].SeenAt < ranked[j].SeenAt
			}
			return len([]rune(ranked[i].Text)) < len([]rune(ranked[j].Text))
		}
		return ranked[i].Weight > ranked[j].Weight
	})

	res := make([]string, 0, len(ranked))
	for _, item := range ranked {
		res = append(res, item.Text)
		if topK > 0 && len(res) >= topK {
			break
		}
	}
	return res
}

func KeywordCoverageScore(keywordCount int) float32 {
	if keywordCount <= 0 {
		return 0
	}
	if keywordCount > 5 {
		keywordCount = 5
	}
	return float32(keywordCount) * 0.2
}

func collectH2(a Article) []string {
	var hs []string
	for _, s := range a.Sections {
		if strings.TrimSpace(s.H2) != "" {
			hs = append(hs, strings.TrimSpace(s.H2))
		}
	}
	return hs
}

func ApproxTokenCount(s string) int {
	return utf8.RuneCountInString(s)
}

func SplitByTokenBudget(text string, maxTokens int, overlapTokens int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxTokens <= 0 {
		return []string{text}
	}

	paras := strings.Split(text, "\n")
	var chunks []string

	var cur []string
	curTokens := 0

	flush := func() {
		if len(cur) == 0 {
			return
		}
		chunks = append(chunks, strings.TrimSpace(strings.Join(cur, "\n")))
		cur = nil
		curTokens = 0
	}

	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		t := ApproxTokenCount(p)
		if curTokens+t <= maxTokens {
			cur = append(cur, p)
			curTokens += t
			continue
		}

		flush()

		if t > maxTokens {
			parts := splitLongLine(p, maxTokens, overlapTokens)
			chunks = append(chunks, parts...)
			continue
		}

		cur = append(cur, p)
		curTokens = t
	}

	flush()

	if overlapTokens > 0 && len(chunks) > 1 {
		var withOverlap []string
		withOverlap = append(withOverlap, chunks[0])
		for i := 1; i < len(chunks); i++ {
			prev := chunks[i-1]
			tail := tailByTokens(prev, overlapTokens)
			withOverlap = append(withOverlap, strings.TrimSpace(tail+"\n"+chunks[i]))
		}
		chunks = withOverlap
	}

	return chunks
}

func splitLongLine(s string, maxTokens int, overlapTokens int) []string {
	runes := []rune(s)
	if len(runes) <= maxTokens {
		return []string{s}
	}

	step := maxTokens
	if overlapTokens > 0 && overlapTokens < maxTokens {
		step = maxTokens - overlapTokens
	}

	var res []string
	for i := 0; i < len(runes); i += step {
		end := i + maxTokens
		if end > len(runes) {
			end = len(runes)
		}
		res = append(res, string(runes[i:end]))
		if end == len(runes) {
			break
		}
	}
	return res
}

func tailByTokens(s string, tokens int) string {
	r := []rune(s)
	if tokens <= 0 || len(r) <= tokens {
		return s
	}
	return string(r[len(r)-tokens:])
}

func extractKeywordPieces(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	splitter := func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', ',', '，', '。', '、', '/', '\\', '|', ':', '：', ';', '；', '(', ')', '（', '）', '[', ']', '【', '】', '<', '>', '《', '》', '"', '\'', '“', '”', '‘', '’', '-', '_', '+', '=', '*', '&', '·', '!', '！', '?', '？':
			return true
		default:
			return false
		}
	}

	parts := strings.FieldsFunc(s, splitter)
	if len(parts) == 0 {
		parts = []string{s}
	}

	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		part = normalizeKeywordCandidate(part)
		if part == "" {
			continue
		}
		key := strings.ToLower(part)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, part)
	}
	return out
}

func normalizeKeywordCandidate(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "#,.，。!！?？:：;；/\\|()（）[]【】<>《》\"'“”‘’`~")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) < 2 {
		return ""
	}
	if utf8.RuneCountInString(s) > 24 {
		return ""
	}
	switch strings.ToLower(s) {
	case "type", "title", "tag", "tags", "intro", "summary":
		return ""
	}
	return s
}

func uniqueStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func limitStrings(in []string, limit int) []string {
	if limit <= 0 || len(in) <= limit {
		return append([]string(nil), in...)
	}
	return append([]string(nil), in[:limit]...)
}
