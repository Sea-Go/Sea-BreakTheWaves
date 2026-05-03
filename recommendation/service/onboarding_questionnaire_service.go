package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"sea/chunk"
	"sea/config"
	embedsvc "sea/embedding/service"
	"sea/storage"
	"sea/zlog"

	"go.uber.org/zap"
)

var (
	ErrOnboardingMemoryUnavailable = errors.New("onboarding_memory_unavailable")
	ErrInvalidOnboardingAnswer     = errors.New("invalid_onboarding_answer")
)

const defaultOnboardingPeriodBucket = "d1"

type OnboardingQuestionnaireRequest struct {
	UserID                          string   `json:"user_id" binding:"required"`
	Username                        string   `json:"username,omitempty"`
	Interests                       []string `json:"interests"`
	PrimaryPurpose                  string   `json:"primary_purpose"`
	PreferredArticleTypes           []string `json:"preferred_article_types"`
	PreferredArticleLength          string   `json:"preferred_article_length"`
	PreferredStyle                  string   `json:"preferred_style"`
	Backgrounds                     []string `json:"backgrounds"`
	DifficultyPreference            string   `json:"difficulty_preference"`
	ExcludedContents                []string `json:"excluded_contents,omitempty"`
	ReadingTimeSlots                []string `json:"reading_time_slots,omitempty"`
	PersonalizedRecommendationTypes []string `json:"personalized_recommendation_types"`
}

type OnboardingQuestionnaireResponse struct {
	TraceID      string    `json:"trace_id"`
	Status       string    `json:"status"`
	UserID       string    `json:"user_id"`
	PeriodBucket string    `json:"period_bucket"`
	MemoryTypes  []string  `json:"memory_types"`
	UpdatedAt    time.Time `json:"updated_at"`
	Warnings     []string  `json:"warnings,omitempty"`
}

type onboardingMemoryRepo interface {
	Upsert(ctx context.Context, m storage.UserMemory) error
}

type onboardingMemoryChunkRepo interface {
	ReplaceChunks(
		ctx context.Context,
		userID string,
		memType storage.MemoryType,
		periodBucket string,
		updatedAt time.Time,
		chunks []string,
		vectors [][]float32,
	) error
}

type onboardingMemoryContentSet struct {
	LongTerm  string
	ShortTerm string
	Periodic  string
}

type OnboardingQuestionnaireService struct {
	memoryRepo  onboardingMemoryRepo
	memoryChunk onboardingMemoryChunkRepo
}

func NewOnboardingQuestionnaireService(
	memoryRepo onboardingMemoryRepo,
	memoryChunk onboardingMemoryChunkRepo,
) *OnboardingQuestionnaireService {
	if isNilSearchDependency(memoryRepo) {
		return &OnboardingQuestionnaireService{}
	}
	return &OnboardingQuestionnaireService{
		memoryRepo:  memoryRepo,
		memoryChunk: memoryChunk,
	}
}

func (s *OnboardingQuestionnaireService) Submit(
	ctx context.Context,
	req OnboardingQuestionnaireRequest,
) (OnboardingQuestionnaireResponse, error) {
	req = normalizeOnboardingQuestionnaireRequest(req)
	resp := OnboardingQuestionnaireResponse{
		TraceID:      newMetadataSearchTraceID(),
		Status:       "ok",
		UserID:       req.UserID,
		PeriodBucket: defaultOnboardingPeriodBucket,
		MemoryTypes:  []string{},
	}

	if s == nil || s.memoryRepo == nil {
		return resp, ErrOnboardingMemoryUnavailable
	}
	if err := validateOnboardingQuestionnaireRequest(req); err != nil {
		return resp, err
	}

	updatedAt := time.Now()
	memories := buildOnboardingMemoryContents(req)
	warnings := make([]string, 0, 3)

	writePlans := []struct {
		memoryType   storage.MemoryType
		periodBucket string
		content      string
	}{
		{memoryType: storage.MemoryLongTerm, content: memories.LongTerm},
		{memoryType: storage.MemoryShortTerm, content: memories.ShortTerm},
		{memoryType: storage.MemoryPeriodic, periodBucket: defaultOnboardingPeriodBucket, content: memories.Periodic},
	}

	for _, plan := range writePlans {
		if err := s.memoryRepo.Upsert(ctx, storage.UserMemory{
			UserID:       req.UserID,
			MemoryType:   plan.memoryType,
			PeriodBucket: plan.periodBucket,
			Content:      plan.content,
			UpdatedAt:    updatedAt,
		}); err != nil {
			return resp, err
		}

		resp.MemoryTypes = append(resp.MemoryTypes, string(plan.memoryType))

		if warning, err := s.replaceMemoryChunks(ctx, req.UserID, plan.memoryType, plan.periodBucket, updatedAt, plan.content); err != nil {
			return resp, err
		} else if warning != "" {
			warnings = append(warnings, warning)
		}
	}

	resp.UpdatedAt = updatedAt
	if len(warnings) > 0 {
		resp.Warnings = warnings
	}
	return resp, nil
}

func normalizeOnboardingQuestionnaireRequest(req OnboardingQuestionnaireRequest) OnboardingQuestionnaireRequest {
	req.UserID = strings.TrimSpace(req.UserID)
	req.Username = strings.TrimSpace(req.Username)
	req.PrimaryPurpose = strings.TrimSpace(req.PrimaryPurpose)
	req.PreferredArticleLength = strings.TrimSpace(req.PreferredArticleLength)
	req.PreferredStyle = strings.TrimSpace(req.PreferredStyle)
	req.DifficultyPreference = strings.TrimSpace(req.DifficultyPreference)
	req.Interests = normalizeOnboardingSelections(req.Interests, 5)
	req.PreferredArticleTypes = normalizeOnboardingSelections(req.PreferredArticleTypes, 3)
	req.Backgrounds = normalizeOnboardingSelections(req.Backgrounds, 4)
	req.ExcludedContents = normalizeOnboardingSelections(req.ExcludedContents, 5)
	req.ReadingTimeSlots = normalizeOnboardingSelections(req.ReadingTimeSlots, 4)
	req.PersonalizedRecommendationTypes = normalizeOnboardingSelections(req.PersonalizedRecommendationTypes, 4)
	return req
}

func normalizeOnboardingSelections(values []string, maxCount int) []string {
	if maxCount <= 0 {
		maxCount = len(values)
	}

	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) >= maxCount {
			break
		}
	}
	return result
}

func validateOnboardingQuestionnaireRequest(req OnboardingQuestionnaireRequest) error {
	switch {
	case req.UserID == "":
		return fmt.Errorf("%w: user_id cannot be empty", ErrInvalidOnboardingAnswer)
	case len(req.Interests) == 0:
		return fmt.Errorf("%w: interests cannot be empty", ErrInvalidOnboardingAnswer)
	case req.PrimaryPurpose == "":
		return fmt.Errorf("%w: primary_purpose cannot be empty", ErrInvalidOnboardingAnswer)
	case len(req.PreferredArticleTypes) == 0:
		return fmt.Errorf("%w: preferred_article_types cannot be empty", ErrInvalidOnboardingAnswer)
	case req.PreferredArticleLength == "":
		return fmt.Errorf("%w: preferred_article_length cannot be empty", ErrInvalidOnboardingAnswer)
	case req.PreferredStyle == "":
		return fmt.Errorf("%w: preferred_style cannot be empty", ErrInvalidOnboardingAnswer)
	case len(req.Backgrounds) == 0:
		return fmt.Errorf("%w: backgrounds cannot be empty", ErrInvalidOnboardingAnswer)
	case req.DifficultyPreference == "":
		return fmt.Errorf("%w: difficulty_preference cannot be empty", ErrInvalidOnboardingAnswer)
	case len(req.PersonalizedRecommendationTypes) == 0:
		return fmt.Errorf("%w: personalized_recommendation_types cannot be empty", ErrInvalidOnboardingAnswer)
	default:
		return nil
	}
}

func buildOnboardingMemoryContents(req OnboardingQuestionnaireRequest) onboardingMemoryContentSet {
	nameLine := "这是用户注册问卷形成的初始推荐画像。"
	if req.Username != "" {
		nameLine = fmt.Sprintf("这是用户 %s 注册问卷形成的初始推荐画像。", req.Username)
	}

	longTermLines := []string{
		nameLine,
		fmt.Sprintf("核心兴趣方向：%s。", joinOnboardingSelections(req.Interests, "未提供")),
		fmt.Sprintf("使用产品的主要目的：%s。", defaultOnboardingValue(req.PrimaryPurpose, "未提供")),
		fmt.Sprintf("偏好的文章类型：%s。", joinOnboardingSelections(req.PreferredArticleTypes, "未提供")),
		fmt.Sprintf("偏好的文章长度：%s。", defaultOnboardingValue(req.PreferredArticleLength, "未提供")),
		fmt.Sprintf("偏好的内容风格：%s。", defaultOnboardingValue(req.PreferredStyle, "未提供")),
		fmt.Sprintf("专业背景或熟悉领域：%s。", joinOnboardingSelections(req.Backgrounds, "未提供")),
		fmt.Sprintf("内容难度偏好：%s。", defaultOnboardingValue(req.DifficultyPreference, "未提供")),
	}
	if len(req.ExcludedContents) > 0 {
		longTermLines = append(longTermLines, fmt.Sprintf("明确不想看到的内容：%s。", joinOnboardingSelections(req.ExcludedContents, "无")))
	}
	if len(req.PersonalizedRecommendationTypes) > 0 {
		longTermLines = append(longTermLines, fmt.Sprintf("愿意接收的个性化推荐方式：%s。", joinOnboardingSelections(req.PersonalizedRecommendationTypes, "未提供")))
	}
	longTermLines = append(longTermLines, "推荐策略：优先推荐与核心兴趣、使用目的、文章类型和难度偏好一致的内容，冷启动阶段降低与明确负反馈相冲突的主题。")

	shortTermLines := []string{
		"这是新注册阶段的即时推荐偏好，请优先满足。",
		fmt.Sprintf("前几轮推荐优先主题：%s。", joinOnboardingSelections(req.Interests, "未提供")),
		fmt.Sprintf("优先内容形式：%s。", joinOnboardingSelections(req.PreferredArticleTypes, "未提供")),
		fmt.Sprintf("文章长度应更贴近：%s。", defaultOnboardingValue(req.PreferredArticleLength, "都可以")),
		fmt.Sprintf("表达风格应更贴近：%s。", defaultOnboardingValue(req.PreferredStyle, "都可以")),
		fmt.Sprintf("内容难度应更贴近：%s。", defaultOnboardingValue(req.DifficultyPreference, "根据主题自动调整")),
	}
	if len(req.ExcludedContents) > 0 {
		shortTermLines = append(shortTermLines, fmt.Sprintf("本轮尽量避免：%s。", joinOnboardingSelections(req.ExcludedContents, "无")))
	}

	periodicLines := []string{
		"这是当前 d1 周期桶的初始推荐节奏偏好。",
		fmt.Sprintf("本周期阅读目的：%s。", defaultOnboardingValue(req.PrimaryPurpose, "未提供")),
		fmt.Sprintf("本周期优先兴趣：%s。", joinOnboardingSelections(req.Interests, "未提供")),
		fmt.Sprintf("本周期推荐形式：%s。", joinOnboardingSelections(req.PreferredArticleTypes, "未提供")),
	}
	if len(req.ReadingTimeSlots) > 0 {
		periodicLines = append(periodicLines, fmt.Sprintf("更常阅读的时间段：%s。", joinOnboardingSelections(req.ReadingTimeSlots, "时间不固定")))
	}
	if len(req.PersonalizedRecommendationTypes) > 0 {
		periodicLines = append(periodicLines, fmt.Sprintf("当前更愿意接收：%s。", joinOnboardingSelections(req.PersonalizedRecommendationTypes, "只在首页推荐")))
	}
	if len(req.ExcludedContents) > 0 {
		periodicLines = append(periodicLines, fmt.Sprintf("当前周期尽量避开：%s。", joinOnboardingSelections(req.ExcludedContents, "无")))
	}

	return onboardingMemoryContentSet{
		LongTerm:  strings.Join(longTermLines, "\n"),
		ShortTerm: strings.Join(shortTermLines, "\n"),
		Periodic:  strings.Join(periodicLines, "\n"),
	}
}

func joinOnboardingSelections(values []string, fallback string) string {
	if len(values) == 0 {
		return fallback
	}
	return strings.Join(values, "、")
}

func defaultOnboardingValue(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func (s *OnboardingQuestionnaireService) replaceMemoryChunks(
	ctx context.Context,
	userID string,
	memType storage.MemoryType,
	periodBucket string,
	updatedAt time.Time,
	content string,
) (string, error) {
	if s.memoryChunk == nil {
		return "", nil
	}

	maxTokens := config.Cfg.Split.MemoryChunkMaxTokens
	overlapTokens := config.Cfg.Split.MemoryChunkOverlapTokens
	if maxTokens <= 0 {
		maxTokens = 600
	}
	if overlapTokens < 0 {
		overlapTokens = 0
	}

	rawChunks := chunk.SplitByTokenBudget(content, maxTokens, overlapTokens)
	finalChunks := make([]string, 0, len(rawChunks))
	vectors := make([][]float32, 0, len(rawChunks))
	for _, item := range rawChunks {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		vector, err := embedsvc.TextVector(ctx, item)
		if err != nil {
			zlog.L().Warn(
				"onboarding memory chunk embedding failed",
				zap.Error(err),
				zap.String("user_id", userID),
				zap.String("memory_type", string(memType)),
				zap.String("period_bucket", periodBucket),
			)
			return "memory_chunk_embedding_degraded", nil
		}

		finalChunks = append(finalChunks, item)
		vectors = append(vectors, vector)
	}

	if len(finalChunks) == 0 {
		return "", nil
	}

	if err := s.memoryChunk.ReplaceChunks(ctx, userID, memType, periodBucket, updatedAt, finalChunks, vectors); err != nil {
		zlog.L().Warn(
			"onboarding memory chunk replace failed",
			zap.Error(err),
			zap.String("user_id", userID),
			zap.String("memory_type", string(memType)),
			zap.String("period_bucket", periodBucket),
		)
		return "memory_chunk_replace_degraded", nil
	}

	return "", nil
}
