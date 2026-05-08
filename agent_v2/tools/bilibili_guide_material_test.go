package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent_v2/config"
)

type fakeBilibiliGuideSearcher struct {
	byQuery map[string]BilibiliSearchResult
	errs    map[string]error
	calls   []BilibiliSearchInput
}

func (s *fakeBilibiliGuideSearcher) Search(_ context.Context, in BilibiliSearchInput) (BilibiliSearchResult, error) {
	s.calls = append(s.calls, in)
	if err, ok := s.errs[in.Query]; ok {
		return BilibiliSearchResult{}, err
	}
	if result, ok := s.byQuery[in.Query]; ok {
		return result, nil
	}
	return BilibiliSearchResult{Code: 0, Message: "success"}, nil
}

func TestGenerateBilibiliGuideQueryPlan(t *testing.T) {
	got := GenerateBilibiliGuideQueryPlan("成都旅游攻略", 6)
	if len(got) != 6 {
		t.Fatalf("query count = %d, want 6", len(got))
	}
	if got[0].Query != "成都旅游攻略" || got[0].Intent != "overview" {
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

func TestCollectBilibiliGuideMaterialDedupsAndSelects(t *testing.T) {
	now := time.Now().Unix()
	searcher := &fakeBilibiliGuideSearcher{byQuery: map[string]BilibiliSearchResult{}}
	opts := BilibiliGuideRunOptions{
		QueryCount:         3,
		PerQueryCount:      10,
		ReviewPoolSize:     5,
		SelectedVideoCount: 3,
		ReviewScore:        20,
		AcceptScore:        45,
		MustKeywords:       []string{"成都"},
	}
	queries := GenerateBilibiliGuideQueryPlan("成都旅游攻略", opts.QueryCount)
	searcher.byQuery[queries[0].Query] = BilibiliSearchResult{Code: 0, Message: "success", Items: []BilibiliSearchItem{
		bilibiliItem("成都旅游完整路线攻略", "BVroute", "UP主A", "成都路线、住宿、交通和美食的完整经验总结，适合第一次去成都自由行。", 120000, 3000, 6000, 8000, now),
		bilibiliItem("无关内容", "BVother", "UP主B", "这是一条和主题关系很弱的内容。", 100, 1, 1, 1, now),
	}}
	searcher.byQuery[queries[1].Query] = BilibiliSearchResult{Code: 0, Message: "success", Items: []BilibiliSearchItem{
		bilibiliItem("成都旅游完整路线攻略", "BVroute", "UP主A", "重复视频但简介略短。", 80000, 1000, 4000, 5000, now),
		bilibiliItem("成都交通避坑", "BVtraffic", "UP主C", "成都机场、地铁、换乘和交通避坑经验，适合做攻略素材。", 90000, 1200, 5000, 7000, now),
	}}
	searcher.byQuery[queries[2].Query] = BilibiliSearchResult{Code: 0, Message: "success", Items: []BilibiliSearchItem{
		bilibiliItem("成都美食推荐清单", "BVfood", "UP主D", "成都美食、餐厅、市场和排队注意事项，适合安排每天吃什么。", 70000, 800, 3000, 6000, now),
	}}

	run, err := collectBilibiliGuideMaterial(context.Background(), searcher, "成都旅游攻略", opts)
	if err != nil {
		t.Fatalf("collectBilibiliGuideMaterial() error = %v", err)
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
	route := findBilibiliCandidateByBVID(run.FilteredCandidates, "BVroute")
	if route.BVID == "" {
		t.Fatalf("deduped route candidate not found: %+v", run.FilteredCandidates)
	}
	if len(route.Sources) != 2 {
		t.Fatalf("deduped route sources = %d, want 2: %+v", len(route.Sources), route.Sources)
	}
}

func TestScoreBilibiliGuideCandidateRejectsBlockedContent(t *testing.T) {
	opts := normalizeBilibiliGuideOptions(BilibiliGuideRunOptions{
		ReviewScore:      45,
		AcceptScore:      70,
		NegativeKeywords: []string{"广告"},
		BlockedAuthors:   []string{"黑名单UP"},
	})
	cases := []BilibiliGuideCandidate{
		{Title: "成都广告软文", URL: "https://www.bilibili.com/video/BVad", AuthorName: "UP主", Summary: "成都旅游广告内容", Sources: []BilibiliGuideSource{{Intent: "overview"}}},
		{Title: "成都攻略", URL: "https://www.bilibili.com/video/BVblock", AuthorName: "黑名单UP", Summary: "成都旅游攻略内容", Sources: []BilibiliGuideSource{{Intent: "overview"}}},
	}
	for i, tc := range cases {
		_, status, reasons := scoreBilibiliGuideCandidate(tc, "成都旅游攻略", opts)
		if status != BilibiliGuideStatusRejected {
			t.Fatalf("case %d status = %s, want rejected, reasons=%v", i, status, reasons)
		}
	}
}

func TestSelectBilibiliGuideVideosHonorsLimit(t *testing.T) {
	pool := make([]BilibiliGuideCandidate, 0, 8)
	for i := 0; i < 8; i++ {
		pool = append(pool, BilibiliGuideCandidate{
			Title:        fmt.Sprintf("成都素材 %d", i),
			URL:          fmt.Sprintf("https://www.bilibili.com/video/BV%d", i),
			URLHash:      fmt.Sprintf("hash-%d", i),
			Score:        float64(90 - i),
			Status:       BilibiliGuideStatusAccepted,
			Sources:      []BilibiliGuideSource{{Intent: "overview"}},
			SourceIntent: "overview",
		})
	}
	got := selectBilibiliGuideVideos(pool, 3)
	if len(got) != 3 {
		t.Fatalf("selected = %d, want 3", len(got))
	}
	for _, item := range got {
		if item.Status != BilibiliGuideStatusSelected {
			t.Fatalf("selected item status = %s, want selected", item.Status)
		}
	}
}

func TestBilibiliGuideMaterialToolCall(t *testing.T) {
	skillDir := writeBilibiliGuideMaterialTestSkill(t)
	runtime := &bilibiliRuntime{
		skillDir:      skillDir,
		pythonCommand: "python",
		timeout:       5 * time.Second,
		cfg: config.BilibiliConfig{
			GuideMaterial: config.BilibiliGuideMaterialConfig{
				QueryCount:         2,
				PerQueryCount:      2,
				ReviewPoolSize:     4,
				SelectedVideoCount: 2,
			},
		},
	}
	out, err := runtime.GuideMaterial(context.Background(), BilibiliGuideMaterialInput{
		Topic: "成都旅游攻略",
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
}

func TestCollectBilibiliGuideMaterialRecordsQueryErrors(t *testing.T) {
	searcher := &fakeBilibiliGuideSearcher{
		byQuery: map[string]BilibiliSearchResult{},
		errs:    map[string]error{},
	}
	opts := BilibiliGuideRunOptions{
		QueryCount:         2,
		PerQueryCount:      3,
		ReviewPoolSize:     3,
		SelectedVideoCount: 2,
		ReviewScore:        20,
		AcceptScore:        45,
		MustKeywords:       []string{"成都"},
	}
	queries := GenerateBilibiliGuideQueryPlan("成都旅游攻略", opts.QueryCount)
	searcher.errs[queries[0].Query] = errors.New("network unavailable")
	searcher.byQuery[queries[1].Query] = BilibiliSearchResult{
		Code:    0,
		Message: "success",
		Items: []BilibiliSearchItem{
			bilibiliItem("成都美食攻略", "BVfood", "UP主", "成都美食、餐厅和排队避坑经验。", 50000, 100, 2000, 3000, time.Now().Unix()),
		},
	}

	run, err := collectBilibiliGuideMaterial(context.Background(), searcher, "成都旅游攻略", opts)
	if err != nil {
		t.Fatalf("collectBilibiliGuideMaterial() error = %v", err)
	}
	if len(run.Errors) != 1 || !strings.Contains(run.Errors[0].Error, "network unavailable") {
		t.Fatalf("errors = %+v, want network error", run.Errors)
	}
	if run.Stats.RawCount != 1 || run.Stats.SelectedCount != 1 {
		t.Fatalf("unexpected stats after partial failure: %+v", run.Stats)
	}
	result := buildBilibiliGuideMaterialResult(run)
	if !strings.Contains(result.Message, "有 1 个 query 失败") {
		t.Fatalf("message does not mention query failure: %s", result.Message)
	}
}

func TestBilibiliGuideMaterialInputOverridesConfig(t *testing.T) {
	skillDir := writeBilibiliGuideMaterialTestSkill(t)
	runtime := &bilibiliRuntime{
		skillDir:      skillDir,
		pythonCommand: "python",
		timeout:       5 * time.Second,
		cfg: config.BilibiliConfig{
			GuideMaterial: config.BilibiliGuideMaterialConfig{
				QueryCount:         5,
				PerQueryCount:      5,
				ReviewPoolSize:     10,
				SelectedVideoCount: 5,
				ShouldKeywords:     []string{"配置词"},
			},
		},
	}
	out, err := runtime.GuideMaterial(context.Background(), BilibiliGuideMaterialInput{
		Topic:              "成都旅游攻略",
		QueryCount:         1,
		PerQueryCount:      2,
		ReviewPoolSize:     2,
		SelectedVideoCount: 1,
		ShouldKeywords:     []string{"输入词"},
	})
	if err != nil {
		t.Fatalf("GuideMaterial() error = %v", err)
	}
	if out.QueryCount != 1 || out.RawCount != 2 || out.ReviewPoolCount != 2 || out.SelectedCount != 1 {
		t.Fatalf("input overrides not applied: %+v", out)
	}
}

func bilibiliItem(title, bvid, author, summary string, views, danmaku, likes, favorites, publishTime int64) BilibiliSearchItem {
	return BilibiliSearchItem{
		Title:         title,
		URL:           "https://www.bilibili.com/video/" + bvid,
		BVID:          bvid,
		AuthorName:    author,
		Summary:       summary,
		ViewCount:     views,
		DanmakuCount:  danmaku,
		LikeCount:     likes,
		FavoriteCount: favorites,
		PublishTime:   publishTime,
	}
}

func writeBilibiliGuideMaterialTestSkill(t *testing.T) string {
	t.Helper()
	skillDir := filepath.Join(t.TempDir(), "bilibili-search")
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
    bvid = "BV" + str(abs(hash(query + str(i))))
    items.append({
        "title": query + " 素材 " + str(i),
        "url": "https://www.bilibili.com/video/" + bvid,
        "bvid": bvid,
        "author_name": "tester",
        "summary": query + " 的路线、交通、美食和避坑经验，适合生成攻略素材。",
        "view_count": 80000 + i,
        "danmaku_count": 100 + i,
        "like_count": 2000 + i,
        "favorite_count": 3000 + i,
        "publish_time": 1893456000
    })
print(json.dumps({"code": 0, "message": "success", "item_count": len(items), "items": items}, ensure_ascii=False))
`
	if err := os.WriteFile(filepath.Join(scriptsDir, "bilibili-search.py"), []byte(script), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return skillDir
}

func findBilibiliCandidateByBVID(items []BilibiliGuideCandidate, bvid string) BilibiliGuideCandidate {
	for _, item := range items {
		if item.BVID == bvid {
			return item
		}
	}
	return BilibiliGuideCandidate{}
}
