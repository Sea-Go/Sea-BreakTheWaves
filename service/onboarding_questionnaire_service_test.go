package service

import (
	"strings"
	"testing"
)

func TestNormalizeOnboardingQuestionnaireRequest(t *testing.T) {
	req := normalizeOnboardingQuestionnaireRequest(OnboardingQuestionnaireRequest{
		UserID:                          "  user-1  ",
		Username:                        "  alice  ",
		Interests:                       []string{"科技", "科技", "  ", "旅行", "商业", "美食", "历史"},
		PreferredArticleTypes:           []string{"深度分析", "深度分析", "实用教程 / 干货", "案例拆解", "榜单 / 推荐"},
		Backgrounds:                     []string{"学生", "互联网 / 科技", "学生", "产品 / 设计", "教育 / 培训", "其他"},
		ExcludedContents:                []string{"广告软文", "广告软文", "娱乐八卦"},
		ReadingTimeSlots:                []string{"通勤路上", "晚上睡前", "通勤路上", "周末", "午休时间"},
		PersonalizedRecommendationTypes: []string{"每日精选", "每日精选", "热门趋势", "相似文章推荐", "首页推荐"},
	})

	if req.UserID != "user-1" {
		t.Fatalf("expected trimmed user id, got %q", req.UserID)
	}
	if req.Username != "alice" {
		t.Fatalf("expected trimmed username, got %q", req.Username)
	}
	if len(req.Interests) != 5 {
		t.Fatalf("expected interests to be capped at 5, got %d", len(req.Interests))
	}
	if len(req.PreferredArticleTypes) != 3 {
		t.Fatalf("expected preferred article types to be capped at 3, got %d", len(req.PreferredArticleTypes))
	}
	if len(req.Backgrounds) != 4 {
		t.Fatalf("expected backgrounds to be capped at 4, got %d", len(req.Backgrounds))
	}
	if len(req.ExcludedContents) != 2 {
		t.Fatalf("expected duplicates removed from excluded contents, got %d", len(req.ExcludedContents))
	}
	if len(req.ReadingTimeSlots) != 4 {
		t.Fatalf("expected reading time slots to be capped at 4, got %d", len(req.ReadingTimeSlots))
	}
	if len(req.PersonalizedRecommendationTypes) != 4 {
		t.Fatalf("expected recommendation types to be capped at 4, got %d", len(req.PersonalizedRecommendationTypes))
	}
}

func TestValidateOnboardingQuestionnaireRequest(t *testing.T) {
	req := OnboardingQuestionnaireRequest{}
	err := validateOnboardingQuestionnaireRequest(req)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "user_id") {
		t.Fatalf("expected user_id validation error, got %v", err)
	}
}

func TestBuildOnboardingMemoryContents(t *testing.T) {
	req := OnboardingQuestionnaireRequest{
		UserID:                          "user-1",
		Username:                        "alice",
		Interests:                       []string{"科技", "旅行"},
		PrimaryPurpose:                  "学习新知识",
		PreferredArticleTypes:           []string{"深度分析", "实用教程 / 干货"},
		PreferredArticleLength:          "5～10 分钟，愿意认真读",
		PreferredStyle:                  "专业严谨",
		Backgrounds:                     []string{"互联网 / 科技", "产品 / 设计"},
		DifficultyPreference:            "中等，有一定信息量",
		ExcludedContents:                []string{"广告软文", "娱乐八卦"},
		ReadingTimeSlots:                []string{"通勤路上", "晚上睡前"},
		PersonalizedRecommendationTypes: []string{"每日精选", "与我兴趣相关的新内容"},
	}

	memories := buildOnboardingMemoryContents(req)
	if !strings.Contains(memories.LongTerm, "科技、旅行") {
		t.Fatalf("expected long term memory to contain interests, got %q", memories.LongTerm)
	}
	if !strings.Contains(memories.ShortTerm, "前几轮推荐优先主题") {
		t.Fatalf("expected short term memory guidance, got %q", memories.ShortTerm)
	}
	if !strings.Contains(memories.Periodic, "通勤路上、晚上睡前") {
		t.Fatalf("expected periodic memory to contain reading time slots, got %q", memories.Periodic)
	}
}
