package zhihu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent_v3/internal/config"
)

type fakeZhihuGuideSearcher struct {
	byQuery       map[string]ZhihuSearchResult
	globalByQuery map[string]ZhihuSearchResult
	calls         []ZhihuSearchInput
	globalCalls   []ZhihuSearchInput
}

func (s *fakeZhihuGuideSearcher) Search(_ context.Context, in ZhihuSearchInput) (ZhihuSearchResult, error) {
	s.calls = append(s.calls, in)
	if result, ok := s.byQuery[in.Query]; ok {
		return result, nil
	}
	return ZhihuSearchResult{Code: 0, Message: "success"}, nil
}

func (s *fakeZhihuGuideSearcher) GlobalSearch(_ context.Context, in ZhihuSearchInput) (ZhihuSearchResult, error) {
	s.globalCalls = append(s.globalCalls, in)
	if result, ok := s.globalByQuery[in.Query]; ok {
		return result, nil
	}
	return ZhihuSearchResult{Code: 0, Message: "success"}, nil
}

func TestGenerateZhihuGuideQueryPlan(t *testing.T) {
	got := GenerateZhihuGuideQueryPlan("大阪旅游攻略", 6)
	if len(got) != 6 {
		t.Fatalf("query count = %d, want 6", len(got))
	}
	if got[0].Query != "大阪旅游攻略" || got[0].Intent != "overview" {
		t.Fatalf("unexpected first query: %+v", got[0])
	}
	seen := map[string]bool{}
	for _, item := range got {
		if strings.TrimSpace(item.Query) == "" || item.Intent == "" {
			t.Fatalf("invalid query item: %+v", item)
		}
		if seen[item.Query] {
			t.Fatalf("duplicated query: %s", item.Query)
		}
		seen[item.Query] = true
	}
}

func TestCollectZhihuGuideMaterialDedupsAndSelects(t *testing.T) {
	now := time.Now().Unix()
	searcher := &fakeZhihuGuideSearcher{byQuery: map[string]ZhihuSearchResult{}}
	opts := ZhihuGuideRunOptions{
		QueryCount:           3,
		PerQueryCount:        10,
		ReviewPoolSize:       5,
		SelectedArticleCount: 3,
		ReviewScore:          20,
		AcceptScore:          45,
		MustKeywords:         []string{"大阪"},
	}
	queries := GenerateZhihuGuideQueryPlan("大阪旅游攻略", opts.QueryCount)
	searcher.byQuery[queries[0].Query] = ZhihuSearchResult{Code: 0, Message: "success", Items: []ZhihuSearchItem{
		item("大阪旅游完整路线攻略", "https://zhuanlan.zhihu.com/p/1?utm_source=test", "作者A", "大阪路线、住宿、交通和美食的完整经验总结，适合第一次去大阪自由行。", 120, 18, now),
		item("无关内容", "https://zhuanlan.zhihu.com/p/2", "作者B", "这是一条和主题关系很弱的内容。", 1, 0, now),
	}}
	searcher.byQuery[queries[1].Query] = ZhihuSearchResult{Code: 0, Message: "success", Items: []ZhihuSearchItem{
		item("大阪旅游完整路线攻略", "https://zhuanlan.zhihu.com/p/1?utm_medium=openapi", "作者A", "重复链接但摘要略短。", 80, 10, now),
		item("大阪交通避坑回答", "https://www.zhihu.com/question/1/answer/2", "作者C", "大阪机场、地铁、周游券、换乘和交通避坑经验，虽然是回答但很适合做攻略素材。", 90, 22, now),
	}}
	searcher.byQuery[queries[2].Query] = ZhihuSearchResult{Code: 0, Message: "success", Items: []ZhihuSearchItem{
		item("大阪美食推荐清单", "https://zhuanlan.zhihu.com/p/3", "作者D", "大阪美食、餐厅、市场和排队注意事项，适合安排每天吃什么。", 70, 8, now),
	}}

	run, err := collectZhihuGuideMaterial(context.Background(), searcher, "大阪旅游攻略", opts)
	if err != nil {
		t.Fatalf("collectZhihuGuideMaterial() error = %v", err)
	}
	if len(searcher.calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(searcher.calls))
	}
	if run.Stats.RawCount != 5 {
		t.Fatalf("raw count = %d, want 5", run.Stats.RawCount)
	}
	if run.Stats.DedupedCount != 4 {
		t.Fatalf("deduped count = %d, want 4", run.Stats.DedupedCount)
	}
	if run.Stats.SelectedCount != 3 {
		t.Fatalf("selected count = %d, want 3", run.Stats.SelectedCount)
	}
	if len(run.SelectedForLLM.Items) != 3 {
		t.Fatalf("selected items = %d, want 3", len(run.SelectedForLLM.Items))
	}
	foundAnswer := false
	for _, selected := range run.SelectedForLLM.Items {
		if strings.Contains(selected.URL, "/answer/") {
			foundAnswer = true
		}
	}
	if !foundAnswer {
		t.Fatalf("expected high quality answer to be eligible for selected materials")
	}
}

func TestCollectZhihuGuideMaterialUsesGlobalSearchSupplement(t *testing.T) {
	now := time.Now().Unix()
	searcher := &fakeZhihuGuideSearcher{
		byQuery:       map[string]ZhihuSearchResult{},
		globalByQuery: map[string]ZhihuSearchResult{},
	}
	opts := ZhihuGuideRunOptions{
		QueryCount:           1,
		PerQueryCount:        2,
		ReviewPoolSize:       4,
		SelectedArticleCount: 2,
		ReviewScore:          20,
		AcceptScore:          45,
		MustKeywords:         []string{"大阪"},
	}
	query := GenerateZhihuGuideQueryPlan("大阪旅游攻略", opts.QueryCount)[0]
	searcher.byQuery[query.Query] = ZhihuSearchResult{Code: 0, Message: "success", Items: []ZhihuSearchItem{
		item("大阪站内路线攻略", "https://zhuanlan.zhihu.com/p/local", "作者A", "大阪路线、交通和美食攻略，适合第一次自由行。", 80, 10, now),
	}}
	searcher.globalByQuery[query.Query] = ZhihuSearchResult{Code: 0, Message: "success", Items: []ZhihuSearchItem{
		item("大阪全网补充攻略", "https://example.com/osaka-guide", "作者B", "大阪全网补充内容，包含住宿区域、换乘方式、美食街区、排队避坑和雨天备选，摘要更长，适合补充给大模型。", 0, 0, now),
	}}

	run, err := collectZhihuGuideMaterial(context.Background(), searcher, "大阪旅游攻略", opts)
	if err != nil {
		t.Fatalf("collectZhihuGuideMaterial() error = %v", err)
	}
	if len(searcher.globalCalls) != 1 {
		t.Fatalf("global calls = %d, want 1", len(searcher.globalCalls))
	}
	if run.Stats.RawCount != 2 || run.Stats.SelectedCount != 2 {
		t.Fatalf("unexpected stats: %+v", run.Stats)
	}
	if run.Stats.ZhihuSearchRawCount != 1 || run.Stats.GlobalSearchRawCount != 1 {
		t.Fatalf("unexpected raw scope stats: %+v", run.Stats)
	}
	foundGlobal := false
	for _, selected := range run.SelectedForLLM.Items {
		if strings.Contains(selected.Title, "[global_search]") && selected.SearchScope == "global_search" && selected.URL == "https://example.com/osaka-guide" {
			foundGlobal = true
		}
	}
	if !foundGlobal {
		t.Fatalf("expected global search supplement in selected_for_llm: %+v", run.SelectedForLLM.Items)
	}
}

func TestScoreZhihuGuideCandidateRejectsBlockedContent(t *testing.T) {
	opts := normalizeGuideOptions(ZhihuGuideRunOptions{
		ReviewScore:      45,
		AcceptScore:      70,
		NegativeKeywords: []string{"广告"},
		BlockedAuthors:   []string{"黑名单作者"},
	})
	cases := []ZhihuGuideCandidate{
		{Title: "大阪广告软文", URL: "https://zhuanlan.zhihu.com/p/1", AuthorName: "作者", Summary: "大阪旅游广告内容", Sources: []ZhihuGuideSource{{Intent: "overview"}}},
		{Title: "大阪攻略", URL: "https://zhuanlan.zhihu.com/p/2", AuthorName: "黑名单作者", Summary: "大阪旅游攻略内容", Sources: []ZhihuGuideSource{{Intent: "overview"}}},
	}
	for i, tc := range cases {
		_, status, reasons := scoreZhihuGuideCandidate(tc, "大阪旅游攻略", opts)
		if status != ZhihuGuideStatusRejected {
			t.Fatalf("case %d status = %s, want rejected, reasons=%v", i, status, reasons)
		}
	}
}

func TestSelectZhihuGuideArticlesHonorsLimit(t *testing.T) {
	pool := make([]ZhihuGuideCandidate, 0, 8)
	for i := 0; i < 8; i++ {
		pool = append(pool, ZhihuGuideCandidate{
			Title:        fmt.Sprintf("大阪素材 %d", i),
			URL:          fmt.Sprintf("https://zhuanlan.zhihu.com/p/%d", i),
			URLHash:      fmt.Sprintf("hash-%d", i),
			Score:        float64(90 - i),
			Status:       ZhihuGuideStatusAccepted,
			Sources:      []ZhihuGuideSource{{Intent: "overview"}},
			SourceIntent: "overview",
		})
	}
	got := selectZhihuGuideArticles(pool, 3)
	if len(got) != 3 {
		t.Fatalf("selected = %d, want 3", len(got))
	}
	for _, item := range got {
		if item.Status != ZhihuGuideStatusSelected {
			t.Fatalf("selected item status = %s, want selected", item.Status)
		}
	}
}

func TestBuildAndApplyZhihuGuideReviewDecisions(t *testing.T) {
	pool := []ZhihuGuideCandidate{
		reviewCandidate("大阪路线攻略", "https://zhuanlan.zhihu.com/p/route", "itinerary", 88),
		reviewCandidate("大阪交通攻略", "https://zhuanlan.zhihu.com/p/transport", "transport", 86),
		reviewCandidate("大阪美食攻略", "https://zhuanlan.zhihu.com/p/food", "food", 84),
	}
	selected := selectZhihuGuideArticles(pool, 2)
	run := ZhihuGuideRun{
		RunID:          "run-1",
		Topic:          "大阪旅游攻略",
		ReviewPool:     pool,
		SelectedForLLM: buildZhihuGuideLLMInput("大阪旅游攻略", selected),
		Options: ZhihuGuideRunOptions{
			SelectedArticleCount: 2,
		},
	}
	decisions := BuildZhihuGuideReviewDecisions(run)
	if len(decisions.Decisions) != 3 {
		t.Fatalf("decisions = %d, want 3", len(decisions.Decisions))
	}

	for i := range decisions.Decisions {
		decisions.Decisions[i].Decision = "pending"
	}
	decisions.Decisions[0].Decision = "rejected"
	decisions.Decisions[2].Decision = "selected"
	decisions.Decisions[2].HumanNote = "人工认为美食维度更关键"

	input, warnings := ApplyZhihuGuideReviewDecisions("大阪旅游攻略", pool, decisions, 2)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if input.SelectedCount != 2 {
		t.Fatalf("selected count = %d, want 2", input.SelectedCount)
	}
	if input.Items[0].URL != "https://zhuanlan.zhihu.com/p/food" {
		t.Fatalf("first selected URL = %s, want food URL", input.Items[0].URL)
	}
	for _, item := range input.Items {
		if item.URL == "https://zhuanlan.zhihu.com/p/route" {
			t.Fatalf("rejected item should not be selected")
		}
	}
	if len(input.Items[0].Evidence) == 0 || !strings.Contains(input.Items[0].Evidence[0], "人工备注") {
		t.Fatalf("expected human note in evidence: %+v", input.Items[0].Evidence)
	}
}

func TestApplyZhihuGuideReviewDecisionsTruncatesManualOverSelection(t *testing.T) {
	pool := []ZhihuGuideCandidate{
		reviewCandidate("大阪路线攻略", "https://zhuanlan.zhihu.com/p/route", "itinerary", 88),
		reviewCandidate("大阪交通攻略", "https://zhuanlan.zhihu.com/p/transport", "transport", 86),
		reviewCandidate("大阪美食攻略", "https://zhuanlan.zhihu.com/p/food", "food", 84),
	}
	decisions := ZhihuGuideReviewDecisionFile{
		Topic:                "大阪旅游攻略",
		SelectedArticleCount: 2,
	}
	for _, item := range pool {
		decisions.Decisions = append(decisions.Decisions, ZhihuGuideReviewDecision{
			URLHash:  item.URLHash,
			URL:      item.URL,
			Title:    item.Title,
			Decision: "selected",
		})
	}
	input, warnings := ApplyZhihuGuideReviewDecisions("", pool, decisions, 2)
	if input.SelectedCount != 2 {
		t.Fatalf("selected count = %d, want 2", input.SelectedCount)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected truncation warning")
	}
}

func TestZhihuGuideMaterialToolCall(t *testing.T) {
	skillDir := writeZhihuGuideMaterialTestSkill(t)
	runtime := &zhihuRuntime{
		skillDir:      skillDir,
		pythonCommand: "python",
		timeout:       5 * time.Second,
		cfg: config.ZhihuConfig{
			GuideMaterial: config.ZhihuGuideMaterialConfig{
				QueryCount:           2,
				PerQueryCount:        2,
				ReviewPoolSize:       4,
				SelectedArticleCount: 2,
			},
		},
	}
	out, err := runtime.GuideMaterial(context.Background(), ZhihuGuideMaterialInput{
		Topic: "大阪旅游攻略",
	})
	if err != nil {
		t.Fatalf("GuideMaterial() error = %v", err)
	}
	if out.RawCount != 4 || out.DedupedCount != 4 || out.SelectedCount != 2 {
		t.Fatalf("unexpected result stats: %+v", out)
	}
	if out.SelectedForLLM.SelectedCount != 2 {
		t.Fatalf("selected_for_llm count = %d, want 2", out.SelectedForLLM.SelectedCount)
	}
	if out.ReviewDecisions.Topic != "大阪旅游攻略" || len(out.ReviewDecisions.Decisions) == 0 {
		t.Fatalf("missing review decisions: %+v", out.ReviewDecisions)
	}
}

func item(title, url, author, summary string, votes, comments, editTime int64) ZhihuSearchItem {
	return ZhihuSearchItem{
		Title:        title,
		URL:          url,
		AuthorName:   author,
		Summary:      summary,
		VoteUpCount:  votes,
		CommentCount: comments,
		EditTime:     editTime,
	}
}

func writeZhihuGuideMaterialTestSkill(t *testing.T) string {
	t.Helper()
	skillDir := filepath.Join(t.TempDir(), "zhihu-search")
	scriptsDir := filepath.Join(skillDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	script := `import json
import sys
query = ""
count = 2
for i, arg in enumerate(sys.argv):
    if arg == "--query":
        query = sys.argv[i + 1]
    if arg == "--count":
        count = int(sys.argv[i + 1])
items = []
for i in range(count):
    items.append({
        "title": query + " 素材 " + str(i),
        "url": "https://zhuanlan.zhihu.com/p/" + str(abs(hash(query + str(i)))),
        "author_name": "tester",
        "summary": query + " 的路线、交通、美食和避坑经验，适合生成攻略素材。",
        "vote_up_count": 80 + i,
        "comment_count": 10 + i,
        "edit_time": 1893456000
    })
print(json.dumps({"code": 0, "message": "success", "item_count": len(items), "items": items}, ensure_ascii=False))
`
	if err := os.WriteFile(filepath.Join(scriptsDir, "zhihu-search.py"), []byte(script), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return skillDir
}

func reviewCandidate(title, rawURL, intent string, score float64) ZhihuGuideCandidate {
	return ZhihuGuideCandidate{
		Title:        title,
		URL:          rawURL,
		AuthorName:   "作者",
		Summary:      title + "，适合生成攻略素材，包含清晰经验和可执行建议。",
		VoteUpCount:  80,
		CommentCount: 12,
		SourceIntent: intent,
		Sources:      []ZhihuGuideSource{{Query: title, Intent: intent}},
		URLHash:      hashString(normalizeZhihuURL(rawURL)),
		IsArticle:    true,
		Score:        score,
		Status:       ZhihuGuideStatusAccepted,
		Reasons:      []string{"测试原因"},
	}
}
