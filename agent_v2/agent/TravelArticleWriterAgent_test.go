package agent

import (
	"testing"

	"agent_v2/tools"
)

func TestNormalizeTravelArticleBrief(t *testing.T) {
	plan := TravelPlanningOutput{
		Query:           "帮我规划成都三天慢旅行",
		NormalizedQuery: "成都3天慢旅行",
	}
	brief := TravelArticleBrief{}

	got := normalizeTravelArticleBrief(brief, plan)
	if got.Topic != "成都3天慢旅行" {
		t.Fatalf("topic = %q", got.Topic)
	}
	if got.TargetAudience == "" {
		t.Fatalf("target audience should be defaulted")
	}
	if len(got.Constraints) < 3 {
		t.Fatalf("constraints = %#v", got.Constraints)
	}
}

func TestTravelArticleWriterRequestKeepsTravelPlanAndMemory(t *testing.T) {
	plan := TravelPlanningOutput{
		Query:                   "成都三天游",
		InsufficientInformation: true,
		FollowUpQuestions:       []string{"住宿地是哪里？"},
	}
	memory := tools.ContentStrategyMemory{
		PreferredStructure: []string{"先给总览，再按天展开"},
	}

	req := TravelArticleWriterRequest{
		Brief:          normalizeTravelArticleBrief(TravelArticleBrief{}, plan),
		TravelPlan:     plan,
		FeedbackMemory: tools.NormalizeContentStrategyMemory(memory),
	}

	if !req.TravelPlan.InsufficientInformation {
		t.Fatalf("expected travel plan insufficient flag to be preserved")
	}
	if req.Brief.Topic == "" {
		t.Fatalf("expected brief topic to be preserved")
	}
	if len(req.TravelPlan.FollowUpQuestions) != 1 {
		t.Fatalf("follow up questions = %#v", req.TravelPlan.FollowUpQuestions)
	}
	if len(req.FeedbackMemory.PreferredStructure) != 1 {
		t.Fatalf("feedback memory = %#v", req.FeedbackMemory)
	}
}

func TestLocalFeedbackToArticleWriterChain(t *testing.T) {
	comments := []string{
		"这条路线适合带老人吗？",
		"如果是亲子出行，要不要替换掉晚上那段？",
		"预算大概多少？吃饭部分还能再补一点吗？",
	}
	existingMemory := tools.ContentStrategyMemory{
		PreferredStructure: []string{"先给总览，再按天展开"},
		DoMore:             []string{"多写交通衔接和体力成本"},
		Avoid:              []string{"少写空泛形容词"},
	}

	feedback := CommentFeedbackOutput{
		StrategyMemoryUpdate: tools.ContentStrategyMemory{
			DoMore: []string{
				"补充老人和亲子适配说明",
				"补充预算和吃饭信息",
			},
			UnansweredQuestions: comments,
		},
	}
	mergedMemory := tools.MergeContentStrategyMemory(existingMemory, feedback.StrategyMemoryUpdate)

	plan := TravelPlanningOutput{
		Query:           "帮我规划成都三天慢旅行",
		NormalizedQuery: "成都3天慢旅行",
		ThinkingResult:  "已识别为成都三天慢节奏自由行。",
		PlanningProcess: "按片区组织路线，并区分攻略建议与地理事实。",
		Answer:          "第一天围绕人民公园和宽窄巷子，第二天围绕武侯祠和锦里，第三天保留轻松备选。",
		ContentInsights: []string{
			"评论持续追问老人、亲子和预算适配。",
		},
		RouteValidation: []string{
			"人民公园、宽窄巷子、武侯祠均需以后端接入后继续实时校验。",
		},
		InsufficientInformation: false,
	}

	req := TravelArticleWriterRequest{
		Brief:          normalizeTravelArticleBrief(TravelArticleBrief{}, plan),
		TravelPlan:     plan,
		FeedbackMemory: tools.NormalizeContentStrategyMemory(mergedMemory),
	}

	if req.Brief.Topic != "成都3天慢旅行" {
		t.Fatalf("topic = %q", req.Brief.Topic)
	}
	if req.TravelPlan.Answer == "" || len(req.TravelPlan.RouteValidation) == 0 {
		t.Fatalf("travel plan was not carried into writer request: %#v", req.TravelPlan)
	}
	if !containsString(req.FeedbackMemory.DoMore, "补充老人和亲子适配说明") {
		t.Fatalf("feedback memory did not include comment-derived improvement: %#v", req.FeedbackMemory.DoMore)
	}
	if !containsString(req.FeedbackMemory.DoMore, "多写交通衔接和体力成本") {
		t.Fatalf("feedback memory did not keep existing strategy: %#v", req.FeedbackMemory.DoMore)
	}
	if !containsString(req.FeedbackMemory.UnansweredQuestions, "预算大概多少？吃饭部分还能再补一点吗？") {
		t.Fatalf("feedback questions were not carried forward: %#v", req.FeedbackMemory.UnansweredQuestions)
	}
}

func TestTravelArticleOutputToBackendDraft(t *testing.T) {
	got := TravelArticleOutputToBackendDraft(TravelArticleOutput{
		Title:   " 成都三天慢旅行 ",
		Summary: " 适合第一次去成都的慢旅行攻略 ",
		Article: " 正文 ",
	}, "旅游攻略", []string{" 成都 ", "", "慢旅行"})

	if got.Title != "成都三天慢旅行" {
		t.Fatalf("title = %q", got.Title)
	}
	if got.Brief != "适合第一次去成都的慢旅行攻略" {
		t.Fatalf("brief = %q", got.Brief)
	}
	if got.Content != "正文" {
		t.Fatalf("content = %q", got.Content)
	}
	if got.ManualTypeTag != "旅游攻略" {
		t.Fatalf("manual type tag = %q", got.ManualTypeTag)
	}
	if len(got.SecondaryTags) != 2 {
		t.Fatalf("secondary tags = %#v", got.SecondaryTags)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
