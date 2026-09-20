package cf

import (
	"context"
	"errors"
	"testing"

	"sea/internal/domain"
)

// ============================================================================
// 该文件测试 ColdStart（冷启动策略），覆盖：
//   - ColdStartUser：新用户热门兜底 + 兴趣标记
//   - ColdStartArticle：新文章内容相似度 CF 分数迁移
//   - HotArticleRepo/ContentSimilarityRepo mock 注入
//   - 错误传播
// ============================================================================

// --- mock repos ---

// mockHotRepoCF 测试用 HotArticleRepo mock。
type mockHotRepoCF struct {
	cands []domain.Candidate
	err   error
}

func (m *mockHotRepoCF) ListHot(ctx context.Context, topK int) ([]domain.Candidate, error) {
	if m.err != nil {
		return nil, m.err
	}
	if topK > 0 && len(m.cands) > topK {
		return m.cands[:topK], nil
	}
	return m.cands, nil
}

// mockContentRepoCF 测试用 ContentSimilarityRepo mock。
type mockContentRepoCF struct {
	cands map[string][]domain.Candidate
	err   error
}

func (m *mockContentRepoCF) FindSimilarArticles(ctx context.Context, articleID string, topK int) ([]domain.Candidate, error) {
	if m.err != nil {
		return nil, m.err
	}
	cands := m.cands[articleID]
	if topK > 0 && len(cands) > topK {
		return cands[:topK], nil
	}
	return cands, nil
}

// --- ColdStartUser 测试 ---

// TestColdStartUser_HotFallback 验证新用户冷启动走热门兜底。
func TestColdStartUser_HotFallback(t *testing.T) {
	hotRepo := &mockHotRepoCF{
		cands: []domain.Candidate{
			{ArticleID: "hot1", Score: 0.9},
			{ArticleID: "hot2", Score: 0.8},
			{ArticleID: "hot3", Score: 0.7},
		},
	}
	cs := NewColdStart(hotRepo, nil, 10)

	profile := domain.UserProfile{
		Static: &domain.StaticProfile{
			Interests: []string{"科技", "旅行"},
		},
	}

	cands, err := cs.ColdStartUser(context.Background(), profile)
	if err != nil {
		t.Fatalf("ColdStartUser 失败: %v", err)
	}
	if len(cands) != 3 {
		t.Fatalf("候选数 = %d, 期望 3", len(cands))
	}
	if cands[0].ArticleID != "hot1" {
		t.Errorf("首位 = %q, 期望 hot1", cands[0].ArticleID)
	}
	// 验证 Source 标记。
	if cands[0].Source != "cf_cold_user" {
		t.Errorf("Source = %q, 期望 cf_cold_user", cands[0].Source)
	}
	// 验证 Scores map 含 cf_cold。
	if cands[0].Scores["cf_cold"] != 0.9 {
		t.Errorf("cf_cold = %v, 期望 0.9", cands[0].Scores["cf_cold"])
	}
	// 有兴趣时应写入 interest_boost。
	if _, ok := cands[0].Scores["cf_cold_interest_boost"]; !ok {
		t.Errorf("期望含 cf_cold_interest_boost 分数")
	}
}

// TestColdStartUser_NoInterests 验证无兴趣标签时仍返回热门文章。
func TestColdStartUser_NoInterests(t *testing.T) {
	hotRepo := &mockHotRepoCF{
		cands: []domain.Candidate{
			{ArticleID: "hot1", Score: 0.9},
		},
	}
	cs := NewColdStart(hotRepo, nil, 10)

	profile := domain.UserProfile{
		Static: &domain.StaticProfile{}, // 无 Interests
	}

	cands, err := cs.ColdStartUser(context.Background(), profile)
	if err != nil {
		t.Fatalf("ColdStartUser 失败: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("候选数 = %d, 期望 1", len(cands))
	}
	// 无兴趣时不应有 interest_boost。
	if _, ok := cands[0].Scores["cf_cold_interest_boost"]; ok {
		t.Errorf("无兴趣时不应有 cf_cold_interest_boost")
	}
}

// TestColdStartUser_TopKTruncation 验证 topK 截断。
func TestColdStartUser_TopKTruncation(t *testing.T) {
	hotRepo := &mockHotRepoCF{
		cands: []domain.Candidate{
			{ArticleID: "h1", Score: 0.9},
			{ArticleID: "h2", Score: 0.8},
			{ArticleID: "h3", Score: 0.7},
		},
	}
	cs := NewColdStart(hotRepo, nil, 2)

	cands, err := cs.ColdStartUser(context.Background(), domain.UserProfile{})
	if err != nil {
		t.Fatalf("ColdStartUser 失败: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("候选数 = %d, 期望 2（topK 截断）", len(cands))
	}
}

// TestColdStartUser_HotRepoError 验证 HotRepo 错误传播。
func TestColdStartUser_HotRepoError(t *testing.T) {
	hotRepo := &mockHotRepoCF{err: errors.New("redis down")}
	cs := NewColdStart(hotRepo, nil, 10)

	_, err := cs.ColdStartUser(context.Background(), domain.UserProfile{})
	if err == nil {
		t.Fatalf("期望 HotRepo 错误传播")
	}
}

// TestColdStartUser_DefaultTopK 验证 topK<=0 时用默认值 20。
func TestColdStartUser_DefaultTopK(t *testing.T) {
	hotRepo := &mockHotRepoCF{
		cands: []domain.Candidate{
			{ArticleID: "h1", Score: 0.9},
		},
	}
	cs := NewColdStart(hotRepo, nil, 0) // topK=0 → 默认 20

	cands, err := cs.ColdStartUser(context.Background(), domain.UserProfile{})
	if err != nil {
		t.Fatalf("ColdStartUser 失败: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("候选数 = %d, 期望 1", len(cands))
	}
}

// --- ColdStartArticle 测试 ---

// TestColdStartArticle_ContentSimilarityTransfer 验证新文章内容相似度 CF 分数迁移。
func TestColdStartArticle_ContentSimilarityTransfer(t *testing.T) {
	contentRepo := &mockContentRepoCF{
		cands: map[string][]domain.Candidate{
			"new_art": {
				{ArticleID: "sim1", Score: 0.9},
				{ArticleID: "sim2", Score: 0.7},
				{ArticleID: "sim3", Score: 0.5},
			},
		},
	}
	cs := NewColdStart(nil, contentRepo, 10)

	cands, err := cs.ColdStartArticle(context.Background(), "new_art")
	if err != nil {
		t.Fatalf("ColdStartArticle 失败: %v", err)
	}
	if len(cands) != 3 {
		t.Fatalf("候选数 = %d, 期望 3", len(cands))
	}

	// 验证按迁移分数倒序。
	if cands[0].ArticleID != "sim1" {
		t.Errorf("首位 = %q, 期望 sim1", cands[0].ArticleID)
	}
	if cands[1].ArticleID != "sim2" {
		t.Errorf("第二位 = %q, 期望 sim2", cands[1].ArticleID)
	}
	if cands[2].ArticleID != "sim3" {
		t.Errorf("第三位 = %q, 期望 sim3", cands[2].ArticleID)
	}

	// 验证 Source 标记与分数迁移。
	if cands[0].Source != "cf_cold_article" {
		t.Errorf("Source = %q, 期望 cf_cold_article", cands[0].Source)
	}
	if cands[0].Scores["cf_transfer"] != 0.9 {
		t.Errorf("cf_transfer = %v, 期望 0.9", cands[0].Scores["cf_transfer"])
	}
	if cands[0].Scores["cf_cold"] != 0.9 {
		t.Errorf("cf_cold = %v, 期望 0.9", cands[0].Scores["cf_cold"])
	}
}

// TestColdStartArticle_NoSimilar 验证无相似文章时返回空。
func TestColdStartArticle_NoSimilar(t *testing.T) {
	contentRepo := &mockContentRepoCF{
		cands: map[string][]domain.Candidate{},
	}
	cs := NewColdStart(nil, contentRepo, 10)

	cands, err := cs.ColdStartArticle(context.Background(), "no_sim_art")
	if err != nil {
		t.Fatalf("ColdStartArticle 失败: %v", err)
	}
	if len(cands) != 0 {
		t.Errorf("候选数 = %d, 期望 0", len(cands))
	}
}

// TestColdStartArticle_TopKTruncation 验证 topK 截断。
func TestColdStartArticle_TopKTruncation(t *testing.T) {
	contentRepo := &mockContentRepoCF{
		cands: map[string][]domain.Candidate{
			"new": {
				{ArticleID: "s1", Score: 0.9},
				{ArticleID: "s2", Score: 0.8},
				{ArticleID: "s3", Score: 0.7},
			},
		},
	}
	cs := NewColdStart(nil, contentRepo, 2)

	cands, err := cs.ColdStartArticle(context.Background(), "new")
	if err != nil {
		t.Fatalf("ColdStartArticle 失败: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("候选数 = %d, 期望 2（topK 截断）", len(cands))
	}
}

// TestColdStartArticle_ContentRepoError 验证 ContentRepo 错误传播。
func TestColdStartArticle_ContentRepoError(t *testing.T) {
	contentRepo := &mockContentRepoCF{err: errors.New("milvus down")}
	cs := NewColdStart(nil, contentRepo, 10)

	_, err := cs.ColdStartArticle(context.Background(), "new_art")
	if err == nil {
		t.Fatalf("期望 ContentRepo 错误传播")
	}
}

// TestColdStartArticle_DefaultTopK 验证 topK<=0 时用默认值 20。
func TestColdStartArticle_DefaultTopK(t *testing.T) {
	contentRepo := &mockContentRepoCF{
		cands: map[string][]domain.Candidate{
			"new": {
				{ArticleID: "s1", Score: 0.9},
			},
		},
	}
	cs := NewColdStart(nil, contentRepo, -1) // topK<0 → 默认 20

	cands, err := cs.ColdStartArticle(context.Background(), "new")
	if err != nil {
		t.Fatalf("ColdStartArticle 失败: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("候选数 = %d, 期望 1", len(cands))
	}
}
